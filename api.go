package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// safePath prevents directory traversal attacks
func safePath(base string, userPath string) (string, error) {
	cleanPath := filepath.Clean(filepath.FromSlash(userPath))
	fullPath := filepath.Join(base, cleanPath)

	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(absFull+string(filepath.Separator), absBase+string(filepath.Separator)) && absFull != absBase {
		return "", fmt.Errorf("path out of bounds")
	}
	return fullPath, nil
}

type NoteCacheEntry struct {
	Note      Note
	ModTime   int64
	BodyLower string
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

var frontMatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)
var tagLineRe = regexp.MustCompile(`(?m)^tags:\s*\[([^\]]*)\]`)

func parseFrontMatter(content string) (tags []string, body string) {
	matches := frontMatterRe.FindStringSubmatch(content)
	if matches == nil {
		return nil, content
	}
	fm := matches[1]
	body = content[len(matches[0]):]

	tagMatch := tagLineRe.FindStringSubmatch(fm)
	if tagMatch != nil {
		raw := tagMatch[1]
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}
	return tags, body
}

func buildFrontMatter(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	quoted := make([]string, len(tags))
	copy(quoted, tags)
	return fmt.Sprintf("---\ntags: [%s]\n---\n", strings.Join(quoted, ", "))
}

func readNote(fullPath string, basePath string, includeContent bool) (*Note, error) {
	info, err := os.Stat(fullPath)
	if err != nil {
		noteCache.Delete(fullPath)
		return nil, err
	}
	modTime := info.ModTime().UnixMilli()

	if entry, ok := noteCache.Load(fullPath); ok {
		cached := entry.(*NoteCacheEntry)
		if cached.ModTime == modTime {
			noteCopy := cached.Note
			if includeContent {
				data, err := os.ReadFile(fullPath)
				if err == nil {
					_, body := parseFrontMatter(string(data))
					noteCopy.Content = body
				}
			}
			return &noteCopy, nil
		}
	}

	relPath, _ := filepath.Rel(basePath, fullPath)
	relPath = filepath.ToSlash(relPath)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		noteCache.Delete(fullPath)
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

	preview := strings.TrimSpace(body)
	if len(preview) > 1000 {
		preview = preview[:1000] + "..."
	}
	note.Preview = preview

	noteCache.Store(fullPath, &NoteCacheEntry{
		Note:      note,
		ModTime:   modTime,
		BodyLower: strings.ToLower(body),
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

	// Generate filename
	filename := req.Name
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
	fullPath := filepath.Join(dir, filename)

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

	// Read existing
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

	if err := os.WriteFile(fullPath, []byte(finalContent), 0644); err != nil {
		http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
		return
	}

	// Handle rename
	targetPath := fullPath
	if req.Rename != nil {
		newName := *req.Rename
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
		if err := os.Rename(fullPath, newPath); err != nil {
			http.Error(w, `{"error":"Failed to rename note"}`, http.StatusInternalServerError)
			return
		}
		targetPath = newPath
	}

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
	if err := os.Rename(fullPath, newPath); err != nil {
		http.Error(w, `{"error":"Failed to move note"}`, http.StatusInternalServerError)
		return
	}

	note, _ := readNote(newPath, dataDir, false)
	writeJSON(w, http.StatusOK, note)
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

	var folders []Folder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		relPath, _ := filepath.Rel(base, filepath.Join(dir, e.Name()))
		relPath = filepath.ToSlash(relPath)
		folderDir := filepath.Join(dir, e.Name())

		f := Folder{
			Name:     e.Name(),
			Path:     relPath,
			Children: buildFolderTree(folderDir, base),
		}

		subEntries, _ := os.ReadDir(folderDir)
		var notes []Note
		for _, se := range subEntries {
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".md") {
				continue
			}
			note, err := readNote(filepath.Join(folderDir, se.Name()), base, false)
			if err == nil {
				notes = append(notes, *note)
			}
		}
		f.Notes = notes

		folders = append(folders, f)
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
	newPath, err := safePath(dataDir, newRel)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}
	if err := os.Rename(fullPath, newPath); err != nil {
		http.Error(w, `{"error":"Failed to rename folder"}`, http.StatusInternalServerError)
		return
	}

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

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSearch searches notes by content or tag
func handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(r.URL.Query().Get("q"))
	tag := strings.ToLower(r.URL.Query().Get("tag"))

	var results []Note
	filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}

		// readNote uses the extremely fast ModTime cache to avoid re-reading disk
		note, checkErr := readNote(path, dataDir, false)
		if checkErr != nil {
			return nil
		}

		var bodyLower string
		if entry, ok := noteCache.Load(path); ok {
			bodyLower = entry.(*NoteCacheEntry).BodyLower
		} else {
			// fallback check safely
			note, checkErr = readNote(path, dataDir, true)
			if checkErr != nil {
				return nil
			}
			bodyLower = strings.ToLower(note.Content)
		}

		match := false
		if query != "" && strings.Contains(bodyLower, query) {
			match = true
		}
		if tag != "" {
			for _, t := range note.Tags {
				if strings.ToLower(t) == tag {
					match = true
					break
				}
			}
		}
		if query == "" && tag == "" {
			match = true
		}

		if match {
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

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// readBody reads the request body
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}
