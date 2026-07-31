package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxBodySize = 10 << 20 // 10 MB

// safePath prevents directory traversal attacks by confining the resolved path
// to base. base is expected to be absolute (dataDir is made absolute at startup),
// so the returned path is absolute too.
//
// Symlinks are deliberately not resolved here: filepath.EvalSymlinks fails for
// paths that don't exist yet (every note/folder creation hits that case), and it
// would also reject a perfectly valid data dir that itself sits behind a symlink
// (e.g. macOS /tmp -> /private/tmp). The API never creates symlinks, so the only
// way one could appear inside the data dir is via direct filesystem access, which
// is already a trusted boundary. A lexical containment check is what matters for
// blocking "../" traversal.
func safePath(base string, userPath string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base path: %w", err)
	}

	// filepath.Join cleans the result and neutralizes a leading "/" in userPath.
	absFull := filepath.Join(absBase, filepath.FromSlash(userPath))

	rel, err := filepath.Rel(absBase, absFull)
	if err != nil {
		return "", fmt.Errorf("path out of bounds: %s", userPath)
	}
	// rel == ".." means the parent; a "../" prefix means it escaped base. The
	// trailing separator avoids a false positive on a real file named "..foo".
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path out of bounds: %s", userPath)
	}

	return absFull, nil
}

type NoteCacheEntry struct {
	Note    Note
	ModTime int64
	Size    int64
	Body    string
}

var noteCache sync.Map

// Note represents a markdown note
type Note struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Content string   `json:"content,omitempty"`
	Tags    []string `json:"tags"`
	ModTime int64    `json:"modTime"`
	Preview string   `json:"preview,omitempty"`
}

// Folder represents a directory node
type Folder struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Children []Folder `json:"children,omitempty"`
	Notes    []Note   `json:"notes,omitempty"`
}

// parseFrontMatter splits leading YAML front matter from the body. It scans the
// header line by line (cost independent of body size); the closing fence must be a
// line that is exactly "---", so a Markdown horizontal rule later in the document
// is not mistaken for the terminator. If no such fence exists the whole content is
// treated as the body. CRLF line endings are tolerated.
func parseFrontMatter(content string) (tags []string, body string) {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return nil, content
	}

	var fmLines []string
	var hasClosingFence bool
	var bodyStartIndex int

	currentPos := 0
	isFirst := true

	for line := range strings.Lines(content) {
		lineLen := len(line)
		// skip the first "---"
		if isFirst {
			isFirst = false
			currentPos += lineLen
			continue
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "---" {
			hasClosingFence = true
			bodyStartIndex = currentPos + lineLen
			break
		}
		fmLines = append(fmLines, trimmed)
		currentPos += lineLen
	}

	if !hasClosingFence {
		return nil, content
	}

	body = content[bodyStartIndex:]

	for _, line := range fmLines {
		if !strings.HasPrefix(line, "tags:") {
			continue
		}

		val := strings.TrimSpace(line[5:])
		// buildFrontMatter writes a JSON-compatible array. Decode that first so
		// commas, quotes and backslashes inside tags round-trip correctly.
		if err := json.Unmarshal([]byte(val), &tags); err != nil {
			// Backward compatibility with older unquoted `tags: [a, b]` files.
			val = strings.TrimPrefix(val, "[")
			val = strings.TrimSuffix(val, "]")
			for tag := range strings.SplitSeq(val, ",") {
				tag = strings.Trim(strings.TrimSpace(tag), `"'`)
				if tag != "" {
					tags = append(tags, tag)
				}
			}
		}
		break
	}
	return tags, body
}

// truncateRunes returns s clamped to at most max runes, never splitting a
// multi-byte UTF-8 character mid-rune (a plain s[:max] byte slice can).
func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", false
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	count := 0
	for byteIdx := range s {
		if count == max {
			return s[:byteIdx], true
		}
		count++
	}
	return s, false
}

func buildFrontMatter(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("---\ntags: [")
	for i, tag := range tags {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%q", tag)
	}
	sb.WriteString("]\n---\n")
	return sb.String()
}

func readNote(fullPath string, basePath string, includeContent bool) (*Note, error) {
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			noteCache.Delete(fullPath)
		}
		return nil, err
	}
	modTime := info.ModTime().UnixMilli()
	size := info.Size()

	if entry, ok := noteCache.Load(fullPath); ok {
		cached := entry.(*NoteCacheEntry)
		if cached.ModTime == modTime && cached.Size == size {
			noteCopy := cached.Note
			// Defensive copy so a caller mutating Tags can't corrupt the cache.
			if len(cached.Note.Tags) > 0 {
				noteCopy.Tags = append([]string(nil), cached.Note.Tags...)
			}
			if includeContent {
				// Body is cached, so a content read on a cache hit no longer
				// re-reads/re-parses the file (and can't silently return empty).
				noteCopy.Content = cached.Body
			}
			return &noteCopy, nil
		}
	}

	relPath, err := filepath.Rel(basePath, fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get relative path: %w", err)
	}
	relPath = filepath.ToSlash(relPath)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			noteCache.Delete(fullPath)
		}
		return nil, err
	}

	content := string(data)
	tags, body := parseFrontMatter(content)

	note := Note{
		Path:    relPath,
		Name:    strings.TrimSuffix(filepath.Base(relPath), ".md"),
		Tags:    tags,
		ModTime: modTime,
	}

	preview, truncated := truncateRunes(strings.TrimSpace(body), 1000)
	if truncated {
		preview += "..."
	}
	note.Preview = preview

	noteCache.Store(fullPath, &NoteCacheEntry{
		Note:    note,
		ModTime: modTime,
		Size:    size,
		Body:    body,
	})

	noteCopy := note
	if includeContent {
		noteCopy.Content = body
	}
	return &noteCopy, nil
}

// handleListNotes lists .md files in a specific directory (default: root)
func handleListNotes(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("folder")
	dir := dataDir
	if folder != "" {
		var err error
		dir, err = safePath(dataDir, folder)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
	}

	var notes []Note
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, notes)
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		note, err := readNote(fullPath, dataDir, false)
		if err != nil {
			continue
		}
		notes = append(notes, *note)
	}

	sort.Slice(notes, func(i, j int) bool {
		return notes[i].ModTime > notes[j].ModTime
	})

	if notes == nil {
		notes = []Note{}
	}
	writeJSON(w, http.StatusOK, notes)
}

// handleGetNote reads a single note
func handleGetNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	if notePath == "" {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}
	fullPath, err := safePath(dataDir, notePath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	note, err := readNote(fullPath, dataDir, true)
	if err != nil {
		http.Error(w, `{"error":"File not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// handleCreateNote creates a new note
func handleCreateNote(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req struct {
		Content string   `json:"content"`
		Name    string   `json:"name"`
		Folder  string   `json:"folder"`
		Tags    []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	// Generate filename. Sanitize the user-supplied name first so it cannot
	// contain path separators or traversal segments (e.g. "../../evil").
	filename := sanitizeUploadName(req.Name)
	if filename == "" {
		filename = time.Now().Format("2006-01-02_150405")
	}
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}

	dir := dataDir
	if req.Folder != "" {
		var err error
		dir, err = safePath(dataDir, req.Folder)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
		os.MkdirAll(dir, 0755)
	}
	// Defense in depth: confirm the assembled path stays inside the data dir.
	fullPath, err := safePath(dir, filename)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	// Avoid overwriting
	if _, err := os.Stat(fullPath); err == nil {
		filename = time.Now().Format("2006-01-02_150405") + "_" + filename
		fullPath = filepath.Join(dir, filename)
	}

	fm := buildFrontMatter(req.Tags)
	content := fm + req.Content

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}

	note, _ := readNote(fullPath, dataDir, true)
	writeJSON(w, http.StatusCreated, note)
}

// handleUpdateNote updates an existing note
func handleUpdateNote(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	notePath := r.PathValue("path")
	fullPath, err := safePath(dataDir, notePath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, `{"error":"File not found"}`, http.StatusNotFound)
		return
	}

	var req struct {
		Content *string  `json:"content"`
		Tags    []string `json:"tags"`
		Rename  *string  `json:"rename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	// Read existing content
	data, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, `{"error":"Failed to read note"}`, http.StatusInternalServerError)
		return
	}

	_, existingBody := parseFrontMatter(string(data))

	body := existingBody
	if req.Content != nil {
		body = *req.Content
	}

	fm := buildFrontMatter(req.Tags)
	finalContent := fm + body

	// Determine target path upfront (rename if requested)
	targetPath := fullPath
	if req.Rename != nil {
		newName := sanitizeUploadName(*req.Rename)
		if newName == "" {
			newName = time.Now().Format("2006-01-02_150405")
		}
		if !strings.HasSuffix(newName, ".md") {
			newName += ".md"
		}
		newRel := filepath.Join(filepath.Dir(notePath), newName)
		newPath, err := safePath(dataDir, newRel)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
		targetPath = newPath
		if targetPath != fullPath {
			if _, err := os.Stat(targetPath); err == nil {
				http.Error(w, `{"error":"A note with that name already exists"}`, http.StatusConflict)
				return
			} else if !os.IsNotExist(err) {
				http.Error(w, `{"error":"Failed to check target note"}`, http.StatusInternalServerError)
				return
			}
		}
	}

	// Use a unique temporary file so concurrent saves never share target+".tmp".
	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".memodump-*.tmp")
	if err != nil {
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}
	if err := tmpFile.Chmod(0644); err != nil {
		cleanupTmp()
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}
	if _, err := tmpFile.Write([]byte(finalContent)); err != nil {
		cleanupTmp()
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		os.Remove(tmpPath)
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}
	// If the file was renamed, remove the old path and its cache entry.
	if targetPath != fullPath {
		noteCache.Delete(fullPath)
		os.Remove(fullPath)
	}
	// Invalidate the target's cache so the re-read below reflects this write even
	// if the filesystem reports an unchanged mtime/size for the rewrite.
	noteCache.Delete(targetPath)

	note, _ := readNote(targetPath, dataDir, true)
	writeJSON(w, http.StatusOK, note)
}

// handleDeleteNote deletes a note
func handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	fullPath, err := safePath(dataDir, notePath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	if err := os.Remove(fullPath); err != nil {
		http.Error(w, `{"error":"Failed to delete note"}`, http.StatusInternalServerError)
		return
	}
	noteCache.Delete(fullPath)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMoveNote moves a note to a different folder
func handleMoveNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	fullPath, err := safePath(dataDir, notePath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Destination string `json:"destination"` // empty string means root
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	destDir := dataDir
	if req.Destination != "" {
		destDir, err = safePath(dataDir, req.Destination)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
		os.MkdirAll(destDir, 0755)
	}

	newPath := filepath.Join(destDir, filepath.Base(fullPath))
	if newPath == fullPath {
		// Moving to the same location is a no-op
		note, _ := readNote(fullPath, dataDir, false)
		writeJSON(w, http.StatusOK, note)
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		http.Error(w, `{"error":"A note with that name already exists in the destination"}`, http.StatusConflict)
		return
	}

	if err := os.Rename(fullPath, newPath); err != nil {
		http.Error(w, `{"error":"Failed to move note"}`, http.StatusInternalServerError)
		return
	}
	noteCache.Delete(fullPath)

	note, _ := readNote(newPath, dataDir, false)
	writeJSON(w, http.StatusOK, note)
}

// handleDuplicateNote creates a copy of a note in the same folder. The raw file
// bytes are copied verbatim so YAML front matter and tags survive byte-for-byte.
// The copy is named "<base> (copy).md", de-colliding as "(copy 2)", "(copy 3)"...
func handleDuplicateNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	fullPath, err := safePath(dataDir, notePath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, `{"error":"File not found"}`, http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, `{"error":"Failed to read note"}`, http.StatusInternalServerError)
		return
	}

	dir := filepath.Dir(fullPath)
	base := strings.TrimSuffix(filepath.Base(fullPath), ".md")
	candidate := base + " (copy).md"
	n := 2
	for {
		destPath, err := safePath(dir, candidate)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			// Atomic write: write to .tmp then rename, matching the save path.
			tmpPath := destPath + ".tmp"
			if err := os.WriteFile(tmpPath, data, 0644); err != nil {
				http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
				return
			}
			if err := os.Rename(tmpPath, destPath); err != nil {
				os.Remove(tmpPath)
				http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
				return
			}
			note, _ := readNote(destPath, dataDir, true)
			writeJSON(w, http.StatusCreated, note)
			return
		}
		candidate = fmt.Sprintf("%s (copy %d).md", base, n)
		n++
	}
}

// handleListFolders returns folder tree
func handleListFolders(w http.ResponseWriter, r *http.Request) {
	tree := buildFolderTree(dataDir, dataDir)
	writeJSON(w, http.StatusOK, tree)
}

func buildFolderTree(dir string, base string) []Folder {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return buildFolderNodes(dir, base, entries)
}

// buildFolderNodes builds the folder tree from an already-read entries slice,
// avoiding a second ReadDir call per folder that the original code had.
func buildFolderNodes(parentDir string, base string, entries []os.DirEntry) []Folder {
	var folders []Folder
	for _, e := range entries {
		// Skip dot-prefixed directories (e.g. the .images vault) so the legacy
		// folder tree matches the v2 API, which already hides them.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		folderDir := filepath.Join(parentDir, e.Name())
		relPath, _ := filepath.Rel(base, folderDir)
		relPath = filepath.ToSlash(relPath)

		// Single ReadDir per folder: used for both notes and recursive children.
		subEntries, _ := os.ReadDir(folderDir)

		var notes []Note
		for _, se := range subEntries {
			if !se.IsDir() && strings.HasSuffix(se.Name(), ".md") {
				if note, err := readNote(filepath.Join(folderDir, se.Name()), base, false); err == nil {
					notes = append(notes, *note)
				}
			}
		}

		folders = append(folders, Folder{
			Name:     e.Name(),
			Path:     relPath,
			Children: buildFolderNodes(folderDir, base, subEntries),
			Notes:    notes,
		})
	}
	return folders
}

// handleCreateFolder creates a new folder
func handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}
	if containsReservedSegment(req.Path) {
		http.Error(w, `{"error":"Reserved folder name"}`, http.StatusBadRequest)
		return
	}

	fullPath, err := safePath(dataDir, req.Path)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		http.Error(w, `{"error":"Failed to create folder"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok", "path": req.Path})
}

// handleRenameFolder renames a folder
func handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := r.PathValue("path")
	fullPath, err := safePath(dataDir, folderPath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	newRel := filepath.Join(filepath.Dir(folderPath), req.NewName)
	if containsReservedSegment(newRel) {
		http.Error(w, `{"error":"Reserved folder name"}`, http.StatusBadRequest)
		return
	}
	newPath, err := safePath(dataDir, newRel)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}
	if err := os.Rename(fullPath, newPath); err != nil {
		http.Error(w, `{"error":"Failed to rename folder"}`, http.StatusInternalServerError)
		return
	}

	// Invalidate all note cache entries under the renamed folder.
	oldPrefix := fullPath + string(filepath.Separator)
	noteCache.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), oldPrefix) {
			noteCache.Delete(key)
		}
		return true
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteFolder deletes a folder and all contents
func handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := r.PathValue("path")
	fullPath, err := safePath(dataDir, folderPath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	if err := os.RemoveAll(fullPath); err != nil {
		http.Error(w, `{"error":"Failed to delete folder"}`, http.StatusInternalServerError)
		return
	}

	// Invalidate all note cache entries under the deleted folder.
	deletedPrefix := fullPath + string(filepath.Separator)
	noteCache.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), deletedPrefix) {
			noteCache.Delete(key)
		}
		return true
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSearch searches notes by content or tag.
//
// The cache stores only the body; we lowercase it on demand here instead of
// keeping a second lowercased copy per note. Search is infrequent relative to
// reads, so trading a little CPU per query for roughly half the cache memory is
// a good deal.
func handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(r.URL.Query().Get("q"))
	tag := strings.ToLower(r.URL.Query().Get("tag"))

	var results []Note
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		// readNote populates the cache (including Body) on a miss, so the
		// subsequent Load is guaranteed to hit for a successfully read note.
		note, checkErr := readNote(path, dataDir, false)
		if checkErr != nil {
			return nil
		}

		// AND logic: both conditions must match when both are specified.
		matchQuery := query == ""
		if !matchQuery {
			if entry, ok := noteCache.Load(path); ok {
				matchQuery = strings.Contains(strings.ToLower(entry.(*NoteCacheEntry).Body), query)
			}
		}

		matchTag := tag == ""
		if !matchTag {
			for _, t := range note.Tags {
				if strings.ToLower(t) == tag {
					matchTag = true
					break
				}
			}
		}

		if matchQuery && matchTag {
			results = append(results, *note)
		}
		return nil
	})

	sort.Slice(results, func(i, j int) bool {
		return results[i].ModTime > results[j].ModTime
	})

	if results == nil {
		results = []Note{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleMoveFolder moves a folder (and all its contents) to a new parent.
func handleMoveFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := r.PathValue("path")
	fullPath, err := safePath(dataDir, folderPath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Destination string `json:"destination"` // empty = root
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"Request format error"}`, http.StatusBadRequest)
		return
	}

	destDir := dataDir
	if req.Destination != "" {
		if containsReservedSegment(req.Destination) {
			http.Error(w, `{"error":"Reserved folder name"}`, http.StatusBadRequest)
			return
		}
		destDir, err = safePath(dataDir, req.Destination)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
	}

	folderName := filepath.Base(fullPath)
	newPath := filepath.Join(destDir, folderName)

	if newPath == fullPath {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	// Prevent moving a folder into itself or any of its descendants.
	if strings.HasPrefix(newPath+string(filepath.Separator), fullPath+string(filepath.Separator)) {
		http.Error(w, `{"error":"Cannot move folder into itself"}`, http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		http.Error(w, `{"error":"A folder with that name already exists in the destination"}`, http.StatusConflict)
		return
	}

	os.MkdirAll(destDir, 0755)
	if err := os.Rename(fullPath, newPath); err != nil {
		http.Error(w, `{"error":"Failed to move folder"}`, http.StatusInternalServerError)
		return
	}

	oldPrefix := fullPath + string(filepath.Separator)
	noteCache.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), oldPrefix) {
			noteCache.Delete(key)
		}
		return true
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePing is a lightweight authenticated endpoint used by the frontend
// keepalive to prevent session expiry during long idle periods.
func handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleConfig is unauthenticated and returns server configuration the UI needs
// before login (e.g. whether auth is required so the Sign Out button can be hidden).
func handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"noAuth": noAuth})
}

// handleUploadNote accepts a multipart .md/.txt file upload and saves it as a note.
// Security: extension + size + UTF-8 + null-byte + path traversal checks.
func handleUploadNote(w http.ResponseWriter, r *http.Request) {
	const uploadLimit = 1 << 20 // 1 MB

	writeError := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit+4096)
	if err := r.ParseMultipartForm(uploadLimit); err != nil {
		writeError(http.StatusRequestEntityTooLarge, "File too large or invalid request (max 1 MB)")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(http.StatusBadRequest, "No file provided")
		return
	}
	defer file.Close()

	origName := header.Filename
	if idx := strings.LastIndexAny(origName, `/\`); idx != -1 {
		origName = origName[idx+1:]
	}

	ext := strings.ToLower(filepath.Ext(origName))
	if ext != ".md" && ext != ".txt" {
		writeError(http.StatusBadRequest, "Only .md and .txt files are accepted")
		return
	}

	if header.Size > uploadLimit {
		writeError(http.StatusRequestEntityTooLarge, "File too large (max 1 MB)")
		return
	}

	content, err := io.ReadAll(io.LimitReader(file, uploadLimit+1))
	if err != nil {
		writeError(http.StatusInternalServerError, "Failed to read file")
		return
	}
	if len(content) > uploadLimit {
		writeError(http.StatusRequestEntityTooLarge, "File too large (max 1 MB)")
		return
	}

	if !utf8.Valid(content) {
		writeError(http.StatusBadRequest, "File must be valid UTF-8 text")
		return
	}
	if bytes.IndexByte(content, 0) >= 0 {
		writeError(http.StatusBadRequest, "File contains binary data")
		return
	}

	noteName := sanitizeUploadName(strings.TrimSuffix(origName, ext))

	dir := dataDir
	folder := r.FormValue("folder")
	if folder != "" {
		dir, err = safePath(dataDir, folder)
		if err != nil {
			writeError(http.StatusBadRequest, "Invalid folder path")
			return
		}
	}

	filename := noteName
	if filename == "" {
		filename = time.Now().Format("2006-01-02_150405")
	}
	filename += ".md"
	fullPath := filepath.Join(dir, filename)

	if _, err := os.Stat(fullPath); err == nil {
		base := strings.TrimSuffix(filename, ".md")
		filename = base + "_" + time.Now().Format("150405_000000") + ".md"
		fullPath = filepath.Join(dir, filename)
	}

	tmpFile, err := os.CreateTemp(dir, "upload_*.tmp")
	if err != nil {
		writeError(http.StatusInternalServerError, "Failed to create temporary file")
		return
	}
	tmpPath := tmpFile.Name()

	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(content); err != nil {
		writeError(http.StatusInternalServerError, "Failed to write data")
		return
	}
	_ = tmpFile.Close() // Close before rename: Windows can't rename an open file.

	if err := os.Rename(tmpPath, fullPath); err != nil {
		writeError(http.StatusInternalServerError, "Failed to save file")
		return
	}

	note, _ := readNote(fullPath, dataDir, true)
	writeJSON(w, http.StatusCreated, note)
}

var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func sanitizeUploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7F,
			r == '/', r == '\\', r == ':',
			r == '*', r == '?', r == '"',
			r == '<', r == '>', r == '|':
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	result, _ := truncateRunes(b.String(), 200)
	result = strings.Trim(result, " .")
	if result == "" {
		return ""
	}

	ext := filepath.Ext(result)
	baseWithoutExt := strings.ToUpper(strings.TrimSuffix(result, ext))
	if windowsReservedNames[baseWithoutExt] {
		result = "_" + result
	}
	return result
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// readBody reads the request body
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}
