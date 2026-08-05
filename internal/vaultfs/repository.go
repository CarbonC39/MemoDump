package vaultfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotFound reports a missing note or folder.
	ErrNotFound = errors.New("note not found")
	// ErrNameConflict reports that a destination path already exists.
	ErrNameConflict = errors.New("a note with that name already exists")
	// ErrInvalidPath reports a path that escapes the repository root or is
	// otherwise unusable.
	ErrInvalidPath = errors.New("path is illegal")
	// ErrRevisionConflict reports an update/delete whose base revision no
	// longer matches the durable content — the writer is stale.
	ErrRevisionConflict = errors.New("local revision conflict")
)

// previewLimit caps the preview text a note summary carries.
const previewLimit = 1000

// Note is a Markdown note within a Repository. Path is slash-relative to the
// repository root. Markdown is the full canonical document; Content is the body
// with front matter stripped. Revision is the opaque content digest of
// Markdown — the local CAS token. It is computed from the raw file bytes and is
// never compared across repositories or replicas.
type Note struct {
	Path     string   `json:"path"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags"`
	ModTime  int64    `json:"modTime"`
	Content  string   `json:"content,omitempty"`
	Preview  string   `json:"preview,omitempty"`
	Markdown string   `json:"-"`
	Revision string   `json:"-"`
}

// Folder is a directory node in the repository's folder tree.
type Folder struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Children []Folder `json:"children,omitempty"`
	Notes    []Note   `json:"notes,omitempty"`
}

// CreateOptions describes a new note created from body + tags (front matter is
// built from tags).
type CreateOptions struct {
	Name    string
	Folder  string // slash-relative; "" = root
	Content string // body only
	Tags    []string
}

// UpdateOptions describes a note update. nil pointers mean "keep current";
// BaseRevision "" means "no CAS check" (legacy compatibility). Rename and
// Destination are applied together (target = Destination / newName), so
// content, rename and move commit as one CAS-guarded mutation.
type UpdateOptions struct {
	Content      *string
	Tags         *[]string
	Rename       *string
	Destination  *string
	BaseRevision string
}

// Repository is the filesystem note repository. It is the only component
// allowed to materialize changes in the vault. All writes are atomic
// (sibling temp + rename) and serialized per path; note updates and deletes can
// be guarded by an expected revision for optimistic concurrency.
type Repository struct {
	root  string
	locks *lockManager
	cache *noteCache
}

// New creates a repository rooted at an absolute copy of root.
func New(root string) (*Repository, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Repository{
		root:  abs,
		locks: newLockManager(),
		cache: newNoteCache(),
	}, nil
}

// Root returns the absolute repository root.
func (r *Repository) Root() string { return r.root }

func (r *Repository) resolve(rel string) (string, error) {
	abs, err := SafePath(r.root, rel)
	if err != nil {
		return "", ErrInvalidPath
	}
	return abs, nil
}

// Rel converts an absolute path inside the repository to a slash-relative one.
func (r *Repository) Rel(abs string) (string, error) {
	rel, err := filepath.Rel(r.root, abs)
	if err != nil {
		return "", ErrInvalidPath
	}
	return filepath.ToSlash(rel), nil
}

// rejectReserved returns ErrInvalidPath when a note write would materialize a
// file inside a reserved repository directory (the image vault or sync
// metadata). Read paths are deliberately not rejected: sync metadata files live
// in .memodump and must remain readable by their owner.
func rejectReserved(rel string) error {
	if ContainsReservedSegment(rel) {
		return ErrInvalidPath
	}
	return nil
}

// RevisionOfBytes returns the opaque content digest of raw file bytes.
func RevisionOfBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// --- cache ------------------------------------------------------------------

type cacheEntry struct {
	note     Note
	body     string
	revision string
	// modNano is the file mtime at nanosecond resolution. ms-granularity alone
	// could be fooled by an external write of the same size within the same
	// millisecond; the CAS paths re-read fresh under the lock regardless, so
	// this narrows (does not fully close) the listing staleness window.
	modNano int64
	size    int64
}

type noteCache struct {
	mu     sync.RWMutex
	byPath map[string]*cacheEntry
}

func newNoteCache() *noteCache {
	return &noteCache{byPath: make(map[string]*cacheEntry)}
}

func (c *noteCache) get(abs string) (*cacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byPath[abs]
	return e, ok
}

func (c *noteCache) put(abs string, e *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byPath[abs] = e
}

func (c *noteCache) delete(abs string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byPath, abs)
}

func (c *noteCache) deletePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.byPath {
		if strings.HasPrefix(k, prefix) {
			delete(c.byPath, k)
		}
	}
}

// --- reads ------------------------------------------------------------------

// Get returns a note. With includeContent true, Content carries the body;
// Markdown and Revision are always populated.
func (r *Repository) Get(rel string, includeContent bool) (*Note, error) {
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}
	return r.getAbs(abs, rel, includeContent)
}

func (r *Repository) getAbs(abs, rel string, includeContent bool) (*Note, error) {
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			r.cache.delete(abs)
			return nil, ErrNotFound
		}
		return nil, err
	}
	modNano := info.ModTime().UnixNano()
	size := info.Size()

	if e, ok := r.cache.get(abs); ok && e.modNano == modNano && e.size == size {
		n := e.note
		n.Tags = append([]string(nil), e.note.Tags...)
		if includeContent {
			n.Content = e.body
		}
		return &n, nil
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			r.cache.delete(abs)
			return nil, ErrNotFound
		}
		return nil, err
	}
	doc := ParseDocument(string(data))
	note := r.buildNote(rel, info, doc)
	note.Markdown = string(data)
	note.Revision = RevisionOfBytes(data)
	r.cache.put(abs, &cacheEntry{
		note:     note,
		body:     doc.Body,
		revision: note.Revision,
		modNano:  modNano,
		size:     size,
	})

	out := note
	if includeContent {
		out.Content = doc.Body
	}
	return &out, nil
}

func (r *Repository) buildNote(rel string, info os.FileInfo, doc *Document) Note {
	name := strings.TrimSuffix(path.Base(rel), ".md")
	preview := strings.TrimSpace(doc.Body)
	if truncated, ok := truncateRunes(preview, previewLimit); ok {
		preview = truncated + "..."
	}
	return Note{
		Path:    rel,
		Name:    name,
		Tags:    doc.Tags,
		ModTime: info.ModTime().UnixMilli(),
		Preview: preview,
	}
}

// ListNotes returns summary notes in a folder (rel, "" = root), sorted by
// modified time descending. A missing folder returns ErrNotFound.
func (r *Repository) ListNotes(rel string) ([]Note, error) {
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var notes []Note
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		noteRel := path.Join(rel, e.Name())
		if n, err := r.Get(noteRel, false); err == nil {
			notes = append(notes, *n)
		}
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].ModTime > notes[j].ModTime })
	return notes, nil
}

// --- writes -----------------------------------------------------------------

// writeAtomic writes data to target via a unique sibling temp file and an
// atomic rename, so a crash never leaves a partially written note.
func writeAtomic(target string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".memodump-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, target)
}

func timestampBase() string { return time.Now().Format("2006-01-02_150405") }

// Create writes a new note built from body + tags, de-colliding on an existing
// path. The caller's name is sanitized first. The existence check and the write
// happen under the same path lock, so concurrent creates of the same name
// serialize.
func (r *Repository) Create(opts CreateOptions) (*Note, error) {
	name := SanitizeName(opts.Name)
	if name == "" {
		name = timestampBase()
	}
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}

	rel := name
	if opts.Folder != "" {
		rel = path.Join(opts.Folder, name)
	}
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}

	release := r.locks.acquire(rel)
	defer release()

	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}

	doc := &Document{Body: opts.Content, Tags: opts.Tags}
	markdown, err := doc.WithTags(opts.Tags)
	if err != nil {
		return nil, err
	}

	for {
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			break
		}
		name = timestampBase() + "_" + name
		rel = name
		if opts.Folder != "" {
			rel = path.Join(opts.Folder, name)
		}
		abs, err = r.resolve(rel)
		if err != nil {
			return nil, err
		}
	}

	if err := writeAtomic(abs, []byte(markdown), 0644); err != nil {
		return nil, err
	}
	r.cache.delete(abs)
	return r.Get(rel, true)
}

// CreateMarkdown writes a note whose full Markdown document is stored verbatim
// (front matter preserved byte-for-byte). It de-collides on an existing path
// under the same path lock. Used by import/upload and the future sync apply
// path.
func (r *Repository) CreateMarkdown(name, folder, markdown string) (*Note, error) {
	clean := SanitizeName(name)
	if clean == "" {
		clean = timestampBase()
	}
	if !strings.HasSuffix(clean, ".md") {
		clean += ".md"
	}

	rel := clean
	if folder != "" {
		rel = path.Join(folder, clean)
	}
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}

	release := r.locks.acquire(rel)
	defer release()

	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}
	for {
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			break
		}
		clean = timestampBase() + "_" + clean
		rel = clean
		if folder != "" {
			rel = path.Join(folder, clean)
		}
		abs, err = r.resolve(rel)
		if err != nil {
			return nil, err
		}
	}

	if err := writeAtomic(abs, []byte(markdown), 0644); err != nil {
		return nil, err
	}
	r.cache.delete(abs)
	return r.Get(rel, true)
}

// Update rewrites a note's body and/or tags (and optionally renames it), under
// the source and target path locks. When BaseRevision is set it must match the
// current durable revision or ErrRevisionConflict is returned and nothing is
// written.
func (r *Repository) Update(rel string, opts UpdateOptions) (*Note, error) {
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}

	targetRel := rel
	targetAbs := abs
	if opts.Rename != nil {
		newName := SanitizeName(*opts.Rename)
		if newName == "" {
			newName = timestampBase()
		}
		if !strings.HasSuffix(newName, ".md") {
			newName += ".md"
		}
		targetRel = path.Join(path.Dir(rel), newName)
	}
	if opts.Destination != nil {
		targetRel = path.Join(*opts.Destination, path.Base(targetRel))
	}
	if targetRel != rel {
		targetAbs, err = r.resolve(targetRel)
		if err != nil {
			return nil, err
		}
	}
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	if targetRel != rel {
		if err := rejectReserved(targetRel); err != nil {
			return nil, err
		}
	}

	locks := []string{rel}
	if targetRel != rel {
		locks = append(locks, targetRel)
	}
	err = r.locks.withLock(locks, func() error {
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			return ErrNotFound
		}
		if targetAbs != abs {
			if _, err := os.Stat(targetAbs); err == nil {
				return ErrNameConflict
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetAbs), 0755); err != nil {
				return err
			}
		}

		// Fresh read under the lock (bypassing the cache) so the CAS check and
		// the write are one atomic unit.
		current, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		if opts.BaseRevision != "" && RevisionOfBytes(current) != opts.BaseRevision {
			return ErrRevisionConflict
		}

		doc := ParseDocument(string(current))
		newTags := doc.Tags
		if opts.Tags != nil {
			newTags = *opts.Tags
		}
		newBody := doc.Body
		if opts.Content != nil {
			newBody = *opts.Content
		}
		if targetRel == rel && tagsEqual(doc.Tags, newTags) && newBody == doc.Body {
			// No semantic change and no move/rename: leave the file bytes
			// untouched so the revision and any preserved front-matter
			// formatting stay identical.
			return nil
		}
		prefix, err := doc.FrontMatterPartWithTags(newTags)
		if err != nil {
			return err
		}
		if err := writeAtomic(targetAbs, []byte(prefix+newBody), 0644); err != nil {
			return err
		}
		if targetAbs != abs {
			r.cache.delete(abs)
			if err := os.Remove(abs); err != nil {
				return err
			}
		}
		r.cache.delete(targetAbs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.Get(targetRel, true)
}

// Delete removes a note. When baseRevision is set it must match the current
// durable revision or ErrRevisionConflict is returned and nothing is deleted.
func (r *Repository) Delete(rel, baseRevision string) error {
	if err := rejectReserved(rel); err != nil {
		return err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return err
	}
	return r.locks.withLock([]string{rel}, func() error {
		current, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}
			return err
		}
		if baseRevision != "" && RevisionOfBytes(current) != baseRevision {
			return ErrRevisionConflict
		}
		if err := os.Remove(abs); err != nil {
			return err
		}
		r.cache.delete(abs)
		return nil
	})
}

// Move relocates a note to a folder. destination "" means root.
func (r *Repository) Move(rel, destination string) (*Note, error) {
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}
	newRel := path.Join(destination, path.Base(rel))
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	if err := rejectReserved(newRel); err != nil {
		return nil, err
	}
	if newRel == rel {
		return r.Get(rel, true)
	}
	newAbs, err := r.resolve(newRel)
	if err != nil {
		return nil, err
	}

	err = r.locks.withLock([]string{rel, newRel}, func() error {
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			return ErrNotFound
		}
		if _, err := os.Stat(newAbs); err == nil {
			return ErrNameConflict
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(newAbs), 0755); err != nil {
			return err
		}
		if err := os.Rename(abs, newAbs); err != nil {
			return err
		}
		r.cache.delete(abs)
		r.cache.delete(newAbs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.Get(newRel, true)
}

// Duplicate copies a note's raw bytes to a "(copy)"-suffixed sibling name.
func (r *Repository) Duplicate(rel string) (*Note, error) {
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	dir := path.Dir(rel)
	base := strings.TrimSuffix(path.Base(rel), ".md")
	resultRel := ""
	// Serialize duplicates of the same source under the source's lock, and pick
	// the first free "(copy)" name inside that lock, so two concurrent
	// duplicates never target the same path.
	err = r.locks.withLock([]string{rel}, func() error {
		for n := 1; ; n++ {
			name := fmt.Sprintf("%s (copy).md", base)
			if n > 1 {
				name = fmt.Sprintf("%s (copy %d).md", base, n)
			}
			candidateRel := name
			if dir != "." {
				candidateRel = path.Join(dir, name)
			}
			candidateAbs, err := r.resolve(candidateRel)
			if err != nil {
				return err
			}
			_, statErr := os.Stat(candidateAbs)
			switch {
			case statErr == nil:
				// Candidate exists: try the next name.
			case os.IsNotExist(statErr):
				if err := writeAtomic(candidateAbs, data, 0644); err != nil {
					return err
				}
				r.cache.delete(candidateAbs)
				resultRel = candidateRel
				return nil
			default:
				return statErr
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return r.Get(resultRel, true)
}

// Apply materializes a note from its canonical Markdown, replacing content only
// when expectedRevision matches the current durable revision. expectedRevision
// "" means create-if-absent. This is the boundary the future sync worker and
// external scans will use; nothing in Phase 0 calls it yet.
func (r *Repository) Apply(rel, markdown, expectedRevision string) (*Note, error) {
	if err := rejectReserved(rel); err != nil {
		return nil, err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return nil, err
	}
	err = r.locks.withLock([]string{rel}, func() error {
		current, readErr := os.ReadFile(abs)
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		if os.IsNotExist(readErr) {
			// create-if-absent.
			if expectedRevision != "" {
				return ErrRevisionConflict
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
				return err
			}
		} else if expectedRevision == "" {
			// create-if-absent was requested but the file already exists; never
			// silently overwrite a local note on a sync first delivery.
			return ErrRevisionConflict
		} else if RevisionOfBytes(current) != expectedRevision {
			return ErrRevisionConflict
		}
		if err := writeAtomic(abs, []byte(markdown), 0644); err != nil {
			return err
		}
		r.cache.delete(abs)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.Get(rel, true)
}

// --- folders ----------------------------------------------------------------

// CreateFolder makes a directory (and its ancestors) inside the repository.
func (r *Repository) CreateFolder(rel string) error {
	if ContainsReservedSegment(rel) {
		return ErrInvalidPath
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return err
	}
	release := r.locks.acquire(rel)
	defer release()
	return os.MkdirAll(abs, 0755)
}

// RenameFolder renames a directory, invalidating cached notes under it.
func (r *Repository) RenameFolder(rel, newName string) error {
	if err := rejectReserved(rel); err != nil {
		return ErrInvalidPath
	}
	newRel := path.Join(path.Dir(rel), newName)
	if ContainsReservedSegment(newRel) {
		return ErrInvalidPath
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return err
	}
	newAbs, err := r.resolve(newRel)
	if err != nil {
		return err
	}
	return r.locks.withLock([]string{rel, newRel}, func() error {
		if err := os.Rename(abs, newAbs); err != nil {
			if os.IsNotExist(err) {
				return ErrNotFound
			}
			return err
		}
		r.cache.deletePrefix(abs + string(filepath.Separator))
		return nil
	})
}

// DeleteFolder removes a directory and all its contents. Reserved directories
// (the image vault and the sync metadata directory) are never deletable through
// the note repository: deleting .memodump would destroy the vault's identity.
func (r *Repository) DeleteFolder(rel string) error {
	if err := rejectReserved(rel); err != nil {
		return err
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return err
	}
	release := r.locks.acquire(rel)
	defer release()
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	r.cache.deletePrefix(abs + string(filepath.Separator))
	return nil
}

// MoveFolder relocates a directory (and its contents) to a new parent.
func (r *Repository) MoveFolder(rel, destination string) error {
	if err := rejectReserved(rel); err != nil {
		return ErrInvalidPath
	}
	newRel := path.Join(destination, path.Base(rel))
	if ContainsReservedSegment(newRel) {
		return ErrInvalidPath
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return err
	}
	if newRel == rel {
		return nil
	}
	newAbs, err := r.resolve(newRel)
	if err != nil {
		return err
	}
	// Prevent moving a folder into itself or any of its descendants.
	if strings.HasPrefix(newAbs+string(filepath.Separator), abs+string(filepath.Separator)) {
		return ErrInvalidPath
	}
	return r.locks.withLock([]string{rel, newRel}, func() error {
		if _, err := os.Stat(newAbs); err == nil {
			return ErrNameConflict
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(newAbs), 0755); err != nil {
			return err
		}
		if err := os.Rename(abs, newAbs); err != nil {
			return err
		}
		r.cache.deletePrefix(abs + string(filepath.Separator))
		return nil
	})
}

// FolderTree builds the recursive folder tree, hiding dot-prefixed entries
// (the image vault, sync metadata) and including summary notes per folder.
func (r *Repository) FolderTree() []Folder {
	return r.folderNodes(r.root, "")
}

func (r *Repository) folderNodes(absDir, relDir string) []Folder {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}
	var folders []Folder
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		childAbs := filepath.Join(absDir, e.Name())
		childRel := e.Name()
		if relDir != "" {
			childRel = relDir + "/" + e.Name()
		}
		subEntries, _ := os.ReadDir(childAbs)
		var notes []Note
		for _, se := range subEntries {
			if !se.IsDir() && strings.HasSuffix(se.Name(), ".md") {
				noteRel := childRel + "/" + se.Name()
				if n, err := r.Get(noteRel, false); err == nil {
					notes = append(notes, *n)
				}
			}
		}
		folders = append(folders, Folder{
			Name:     e.Name(),
			Path:     childRel,
			Children: r.folderNodes(childAbs, childRel),
			Notes:    notes,
		})
	}
	return folders
}
