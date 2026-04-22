package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed frontend/dist/*
var frontendFS embed.FS

var (
	dataDir  string
	username string
	password string
	port     int
)

func main() {
	flag.StringVar(&dataDir, "data", "", "Data directory path")
	flag.StringVar(&username, "user", "", "Login username")
	flag.StringVar(&password, "pass", "", "Login password")
	flag.IntVar(&port, "port", 8080, "Service port")
	flag.Parse()

	if dataDir == "" || username == "" || password == "" {
		fmt.Println("Usage: memodump --data <folder> --user <username> --pass <password> [--port <port>]")
		os.Exit(1)
	}

	absData, err := filepath.Abs(dataDir)
	if err != nil {
		log.Fatalf("Failed to parse data directory: %v", err)
	}
	dataDir = absData

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	sessionFile = filepath.Join(dataDir, ".sessions.json")
	loadSessions()
	startSessionCleanup()

	mux := http.NewServeMux()

	// API routes
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

	// Serve frontend SPA
	distFS, err := fs.Sub(frontendFS, "frontend/dist")
	if err != nil {
		log.Fatalf("Failed to load frontend resources: %v", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Try to serve static file first
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		f, err := distFS.Open(path[1:])
		if err != nil {
			// SPA fallback: serve index.html for client-side routing
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf(":%d", port)
	log.Printf("MemoDump started at http://localhost%s", addr)
	log.Printf("Data directory: %s", dataDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
