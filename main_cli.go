//go:build !production && !dev && !bindings

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
	"strconv"
)

//go:embed frontend/dist/*
var frontendFS embed.FS

func main() {
	flag.StringVar(&dataDir, "data", "", "Data directory path")
	flag.StringVar(&username, "user", "", "Login username")
	flag.StringVar(&password, "pass", "", "Login password")
	flag.IntVar(&port, "port", 0, "Service port (default 8080)")
	flag.StringVar(&cssFile, "css", "", "Custom CSS file to inject")
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
	if cssFile == "" {
		if v := os.Getenv("MEMODUMP_CSS"); v != "" {
			cssFile = v
		} else if v := dotenv["CSS"]; v != "" {
			cssFile = v
		}
	}
	if cssFile != "" {
		if abs, err := filepath.Abs(cssFile); err == nil {
			cssFile = abs
		}
		if _, err := os.Stat(cssFile); err != nil {
			log.Fatalf("CSS file not found: %s", cssFile)
		}
	}
	if port == 0 {
		port = 8080
	}

	if dataDir == "" {
		fmt.Println("Usage: memodump --data <folder> [--user <username> --pass <password>] [--port <port>] [--css <file>]")
		fmt.Println("  Credentials can also be set via MEMODUMP_USER / MEMODUMP_PASS env vars")
		fmt.Println("  or a .env file in the current directory (DATA=, USER=, PASS=, PORT=, CSS=).")
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

	mux := buildAPIMux()

	// Serve frontend SPA
	distFS, err := fs.Sub(frontendFS, "frontend/dist")
	if err != nil {
		log.Fatalf("Failed to load frontend resources: %v", err)
	}
	fileServer := http.FileServer(http.FS(distFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		f, err := distFS.Open(path[1:])
		if err != nil {
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
