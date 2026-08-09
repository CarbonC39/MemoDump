package main

import (
	"context"
	"encoding/json"
	"errors"
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

// syncRepoIdentity reads the remote repository descriptor. repo.json is created
// only by the explicit enable/setup flow; a missing descriptor here is remote
// damage and stops with zero writes — never replaced.
func syncRepoIdentity(ctx context.Context, remote cloudsync.RemoteStore) (repoID, profile string, err error) {
	profile = providerProfile(remote)
	data, _, rerr := remote.Read(ctx, "repo.json")
	if rerr != nil {
		if cloudsync.IsStoreError(rerr, cloudsync.ErrNotFound) {
			return "", "", fmt.Errorf("remote repository lost though sync was established")
		}
		return "", "", rerr
	}
	return parseRepoIdentity(data, profile)
}

// syncRepoSetup establishes or re-adopts the remote repository during the
// EXPLICIT enable flow — the only place repo.json is ever created. A missing
// descriptor is created only-if-absent; a lost concurrent create race re-reads
// the winner. A vault pinned to a repository (the connection record carries a
// RepoID) must match it, and a vault pinned to a provider (the record carries a
// Profile) must still be on that provider: a changed provider or repository is
// refused without modifying the record. The deliberate switch happens through
// the reset/reconnect flow.
func syncRepoSetup(ctx context.Context, remote cloudsync.RemoteStore, prev *syncConnectionRecord) (repoID, profile string, err error) {
	profile = providerProfile(remote)
	if prev != nil && prev.Profile != "" && prev.Profile != profile {
		return "", "", fmt.Errorf("sync provider changed (was %s, now %s); reset and re-enable sync to switch", shortID(prev.Profile), shortID(profile))
	}
	data, _, rerr := remote.Read(ctx, "repo.json")
	if rerr == nil {
		parsed, perr := cloudsync.ParseRepositoryDescriptor(data)
		if perr != nil {
			return "", "", fmt.Errorf("invalid remote repo.json: %w", perr)
		}
		if prev != nil && prev.RepoID != "" && parsed.RepositoryID != prev.RepoID {
			return "", "", fmt.Errorf("remote repository changed (was %s, now %s); reset and re-enable sync to switch", shortID(prev.RepoID), shortID(parsed.RepositoryID))
		}
		return parsed.RepositoryID, profile, nil
	}
	if !cloudsync.IsStoreError(rerr, cloudsync.ErrNotFound) {
		return "", "", rerr
	}
	if prev != nil && prev.RepoID != "" {
		return "", "", fmt.Errorf("remote repository lost though sync was established; reset and re-enable sync to create a new one")
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

// shortID returns the first 8 characters of an ID for human-readable messages.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
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

// syncConnectedPath is the persistent connection-record file in a replica's
// state directory. It stores the record: connected, the verified provider
// profile (secret-free), and the pinned repository ID. The repository ID lives
// here — NOT in the disposable snapshot — so a lost snapshot can never make a
// connected vault look like a fresh setup.
func syncConnectedPath(stateRoot, vaultID, replicaID string) string {
	return filepath.Join(syncstate.StateDir(stateRoot, vaultID, replicaID), "connected.json")
}

// syncConnectionRecord is the durable connection record written at enable. The
// verified provider profile and the pinned repository ID let a later run detect
// a changed provider or repository without trusting a disposable snapshot.
type syncConnectionRecord struct {
	Connected bool   `json:"connected"`
	Profile   string `json:"profile"`
	RepoID    string `json:"repoId"`
}

// syncWriteConnected durably records the connection state for the replica.
// Passing nil removes the marker (disable no longer removes identity — the
// reset flow does). The record is the identity authority, so it is written
// atomically (unique temp + file sync + rename + directory sync), never with a
// bare os.WriteFile. The index, identity, snapshot, and recovery copies are
// always preserved otherwise. Disabling a never-enabled vault is a no-op.
func syncWriteConnected(rec *syncConnectionRecord) error {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		if rec == nil && errors.Is(err, syncindex.ErrNotEnabled) {
			return nil // disabling a never-enabled vault: nothing to remove
		}
		return err
	}
	path := syncConnectedPath(stateRoot, vaultID, replicaID)
	if rec == nil {
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, _ := json.Marshal(rec)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memodump-connected-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// syncReadConnected returns the replica's connection record, or nil when sync
// has never been enabled or was disabled. A corrupt record is an error, never
// silently treated as disconnected.
func syncReadConnected() (*syncConnectionRecord, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		if errors.Is(err, syncindex.ErrNotEnabled) {
			return nil, nil // never enabled: no record
		}
		return nil, err
	}
	data, rerr := os.ReadFile(syncConnectedPath(stateRoot, vaultID, replicaID))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, nil
		}
		return nil, rerr
	}
	var rec syncConnectionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("corrupt sync connection record: %w", err)
	}
	return &rec, nil
}

// syncConnected reports whether the user has enabled sync (a connection record
// marked connected exists).
func syncConnected() bool {
	rec, err := syncReadConnected()
	return err == nil && rec != nil && rec.Connected
}

// syncConnectionExists reports whether a connection-record file exists for this
// replica, even when it is corrupt or unreadable — the status uses it so a
// corrupt record still surfaces the reset/reconnect affordance.
func syncConnectionExists() bool {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return false
	}
	_, rerr := os.Stat(syncConnectedPath(stateRoot, vaultID, replicaID))
	return rerr == nil
}

// withSyncLifecycleLock runs fn while holding the replica's OS lock, so the
// connection record and the disposable snapshot are never mutated concurrently
// with a running cycle in another process (which commits the snapshot and
// re-reads the record). The lock is passed to fn so a run can reuse the SAME
// lock critical section for its connection validation and the cycle. The lock
// is non-blocking: a run in flight refuses the lifecycle op with a descriptive
// error instead of queueing behind it.
func withSyncLifecycleLock(fn func(vaultID, replicaID, stateRoot string, lock *syncstate.Lock) error) error {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return err
	}
	lock, err := syncstate.AcquireReplicaLock(stateRoot, vaultID, replicaID)
	if err != nil {
		if errors.Is(err, syncstate.ErrLocked) {
			return fmt.Errorf("sync is running in another process; try again")
		}
		return err
	}
	defer lock.Close()
	return fn(vaultID, replicaID, stateRoot, lock)
}

// syncReplicaResetAt is the destructive part of the reset flow, run under the
// replica OS lock. It discards the replica's disposable snapshot and clears the
// connection record (identity pin), so the next enable starts a fresh setup —
// the ONLY deliberate way to switch repositories or recreate a lost one.
// Recovery copies and local notes are never touched.
func syncReplicaResetAt(vaultID, replicaID, stateRoot string) error {
	snaps, err := syncstate.NewSnapshotStoreV2(stateRoot, vaultID, replicaID)
	if err != nil {
		return err
	}
	if err := os.Remove(snaps.Path()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset snapshot: %w", err)
	}
	return syncWriteConnected(nil)
}

// syncConnectionIssue reports a corrupt or unreadable connection record so the
// status endpoint can surface it instead of silently reporting "disabled".
// A nil return means the record is absent, clean, or sync was never enabled.
func syncConnectionIssue() error {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		if errors.Is(err, syncindex.ErrNotEnabled) {
			return nil
		}
		return err
	}
	data, rerr := os.ReadFile(syncConnectedPath(stateRoot, vaultID, replicaID))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil
		}
		return rerr
	}
	var rec syncConnectionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("corrupt sync connection record: %w", err)
	}
	return nil
}

// buildSyncService assembles the sync service for the current data dir, with
// the resolved state root passed to every store. The caller has already
// resolved and verified the repository identity (repoID + secret-free profile)
// against the connection record under the replica lock; the SAME remote
// instance is bound into the service so identity resolution and the cycle
// cannot drift onto a different provider. lock is a pre-held replica OS lock
// the service uses without closing (the caller owns it).
func buildSyncService(ctx context.Context, repoID, profile string, remote cloudsync.RemoteStore, lock *syncstate.Lock) (*syncservice.Service, error) {
	vaultID, replicaID, stateRoot, err := syncIdentity()
	if err != nil {
		return nil, err
	}
	return syncservice.New(syncservice.Config{
		RepoRoot: dataDir, StateRoot: stateRoot,
		VaultID: vaultID, ReplicaID: replicaID,
		RepoID: repoID, Profile: profile,
		Remote: remote, Lock: lock,
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
