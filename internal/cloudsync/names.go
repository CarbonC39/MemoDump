package cloudsync

import (
	"fmt"
	"strings"
	"time"

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

// ConflictName builds the synchronized conflict-copy filename in the canonical
// form "<stem> (conflict <device> <YYYYMMDD-HHmmss>).md". The caller passes a
// stem that already went through the portable filename rules.
func ConflictName(stem, deviceID string, ts time.Time) string {
	return fmt.Sprintf("%s (conflict %s %s).md", stem, deviceID, ts.UTC().Format("20060102-150405"))
}
