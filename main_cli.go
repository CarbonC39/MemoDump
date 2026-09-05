//go:build !production && !dev && !bindings

package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"memodump/internal/appstate"
	"memodump/internal/httpx"
	"memodump/internal/imagesvc"
	"memodump/internal/syncsvc"
)

//go:embed frontend/dist/*
var frontendFS embed.FS

func main() {
	flag.StringVar(&appstate.DataDir, "data", "", "Data directory path")
	flag.StringVar(&appstate.Username, "user", "", "Login username")
	flag.StringVar(&appstate.Password, "pass", "", "Login password")
	flag.IntVar(&appstate.Port, "port", 0, "Service port (default 8080)")
	flag.StringVar(&appstate.CSSFile, "css", "", "Custom CSS file to inject")
	flag.StringVar(&imagesvc.FlagEndpoint, "image-s3-endpoint", "", "S3-compatible endpoint (e.g. https://s3.region.amazonaws.com)")
	flag.StringVar(&imagesvc.FlagRegion, "image-s3-region", "", "S3 region (default us-east-1)")
	flag.StringVar(&imagesvc.FlagBucket, "image-s3-bucket", "", "S3 bucket name")
	flag.StringVar(&imagesvc.FlagPrefix, "image-s3-prefix", "", "S3 object prefix")
	flag.StringVar(&imagesvc.FlagPublicURL, "image-s3-public-url", "", "Public base URL for S3 objects")
	flag.StringVar(&imagesvc.FlagAccessKey, "image-s3-access-key", "", "S3 access key")
	flag.StringVar(&imagesvc.FlagSecretKey, "image-s3-secret-key", "", "S3 secret key")
	flag.Parse()

	// Load .env from CWD (lower priority than flags and env vars).
	dotenv := appstate.ParseEnvFile(".env")

	// Precedence: CLI flag (non-empty / non-zero) > env var > .env file.
	if appstate.DataDir == "" {
		if v := os.Getenv("MEMODUMP_DATA"); v != "" {
			appstate.DataDir = v
		} else if v := dotenv["DATA"]; v != "" {
			appstate.DataDir = v
		}
	}
	if appstate.Username == "" {
		if v := os.Getenv("MEMODUMP_USER"); v != "" {
			appstate.Username = v
		} else if v := dotenv["USER"]; v != "" {
			appstate.Username = v
		}
	}
	if appstate.Password == "" {
		if v := os.Getenv("MEMODUMP_PASS"); v != "" {
			appstate.Password = v
		} else if v := dotenv["PASS"]; v != "" {
			appstate.Password = v
		}
	}
	if appstate.Port == 0 {
		if v := os.Getenv("MEMODUMP_PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				appstate.Port = p
			}
		} else if v := dotenv["PORT"]; v != "" {
			if p, err := strconv.Atoi(v); err == nil && p > 0 {
				appstate.Port = p
			}
		}
	}
	if appstate.CSSFile == "" {
		if v := os.Getenv("MEMODUMP_CSS"); v != "" {
			appstate.CSSFile = v
		} else if v := dotenv["CSS"]; v != "" {
			appstate.CSSFile = v
		}
	}
	if appstate.CSSFile != "" {
		if abs, err := filepath.Abs(appstate.CSSFile); err == nil {
			appstate.CSSFile = abs
		}
		if _, err := os.Stat(appstate.CSSFile); err != nil {
			log.Fatalf("CSS file not found: %s", appstate.CSSFile)
		}
	}
	if appstate.Port == 0 {
		appstate.Port = 8080
	}

	if appstate.DataDir == "" {
		fmt.Println("Usage: memodump --data <folder> [-user <username> --pass <password>] [--port <port>] [--css <file>]")
		fmt.Println("  Credentials can also be set via MEMODUMP_USER / MEMODUMP_PASS env vars")
		fmt.Println("  or a .env file in the current directory (DATA=, USER=, PASS=, PORT=, CSS=).")
		fmt.Println("  Omitting username and password starts the server in no-auth mode.")
		os.Exit(1)
	}

	if appstate.Username == "" && appstate.Password == "" {
		appstate.NoAuth = true
		log.Println("WARNING: No credentials configured — running in no-auth mode (all requests allowed)")
	}

	absData, err := filepath.Abs(appstate.DataDir)
	if err != nil {
		log.Fatalf("Failed to parse data directory: %v", err)
	}
	appstate.DataDir = absData

	if err := os.MkdirAll(appstate.DataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	appstate.InitRepo()

	httpx.SessionFile = filepath.Join(appstate.DataDir, ".sessions.json")
	imagesvc.ConfigFile = filepath.Join(appstate.DataDir, ".image-config.json")
	httpx.LoadSessions()
	httpx.StartSessionCleanup()
	imagesvc.StartImageCleanupLoop()

	mux := buildAPIMux()

	// Serve frontend SPA
	distFS, err := fs.Sub(frontendFS, "frontend/dist")
	if err != nil {
		log.Fatalf("Failed to load frontend resources: %v", err)
	}
	if err := verifyServerFrontend(distFS); err != nil {
		log.Fatal(err)
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

	addr := fmt.Sprintf(":%d", appstate.Port)
	log.Printf("MemoDump started at http://localhost%s", addr)
	log.Printf("Data directory: %s", appstate.DataDir)

	// The CLI Web server does not own cloud sync (R6.0): all of its browser
	// clients already share this one server vault, so no scheduler is started
	// and every /api/sync route returns a stable unavailable response. The root
	// context is still tied to process signals so shutdown drains in-flight
	// requests.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	case <-ctx.Done():
	}
	// No-op on the CLI runtime (no scheduler is ever started); kept so shutdown
	// is symmetric with the Wails lifecycle.
	syncsvc.StopSyncScheduler()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}
