package vaultfs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"memodump/internal/cloudsync"
)

func writeNote(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func hashOf(markdown string) string {
	sum := sha256.Sum256([]byte(cloudsync.NormalizeMarkdown(markdown)))
	return hex.EncodeToString(sum[:])
}

func scanPaths(t *testing.T, root string, opts ScanOptions) *ScanResult {
	t.Helper()
	res, err := Scan(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func notePaths(res *ScanResult) []string {
	var out []string
	for _, n := range res.Notes {
		out = append(out, n.Path)
	}
	return out
}

func folderPaths(res *ScanResult) []string {
	var out []string
	for _, f := range res.Folders {
		out = append(out, f.Path)
	}
	return out
}

func TestScanStableNotesAndFolders(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "# A\nbody\n")
	writeNote(t, root, "sub/b.md", "# B\n")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	res := scanPaths(t, root, ScanOptions{})
	if got := notePaths(res); !reflect.DeepEqual(got, []string{"a.md", "sub/b.md"}) {
		t.Fatalf("notes = %v", got)
	}
	if got := folderPaths(res); !reflect.DeepEqual(got, []string{"empty", "sub"}) {
		t.Fatalf("folders = %v", got)
	}
	// The observed hash is the digest of the LF-normalized bytes.
	if res.Notes[0].Path != "a.md" || res.Notes[0].LocalHash != hashOf("# A\nbody\n") {
		t.Fatalf("a.md observation = %+v", res.Notes[0])
	}
}

func TestScanNormalizesLineEndings(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "crlf.md", "# A\r\nbody\r\n")
	writeNote(t, root, "lf.md", "# A\nbody\n")

	res := scanPaths(t, root, ScanOptions{})
	byPath := map[string]string{}
	for _, n := range res.Notes {
		byPath[n.Path] = n.LocalHash
	}
	if byPath["crlf.md"] != byPath["lf.md"] {
		t.Fatalf("CRLF and LF notes have different digests: %s vs %s", byPath["crlf.md"], byPath["lf.md"])
	}
	if byPath["crlf.md"] != hashOf("# A\nbody\n") {
		t.Fatalf("crlf digest = %s", byPath["crlf.md"])
	}
}

func TestScanUnstableNoteDeferred(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "busy.md", "v1")
	// Between the stat pass and the content pass the file changes: the scan
	// must not read a version it cannot confirm, and must report it unstable.
	opts := ScanOptions{BetweenPasses: func() {
		writeNote(t, root, "busy.md", "v2 much longer")
	}}
	res := scanPaths(t, root, opts)
	if len(res.Notes) != 0 {
		t.Fatalf("unstable note observed: %+v", res.Notes)
	}
	if len(res.Unstable) != 1 || res.Unstable[0] != "busy.md" {
		t.Fatalf("unstable = %v", res.Unstable)
	}
	// A later scan sees the settled file normally.
	writeNote(t, root, "busy.md", "v2 much longer")
	res = scanPaths(t, root, ScanOptions{})
	if len(res.Notes) != 1 || res.Notes[0].Path != "busy.md" || res.Notes[0].LocalHash != hashOf("v2 much longer") {
		t.Fatalf("settled note not observed: %+v", res.Notes)
	}
}

func TestScanSettlesAcrossDelay(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	var slept bool
	// A settle delay with an injected sleep runs between the passes; the write
	// finishes before the content pass, so the note is observed stable.
	opts := ScanOptions{
		SettleDelay: time.Millisecond * 5,
		SleepFn:     func(d time.Duration) { slept = true },
		BetweenPasses: func() {
			writeNote(t, root, "a.md", "v2")
		},
	}
	res := scanPaths(t, root, opts)
	if !slept {
		t.Fatal("settle sleep was not invoked")
	}
	if len(res.Notes) != 1 || res.Notes[0].Path != "a.md" || res.Notes[0].LocalHash != hashOf("v2") {
		t.Fatalf("settled note not observed after delay: %+v", res.Notes)
	}
}

func TestScanSkipsReservedAndHiddenDirectories(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "visible.md", "x")
	writeNote(t, root, ".memodump/sync-index.json", "not a note")
	writeNote(t, root, ".memodump/note.md", "hidden inside sync metadata")
	writeNote(t, root, ".images/a.png.md", "hidden inside image vault")
	writeNote(t, root, ".hidden/nested.md", "hidden user folder")
	writeNote(t, root, ".git/config.md", "vcs internals")

	res := scanPaths(t, root, ScanOptions{})
	if got := notePaths(res); !reflect.DeepEqual(got, []string{"visible.md"}) {
		t.Fatalf("notes = %v", got)
	}
	if len(res.Folders) != 0 {
		t.Fatalf("hidden directories observed as folders: %v", folderPaths(res))
	}
}

func TestScanIgnoresTempAndNonMarkdownFiles(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "real.md", "x")
	writeNote(t, root, ".memodump-123.tmp", "atomic temp")
	writeNote(t, root, "~$lock.md", "office lock")
	writeNote(t, root, "notes.txt", "not markdown")
	writeNote(t, root, "image.png", "not markdown")

	res := scanPaths(t, root, ScanOptions{})
	if got := notePaths(res); !reflect.DeepEqual(got, []string{"real.md"}) {
		t.Fatalf("notes = %v", got)
	}
}

func TestScanReportsSymlinksWithoutFollowing(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "real.md", "x")
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	outside := t.TempDir()
	writeNote(t, outside, "secret.md", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res := scanPaths(t, root, ScanOptions{})
	if got := notePaths(res); !reflect.DeepEqual(got, []string{"real.md"}) {
		t.Fatalf("notes = %v", got)
	}
	sort.Strings(res.Blocked)
	if got := res.Blocked; !reflect.DeepEqual(got, []string{"link.md", "linkdir"}) {
		t.Fatalf("blocked = %v", got)
	}
}

func TestScanResolvesRootSymlink(t *testing.T) {
	realDir := t.TempDir()
	writeNote(t, realDir, "a.md", "x")
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	res := scanPaths(t, link, ScanOptions{})
	if got := notePaths(res); !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("notes through symlinked root = %v", got)
	}
}

func TestScanCaseVariantNames(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "Note.md", "capital")
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("lower"), 0644); err != nil {
		t.Fatal(err)
	}
	// On a case-insensitive filesystem the second write overwrote the first; on
	// a case-sensitive one both exist. The scan must deterministically report
	// whatever the filesystem actually holds.
	res := scanPaths(t, root, ScanOptions{})
	seen := map[string]bool{}
	for _, n := range res.Notes {
		seen[n.Path] = true
	}
	if _, ok := seen["Note.md"]; !ok {
		t.Fatal("Note.md not observed")
	}
	lower, err := os.Stat(filepath.Join(root, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	upper, err := os.Stat(filepath.Join(root, "Note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(lower, upper) {
		// Case-insensitive filesystem: exactly one path is reported.
		if len(res.Notes) != 1 {
			t.Fatalf("case-insensitive fs: notes = %v", notePaths(res))
		}
	} else if !seen["note.md"] {
		t.Fatal("note.md not observed on a case-sensitive filesystem")
	}
}

func TestScanBlocksSubtreeUnderNewlySymlinkedDir(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "dir/a.md", "content")
	outside := t.TempDir()
	writeNote(t, outside, "secret.md", "outside")
	// The directory becomes a symlink to a location outside the vault between
	// the passes: the whole subtree must be blocked, never read or observed.
	opts := ScanOptions{BetweenPasses: func() {
		if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}}
	res := scanPaths(t, root, opts)
	if len(res.Notes) != 0 {
		t.Fatalf("observed a note under a symlinked directory: %+v", res.Notes)
	}
	if len(res.Folders) != 0 {
		t.Fatalf("observed a folder that became a symlink: %+v", res.Folders)
	}
	if !containsPath(res.Blocked, "dir") {
		t.Fatalf("blocked = %v, want the symlinked dir", res.Blocked)
	}
}

func TestScanVanishedNoteIsUnstableNotAbsent(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "v1")
	opts := ScanOptions{BetweenPasses: func() {
		if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
			t.Fatal(err)
		}
	}}
	res := scanPaths(t, root, opts)
	if len(res.Notes) != 0 {
		t.Fatalf("vanished note observed: %+v", res.Notes)
	}
	// A path present at the stat pass and gone at the content pass is deferred,
	// never silently dropped (which would look like a deletion).
	if len(res.Unstable) != 1 || res.Unstable[0] != "a.md" {
		t.Fatalf("unstable = %v, want a.md", res.Unstable)
	}
}

func TestScanDefersKindFlip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "flip.md"), 0755); err != nil {
		t.Fatal(err)
	}
	// The folder becomes a note file between passes: it must be deferred, never
	// observed as the stale folder kind.
	opts := ScanOptions{BetweenPasses: func() {
		if err := os.RemoveAll(filepath.Join(root, "flip.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "flip.md"), []byte("now a file"), 0644); err != nil {
			t.Fatal(err)
		}
	}}
	res := scanPaths(t, root, opts)
	if len(res.Folders) != 0 {
		t.Fatalf("kind-flipped folder observed as folder: %+v", res.Folders)
	}
	if !containsPath(res.Unstable, "flip.md") {
		t.Fatalf("unstable = %v, want flip.md", res.Unstable)
	}
}

func TestScanDefersNoteToFolderFlip(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "x.md", "content")
	opts := ScanOptions{BetweenPasses: func() {
		if err := os.Remove(filepath.Join(root, "x.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "x.md"), 0755); err != nil {
			t.Fatal(err)
		}
	}}
	res := scanPaths(t, root, opts)
	if len(res.Notes) != 0 {
		t.Fatalf("kind-flipped note observed: %+v", res.Notes)
	}
	if !containsPath(res.Unstable, "x.md") {
		t.Fatalf("unstable = %v, want x.md", res.Unstable)
	}
}

func TestScanBlocksSiblingAfterDirBecomesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeNote(t, root, "a/x.md", "one")
	writeNote(t, root, "a/y.md", "two")
	// A file with the sibling's name outside the vault: the OLD (cached)
	// ancestor check would read it through the swapped symlink.
	writeNote(t, outside, "y.md", "outside")

	flipped := false
	// After the first candidate under a/ is processed, the directory becomes a
	// symlink. The ancestor check is re-run for every candidate (never cached),
	// so the remaining sibling must be blocked, never read through the link.
	opts := ScanOptions{AfterCandidate: func(path string) {
		if flipped || !strings.HasPrefix(path, "a/") {
			return
		}
		flipped = true
		if err := os.RemoveAll(filepath.Join(root, "a")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "a")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}}
	res := scanPaths(t, root, opts)
	for _, n := range res.Notes {
		if n.Path == "a/y.md" {
			t.Fatalf("sibling under a mid-scan symlink observed: %+v", res.Notes)
		}
	}
	if !containsPath(res.Blocked, "a") {
		t.Fatalf("blocked = %v, want the symlinked dir", res.Blocked)
	}
}

func TestScanFolderDetectsRootSwapInStatWindow(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "dir"), 0755); err != nil {
		t.Fatal(err)
	}

	// In the window between the folder's pre-check and its own stat, the vault
	// root is swapped for a symlink to the external directory. Without the
	// folder's post-checks the external dir would be emitted as a stable
	// folder; with them the scan must refuse.
	parent := filepath.Dir(root)
	original := filepath.Join(parent, "vault-original-"+filepath.Base(root))
	swapped := false
	opts := ScanOptions{BeforeFolderStat: func(path string) {
		if path != "dir" || swapped {
			return
		}
		swapped = true
		if err := os.Rename(root, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}}
	if _, err := Scan(root, opts); err == nil {
		t.Fatal("external directory was accepted as a stable folder after the root swap")
	}
}

func TestScanDetectsRootReplacement(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "a.md", "content")
	outside := t.TempDir()
	// Same name and same content as the vault note: if the scanner followed the
	// swapped root, this external file would pass the size/mtime check.
	writeNote(t, outside, "a.md", "content")

	// Between the passes, the vault root is renamed away and a symlink to the
	// external directory takes its place. The scanner must detect that the root
	// is no longer the same real directory and refuse the whole scan.
	parent := filepath.Dir(root)
	original := filepath.Join(parent, "vault-original-"+filepath.Base(root))
	opts := ScanOptions{BetweenPasses: func() {
		if err := os.Rename(root, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}}
	if _, err := Scan(root, opts); err == nil {
		t.Fatal("vault root replaced by a symlink was not detected")
	}
}

func TestScanPropagatesRealReadErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission errors are not enforced")
	}
	root := t.TempDir()
	writeNote(t, root, "a.md", "x")
	writeNote(t, root, "b.md", "y")
	path := filepath.Join(root, "a.md")
	if err := os.Chmod(path, 0); err != nil {
		t.Skipf("chmod unavailable: %v", err)
	}
	defer os.Chmod(path, 0644)
	// A note that exists as a regular file but cannot be opened is a real I/O
	// error: it must fail the scan, not be quietly deferred as unstable.
	if _, err := Scan(root, ScanOptions{}); err == nil {
		t.Fatal("unreadable note did not fail the scan")
	}
}

func TestScanIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "b.md", strings.Repeat("b", 40))
	writeNote(t, root, "a.md", strings.Repeat("a", 30))
	writeNote(t, root, "sub/c.md", "c")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}

	first := scanPaths(t, root, ScanOptions{})
	second := scanPaths(t, root, ScanOptions{})
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two back-to-back scans disagree")
	}
	// A full scan is the authoritative input (the correctness mechanism); a
	// future watcher only reduces latency and its overflow/error path forces
	// exactly this full-scan result.
	if len(first.Notes) != 3 || len(first.Folders) != 2 {
		t.Fatalf("scan incomplete: notes=%v folders=%v", notePaths(first), folderPaths(first))
	}
}
