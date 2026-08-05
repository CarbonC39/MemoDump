package vaultfs

import "strings"

// The ignore predicates below are the single source of truth for what a vault
// scan and an initial import treat as notes, folders, and skippable entries.
// Scanning and Enable/Rebuild must share them: a path that gains a Sync ID
// under one rule and is ignored under the other would first be indexed and then
// disappear from the authoritative scan, being mistaken for a deletion.

// IsSkippedDir reports whether a directory entry is one the repository never
// descends into: any hidden (dot-prefixed) directory. The single rule covers
// the reserved .memodump and .images vaults, .git, OS junk, and user-hidden
// folders, keeping the shared ignore list deterministic and small.
func IsSkippedDir(name string) bool { return strings.HasPrefix(name, ".") }

// IsIgnoredNote reports whether a .md-suffixed file is still a transient
// artifact and never a note: MemoDump's own atomic-write temp files and office
// lock files. OS metadata files (Thumbs.db, .DS_Store, ...) never carry the
// .md suffix, so only .md files reach this check.
func IsIgnoredNote(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, ".memodump-") || strings.HasPrefix(lower, "~$")
}

// IsNoteFile reports whether a file entry is a note: a .md file that is not a
// transient artifact.
func IsNoteFile(name string) bool {
	return strings.HasSuffix(name, ".md") && !IsIgnoredNote(name)
}
