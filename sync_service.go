package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"memodump/internal/cloudsync"
	"memodump/internal/syncindex"
	"memodump/internal/syncprovider/s3"
	"memodump/internal/syncservice"
	"memodump/internal/syncstate"
)

// syncProvider selects the remote store for a manual run. It is a package-level
// seam: tests inject a shared memory store so two replicas can converge, and
// the default reads the S3 sync configuration. Without an S3 config the default
// is a process-local memory remote for the experimental phase.
var syncProvider syncservice.Provider

// syncMemoryRemote is the process-local demo remote used when no real provider
// is configured. The server's own sync state is therefore not durable across
// restarts in that fallback mode.
var syncMemoryRemote = cloudsync.NewMemoryStore()

// memoryProfile is the secret-free provider profile of the memory fallback.
const memoryProfile = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

// syncOpMu serializes the state-mutating sync API operations (enable, run,
// disable) within the process. Each request builds its own Service, so the
// service's instance mutex cannot serialize lifecycle operations; this
// package-level lock does. The replica OS lock still serializes across
// processes.
var syncOpMu sync.Mutex

// syncLastRun is the most recent manual-run outcome (redacted) and when it
// completed, guarded by syncLastRunMu because Status reads it concurrently with
// Run writing it.
var (
	syncLastRunMu sync.RWMutex
	syncLastRun   struct {
		Result    syncservice.Result
		Completed time.Time
	}
)

func init() {
	syncProvider = defaultSyncProvider
}

// syncS3Config reads the S3 sync-provider configuration from the environment.
// An empty endpoint or bucket means "no real provider configured".
func syncS3Config() s3.Config {
	return s3.Config{
		Endpoint:       os.Getenv("MEMODUMP_SYNC_ENDPOINT"),
		Region:         os.Getenv("MEMODUMP_SYNC_REGION"),
		Bucket:         os.Getenv("MEMODUMP_SYNC_BUCKET"),
		Prefix:         os.Getenv("MEMODUMP_SYNC_PREFIX"),
		AccessKey:      os.Getenv("MEMODUMP_SYNC_ACCESS_KEY"),
		SecretKey:      os.Getenv("MEMODUMP_SYNC_SECRET_KEY"),
		ForcePathStyle: os.Getenv("MEMODUMP_SYNC_FORCE_PATH_STYLE") == "1",
	}
}

// defaultSyncProvider selects the remote store. With no S3 config at all, the
// process-local memory remote is used ONLY when the explicit development switch
// MEMODUMP_SYNC_MEMORY=1 is set; production runs must configure a real
// provider. A partially configured S3 endpoint (one of endpoint/bucket missing)
// is an error, never a silent fallback that loses data on restart.
func defaultSyncProvider() (cloudsync.RemoteStore, error) {
	cfg := syncS3Config()
	if cfg.Endpoint == "" && cfg.Bucket == "" && cfg.Prefix == "" &&
		cfg.Region == "" && cfg.AccessKey == "" && cfg.SecretKey == "" {
		if os.Getenv("MEMODUMP_SYNC_MEMORY") == "1" {
			return syncMemoryRemote, nil
		}
		return nil, fmt.Errorf("no sync provider configured (set MEMODUMP_SYNC_ENDPOINT/BUCKET, or MEMODUMP_SYNC_MEMORY=1 for the in-memory dev remote)")
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("incomplete S3 sync config: MEMODUMP_SYNC_ENDPOINT and MEMODUMP_SYNC_BUCKET are required")
	}
	return s3.New(cfg)
}

// providerProfile returns the secret-free provider profile for the remote:
// the S3 provider's location hash, or the memory fallback profile.
func providerProfile(remote cloudsync.RemoteStore) string {
	if p, ok := remote.(interface{ Profile() string }); ok {
		return p.Profile()
	}
	return memoryProfile
}

// syncRepoIdentity returns the repository ID and provider profile for the
// remote. When no repository has ever been established (known == false), a
// missing repo.json is created only-if-absent; a lost create race re-reads the
// winner. When a repository is KNOWN (a durable snapshot exists), a missing
// repo.json is remote damage and stops with zero writes — never replaced.
func syncRepoIdentity(ctx context.Context, remote cloudsync.RemoteStore, known bool) (repoID, profile string, err error) {
	profile = providerProfile(remote)
	data, _, rerr := remote.Read(ctx, "repo.json")
	if rerr != nil {
		if !cloudsync.IsStoreError(rerr, cloudsync.ErrNotFound) {
			return "", "", rerr
		}
		if known {
			return "", "", fmt.Errorf("remote repository lost though sync was established")
		}
		desc := cloudsync.RepositoryDescriptor{
			FormatVersion: 1, RepositoryID: uuid.NewString(),
			CreatedAt: time.Now().UnixMilli(), MinimumClientVersion: "2.0.0",
		}
		ser, serr := desc.Serialize()
		if serr != nil {
			return "", "", serr
		}
		if _, cerr := remote.Create(ctx, "repo.json", ser); cerr != nil {
			if cloudsync.IsStoreError(cerr, cloudsync.ErrPreconditionFailed) {
				// Lost a concurrent first-create race: adopt the winner.
				return reReadRepoIdentity(ctx, remote, profile)
			}
			return "", "", cerr
		}
		return desc.RepositoryID, profile, nil
	}
	return parseRepoIdentity(data, profile)
}

// reReadRepoIdentity re-reads repo.json after a lost create race.
func reReadRepoIdentity(ctx context.Context, remote cloudsync.RemoteStore, profile string) (string, string, error) {
	data, _, rerr := remote.Read(ctx, "repo.json")
	if rerr != nil {
		return "", "", rerr
	}
	return parseRepoIdentity(data, profile)
}

func parseRepoIdentity(data []byte, profile string) (string, string, error) {
	parsed, perr := cloudsync.ParseRepositoryDescriptor(data)
	if perr != nil {
		return "", "", fmt.Errorf("invalid remote repo.json: %w", perr)
	}
	return parsed.RepositoryID, profile, nil
}

// syncRepoEstablished reports whether this replica has ever completed a sync
// (a disposable snapshot exists), meaning the remote repository is known.
func syncRepoEstablished() (bool, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return false, err
	}
	snaps, err := syncstate.NewSnapshotStoreV2(stateRoot, vaultID, replicaID)
	if err != nil {
		return false, err
	}
	_, rerr := os.Stat(snaps.Path())
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return false, nil
		}
		return false, rerr
	}
	return true, nil
}

// syncStateRoot resolves the empty default state root to the OS app-data
// location, matching the other syncstate helpers. All stores must receive the
// resolved path so the lock, index, snapshot, and recovery agree on one root.
func syncStateRoot() (string, error) {
	if syncRoot != "" {
		return syncRoot, nil
	}
	return syncstate.DefaultStateRoot()
}

// syncVaultID returns the vault ID from the enabled note-only index, or an
// error when sync is not enabled, the index is corrupt, or reading it fails.
// Callers must distinguish ErrNotEnabled (never enabled → benign) from corrupt
// and I/O errors (which must be reported, never treated as "no sync").
func syncVaultID() (string, error) {
	store, err := syncindex.LoadNoteStore(dataDir)
	if err != nil {
		return "", err
	}
	return store.Index.VaultID, nil
}

// syncIdentity returns the vault and replica IDs plus the resolved state root
// for the current data dir, resolving the replica through the device registry.
func syncIdentity() (vaultID, replicaID, stateRoot string, err error) {
	stateRoot, err = syncStateRoot()
	if err != nil {
		return "", "", "", err
	}
	vaultID, err = syncVaultID()
	if err != nil {
		return "", "", "", err
	}
	_, replica, err := syncstate.Resolve(stateRoot, dataDir, vaultID)
	if err != nil {
		return "", "", "", err
	}
	return vaultID, string(replica), stateRoot, nil
}

// syncConnectedPath is the persistent connect marker file in a replica's state
// directory. Its presence means the user has enabled the connection.
func syncConnectedPath(stateRoot, vaultID, replicaID string) string {
	return filepath.Join(syncstate.StateDir(stateRoot, vaultID, replicaID), "connected.json")
}

// syncSetConnected durably records whether sync is enabled (connected) for the
// replica. Disable clears only this marker — the index, identity, snapshot,
// and recovery copies are all preserved.
func syncSetConnected(stateRoot, vaultID, replicaID string, connected bool) error {
	path := syncConnectedPath(stateRoot, vaultID, replicaID)
	if connected {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]bool{"connected": true})
		return os.WriteFile(path, data, 0600)
	}
	return os.Remove(path)
}

// syncConnected reports whether the user has enabled sync (the connect marker
// exists).
func syncConnected() bool {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return false
	}
	_, err = os.Stat(syncConnectedPath(stateRoot, vaultID, replicaID))
	return err == nil
}

// buildSyncService assembles the sync service for the current data dir, with
// the resolved state root passed to every store, and the repository identity
// (repoID + secret-free profile) derived from the provider's repo.json. The
// SAME remote instance is bound into the service so identity resolution and the
// cycle cannot drift onto a different provider.
func buildSyncService(ctx context.Context) (*syncservice.Service, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	remote, err := syncProvider()
	if err != nil {
		return nil, err
	}
	known, err := syncRepoEstablished()
	if err != nil {
		return nil, err
	}
	repoID, profile, err := syncRepoIdentity(ctx, remote, known)
	if err != nil {
		return nil, err
	}
	return syncservice.New(syncservice.Config{
		RepoRoot: dataDir, StateRoot: stateRoot,
		VaultID: vaultID, ReplicaID: replicaID,
		RepoID: repoID, Profile: profile,
		Remote: remote,
	}), nil
}

// recoveryStore builds the recovery store for the current replica.
func recoveryStore() (*syncstate.RecoveryStore, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	return syncstate.NewRecoveryStore(stateRoot, vaultID, replicaID)
}
