package syncindex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"

	"memodump/internal/cloudsync"
	"memodump/internal/vaultfs"
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
	// ErrPoisoned reports a store whose last durable write failed. The on-disk
	// index is indeterminate (the primary, the backup, or the directory sync
	// may or may not have landed), so the in-memory index cannot be trusted and
	// the store must be Reloaded before further use.
	ErrPoisoned = errors.New("sync index store poisoned; reload to recover")
)

// Store is a file-backed portable index for one vault. Structural mutations
// (add/update/remove an entity) mark it dirty; Save performs one durable,
// atomic rewrite. Content-only saves never touch the file.
type Store struct {
	root string
	io   indexIO
	// Index is the in-memory portable index. After a failed durable write the
	// store is poisoned (poisoned == true) and Index is no longer trustworthy
	// until Reload.
	Index    *Index
	dirty    bool
	writes   int // durable primary rewrites; observable by tests
	poisoned bool
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

// Create writes an EMPTY fresh index for a vault that is enabling sync for the
// first time. It is a low-level primitive; Enable is the normal entry point and
// additionally assigns stable Sync IDs to every existing note and folder.
// Create never modifies existing Markdown and refuses to run on a vault that is
// already enabled (that would silently discard every existing Sync ID).
func Create(root, vaultID string) (*Store, error) {
	if !cloudsync.IsUUIDv4(vaultID) {
		return nil, fmt.Errorf("invalid vaultId %q", vaultID)
	}
	if err := requireVaultRoot(root); err != nil {
		return nil, err
	}
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	// Capture the directory state BEFORE the enable lock creates it, so a
	// lock-created directory is not mistaken for a lost identity.
	dirExisted, err := metadataDirExists(root)
	if err != nil {
		return nil, err
	}
	l, err := acquireEnableLock(root)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	_, err = Load(root)
	if !dirExisted && (errors.Is(err, ErrNotEnabled) || errors.Is(err, ErrCorrupt)) {
		// A genuine first create: the ErrCorrupt (if any) is only because the
		// enable lock just created the directory.
		idx := New(vaultID)
		data, err := idx.Serialize()
		if err != nil {
			return nil, err
		}
		if err := writeDurable(root, data); err != nil {
			return nil, err
		}
		return newStore(root, osIndexIO{}, idx), nil
	}
	if err != nil {
		// An existing index is never overwritten; a pre-existing directory with
		// no identity files is corruption, and other errors propagate.
		return nil, err
	}
	return nil, fmt.Errorf("sync already enabled for this vault")
}

// Enable makes sure a vault is synced: on first enable it creates the index and
// assigns a stable Sync ID to every existing note and folder in ONE durable
// write; on later calls it reuses the existing identity and only adds newly
// discovered paths in one consolidated write. It never modifies Markdown. When
// both index files are corrupt (or missing while .memodump exists) it returns
// ErrCorrupt and the caller offers a rebuild. First creation is serialized
// across processes by the enable lock so two concurrent enables agree on one
// Vault ID and one Sync ID set.
func Enable(root string) (*Store, error) {
	if err := requireVaultRoot(root); err != nil {
		return nil, err
	}
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	// Scan BEFORE the enable lock creates the metadata directory: a scan
	// failure (unreadable subdirectory, I/O error) must leave no trace so the
	// next enable retries cleanly instead of looking like lost identity. The
	// lock below still serializes the commit, so two concurrent first enables
	// cannot race.
	notes, folders, scanErr := scanVault(root)
	if scanErr != nil {
		return nil, scanErr
	}
	dirExisted, err := metadataDirExists(root)
	if err != nil {
		return nil, err
	}
	l, err := acquireEnableLock(root)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	s, err := Load(root)
	if !dirExisted && (errors.Is(err, ErrNotEnabled) || errors.Is(err, ErrCorrupt)) {
		// We hold the enable lock, so no other process is creating the index
		// concurrently; the ErrCorrupt (if any) is only the lock-created
		// directory. Build the complete index from the pre-lock scan, then
		// write it once.
		idx := New(NewVaultID())
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
		return newStore(root, osIndexIO{}, idx), nil
	}
	if err != nil {
		// Includes ErrCorrupt with a pre-existing directory: the caller decides
		// whether to rebuild.
		return nil, err
	}
	// Already enabled: re-scan under the lock (the vault may have changed
	// during the pre-lock scan) and index only the newly discovered entities.
	notes, folders, scanErr = scanVault(root)
	if scanErr != nil {
		return nil, scanErr
	}
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

// Enable makes sure a vault is synced: on first enable it creates the index and
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
	return loadWithIO(root, osIndexIO{})
}

// loadWithIO is Load with an explicit indexIO (the durability fault-injection
// seam used by tests).
func loadWithIO(root string, io indexIO) (*Store, error) {
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	primary := filepath.Join(root, DirName, IndexName)
	backup := filepath.Join(root, DirName, BackupName)

	idx, err := readIndex(primary)
	if err == nil {
		return newStore(root, io, idx), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		// No primary: maybe only a backup survived (crash between renames).
		idx, err2 := readIndex(backup)
		if err2 != nil {
			if errors.Is(err2, os.ErrNotExist) {
				// Both identity files are gone. ErrNotEnabled means the vault
				// NEVER enabled sync; a vault whose .memodump exists but whose
				// identity files are lost is corruption — silently treating it
				// as a first enable would reassign every Sync ID.
				dirExists, deErr := metadataDirExists(root)
				if deErr != nil {
					return nil, fmt.Errorf("%w: %v", ErrCorrupt, deErr)
				}
				if dirExists {
					return nil, ErrCorrupt
				}
				return nil, ErrNotEnabled
			}
			return nil, fmt.Errorf("%w: backup: %v", ErrCorrupt, err2)
		}
		return newStore(root, io, idx), nil
	}
	// Primary is corrupt: try the backup.
	idx, err2 := readIndex(backup)
	if err2 != nil {
		return nil, fmt.Errorf("%w (primary: %v; backup: %v)", ErrCorrupt, err, err2)
	}
	return newStore(root, io, idx), nil
}

// newStore constructs a store with an explicit io.
func newStore(root string, io indexIO, idx *Index) *Store {
	return &Store{root: root, io: io, Index: idx}
}

func readIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err // propagates os.ErrNotExist for a missing file
	}
	return parseIndex(data)
}

// writeIndex validates and durably writes idx WITHOUT changing the store's
// in-memory index. A validation failure is a caller error and leaves the store
// untouched. A durable-write failure is indeterminate — the primary, the
// backup, or the directory sync may or may not have landed — so the store is
// poisoned (ErrPoisoned) and the caller must Reload.
func (s *Store) writeIndex(idx *Index) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if err := idx.validate(); err != nil {
		return err
	}
	data, err := idx.Serialize()
	if err != nil {
		return err
	}
	if err := writeDurableWith(s.io, s.root, data); err != nil {
		s.poisoned = true
		return fmt.Errorf("%w: %v", ErrPoisoned, err)
	}
	return nil
}

// Save rewrites the index durably if any structural change is pending. A
// content-only save (dirty == false) is a no-op and never touches the file.
// The index is validated first so a buggy mutation can never persist an index
// that a later Load would reject. A failed durable write poisons the store;
// once poisoned, Save returns ErrPoisoned even when clean, so a caller can
// never mistake the poisoned state for normalcy.
func (s *Store) Save() error {
	if s == nil {
		return nil
	}
	if s.poisoned {
		return ErrPoisoned
	}
	if !s.dirty {
		return nil
	}
	if err := s.writeIndex(s.Index); err != nil {
		return err
	}
	s.writes++
	s.dirty = false
	return nil
}

// ReplaceIndex atomically swaps the store's index for a fully-built one. The
// new index is committed durably BEFORE the in-memory swap: a validation
// failure leaves the store untouched (not even in memory), and a durable-write
// failure poisons the store (the on-disk result is indeterminate) without
// changing the in-memory index. It is the commit point for batch identity
// mutations (offline renames plus fresh Sync IDs) that must never leave a
// half-applied state.
func (s *Store) ReplaceIndex(idx *Index) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if err := s.writeIndex(idx); err != nil {
		return err
	}
	s.Index = idx
	s.writes++
	s.dirty = false
	return nil
}

// Reload re-reads the index from disk (through the store's io), clearing any
// poison left by a failed durable write. It returns the same errors as Load.
func (s *Store) Reload() error {
	fresh, err := loadWithIO(s.root, s.io)
	if err != nil {
		return err
	}
	s.Index = fresh.Index
	s.dirty = false
	s.poisoned = false
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
	if s.poisoned {
		return ErrPoisoned
	}
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
	if s.poisoned {
		return ErrPoisoned
	}
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

// RemoveEntity drops a Sync ID from the index. It refuses a poisoned store so a
// mutation can never diverge memory further from the indeterminate disk state.
func (s *Store) RemoveEntity(syncID string) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if s.Index.Entities == nil {
		s.Index.Entities = make(map[string]Entity)
	}
	if _, ok := s.Index.Entities[syncID]; !ok {
		return nil
	}
	delete(s.Index.Entities, syncID)
	s.dirty = true
	return nil
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
	if err := requireVaultRoot(root); err != nil {
		return nil, err
	}
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	l, err := acquireEnableLock(root)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	idx := New(vaultID)
	notes, folders, scanErr := scanVault(root)
	if scanErr != nil {
		return nil, scanErr
	}
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
	return newStore(root, osIndexIO{}, idx), nil
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
		// Symlinks are never indexed or followed: a note symlink may point
		// outside the vault, and Walk would otherwise treat it as a note.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			// Do not descend into reserved or hidden directories. The predicate
			// is shared with the Phase 4 scanner so initial enable and the
			// authoritative scan ignore exactly the same entries.
			if vaultfs.IsSkippedDir(info.Name()) {
				return filepath.SkipDir
			}
			if rel != "." {
				folders = append(folders, filepath.ToSlash(rel))
			}
			return nil
		}
		// The note predicate is shared with the scanner, so a transient file
		// (e.g. an office lock) can never gain a Sync ID at enable and then be
		// ignored by the authoritative scan as if it had disappeared.
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

// writeDurable persists data to <root>/.memodump/sync-index.json following the
// portable-index durability sequence: unique temp file, flush, preserve the
// last known-good as .bak, atomic rename, directory sync where supported. It
// is the production (os) entry point; Save and ReplaceIndex use
// writeDurableWith so tests can inject failures at each numbered step.
func writeDurable(root string, data []byte) error {
	return writeDurableWith(osIndexIO{}, root, data)
}

// writeDurableWith is writeDurable over an explicit indexIO.
//
// The .bak file is written from the last VALID index (primary, else backup,
// else the new bytes), never from the current primary blindly: a corrupt
// primary (e.g. loaded-from-backup or post-crash state) must not clobber a
// good backup. Both files therefore always hold a parseable index.
func writeDurableWith(io indexIO, root string, data []byte) error {
	if err := checkMetadataSafe(root); err != nil {
		return err
	}
	dir := filepath.Join(root, DirName)
	target := filepath.Join(dir, IndexName)
	backup := filepath.Join(dir, BackupName)

	backupBytes := knownGoodBytes(io, target, backup, data)
	if err := io.WriteFileAtomic(dir, IndexName, data); err != nil {
		return err
	}
	// Skip rewriting .bak when it already holds the known-good bytes so a
	// steady-state structural save is one rename, not two.
	if b, err := io.ReadFile(backup); err != nil || !bytes.Equal(b, backupBytes) {
		if err := io.WriteFileAtomic(dir, BackupName, backupBytes); err != nil {
			return err
		}
	}
	return io.SyncDir(dir)
}

// knownGoodBytes returns the last parseable index bytes: the current primary if
// valid, else the current backup if valid, else fallback (the bytes being
// written — the only known-good state, e.g. first enable or rebuild).
func knownGoodBytes(io indexIO, primary, backup string, fallback []byte) []byte {
	if b, err := io.ReadFile(primary); err == nil {
		if _, perr := parseIndex(b); perr == nil {
			return b
		}
	}
	if b, err := io.ReadFile(backup); err == nil {
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
