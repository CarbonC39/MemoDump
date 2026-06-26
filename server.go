package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
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
		if key == "" {
			continue
		}

		val := strings.TrimSpace(line[idx+1:])
		if len(val) > 0 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				if unquoted, err := strconv.Unquote(val); err == nil {
					val = unquoted
				} else if val[0] == '\'' {
					val = strings.Trim(val, "'")
				}
			} else {
				// Strip an inline comment, but only when the '#' follows
				// whitespace, so values like passwords or "#fff" survive.
				for i := 1; i < len(val); i++ {
					if val[i] == '#' && (val[i-1] == ' ' || val[i-1] == '\t') {
						val = strings.TrimSpace(val[:i])
						break
					}
				}
			}
		}
		result[key] = val
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading env file %s: %v\n", path, err)
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
// doesn't log a 404 when no CSS is configured. Cache-Control: no-cache lets the
// browser store it but forces revalidation, so edits show up on the next reload.
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
