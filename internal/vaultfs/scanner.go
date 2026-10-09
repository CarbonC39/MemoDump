package vaultfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"memodump/internal/cloudsync"
)

// ScanOptions controls a vault scan. The scan is deterministic: pass 1 stats
// every candidate, then (after the optional settle wait) pass 2 re-stats and
// reads stable notes. Tests inject a sleep stub and mutation hooks so no
// correctness test depends on wall-clock time.
type ScanOptions struct {
	// SettleDelay is how long the scan waits between the stat pass and the
	// content pass so an in-progress external write can finish before a note's
	// bytes are read. 0 disables the wait.
	SettleDelay time.Duration
	// SleepFn replaces time.Sleep during the settle wait. Nil uses time.Sleep.
	SleepFn func(time.Duration)
	// BetweenPasses, when set, runs after the stat pass and before the
	// settle/content pass. Tests use it to mutate files deterministically and
	// force an unstable write.
	BetweenPasses func()
	// AfterCandidate, when set, runs with the candidate's path after each
	// candidate is processed in the content pass. Tests use it to mutate files
	// deterministically between two siblings, so a directory that becomes a
	// symlink after one sibling is checked is still caught for the next.
	AfterCandidate func(path string)
	// BeforeFolderStat, when set, runs with a folder's path after the
	// pre-checks and before the folder's own stat. Tests use it to swap the
	// vault root or an ancestor to a symlink deterministically in the window
	// between the pre-check and the folder's stat, so the folder's post-checks
	// must catch it and never emit an external directory as a stable folder.
	BeforeFolderStat func(path string)
}

// Observation is one stable filesystem observation of a note or folder.
// Notes carry LocalHash (their content digest); folders carry no content.
type Observation struct {
	Path      string // slash-relative vault path
	Kind      string // cloudsync.KindNote or cloudsync.KindFolder
	Size      int64
	ModTime   int64  // unix nanoseconds
	LocalHash string // note content digest; "" for folders
}

// ScanResult is a complete scan of one vault. Paths the repository owns or
// ignores (reserved and hidden directories, temp and OS files) are omitted
// entirely, not listed. Blocked paths are symlinks or sit under a symlinked
// directory: they are never read or synced and are reported so the reconciler
// can tell "became a symlink" apart from "deleted".
type ScanResult struct {
	Notes    []Observation
	Folders  []Observation
	Unstable []string // paths that kept changing or vanished mid-scan; not ready
	Blocked  []string // symlink paths and subtrees under symlinked directories
}

// LocalHash is the path-independent content digest of a note: SHA-256 over the
// LF-normalized Markdown. It is the token ordinary-change detection compares
// against the durable baseline, and — because identical notes share a digest —
// the signal offline rename inference matches on. It is local-only: it never
// leaves the device and is never compared across replicas.
func LocalHash(markdown string) string {
	sum := sha256.Sum256([]byte(cloudsync.NormalizeMarkdown(markdown)))
	return hex.EncodeToString(sum[:])
}

// Scan walks a vault and returns stable note/folder observations. The vault
// root symlink is resolved first (filepath.Walk uses Lstat and would otherwise
// treat a symlinked root as a non-directory and scan nothing). Inner symlinks
// are never followed or indexed; a symlinked directory blocks its whole
// subtree. A note is read only when its size and mtime are unchanged across
// the two passes AND across the read itself, through a descriptor that never
// follows a symlink at any path component, and its identity is re-verified
// with os.SameFile after the read — an in-progress, vanished, swapped, or
// symlinked write is reported unstable or blocked, never hashed torn or
// foreign bytes. The two allowed skips — hidden/reserved directories and
// transient files — are silent; any real walk or read error is returned so a
// partial scan is never trusted.
func Scan(root string, opts ScanOptions) (*ScanResult, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve vault root: %w", err)
	}
	// The identity of the real vault root at scan start. Every candidate is
	// verified against it before and after being read, so a root replaced by a
	// symlink to another directory can never smuggle external content in.
	rootInfo, err := os.Stat(realRoot)
	if err != nil {
		return nil, fmt.Errorf("stat vault root: %w", err)
	}

	// Pass 1: walk and record every candidate's path, kind, size, and mtime.
	type candidate struct {
		path  string
		kind  string
		size  int64
		mtime int64
	}
	var cands []candidate
	var blocked []string
	walkErr := filepath.Walk(realRoot, func(p string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(realRoot, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			blocked = append(blocked, filepath.ToSlash(rel))
			return nil
		}
		if info.IsDir() {
			if IsSkippedDir(info.Name()) {
				return filepath.SkipDir
			}
			cands = append(cands, candidate{
				path:  filepath.ToSlash(rel),
				kind:  cloudsync.KindFolder,
				size:  info.Size(),
				mtime: info.ModTime().UnixNano(),
			})
			return nil
		}
		if !IsNoteFile(info.Name()) {
			return nil
		}
		cands = append(cands, candidate{
			path:  filepath.ToSlash(rel),
			kind:  cloudsync.KindNote,
			size:  info.Size(),
			mtime: info.ModTime().UnixNano(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan vault: %w", walkErr)
	}

	if opts.BetweenPasses != nil {
		opts.BetweenPasses()
	}
	if opts.SettleDelay > 0 {
		sleep := opts.SleepFn
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(opts.SettleDelay)
	}

	res := &ScanResult{Blocked: blocked}
	process := func(c candidate) error {
		if opts.AfterCandidate != nil {
			defer func() { opts.AfterCandidate(c.path) }()
		}
		// The vault root must still be the same real directory that was
		// scanned; a root replaced by a symlink would otherwise let external
		// content with matching size/mtime pass as a stable note.
		if err := rootStillSame(realRoot, rootInfo); err != nil {
			return err
		}
		abs := filepath.Join(realRoot, filepath.FromSlash(c.path))
		// Verify the ancestor chain fresh for EVERY candidate — never cached
		// across siblings — so a directory that becomes a symlink after one
		// file is checked is still caught for the next.
		if rel, st, err := blockedAncestor(realRoot, abs); err != nil {
			return fmt.Errorf("stat ancestor of %s: %w", c.path, err)
		} else if st == readBlocked {
			res.Blocked = append(res.Blocked, rel)
			return nil
		} else if st == readUnstable {
			res.Unstable = append(res.Unstable, c.path)
			return nil
		}
		if c.kind == cloudsync.KindFolder && opts.BeforeFolderStat != nil {
			opts.BeforeFolderStat(c.path)
		}
		li, serr := os.Lstat(abs)
		if serr != nil {
			if os.IsNotExist(serr) {
				// Present at the stat pass, gone now: a rename or delete in
				// progress. Never treat a mid-scan disappearance as a clean
				// deletion — defer to the next scan.
				res.Unstable = append(res.Unstable, c.path)
				return nil
			}
			return fmt.Errorf("re-stat %s: %w", c.path, serr)
		}
		if li.Mode()&os.ModeSymlink != 0 {
			res.Blocked = append(res.Blocked, c.path)
			return nil
		}
		// Confirm the entry is still the kind the stat pass saw: a folder that
		// became a file (or vice versa) mid-scan is deferred, never observed as
		// the stale kind.
		if li.IsDir() != (c.kind == cloudsync.KindFolder) {
			res.Unstable = append(res.Unstable, c.path)
			return nil
		}
		if c.kind == cloudsync.KindFolder {
			// A folder is a stable observation only when nothing was swapped in
			// the window between the pre-check and this stat: re-verify the
			// vault root, the ancestor chain, and the entry's own identity (a
			// second Lstat compared with os.SameFile) before emitting it.
			if err := rootStillSame(realRoot, rootInfo); err != nil {
				return err
			}
			if rel, st, err := blockedAncestor(realRoot, abs); err != nil {
				return fmt.Errorf("stat ancestor of %s: %w", c.path, err)
			} else if st == readBlocked {
				res.Blocked = append(res.Blocked, rel)
				return nil
			} else if st == readUnstable {
				res.Unstable = append(res.Unstable, c.path)
				return nil
			}
			li2, serr := os.Lstat(abs)
			if serr != nil {
				if os.IsNotExist(serr) {
					res.Unstable = append(res.Unstable, c.path)
					return nil
				}
				return fmt.Errorf("re-stat %s: %w", c.path, serr)
			}
			if li2.Mode()&os.ModeSymlink != 0 {
				res.Blocked = append(res.Blocked, c.path)
				return nil
			}
			if !os.SameFile(li, li2) {
				res.Unstable = append(res.Unstable, c.path)
				return nil
			}
			res.Folders = append(res.Folders, Observation{
				Path: c.path, Kind: c.kind, Size: li2.Size(), ModTime: li2.ModTime().UnixNano(),
			})
			return nil
		}
		// A note is read only when size and mtime match the stat pass; the read
		// itself re-verifies both, the file identity, and the ancestor chain,
		// so an in-progress, swapped, or symlinked write is deferred instead of
		// hashed torn or foreign bytes.
		if li.Size() != c.size || li.ModTime().UnixNano() != c.mtime {
			res.Unstable = append(res.Unstable, c.path)
			return nil
		}
		data, st, rerr := readNoteStable(realRoot, rootInfo, abs, li.Size(), li.ModTime().UnixNano())
		if rerr != nil {
			return fmt.Errorf("read %s: %w", c.path, rerr)
		}
		switch st {
		case readStable:
			res.Notes = append(res.Notes, Observation{
				Path: c.path, Kind: c.kind, Size: li.Size(), ModTime: li.ModTime().UnixNano(),
				LocalHash: LocalHash(string(data)),
			})
		case readUnstable:
			res.Unstable = append(res.Unstable, c.path)
		case readBlocked:
			res.Blocked = append(res.Blocked, c.path)
		}
		return nil
	}
	for _, c := range cands {
		if err := process(c); err != nil {
			return nil, err
		}
	}

	sortObservations(res.Notes)
	sortObservations(res.Folders)
	sort.Strings(res.Unstable)
	sort.Strings(res.Blocked)
	res.Unstable = dedupSorted(res.Unstable)
	res.Blocked = dedupSorted(res.Blocked)
	return res, nil
}

func sortObservations(obs []Observation) {
	sort.Slice(obs, func(i, j int) bool { return obs[i].Path < obs[j].Path })
}

// dedupSorted removes consecutive duplicates from a sorted slice.
func dedupSorted(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// readStatus is the outcome of a stable note read.
type readStatus int

const (
	readStable readStatus = iota
	readUnstable
	readBlocked
)

// readNoteStable reads a note through a descriptor that never follows a
// symlink (at any path component: the final component via openNoFollow, the
// ancestors via blockedAncestor before and after the read), and only when the
// file's size and mtime are unchanged across the read. The vault root and the
// path's identity are re-verified after the read (os.SameFile), so a root or
// path atomically replaced mid-read invalidates the observation. readUnstable
// means the file changed, vanished, or was replaced; readBlocked means it or
// an ancestor became a symlink. Only explicit vanish/change is unstable — a
// real I/O or permission error is returned so a partial scan is never trusted.
func readNoteStable(realRoot string, rootInfo os.FileInfo, abs string, wantSize, wantMtime int64) ([]byte, readStatus, error) {
	if _, st, err := blockedAncestor(realRoot, abs); err != nil {
		return nil, readUnstable, err
	} else if st != readStable {
		return nil, st, nil
	}
	f, err := openNoFollow(abs)
	if err != nil {
		// Classify the failed open: now a symlink (blocked), gone (unstable),
		// or a real I/O/permission error on an existing regular file
		// (propagate).
		if li, serr := os.Lstat(abs); serr != nil {
			if os.IsNotExist(serr) {
				return nil, readUnstable, nil
			}
			return nil, readUnstable, serr
		} else if li.Mode()&os.ModeSymlink != 0 {
			return nil, readBlocked, nil
		}
		return nil, readUnstable, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, readUnstable, err
	}
	if fi.Size() != wantSize || fi.ModTime().UnixNano() != wantMtime {
		return nil, readUnstable, nil // replaced or changed between stat and open
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, readUnstable, err
	}
	fi, err = f.Stat()
	if err != nil {
		return nil, readUnstable, err
	}
	if fi.Size() != wantSize || fi.ModTime().UnixNano() != wantMtime {
		return nil, readUnstable, nil // changed mid-read
	}
	// The vault root must still be the same real directory that was scanned.
	if err := rootStillSame(realRoot, rootInfo); err != nil {
		return nil, readUnstable, err
	}
	// The path must still be the same real file the descriptor refers to: an
	// ancestor that became a symlink, a final-component symlink, or a swap to a
	// different file all invalidate the read (os.Lstat on the final component
	// does not guard ancestors).
	if _, st, err := blockedAncestor(realRoot, abs); err != nil {
		return nil, readUnstable, err
	} else if st == readBlocked {
		return nil, readBlocked, nil
	}
	li, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, readUnstable, nil
		}
		return nil, readUnstable, err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return nil, readBlocked, nil
	}
	if !os.SameFile(fi, li) {
		return nil, readUnstable, nil // the path no longer names the opened file
	}
	return data, readStable, nil
}

// blockedAncestor reports the nearest ancestor of abs (below realRoot) that is
// not a real directory — it became a symlink, a file, or vanished after the
// stat pass. readBlocked carries the blocked path (slash-relative); an
// ancestor that merely vanished is readUnstable (defer); a real Lstat error is
// returned. It is re-run for every candidate and after every read — never
// cached — so a directory that becomes a symlink mid-scan is caught for every
// sibling.
func blockedAncestor(realRoot, abs string) (string, readStatus, error) {
	dir := filepath.Dir(abs)
	for dir != realRoot {
		li, err := os.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return "", readUnstable, nil
			}
			return "", readUnstable, err
		}
		if li.Mode()&os.ModeSymlink != 0 || !li.IsDir() {
			rel, rerr := filepath.Rel(realRoot, dir)
			if rerr != nil {
				rel = dir
			}
			return filepath.ToSlash(rel), readBlocked, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // walked above the root
		}
		dir = parent
	}
	return "", readStable, nil
}

// rootStillSame verifies that realRoot still resolves to the same real
// directory that was scanned at pass 1. os.Stat follows a symlink, so a root
// replaced by a symlink to another directory fails the SameFile check.
func rootStillSame(realRoot string, want os.FileInfo) error {
	fi, err := os.Stat(realRoot)
	if err != nil {
		return fmt.Errorf("stat vault root: %w", err)
	}
	if !os.SameFile(want, fi) {
		return fmt.Errorf("vault root changed during scan")
	}
	return nil
}
