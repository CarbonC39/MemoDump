package syncstate

import (
	"encoding/json"
	"fmt"

	"memodump/internal/cloudsync"
)

// baselineKeyPrefix scopes the device-state keys that hold per-entity baselines,
// so the reconciliation can read them without colliding with other durable
// state (cursors, config, ...) that later phases store in the same WAL.
const baselineKeyPrefix = "baseline:"

// Baseline is the durable per-entity sync baseline stored in the device-state
// WAL: the last-synced local content digest, the remote content hash, and the
// provider's opaque version/etag. Ordinary local note changes are detected by
// comparing the current LocalHash to LocalHash; they never append dirty WAL
// rows — the baseline is only advanced when a sync cycle actually applies a
// result.
type Baseline struct {
	// LocalHash is the path-independent local content digest (vaultfs.LocalHash)
	// at last sync. An entity whose current digest differs is locally modified.
	LocalHash string `json:"localHash"`
	// RemoteHash is the canonical remote content hash at last sync; "" when the
	// entity has never been pushed.
	RemoteHash string `json:"remoteHash,omitempty"`
	// RemoteVersion is the provider's opaque version/etag; "" when never synced.
	RemoteVersion string `json:"remoteVersion,omitempty"`
}

// BaselineKey returns the device-state key for an entity's baseline.
func BaselineKey(syncID string) string { return baselineKeyPrefix + syncID }

// PutBaseline durably records an entity's baseline. A caller error (for example
// an invalid Sync ID or a malformed value) is rejected before anything reaches
// the WAL, so a bad baseline can never corrupt the durable state.
func PutBaseline(s *Store, syncID string, b Baseline) error {
	if !cloudsync.IsUUIDv4(syncID) {
		return fmt.Errorf("invalid syncId %q", syncID)
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.Put(BaselineKey(syncID), json.RawMessage(data))
	return err
}

// GetBaseline returns an entity's baseline. ok is false when no baseline exists
// yet. A present value that fails to decode is device-state corruption (the
// WAL guarantees stored values are valid JSON, so only a schema/type mismatch
// can reach here); it is surfaced, never silently treated as absent — treating
// a corrupted baseline as "no baseline" could misclassify a synced entity as
// baseline-unknown and probe instead of diffing.
func GetBaseline(s *Store, syncID string) (b Baseline, ok bool, err error) {
	raw, ok := s.Get(BaselineKey(syncID))
	if !ok {
		return Baseline{}, false, nil
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return Baseline{}, true, fmt.Errorf("%w: baseline %s: %v", ErrStateCorrupt, syncID, err)
	}
	return b, true, nil
}
