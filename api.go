package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"memodump/internal/vaultfs"
)

const maxBodySize = 10 << 20 // 10 MB

// Note and Folder are aliases for the vaultfs types so handlers and the legacy
// JSON shapes keep working unchanged.
type Note = vaultfs.Note
type Folder = vaultfs.Folder

// writeErr writes a legacy-shaped error body: {"error":"message"}.
func writeErr(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// handleListNotes lists .md files in a specific directory (default: root)
func handleListNotes(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("folder")
	notes, err := repo.ListNotes(folder)
	if err != nil {
		if errors.Is(err, vaultfs.ErrInvalidPath) {
			writeErr(w, http.StatusBadRequest, "Path is illegal")
			return
		}
		// A missing folder lists as empty, preserving historical behavior.
		notes = []Note{}
	}
	writeJSON(w, http.StatusOK, notes)
}

// handleGetNote reads a single note
func handleGetNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	if notePath == "" {
		writeErr(w, http.StatusBadRequest, "Path is illegal")
		return
	}
	note, err := repo.Get(notePath, true)
	if err != nil {
		if errors.Is(err, vaultfs.ErrInvalidPath) {
			writeErr(w, http.StatusBadRequest, "Path is illegal")
			return
		}
		if errors.Is(err, vaultfs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "File not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Failed to read note")
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
		writeErr(w, http.StatusBadRequest, "Request format error")
		return
	}

	note, err := repo.Create(vaultfs.CreateOptions{
		Name:    req.Name,
		Folder:  req.Folder,
		Content: req.Content,
		Tags:    req.Tags,
	})
	if err != nil {
		if errors.Is(err, vaultfs.ErrInvalidPath) || errors.Is(err, vaultfs.ErrFrontMatterNotEditable) {
			writeErr(w, http.StatusBadRequest, "Path is illegal")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Failed to save note")
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

// handleUpdateNote updates an existing note. baseRevision is optional: when
// present and stale the request is rejected with 409 without touching the file.
func handleUpdateNote(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	notePath := r.PathValue("path")

	var req struct {
		Content      *string  `json:"content"`
		Tags         []string `json:"tags"`
		Rename       *string  `json:"rename"`
		BaseRevision string   `json:"baseRevision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Request format error")
		return
	}

	opts := vaultfs.UpdateOptions{
		Content:      req.Content,
		Rename:       req.Rename,
		BaseRevision: req.BaseRevision,
	}
	if req.Tags != nil {
		opts.Tags = &req.Tags
	}

	note, err := repo.Update(notePath, opts)
	if err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrInvalidPath):
			writeErr(w, http.StatusBadRequest, "Path is illegal")
		case errors.Is(err, vaultfs.ErrNotFound):
			writeErr(w, http.StatusNotFound, "File not found")
		case errors.Is(err, vaultfs.ErrNameConflict):
			writeErr(w, http.StatusConflict, "A note with that name already exists")
		case errors.Is(err, vaultfs.ErrRevisionConflict):
			writeErr(w, http.StatusConflict, "local_revision_conflict")
		case errors.Is(err, vaultfs.ErrFrontMatterNotEditable):
			writeErr(w, http.StatusBadRequest, "Front matter cannot be edited safely")
		default:
			writeErr(w, http.StatusInternalServerError, "Failed to save note")
		}
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// handleDeleteNote deletes a note. baseRevision is optional; when present and
// stale the request is rejected with 409 without deleting.
func handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	baseRevision := r.URL.Query().Get("baseRevision")

	err := repo.Delete(notePath, baseRevision)
	if err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrInvalidPath):
			writeErr(w, http.StatusBadRequest, "Path is illegal")
		case errors.Is(err, vaultfs.ErrNotFound):
			writeErr(w, http.StatusNotFound, "File not found")
		case errors.Is(err, vaultfs.ErrRevisionConflict):
			writeErr(w, http.StatusConflict, "local_revision_conflict")
		default:
			writeErr(w, http.StatusInternalServerError, "Failed to delete note")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMoveNote moves a note to a different folder
func handleMoveNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	var req struct {
		Destination string `json:"destination"` // empty string means root
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Request format error")
		return
	}

	note, err := repo.Move(notePath, req.Destination)
	if err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrInvalidPath):
			writeErr(w, http.StatusBadRequest, "Path is illegal")
		case errors.Is(err, vaultfs.ErrNotFound):
			writeErr(w, http.StatusNotFound, "File not found")
		case errors.Is(err, vaultfs.ErrNameConflict):
			writeErr(w, http.StatusConflict, "A note with that name already exists in the destination")
		default:
			writeErr(w, http.StatusInternalServerError, "Failed to move note")
		}
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// handleDuplicateNote creates a copy of a note in the same folder. The raw file
// bytes are copied verbatim so YAML front matter and tags survive byte-for-byte.
func handleDuplicateNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	note, err := repo.Duplicate(notePath)
	if err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrInvalidPath):
			writeErr(w, http.StatusBadRequest, "Path is illegal")
		case errors.Is(err, vaultfs.ErrNotFound):
			writeErr(w, http.StatusNotFound, "File not found")
		default:
			writeErr(w, http.StatusInternalServerError, "Failed to save note")
		}
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

// handleListFolders returns the folder tree
func handleListFolders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, repo.FolderTree())
}

// handleCreateFolder creates a new folder
func handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Request format error")
		return
	}
	if vaultfs.ContainsReservedSegment(req.Path) {
		writeErr(w, http.StatusBadRequest, "Reserved folder name")
		return
	}
	if err := repo.CreateFolder(req.Path); err != nil {
		if errors.Is(err, vaultfs.ErrInvalidPath) {
			writeErr(w, http.StatusBadRequest, "Path is illegal")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Failed to create folder")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok", "path": req.Path})
}

// handleRenameFolder renames a folder
func handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := r.PathValue("path")
	var req struct {
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Request format error")
		return
	}

	newRel := path.Join(path.Dir(folderPath), req.NewName)
	if vaultfs.ContainsReservedSegment(newRel) {
		writeErr(w, http.StatusBadRequest, "Reserved folder name")
		return
	}
	if err := repo.RenameFolder(folderPath, req.NewName); err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrInvalidPath):
			writeErr(w, http.StatusBadRequest, "Path is illegal")
		case errors.Is(err, vaultfs.ErrNotFound):
			writeErr(w, http.StatusNotFound, "File not found")
		default:
			writeErr(w, http.StatusInternalServerError, "Failed to rename folder")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteFolder deletes a folder and all contents
func handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	folderPath := r.PathValue("path")
	if err := repo.DeleteFolder(folderPath); err != nil {
		if errors.Is(err, vaultfs.ErrInvalidPath) {
			writeErr(w, http.StatusBadRequest, "Path is illegal")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Failed to delete folder")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSearch searches notes by content or tag.
func handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(r.URL.Query().Get("q"))
	tag := strings.ToLower(r.URL.Query().Get("tag"))

	var results []Note
	_ = filepath.Walk(dataDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if vaultfs.IsSyncMetadataDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		rel, relErr := repo.Rel(p)
		if relErr != nil {
			return nil
		}
		note, readErr := repo.Get(rel, true)
		if readErr != nil {
			return nil
		}

		// AND logic: both conditions must match when both are specified.
		matchQuery := query == "" || strings.Contains(strings.ToLower(note.Content), query)
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
	var req struct {
		Destination string `json:"destination"` // empty = root
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Request format error")
		return
	}

	newRel := path.Join(req.Destination, path.Base(folderPath))
	if vaultfs.ContainsReservedSegment(newRel) {
		writeErr(w, http.StatusBadRequest, "Reserved folder name")
		return
	}
	if err := repo.MoveFolder(folderPath, req.Destination); err != nil {
		switch {
		case errors.Is(err, vaultfs.ErrInvalidPath):
			writeErr(w, http.StatusBadRequest, "Path is illegal")
		case errors.Is(err, vaultfs.ErrNameConflict):
			writeErr(w, http.StatusConflict, "A folder with that name already exists in the destination")
		default:
			writeErr(w, http.StatusInternalServerError, "Failed to move folder")
		}
		return
	}
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
	cfg := effectiveImageS3Config()
	image := imageConfigPublic(cfg)
	writeJSON(w, http.StatusOK, map[string]any{"noAuth": noAuth, "image": image})
}

// handleUploadNote accepts a multipart .md/.txt file upload and saves it as a
// note. Security: extension + size + UTF-8 + null-byte + path traversal checks.
func handleUploadNote(w http.ResponseWriter, r *http.Request) {
	const uploadLimit = 1 << 20 // 1 MB

	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit+4096)
	if err := r.ParseMultipartForm(uploadLimit); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "File too large or invalid request (max 1 MB)")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "No file provided")
		return
	}
	defer file.Close()

	origName := header.Filename
	if idx := strings.LastIndexAny(origName, `/\`); idx != -1 {
		origName = origName[idx+1:]
	}

	ext := strings.ToLower(filepath.Ext(origName))
	if ext != ".md" && ext != ".txt" {
		writeErr(w, http.StatusBadRequest, "Only .md and .txt files are accepted")
		return
	}

	if header.Size > uploadLimit {
		writeErr(w, http.StatusRequestEntityTooLarge, "File too large (max 1 MB)")
		return
	}

	content, err := io.ReadAll(io.LimitReader(file, uploadLimit+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Failed to read file")
		return
	}
	if len(content) > uploadLimit {
		writeErr(w, http.StatusRequestEntityTooLarge, "File too large (max 1 MB)")
		return
	}

	if !utf8.Valid(content) {
		writeErr(w, http.StatusBadRequest, "File must be valid UTF-8 text")
		return
	}
	if bytes.IndexByte(content, 0) >= 0 {
		writeErr(w, http.StatusBadRequest, "File contains binary data")
		return
	}

	noteName := vaultfs.SanitizeName(strings.TrimSuffix(origName, ext))
	if noteName == "" {
		noteName = time.Now().Format("2006-01-02_150405")
	}
	folder := r.FormValue("folder")

	note, err := repo.CreateMarkdown(noteName, folder, string(content))
	if err != nil {
		if errors.Is(err, vaultfs.ErrInvalidPath) {
			writeErr(w, http.StatusBadRequest, "Invalid folder path")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Failed to save file")
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
