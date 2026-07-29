package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type noteSummaryV2 struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ParentID   string   `json:"parentId"`
	Tags       []string `json:"tags"`
	ModifiedAt int64    `json:"modifiedAt"`
	Preview    string   `json:"preview"`
}

type folderSummaryV2 struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ParentID    string `json:"parentId"`
	HasChildren bool   `json:"hasChildren"`
}

type notePageV2 struct {
	Items      []noteSummaryV2 `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

type folderPageV2 struct {
	Items []folderSummaryV2 `json:"items"`
}

type noteCursorV2 struct {
	ModifiedAt int64  `json:"modifiedAt"`
	ID         string `json:"id"`
}

func writeV2Error(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func parseV2Limit(r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, false
	}
	if limit > 200 {
		limit = 200
	}
	return limit, true
}

func encodeV2Cursor(note noteSummaryV2) string {
	data, _ := json.Marshal(noteCursorV2{ModifiedAt: note.ModifiedAt, ID: note.ID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeV2Cursor(raw string) (*noteCursorV2, error) {
	if raw == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var cursor noteCursorV2
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.ID == "" {
		if err == nil {
			err = strconv.ErrSyntax
		}
		return nil, err
	}
	return &cursor, nil
}

func noteToSummaryV2(note Note) noteSummaryV2 {
	parent := filepath.ToSlash(filepath.Dir(note.Path))
	if parent == "." {
		parent = ""
	}
	tags := note.Tags
	if tags == nil {
		tags = []string{}
	}
	return noteSummaryV2{
		ID: note.Path, Name: note.Name, ParentID: parent, Tags: tags,
		ModifiedAt: note.ModTime, Preview: note.Preview,
	}
}

func sortNotesV2(notes []noteSummaryV2, order string) {
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].ModifiedAt != notes[j].ModifiedAt {
			if order == "modified-asc" {
				return notes[i].ModifiedAt < notes[j].ModifiedAt
			}
			return notes[i].ModifiedAt > notes[j].ModifiedAt
		}
		return notes[i].ID < notes[j].ID
	})
}

func pageNotesV2(notes []noteSummaryV2, cursor *noteCursorV2, limit int, order string) notePageV2 {
	start := 0
	if cursor != nil {
		start = len(notes)
		for i, note := range notes {
			if (order == "modified-desc" && note.ModifiedAt < cursor.ModifiedAt) ||
				(order == "modified-asc" && note.ModifiedAt > cursor.ModifiedAt) ||
				(note.ModifiedAt == cursor.ModifiedAt && note.ID > cursor.ID) {
				start = i
				break
			}
		}
	}
	end := min(start+limit, len(notes))
	items := append([]noteSummaryV2(nil), notes[start:end]...)
	if items == nil {
		items = []noteSummaryV2{}
	}
	page := notePageV2{Items: items}
	if end < len(notes) && len(items) > 0 {
		next := encodeV2Cursor(items[len(items)-1])
		page.NextCursor = &next
	}
	return page
}

func v2ListArgs(w http.ResponseWriter, r *http.Request) (int, *noteCursorV2, string, bool) {
	limit, ok := parseV2Limit(r)
	if !ok {
		writeV2Error(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
		return 0, nil, "", false
	}
	order := r.URL.Query().Get("sort")
	if order == "" {
		order = "modified-desc"
	}
	if order != "modified-desc" && order != "modified-asc" {
		writeV2Error(w, http.StatusBadRequest, "invalid_sort", "sort must be modified-desc or modified-asc")
		return 0, nil, "", false
	}
	cursor, err := decodeV2Cursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid")
		return 0, nil, "", false
	}
	return limit, cursor, order, true
}

func handleV2ListNotes(w http.ResponseWriter, r *http.Request) {
	limit, cursor, order, ok := v2ListArgs(w, r)
	if !ok {
		return
	}
	parent := r.URL.Query().Get("parent")
	dir, err := safePath(dataDir, parent)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "invalid_parent", "parent folder is invalid")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeV2Error(w, http.StatusNotFound, "folder_not_found", "folder was not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "list_failed", "failed to list notes")
		return
	}
	notes := make([]noteSummaryV2, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		note, readErr := readNote(filepath.Join(dir, entry.Name()), dataDir, false)
		if readErr == nil {
			notes = append(notes, noteToSummaryV2(*note))
		}
	}
	sortNotesV2(notes, order)
	writeJSON(w, http.StatusOK, pageNotesV2(notes, cursor, limit, order))
}

func handleV2ListFolders(w http.ResponseWriter, r *http.Request) {
	parent := r.URL.Query().Get("parent")
	dir, err := safePath(dataDir, parent)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, "invalid_parent", "parent folder is invalid")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeV2Error(w, http.StatusNotFound, "folder_not_found", "folder was not found")
			return
		}
		writeV2Error(w, http.StatusInternalServerError, "list_failed", "failed to list folders")
		return
	}
	items := make([]folderSummaryV2, 0)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		id := entry.Name()
		if parent != "" {
			id = parent + "/" + entry.Name()
		}
		hasChildren := false
		if children, readErr := os.ReadDir(filepath.Join(dir, entry.Name())); readErr == nil {
			for _, child := range children {
				if child.IsDir() && !strings.HasPrefix(child.Name(), ".") {
					hasChildren = true
					break
				}
			}
		}
		items = append(items, folderSummaryV2{
			ID: id, Name: entry.Name(), ParentID: parent, HasChildren: hasChildren,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, folderPageV2{Items: items})
}

func handleV2Search(w http.ResponseWriter, r *http.Request) {
	limit, cursor, order, ok := v2ListArgs(w, r)
	if !ok {
		return
	}
	query := strings.ToLower(r.URL.Query().Get("q"))
	tagQuery := strings.ToLower(r.URL.Query().Get("tag"))
	notes := make([]noteSummaryV2, 0)
	_ = filepath.Walk(dataDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		note, readErr := readNote(path, dataDir, true)
		if readErr != nil {
			return nil
		}
		if query != "" && !strings.Contains(strings.ToLower(note.Content), query) {
			return nil
		}
		if tagQuery != "" {
			matched := false
			for _, tag := range note.Tags {
				if strings.Contains(strings.ToLower(tag), tagQuery) {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
		notes = append(notes, noteToSummaryV2(*note))
		return nil
	})
	sortNotesV2(notes, order)
	writeJSON(w, http.StatusOK, pageNotesV2(notes, cursor, limit, order))
}
