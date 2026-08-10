package syncindex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"memodump/internal/cloudsync"
)

// NoteStore is a file-backed note-only portable index for one vault. Structural
// mutations (add/update/remove a note) mark it dirty; Save performs one durable,
// atomic rewrite; content-only saves never touch the file. It reuses the shared
// durability sequence (unique temp file, fsync, .bak last-known-good, atomic
// rename, directory sync), the enable lock, and the metadata symlink checks.
type NoteStore struct {
	root string
	io   indexIO
	// Index is the in-memory note-only index. After a failed durable write the
	// store is poisoned (poisoned == true) and Index is no longer trustworthy
	// until Reload.
	Index    *NoteIndex
	dirty    bool
	writes   int // durable primary rewrites; observable by tests
	poisoned bool
}

// LoadNoteStore reads the primary note-only index, falling back to the
// last-known-good .bak when the primary is missing or corrupt. A vault that has
// never enabled sync returns ErrNotEnabled; a document with any unsupported
// schema (including the prototype schema-v1 index) returns ErrUnsupportedSchema
// and is never loaded as data; when both files are unusable it returns
// ErrCorrupt.
func LoadNoteStore(root string) (*NoteStore, error) {
	return loadNoteStoreWithIO(root, osIndexIO{})
}

// loadNoteStoreWithIO is LoadNoteStore with an explicit indexIO (the durability
// fault-injection seam used by tests).
//
// The v2 note-only index is the only loadable schema. A non-v2 document
// anywhere — the prototype schema-v1 index included — is unsupported, never
// loaded as a baseline, and never mistaken for an empty vault or ordinary
// corruption. A usable backup is still preferred over a non-v2 primary so a
// crash during a migration never loses the last-good identity. Real I/O errors
// (permission, device failures) stop the load: silently falling back to a
// stale backup could forget recently assigned Sync IDs.
func loadNoteStoreWithIO(root string, io indexIO) (*NoteStore, error) {
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	primary := filepath.Join(root, DirName, IndexName)
	backup := filepath.Join(root, DirName, BackupName)

	idx, primaryErr := readNoteIndex(io, primary)
	if primaryErr == nil {
		return newNoteStore(root, io, idx), nil
	}
	if classifyIndexErr(primaryErr) == indexIOError {
		return nil, fmt.Errorf("read sync-index primary: %w", primaryErr)
	}
	backupIdx, backupErr := readNoteIndex(io, backup)
	if backupErr == nil {
		return newNoteStore(root, io, backupIdx), nil
	}
	if classifyIndexErr(backupErr) == indexIOError {
		return nil, fmt.Errorf("read sync-index backup: %w", backupErr)
	}
	if classifyIndexErr(primaryErr) == indexUnsupported || errors.Is(backupErr, ErrUnsupportedSchema) {
		return nil, fmt.Errorf("%w (primary: %v; backup: %v)", ErrUnsupportedSchema, primaryErr, backupErr)
	}
	if classifyIndexErr(primaryErr) == indexMissing && errors.Is(backupErr, os.ErrNotExist) {
		dirExists, deErr := metadataDirExists(root)
		if deErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, deErr)
		}
		if dirExists {
			return nil, ErrCorrupt
		}
		return nil, ErrNotEnabled
	}
	return nil, fmt.Errorf("%w (primary: %v; backup: %v)", ErrCorrupt, primaryErr, backupErr)
}

// indexErrClass classifies a readNoteIndex error so the loader can decide
// whether a backup is a safe recovery source or the load must stop.
type indexErrClass int

const (
	indexOK indexErrClass = iota
	indexMissing
	indexUnsupported
	indexCorrupt
	indexIOError
)

// classifyIndexErr maps a readNoteIndex error onto its class. Real filesystem
// errors are distinct from a document that is merely invalid, so a permission
// failure never degrades into "load the stale backup".
func classifyIndexErr(err error) indexErrClass {
	switch {
	case err == nil:
		return indexOK
	case errors.Is(err, os.ErrNotExist):
		return indexMissing
	case errors.Is(err, ErrUnsupportedSchema):
		return indexUnsupported
	case errors.Is(err, ErrInvalidIndex):
		return indexCorrupt
	default:
		return indexIOError
	}
}

// newNoteStore constructs a note store with an explicit io.
func newNoteStore(root string, io indexIO, idx *NoteIndex) *NoteStore {
	return &NoteStore{root: root, io: io, Index: idx}
}

// readNoteIndex reads one index file through the injected io and parses it. A
// missing file or real I/O error propagates as-is (so the loader can stop);
// every structural parse error that is not the distinct ErrUnsupportedSchema is
// wrapped in ErrInvalidIndex so the loader can classify it as corruption and
// safely use the backup.
func readNoteIndex(io indexIO, path string) (*NoteIndex, error) {
	data, err := io.ReadFile(path)
	if err != nil {
		return nil, err // propagates os.ErrNotExist and real I/O errors
	}
	idx, perr := ParseNoteIndex(data)
	if perr != nil {
		if errors.Is(perr, ErrUnsupportedSchema) {
			return nil, perr
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidIndex, perr)
	}
	return idx, nil
}

// ErrAlreadyEnabled reports that the vault already holds a sync index identity;
// CreateNoteStore is a no-op in that case.
var ErrAlreadyEnabled = errors.New("sync already enabled for this vault")

// CreateNoteStore writes an EMPTY fresh note-only index for a vault enabling
// sync for the first time. It is a low-level primitive; EnableNoteStore is the
// normal entry point and additionally assigns stable Sync IDs to every existing
// note. CreateNoteStore never modifies existing Markdown and refuses to run on
// a vault that is already enabled or holds a non-v2 (prototype) index.
func CreateNoteStore(root, vaultID string) (*NoteStore, error) {
	if !cloudsync.IsUUIDv4(vaultID) {
		return nil, fmt.Errorf("invalid vaultId %q", vaultID)
	}
	if err := requireVaultRoot(root); err != nil {
		return nil, err
	}
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
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
	_, err = LoadNoteStore(root)
	if !dirExisted && (errors.Is(err, ErrNotEnabled) || errors.Is(err, ErrCorrupt)) {
		// A genuine first create: the ErrCorrupt (if any) is only because the
		// enable lock just created the directory.
		idx := NewNoteIndex(vaultID)
		data, err := idx.Serialize()
		if err != nil {
			return nil, err
		}
		if err := writeDurableNote(root, data); err != nil {
			return nil, err
		}
		return newNoteStore(root, osIndexIO{}, idx), nil
	}
	if err != nil {
		// Includes ErrUnsupportedSchema: a prototype index is never overwritten
		// silently; the caller must require an explicit re-enable.
		return nil, err
	}
	return nil, ErrAlreadyEnabled
}

// EnableNoteStore makes sure a vault is synced with the note-only index: on
// first enable it creates the index and assigns a stable Sync ID to every
// existing Markdown note in ONE durable write; on later calls it reuses the
// existing identity and only adds newly discovered notes in one consolidated
// write. It never modifies Markdown. A vault holding a non-v2 (prototype)
// index returns ErrUnsupportedSchema and requires an explicit re-enable.
func EnableNoteStore(root string) (*NoteStore, error) {
	if err := requireVaultRoot(root); err != nil {
		return nil, err
	}
	if err := checkMetadataSafe(root); err != nil {
		return nil, err
	}
	// Scan BEFORE the enable lock creates the metadata directory: a scan
	// failure must leave no trace so the next enable retries cleanly.
	notes, _, scanErr := scanVault(root)
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
	s, err := LoadNoteStore(root)
	if !dirExisted && (errors.Is(err, ErrNotEnabled) || errors.Is(err, ErrCorrupt)) {
		// We hold the enable lock, so no other process is creating the index
		// concurrently. Build the complete note index from the pre-lock scan,
		// then write it once.
		idx := NewNoteIndex(NewVaultID())
		for _, p := range notes {
			idx.Notes[NewVaultID()] = NoteEntry{Path: p}
		}
		if err := idx.validate(); err != nil {
			return nil, err
		}
		data, err := idx.Serialize()
		if err != nil {
			return nil, err
		}
		if err := writeDurableNote(root, data); err != nil {
			return nil, err
		}
		return newNoteStore(root, osIndexIO{}, idx), nil
	}
	if err != nil {
		// Includes ErrUnsupportedSchema (prototype state) and ErrCorrupt with a
		// pre-existing directory: the caller decides whether to re-enable.
		return nil, err
	}
	// Already enabled: re-scan under the lock and index only newly discovered
	// notes.
	notes, _, scanErr = scanVault(root)
	if scanErr != nil {
		return nil, scanErr
	}
	changed, err := indexScannedNotes(s, notes)
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

// indexScannedNotes adds stable Sync IDs for every scanned note path not yet
// indexed. It reports whether anything was added. A note path the note-only
// contract cannot represent aborts the enable rather than being silently
// dropped: an identity that is never recorded would be rediscovered and saved
// on every cycle.
func indexScannedNotes(s *NoteStore, notes []string) (bool, error) {
	changed := false
	for _, p := range notes {
		if _, ok := s.IDByPath(p); ok {
			continue
		}
		if err := s.AddNote(NewVaultID(), p); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// RebuildNoteStore scans the local vault and constructs a fresh note-only
// index, assigning new Sync IDs to every note. It is the conservative fallback
// when both the primary index and its backup are unusable, and the explicit
// re-enable path for a prototype (non-v2) index after the user confirms: it
// never deletes local files.
func RebuildNoteStore(root, vaultID string) (*NoteStore, error) {
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
	notes, _, scanErr := scanVault(root)
	if scanErr != nil {
		return nil, scanErr
	}
	idx := NewNoteIndex(vaultID)
	for _, p := range notes {
		idx.Notes[NewVaultID()] = NoteEntry{Path: p}
	}
	if err := idx.validate(); err != nil {
		return nil, err
	}
	data, err := idx.Serialize()
	if err != nil {
		return nil, err
	}
	if err := writeDurableNote(root, data); err != nil {
		return nil, err
	}
	return newNoteStore(root, osIndexIO{}, idx), nil
}

// writeNoteIndex validates and durably writes idx WITHOUT changing the store's
// in-memory index. A durable-write failure is indeterminate, so the store is
// poisoned and the caller must Reload.
func (s *NoteStore) writeNoteIndex(idx *NoteIndex) error {
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
	if err := writeDurableNoteWith(s.io, s.root, data); err != nil {
		s.poisoned = true
		return fmt.Errorf("%w: %v", ErrPoisoned, err)
	}
	return nil
}

// Save rewrites the note-only index durably if any structural change is
// pending. A content-only save (dirty == false) is a no-op. A failed durable
// write poisons the store; once poisoned, Save returns ErrPoisoned even when
// clean.
func (s *NoteStore) Save() error {
	if s == nil {
		return nil
	}
	if s.poisoned {
		return ErrPoisoned
	}
	if !s.dirty {
		return nil
	}
	if err := s.writeNoteIndex(s.Index); err != nil {
		return err
	}
	s.writes++
	s.dirty = false
	return nil
}

// ReplaceIndex atomically swaps the store's index for a fully-built one,
// committed durably BEFORE the in-memory swap. It is the commit point for batch
// identity mutations that must never leave a half-applied state.
func (s *NoteStore) ReplaceIndex(idx *NoteIndex) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if err := s.writeNoteIndex(idx); err != nil {
		return err
	}
	s.Index = idx
	s.writes++
	s.dirty = false
	return nil
}

// Reload re-reads the index from disk (through the store's io), clearing any
// poison left by a failed durable write.
func (s *NoteStore) Reload() error {
	fresh, err := loadNoteStoreWithIO(s.root, s.io)
	if err != nil {
		return err
	}
	s.Index = fresh.Index
	s.dirty = false
	s.poisoned = false
	return nil
}

// Root returns the vault root this index belongs to.
func (s *NoteStore) Root() string { return s.root }

// Len reports the number of indexed notes.
func (s *NoteStore) Len() int { return len(s.Index.Notes) }

// PathByID returns the local Markdown path for a Sync ID, if indexed.
func (s *NoteStore) PathByID(syncID string) (string, bool) {
	e, ok := s.Index.Notes[syncID]
	return e.Path, ok
}

// IDByPath returns the Sync ID for a vault path, if indexed.
func (s *NoteStore) IDByPath(path string) (string, bool) {
	for syncID, e := range s.Index.Notes {
		if e.Path == path {
			return syncID, true
		}
	}
	return "", false
}

// AddNote records a new note identity. It is idempotent for the same Sync ID
// and the same path (no structural change), but it never silently moves an
// existing Sync ID to another path — that is UpdatePath's job — and rejects a
// path already indexed by a different Sync ID. Conflict reservation in the
// coordinator depends on this: replaying a reservation succeeds only when the
// identity and path match exactly.
func (s *NoteStore) AddNote(syncID, path string) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if !cloudsync.IsSyncID(syncID) {
		return fmt.Errorf("invalid syncId %q", syncID)
	}
	if !cloudsync.ValidNotePath(path) {
		return fmt.Errorf("unsafe path %q", path)
	}
	if e, ok := s.Index.Notes[syncID]; ok {
		if e.Path == path {
			return nil // idempotent no-op
		}
		return fmt.Errorf("syncId %s is already mapped to %q, not %q", syncID, e.Path, path)
	}
	if s.Index.Notes == nil {
		s.Index.Notes = make(map[string]NoteEntry)
	}
	if prev, ok := s.IDByPath(path); ok && prev != syncID {
		return fmt.Errorf("path %q already indexed as %s", path, prev)
	}
	s.Index.Notes[syncID] = NoteEntry{Path: path}
	s.dirty = true
	return nil
}

// UpdatePath moves an existing Sync ID to a new local Markdown path.
func (s *NoteStore) UpdatePath(syncID, newPath string) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if !cloudsync.ValidNotePath(newPath) {
		return fmt.Errorf("unsafe path %q", newPath)
	}
	e, ok := s.Index.Notes[syncID]
	if !ok {
		return fmt.Errorf("unknown syncId %s", syncID)
	}
	if e.Path == newPath {
		return nil // no structural change
	}
	if prev, ok := s.IDByPath(newPath); ok && prev != syncID {
		return fmt.Errorf("path %q already indexed as %s", newPath, prev)
	}
	e.Path = newPath
	s.Index.Notes[syncID] = e
	s.dirty = true
	return nil
}

// RemoveNote drops a Sync ID from the index. It refuses a poisoned store so a
// mutation can never diverge memory further from the indeterminate disk state.
func (s *NoteStore) RemoveNote(syncID string) error {
	if s.poisoned {
		return ErrPoisoned
	}
	if s.Index.Notes == nil {
		s.Index.Notes = make(map[string]NoteEntry)
	}
	if _, ok := s.Index.Notes[syncID]; !ok {
		return nil
	}
	delete(s.Index.Notes, syncID)
	s.dirty = true
	return nil
}

// SortedIDs returns the indexed Sync IDs in sorted order, the deterministic
// iteration order a note coordinator processes notes in.
func (s *NoteStore) SortedIDs() []string {
	ids := make([]string, 0, len(s.Index.Notes))
	for id := range s.Index.Notes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// writeDurableNote persists data to <root>/.memodump/sync-index.json following
// the portable-index durability sequence. It is the note-only entry point,
// mirroring writeDurable but judging "last known good" with the v2 parser so a
// valid v2 primary is preserved as the backup instead of being mistaken for an
// invalid document.
func writeDurableNote(root string, data []byte) error {
	return writeDurableNoteWith(osIndexIO{}, root, data)
}

// writeDurableNoteWith is writeDurableNote over an explicit indexIO. The .bak
// file is written from the last VALID v2 document (primary, else backup, else
// the new bytes), so a valid v2 primary is preserved as the backup and a
// corrupt primary never clobbers a good backup. Both files always hold a
// parseable note-only index. Real I/O errors reading the current primary or
// backup stop the save — the identity files are never rewritten on top of
// unreadable state.
func writeDurableNoteWith(io indexIO, root string, data []byte) error {
	if err := checkMetadataSafe(root); err != nil {
		return err
	}
	dir := filepath.Join(root, DirName)
	target := filepath.Join(dir, IndexName)
	backup := filepath.Join(dir, BackupName)

	backupBytes, err := knownGoodNoteBytes(io, target, backup, data)
	if err != nil {
		return err
	}
	if err := io.WriteFileAtomic(dir, IndexName, data); err != nil {
		return err
	}
	// Skip rewriting .bak when it already holds the known-good bytes so a
	// steady-state structural save is one rename, not two.
	b, err := io.ReadFile(backup)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read sync-index backup: %w", err)
		}
	} else if bytes.Equal(b, backupBytes) {
		return io.SyncDir(dir)
	}
	if err := io.WriteFileAtomic(dir, BackupName, backupBytes); err != nil {
		return err
	}
	return io.SyncDir(dir)
}

// knownGoodNoteBytes returns the last parseable note-only index bytes: the
// current primary if valid, else the current backup if valid, else fallback
// (the bytes being written — the only known-good state, e.g. first enable or
// rebuild). A missing file, structural corruption, or an old schema authorizes
// moving on to the next candidate; any other read error (permission, device
// I/O) stops the save with an error.
func knownGoodNoteBytes(io indexIO, primary, backup string, fallback []byte) ([]byte, error) {
	if b, err := io.ReadFile(primary); err == nil {
		if _, perr := ParseNoteIndex(b); perr == nil {
			return b, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read sync-index primary: %w", err)
	}
	if b, err := io.ReadFile(backup); err == nil {
		if _, perr := ParseNoteIndex(b); perr == nil {
			return b, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read sync-index backup: %w", err)
	}
	return fallback, nil
}
