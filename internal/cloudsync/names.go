package cloudsync

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// PortablePathKey returns the collision key for a local path: Unicode NFC
// normalization plus full case folding (sharp s, Greek final sigma and variant
// letters, ligatures, dotted I, ...). Two paths collide when their keys are
// equal, which catches a case-only rename across OSes while preserving the
// original display name.
//
// The stabilized fold is the full fold followed by a canonical lowercase, so it
// is idempotent — fold(Ꭰ)=ꭰ but fold(ꭰ)=Ꭰ, while both stabilize to ꭰ and two
// case-variant Cherokee names collide. The TypeScript implementation embeds the
// identical stabilized table pinned by testdata/sync/case-fold.json.
func PortablePathKey(path string) string {
	return strings.ToLower(cases.Fold().String(norm.NFC.String(path)))
}

// ConflictNamespace is the fixed MemoDump namespace used to derive
// deterministic conflict Sync IDs. It is pinned verbatim by
// testdata/sync/state-hashes.json and must stay identical in Go and TypeScript.
const ConflictNamespace = "7f139d22-a0f6-50fe-855c-c416516180f0"

// DeriveConflictSyncID returns the deterministic UUID v5 conflict identity for
// a divergence on source Sync ID S, hashing the fixed-role state hashes in the
// order local, then remote. Without an operation journal the derivation itself
// must be idempotent, and the ordering matters: swapping the local and remote
// state hashes changes the result whenever the two sides' semantics differ.
// The caller must already hold validated values; this rejects malformed input
// rather than silently deriving an unusable identity.
func DeriveConflictSyncID(sourceSyncID, localStateHash, remoteStateHash string) (string, error) {
	if !IsSyncID(sourceSyncID) {
		return "", fmt.Errorf("conflict derivation: invalid source syncId %q", sourceSyncID)
	}
	for _, h := range []string{localStateHash, remoteStateHash} {
		if !contentHashRe.MatchString(h) {
			return "", fmt.Errorf("conflict derivation: invalid state hash %q", h)
		}
	}
	ns, err := uuid.Parse(ConflictNamespace)
	if err != nil {
		return "", err
	}
	name := sourceSyncID + "\x00" + localStateHash + "\x00" + remoteStateHash
	return uuid.NewSHA1(ns, []byte(name)).String(), nil
}

// ConflictFilename returns the deterministic conflict-copy filename for a
// derived conflict Sync ID: "<stem> (conflict <first 12 hex digits of the ID
// without hyphens>).md". It contains no clock and no device label, so a crash
// or lost response repeats the same conflict copy instead of producing a second
// one.
func ConflictFilename(stem, conflictSyncID string) string {
	digits := strings.ReplaceAll(conflictSyncID, "-", "")
	if len(digits) > 12 {
		digits = digits[:12]
	}
	return fmt.Sprintf("%s (conflict %s).md", stem, digits)
}
