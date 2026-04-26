package main

import (
	"bufio"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed frontend/dist/*
var frontendFS embed.FS

var (
	dataDir  string
	username string
	password string
	port     int
	noAuth   bool
)

// parseEnvFile reads a KEY=VALUE .env file. Lines starting with '#' and blank
// lines are skipped. Values are whitespace-trimmed but not quote-stripped.
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

func main() {
	flag.StringVar(&dataDir, "data", "", "Data directory path")
	flag.StringVar(&username, "user", "", "Login username")
	flag.StringVar(&password, "pass", "", "Login password")
	flag.IntVar(&port, "port", 0, "Service port (default 8080)")
	flag.Parse()

	// Load .env from CWD (lower priority than flags and env vars).
	dotenv := parseEnvFile(".env")

	// Precedence: CLI flag (non-empty / non-zero) > env var > .env file.
	if dataDir == "" {
		if v := os.Getenv("MEMODUMP_DATA"); v != "" {
			dataDir = v
		} else if v := dotenv["DATA"]; v != "" {
			dataDir = v
		}
	}
	if username == "" {
		if v := os.Getenv("MEMODUMP_USER"); v != "" {
			username = v
		} else if v := dotenv["USER"]; v != "" {
			username = v
		}
	}
	if password == "" {
		if v := os.Getenv("MEMODUMP_PASS"); v != "" {
			password = v
		} else if v := dotenv["PASS"]; v != "" {
			password = v
		}
	}
	if port == 0 {
		if v := os.Getenv("MEMODUMP_PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				port = p
			}
		} else if v := dotenv["PORT"]; v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				port = p
			}
		}
	}
	if port == 0 {
		port = 8080
	}

	if dataDir == "" {
		fmt.Println("Usage: memodump --data <folder> [--user <username> --pass <password>] [--port <port>]")
		fmt.Println("  Credentials can also be set via MEMODUMP_USER / MEMODUMP_PASS env vars")
		fmt.Println("  or a .env file in the current directory (DATA=, USER=, PASS=, PORT=).")
		fmt.Println("  Omitting username and password starts the server in no-auth mode.")
		os.Exit(1)
	}

	if username == "" && password == "" {
		noAuth = true
		log.Println("WARNING: No credentials configured — running in no-auth mode (all requests allowed)")
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
