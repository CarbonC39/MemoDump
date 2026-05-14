package main

import (
	"bufio"
	"net/http"
	"os"
	"strings"
)

// Package-level config vars — shared by CLI (main_cli.go) and Wails (main_wails.go).
var (
	dataDir  string
	username string
	password string
	port     int
	noAuth   bool
	cssFile  string
)

// parseEnvFile reads KEY=VALUE pairs from a .env file. Missing file is silently ignored.
func parseEnvFile(path string) map[string]string {
	result := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return result
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			result[key] = val
		}
	}
	return result
}

// buildAPIMux registers all API routes on a new ServeMux.
func buildAPIMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", handleLogin)
	mux.HandleFunc("POST /api/logout", handleLogout)
	mux.HandleFunc("GET /api/notes", authMiddleware(handleListNotes))
	mux.HandleFunc("GET /api/notes/{path...}", authMiddleware(handleGetNote))
	mux.HandleFunc("POST /api/notes", authMiddleware(handleCreateNote))
	mux.HandleFunc("PUT /api/notes/{path...}", authMiddleware(handleUpdateNote))
	mux.HandleFunc("DELETE /api/notes/{path...}", authMiddleware(handleDeleteNote))
	mux.HandleFunc("PUT /api/move/{path...}", authMiddleware(handleMoveNote))
	mux.HandleFunc("GET /api/folders", authMiddleware(handleListFolders))
	mux.HandleFunc("POST /api/folders", authMiddleware(handleCreateFolder))
	mux.HandleFunc("PUT /api/folders/{path...}", authMiddleware(handleRenameFolder))
	mux.HandleFunc("DELETE /api/folders/{path...}", authMiddleware(handleDeleteFolder))
	mux.HandleFunc("GET /api/search", authMiddleware(handleSearch))
	mux.HandleFunc("PUT /api/move/folder/{path...}", authMiddleware(handleMoveFolder))
	mux.HandleFunc("GET /api/ping", authMiddleware(handlePing))
	mux.HandleFunc("POST /api/upload", authMiddleware(handleUploadNote))
	mux.HandleFunc("GET /api/config", handleConfig)
	mux.HandleFunc("GET /custom.css", handleCustomCSS)
	return mux
}

// handleCustomCSS serves the user-supplied stylesheet (via --css flag).
// Always 200 with text/css so the frontend's unconditional <link> tag
// doesn't log a 404 when no CSS is configured.
func handleCustomCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if cssFile == "" {
		return
	}
	data, err := os.ReadFile(cssFile)
	if err != nil {
		return
	}
	w.Write(data)
}
