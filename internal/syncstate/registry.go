package syncstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"memodump/internal/cloudsync"
)

// registryEntry binds one canonical vault path to its Vault ID and Replica ID.
type registryEntry struct {
	VaultID   string    `json:"vaultId"`
	ReplicaID ReplicaID `json:"replicaId"`
}

// registry is the persisted path -> replica mapping. It is AppData, never the
// authority for whether a local entity exists; the sync-index is authoritative.
type registry struct {
	Version  int                      `json:"version"`
	Replicas map[string]registryEntry `json:"replicas"`
}

// registryMu serializes Resolve's read-modify-write within one process, so two
// concurrent resolves in this process never race the registry map and never
// self-deadlock on the cross-process flock (which treats a second descriptor in
// the same process as a competing holder).
var registryMu sync.Mutex

// resolve determines the Replica ID for a canonical vault path and Vault ID,
// mutating the registry when a new identity must be assigned. It returns the
// Replica ID and whether the registry changed (and therefore must be saved).
//
// Rules (spec §5.4):
//   - the path is already registered to this Vault ID: reuse the replica
//     (reopen / rename-back);
//   - the path is registered to a DIFFERENT Vault ID: the old vault is gone, a
//     new one lives here — assign a fresh replica for the new Vault ID;
//   - the path is new, and exactly one other registered path holds the same
//     Vault ID and no longer exists: an unambiguous move — re-associate;
//   - otherwise (new vault, multiple copies, or the other path still exists):
//     assign a fresh path-scoped replica.
func (r *registry) resolve(key, vaultID string) (ReplicaID, bool) {
	if e, ok := r.Replicas[key]; ok {
		if e.VaultID == vaultID {
			return e.ReplicaID, false
		}
		id := newReplicaID()
		r.Replicas[key] = registryEntry{VaultID: vaultID, ReplicaID: id}
		return id, true
	}

	// A move re-associates ONLY when exactly one other registered path holds
	// this Vault ID and that path no longer exists. Multiple candidates are
	// ambiguous (copies may have been deleted independently) — guessing which
	// replica to reuse risks sharing device state across two physical copies.
	candidates := 0
	var oldPath string
	for p, e := range r.Replicas {
		if e.VaultID == vaultID && p != key {
			candidates++
			oldPath = p
		}
	}
	if candidates == 1 {
		if _, err := os.Stat(oldPath); os.IsNotExist(err) {
			id := r.Replicas[oldPath].ReplicaID
			r.Replicas[key] = registryEntry{VaultID: vaultID, ReplicaID: id}
			delete(r.Replicas, oldPath)
			return id, true
		}
		// The single old path still exists: this is a copy, not a move. A stat
		// error other than not-exist also falls through — creating a new
		// replica is always the non-destructive side.
	}

	id := newReplicaID()
	r.Replicas[key] = registryEntry{VaultID: vaultID, ReplicaID: id}
	return id, true
}

// Resolve returns the DeviceID and the path-scoped ReplicaID for a vault,
// creating and persisting any missing identity in the state root. The vault
// root is canonicalized (absolute, cleaned, symlinks resolved) so a renamed or
// symlinked path maps back to the same replica; a copied vault at a second path
// always receives a new Replica ID. Nothing inside the vault is ever modified.
func Resolve(stateRoot, vaultRoot, vaultID string) (DeviceID, ReplicaID, error) {
	if stateRoot == "" {
		root, err := DefaultStateRoot()
		if err != nil {
			return "", "", err
		}
		stateRoot = root
	}
	key, err := canonicalVaultKey(vaultRoot)
	if err != nil {
		return "", "", err
	}
	if !cloudsync.IsUUIDv4(vaultID) {
		return "", "", fmt.Errorf("invalid vaultId %q", vaultID)
	}

	// Serialize the identity read-modify-write across processes and goroutines:
	// two first-starts must agree on one Replica ID, or they would each sync the
	// same vault under a different replica state directory.
	registryMu.Lock()
	defer registryMu.Unlock()
	rl, err := acquireRegistryLock(stateRoot)
	if err != nil {
		return "", "", err
	}
	defer rl.Close()

	deviceID, err := loadDevice(stateRoot)
	if err != nil {
		return "", "", err
	}
	reg, err := loadRegistry(stateRoot)
	if err != nil {
		return "", "", err
	}
	replicaID, changed := reg.resolve(key, vaultID)
	if changed {
		if err := reg.save(stateRoot); err != nil {
			return "", "", err
		}
	}
	return deviceID, replicaID, nil
}

func (r *registry) save(stateRoot string) error {
	return writeFileDurable(filepath.Join(stateRoot, "registry.json"), r)
}

// loadRegistry reads the path->replica registry. A missing registry is the
// "missing AppData" case: it resolves to an empty registry and every vault
// receives a fresh Replica ID (baseline-unknown probing happens in a later
// phase). A corrupt or unknown-version registry is treated the same way rather
// than failing sync enablement, and invalid entries are dropped so a tampered
// file cannot poison resolution of valid ones.
func loadRegistry(stateRoot string) (*registry, error) {
	path := filepath.Join(stateRoot, "registry.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newRegistry(), nil
		}
		return nil, err
	}
	var r registry
	if err := json.Unmarshal(data, &r); err != nil {
		return newRegistry(), nil
	}
	if r.Version != 1 {
		return newRegistry(), nil
	}
	// A corrupt document may carry replicas:null; degrade to an empty non-nil
	// map so resolve can still assign fresh replicas instead of panicking.
	if r.Replicas == nil {
		r.Replicas = make(map[string]registryEntry)
	}
	for p, e := range r.Replicas {
		if !validRegistryEntry(p, e) {
			delete(r.Replicas, p)
		}
	}
	return &r, nil
}

func newRegistry() *registry {
	return &registry{Version: 1, Replicas: make(map[string]registryEntry)}
}

func validRegistryEntry(path string, e registryEntry) bool {
	return filepath.IsAbs(path) && !strings.ContainsRune(path, 0) &&
		cloudsync.IsUUIDv4(e.VaultID) && cloudsync.IsUUIDv4(string(e.ReplicaID))
}

// canonicalVaultKey normalizes a vault location for registry keying: absolute,
// cleaned, symlinks resolved, and (on the case-insensitive Windows platform)
// lowercased. On macOS the default filesystem is case-insensitive but may be
// case-sensitive; treating it as case-sensitive here keeps two distinct
// directories from accidentally sharing device state — the safe direction.
func canonicalVaultKey(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}
