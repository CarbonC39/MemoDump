package syncindex

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"

	"memodump/internal/vaultfs"
)

// Shared constants and low-level helpers for the note-only index store: the
// reserved metadata directory, the identity-file names, the enable lock, the
// symlink checks, and the durable atomic writer. These are used by NoteStore
// (and the per-platform sync helpers) and live here so the schema-v1 prototype
// store could be deleted without duplicating them.

const (
	// DirName is the reserved vault metadata directory. The literal lives in
	// vaultfs so every repository path shares one source of truth.
	DirName = vaultfs.SyncMetadataDir
	// IndexName is the portable sync index file.
	IndexName = "sync-index.json"
	// BackupName is the last-known-good copy.
	BackupName = "sync-index.json.bak"
)

var (
	// ErrNotEnabled reports a vault that has never enabled sync.
	ErrNotEnabled = fmt.Errorf("sync not enabled for this vault")
	// ErrCorrupt reports a sync index and its backup that both fail to load.
	ErrCorrupt = fmt.Errorf("sync index corrupt")
	// ErrSymlink reports a sync metadata path that is a symlink. Sync metadata
	// must never be read or written through a symlink to a location outside the
	// vault.
	ErrSymlink = fmt.Errorf("sync metadata path must not be a symlink")
	// ErrPoisoned reports a store whose last durable write failed. The on-disk
	// index is indeterminate, so the in-memory index cannot be trusted and the
	// store must be Reloaded before further use.
	ErrPoisoned = fmt.Errorf("sync index store poisoned; reload to recover")
)

// NewVaultID returns a fresh version-4 UUID for a vault or Sync ID.
func NewVaultID() string { return uuid.NewString() }

// checkMetadataSafe verifies that the .memodump directory and its index files
// are not symlinks before any read or write, so a hostile or accidental symlink
// can never redirect sync metadata outside the vault. A missing path is fine:
// it will be created as a real directory.
func checkMetadataSafe(root string) error {
	dir := filepath.Join(root, DirName)
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, dir)
	}
	for _, name := range []string{IndexName, BackupName} {
		p := filepath.Join(dir, name)
		fi, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, p)
		}
	}
	return nil
}

// metadataDirExists reports whether the sync metadata directory exists on disk.
// checkMetadataSafe must have already run, so a symlink cannot pass here.
func metadataDirExists(root string) (bool, error) {
	_, err := os.Lstat(filepath.Join(root, DirName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// requireVaultRoot verifies the vault root exists and is a directory before any
// sync metadata is created or scanned, so a missing or wrong-typed root can
// never yield an empty committed index.
func requireVaultRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("vault root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("vault root %q is not a directory", root)
	}
	return nil
}

// scanVault returns sorted note and folder slash-relative paths, ignoring
// hidden/reserved directories and never following inner symlinks. The vault
// ROOT symlink is resolved first: filepath.Walk uses Lstat and would otherwise
// treat a symlinked root as a non-directory and scan nothing. Only the two
// allowed skips (hidden dirs, symlinks) are silent — any other walk error (a
// missing root, an unreadable directory, an I/O failure) is returned so
// Enable/Rebuild abort instead of committing a partial or empty index.
func scanVault(root string) (notes, folders []string, err error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve vault root: %w", err)
	}
	walkErr := filepath.Walk(realRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(realRoot, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			if vaultfs.IsSkippedDir(info.Name()) {
				return filepath.SkipDir
			}
			if rel != "." {
				folders = append(folders, filepath.ToSlash(rel))
			}
			return nil
		}
		if vaultfs.IsNoteFile(info.Name()) {
			notes = append(notes, filepath.ToSlash(rel))
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, fmt.Errorf("scan vault: %w", walkErr)
	}
	sort.Strings(notes)
	sort.Strings(folders)
	return notes, folders, nil
}

// writeFileAtomic writes data to dir/name via a unique temp file, fsync, and
// atomic rename, so a crash never leaves a partially written index.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".sync-index-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

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
	return os.Rename(tmpPath, filepath.Join(dir, name))
}
