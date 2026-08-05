package syncindex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"memodump/internal/cloudsync"
)

var (
	// ErrNotEnabled reports a vault that has never enabled sync.
	ErrNotEnabled = errors.New("sync not enabled for this vault")
	// ErrCorrupt reports a sync index and its backup that both fail to load.
	ErrCorrupt = errors.New("sync index corrupt")
	// ErrSymlink reports a sync metadata path that is a symlink. Sync metadata
	// must never be read or written through a symlink to a location outside the
	// vault.
	ErrSymlink = errors.New("sync metadata path must not be a symlink")
)

// Store is a file-backed portable index for one vault. Structural mutations
// (add/update/remove an entity) mark it dirty; Save performs one durable,
// atomic rewrite. Content-only saves never touch the file.
type Store struct {
	root   string
	Index  *Index
	dirty  bool
	writes int // durable primary rewrites; observable by tests
}

// IndexPath returns the primary index path for a vault.
func IndexPath(root string) string {
	return filepath.Join(root, DirName, IndexName)
}

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

// Create writes an EMPTY fresh index for a vault that is enabling sync for the
// first time. It is a low-level primitive; Enable is the normal entry point and
// additionally assigns stable Sync IDs to every existing note and folder.
// Create never modifies existing Markdown and refuses to run on a vault that is
// already enabled (that would silently discard every existing Sync ID).
func Create(root, vaultID string) (*Store, error) {
	if !cloudsync.IsUUIDv4(vaultID) {
		return nil, fmt.Errorf("invalid vaultId %q", vaultID)
	}
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	l, err := acquireEnableLock(root)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	// Under the lock: an index that already exists must never be overwritten.
	if _, err := Load(root); err == nil {
		return nil, fmt.Errorf("sync already enabled for this vault")
	} else if !errors.Is(err, ErrNotEnabled) {
		return nil, err
	}
	idx := New(vaultID)
	data, err := idx.Serialize()
	if err != nil {
		return nil, err
	}
	if err := writeDurable(root, data); err != nil {
		return nil, err
	}
	return &Store{root: root, Index: idx}, nil
}

// Enable makes sure a vault is synced: on first enable it creates the index and
// assigns a stable Sync ID to every existing note and folder in ONE durable
// write; on later calls it reuses the existing identity and only adds newly
// discovered paths in one consolidated write. It never modifies Markdown. When
// both index files are corrupt it returns ErrCorrupt and the caller offers a
// rebuild. First creation is serialized across processes by the enable lock so
// two concurrent enables agree on one Vault ID and one Sync ID set.
func Enable(root string) (*Store, error) {
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	l, err := acquireEnableLock(root)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	s, err := Load(root)
	if errors.Is(err, ErrNotEnabled) {
		// We hold the enable lock, so no other process is creating the index
		// concurrently. Build the complete index, then write it once.
		idx := New(NewVaultID())
		notes, folders := scanVault(root)
		for _, p := range notes {
			idx.Entities[NewVaultID()] = Entity{Kind: "note", Path: p}
		}
		for _, p := range folders {
			idx.Entities[NewVaultID()] = Entity{Kind: "folder", Path: p}
		}
		if err := idx.validate(); err != nil {
			return nil, err
		}
		data, err := idx.Serialize()
		if err != nil {
			return nil, err
		}
		if err := writeDurable(root, data); err != nil {
			return nil, err
		}
		return &Store{root: root, Index: idx}, nil
	}
	if err != nil {
		// Includes ErrCorrupt: the caller decides whether to rebuild.
		return nil, err
	}
	// Already enabled: index only the newly discovered entities.
	notes, folders := scanVault(root)
	changed, err := indexScanned(s, notes, folders)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.Save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// indexScanned adds stable Sync IDs for every scanned path not yet indexed.
// It reports whether anything was added.
func indexScanned(s *Store, notes, folders []string) (bool, error) {
	changed := false
	for _, p := range notes {
		if _, ok := s.FindByPath(p); ok {
			continue
		}
		if err := s.AddEntity(NewVaultID(), Entity{Kind: "note", Path: p}); err != nil {
			return false, err
		}
		changed = true
	}
	for _, p := range folders {
		if _, ok := s.FindByPath(p); ok {
			continue
		}
		if err := s.AddEntity(NewVaultID(), Entity{Kind: "folder", Path: p}); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// Load reads the primary index, falling back to the last-known-good .bak when
// the primary is corrupt or missing. A vault that has never enabled sync
// returns ErrNotEnabled; when both files are unusable it returns ErrCorrupt.
func Load(root string) (*Store, error) {
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	primary := filepath.Join(root, DirName, IndexName)
	backup := filepath.Join(root, DirName, BackupName)

	idx, err := readIndex(primary)
	if err == nil {
		return &Store{root: root, Index: idx}, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		// No primary: maybe only a backup survived (crash between renames).
		idx, err2 := readIndex(backup)
		if err2 != nil {
			if errors.Is(err2, os.ErrNotExist) {
				return nil, ErrNotEnabled
			}
			return nil, fmt.Errorf("%w: backup: %v", ErrCorrupt, err2)
		}
		return &Store{root: root, Index: idx}, nil
	}
	// Primary is corrupt: try the backup.
	idx, err2 := readIndex(backup)
	if err2 != nil {
		return nil, fmt.Errorf("%w (primary: %v; backup: %v)", ErrCorrupt, err, err2)
	}
	return &Store{root: root, Index: idx}, nil
}

func readIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err // propagates os.ErrNotExist for a missing file
	}
	return parseIndex(data)
}

// Save rewrites the index durably if any structural change is pending. A
// content-only save (dirty == false) is a no-op and never touches the file.
// The index is validated first so a buggy mutation can never persist an index
// that a later Load would reject.
func (s *Store) Save() error {
	if s == nil || !s.dirty {
		return nil
	}
	if err := s.Index.validate(); err != nil {
		return err
	}
	data, err := s.Index.Serialize()
	if err != nil {
		return err
	}
	if err := writeDurable(s.root, data); err != nil {
		return err
	}
	s.writes++
	s.dirty = false
	return nil
}

// Root returns the vault root this index belongs to.
func (s *Store) Root() string { return s.root }

// Len reports the number of indexed entities.
func (s *Store) Len() int { return len(s.Index.Entities) }

// FindByPath returns the Sync ID for a vault path, if indexed.
func (s *Store) FindByPath(path string) (string, bool) {
	for syncID, e := range s.Index.Entities {
		if e.Path == path {
			return syncID, true
		}
	}
	return "", false
}

// FindBySyncID returns the entity for a Sync ID, if indexed.
func (s *Store) FindBySyncID(syncID string) (Entity, bool) {
	e, ok := s.Index.Entities[syncID]
	return e, ok
}

// AddEntity records a new note/folder identity. It rejects a path that is
// already indexed by a different Sync ID instead of silently displacing it:
// identity conflicts are reconciliation's decision, never a quiet overwrite.
func (s *Store) AddEntity(syncID string, e Entity) error {
	if err := s.validateMutation(syncID, e); err != nil {
		return err
	}
	if s.Index.Entities == nil {
		s.Index.Entities = make(map[string]Entity)
	}
	if prev, ok := s.FindByPath(e.Path); ok && prev != syncID {
		return fmt.Errorf("path %q already indexed as %s", e.Path, prev)
	}
	s.Index.Entities[syncID] = e
	s.dirty = true
	return nil
}

// UpdatePath moves an existing Sync ID to a new vault path.
func (s *Store) UpdatePath(syncID, newPath string) error {
	if !validVaultPath(newPath) {
		return fmt.Errorf("unsafe path %q", newPath)
	}
	e, ok := s.Index.Entities[syncID]
	if !ok {
		return fmt.Errorf("unknown syncId %s", syncID)
	}
	if e.Path == newPath {
		return nil // no structural change
	}
	if prev, ok := s.FindByPath(newPath); ok && prev != syncID {
		return fmt.Errorf("path %q already indexed as %s", newPath, prev)
	}
	e.Path = newPath
	s.Index.Entities[syncID] = e
	s.dirty = true
	return nil
}

// RemoveEntity drops a Sync ID from the index.
func (s *Store) RemoveEntity(syncID string) {
	if s.Index.Entities == nil {
		s.Index.Entities = make(map[string]Entity)
	}
	if _, ok := s.Index.Entities[syncID]; !ok {
		return
	}
	delete(s.Index.Entities, syncID)
	s.dirty = true
}

func (s *Store) validateMutation(syncID string, e Entity) error {
	if !cloudsync.IsUUIDv4(syncID) {
		return fmt.Errorf("invalid syncId %q", syncID)
	}
	if e.Kind != cloudsync.KindNote && e.Kind != cloudsync.KindFolder {
		return fmt.Errorf("invalid kind %q", e.Kind)
	}
	if !validVaultPath(e.Path) {
		return fmt.Errorf("unsafe path %q", e.Path)
	}
	return nil
}

// Rebuild scans the local vault and constructs a fresh index, assigning new
// Sync IDs to every note and folder. It is the conservative fallback when both
// the primary index and its backup are corrupt: it never deletes local files.
func Rebuild(root, vaultID string) (*Store, error) {
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	l, err := acquireEnableLock(root)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	idx := New(vaultID)
	notes, folders := scanVault(root)
	for _, p := range notes {
		idx.Entities[uuid.NewString()] = Entity{Kind: "note", Path: p}
	}
	for _, p := range folders {
		idx.Entities[uuid.NewString()] = Entity{Kind: "folder", Path: p}
	}
	if err := idx.validate(); err != nil {
		return nil, err
	}
	data, err := idx.Serialize()
	if err != nil {
		return nil, err
	}
	if err := writeDurable(root, data); err != nil {
		return nil, err
	}
	return &Store{root: root, Index: idx}, nil
}

// scanVault returns sorted note and folder slash-relative paths, ignoring
// reserved metadata directories and never following symlinks.
func scanVault(root string) (notes, folders []string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		// Symlinks are never indexed or followed: a note symlink may point
		// outside the vault, and Walk would otherwise treat it as a note.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			// Do not descend into reserved or hidden directories.
			if strings.HasPrefix(filepath.Base(path), ".") {
				return filepath.SkipDir
			}
			if rel != "." {
				folders = append(folders, filepath.ToSlash(rel))
			}
			return nil
		}
		if strings.HasSuffix(info.Name(), ".md") {
			notes = append(notes, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(notes)
	sort.Strings(folders)
	return notes, folders
}

// writeDurable persists data to <root>/.memodump/sync-index.json following the
// portable-index durability sequence: unique temp file, flush, preserve the
// last known-good as .bak, atomic rename, directory sync where supported.
//
// The .bak file is written from the last VALID index (primary, else backup,
// else the new bytes), never from the current primary blindly: a corrupt
// primary (e.g. loaded-from-backup or post-crash state) must not clobber a
// good backup. Both files therefore always hold a parseable index.
func writeDurable(root string, data []byte) error {
	if err := checkMetadataSafe(root); err != nil {
		return err
	}
	dir := filepath.Join(root, DirName)
	target := filepath.Join(dir, IndexName)
	backup := filepath.Join(dir, BackupName)

	backupBytes := knownGoodBytes(target, backup, data)
	if err := writeFileAtomic(dir, IndexName, data); err != nil {
		return err
	}
	// Skip rewriting .bak when it already holds the known-good bytes so a
	// steady-state structural save is one rename, not two.
	if b, err := os.ReadFile(backup); err != nil || !bytes.Equal(b, backupBytes) {
		if err := writeFileAtomic(dir, BackupName, backupBytes); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// knownGoodBytes returns the last parseable index bytes: the current primary if
// valid, else the current backup if valid, else fallback (the bytes being
// written — the only known-good state, e.g. first enable or rebuild).
func knownGoodBytes(primary, backup string, fallback []byte) []byte {
	if b, err := os.ReadFile(primary); err == nil {
		if _, perr := parseIndex(b); perr == nil {
			return b
		}
	}
	if b, err := os.ReadFile(backup); err == nil {
		if _, perr := parseIndex(b); perr == nil {
			return b
		}
	}
	return fallback
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
