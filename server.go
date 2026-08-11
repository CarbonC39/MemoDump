package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"memodump/internal/appstate"
	"memodump/internal/httpx"
	"memodump/internal/imagesvc"
	"memodump/internal/syncsvc"
)

func verifyServerFrontend(frontend fs.FS) error {
	data, err := fs.ReadFile(frontend, "build-mode.json")
	if err != nil {
		return fmt.Errorf("frontend build marker missing: run npm run build in frontend: %w", err)
	}
	var marker struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return fmt.Errorf("invalid frontend build marker: %w", err)
	}
	if marker.Mode != "server" {
		return fmt.Errorf("frontend was built in %q mode; embedded server binaries require npm run build", marker.Mode)
	}
	return nil
}

// buildAPIMux registers all API routes on a new ServeMux.
func buildAPIMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", httpx.HandleLogin)
	mux.HandleFunc("POST /api/logout", httpx.HandleLogout)
	mux.HandleFunc("GET /api/notes", httpx.AuthMiddleware(handleListNotes))
	mux.HandleFunc("GET /api/notes/{path...}", httpx.AuthMiddleware(handleGetNote))
	mux.HandleFunc("POST /api/notes", httpx.AuthMiddleware(handleCreateNote))
	mux.HandleFunc("PUT /api/notes/{path...}", httpx.AuthMiddleware(handleUpdateNote))
	mux.HandleFunc("DELETE /api/notes/{path...}", httpx.AuthMiddleware(handleDeleteNote))
	mux.HandleFunc("PUT /api/move/{path...}", httpx.AuthMiddleware(handleMoveNote))
	mux.HandleFunc("POST /api/duplicate/{path...}", httpx.AuthMiddleware(handleDuplicateNote))
	mux.HandleFunc("GET /api/folders", httpx.AuthMiddleware(handleListFolders))
	mux.HandleFunc("POST /api/folders", httpx.AuthMiddleware(handleCreateFolder))
	mux.HandleFunc("PUT /api/folders/{path...}", httpx.AuthMiddleware(handleRenameFolder))
	mux.HandleFunc("DELETE /api/folders/{path...}", httpx.AuthMiddleware(handleDeleteFolder))
	mux.HandleFunc("GET /api/search", httpx.AuthMiddleware(handleSearch))
	mux.HandleFunc("PUT /api/move/folder/{path...}", httpx.AuthMiddleware(handleMoveFolder))
	mux.HandleFunc("PUT /api/images/{key}", httpx.AuthMiddleware(imagesvc.HandleImagePut))
	mux.HandleFunc("GET /api/images/{key}", httpx.AuthMiddleware(imagesvc.HandleImageGet))
	mux.HandleFunc("POST /api/images/gc", httpx.AuthMiddleware(imagesvc.HandleImageCleanup))
	mux.HandleFunc("GET /api/config/image", httpx.AuthMiddleware(imagesvc.HandleImageConfigGet))
	mux.HandleFunc("PUT /api/config/image", httpx.AuthMiddleware(imagesvc.HandleImageConfigSave))
	mux.HandleFunc("POST /api/config/image/test", httpx.AuthMiddleware(imagesvc.HandleImageConfigTest))
	mux.HandleFunc("GET /api/ping", httpx.AuthMiddleware(handlePing))
	mux.HandleFunc("POST /api/upload", httpx.AuthMiddleware(handleUploadNote))
	mux.HandleFunc("GET /api/config", handleConfig)
	mux.HandleFunc("GET /api/v2/notes", httpx.AuthMiddleware(handleV2ListNotes))
	mux.HandleFunc("POST /api/v2/notes", httpx.AuthMiddleware(handleV2CreateNote))
	mux.HandleFunc("GET /api/v2/notes/{path...}", httpx.AuthMiddleware(handleV2GetNote))
	mux.HandleFunc("PUT /api/v2/notes/{path...}", httpx.AuthMiddleware(handleV2UpdateNote))
	mux.HandleFunc("DELETE /api/v2/notes/{path...}", httpx.AuthMiddleware(handleV2DeleteNote))
	mux.HandleFunc("PUT /api/v2/move/{path...}", httpx.AuthMiddleware(handleV2MoveNote))
	mux.HandleFunc("POST /api/v2/duplicate/{path...}", httpx.AuthMiddleware(handleV2DuplicateNote))
	mux.HandleFunc("GET /api/v2/folders", httpx.AuthMiddleware(handleV2ListFolders))
	mux.HandleFunc("GET /api/v2/search", httpx.AuthMiddleware(handleV2Search))
	// Cloud sync is owned by the Wails runtime only (R6.0). The CLI Web server
	// shares one server vault across all its browser clients, so it exposes no
	// sync surface: every /api/sync route returns the single stable unavailable
	// response below instead of leaking engine state or falling through to the
	// SPA fallback handler.
	if appstate.CloudSyncCapable {
		mux.HandleFunc("GET /api/sync/status", httpx.AuthMiddleware(syncsvc.HandleSyncStatus))
		mux.HandleFunc("POST /api/sync/enable", httpx.AuthMiddleware(syncsvc.HandleSyncEnable))
		mux.HandleFunc("POST /api/sync/run", httpx.AuthMiddleware(syncsvc.HandleSyncRun))
		mux.HandleFunc("POST /api/sync/disable", httpx.AuthMiddleware(syncsvc.HandleSyncDisable))
		mux.HandleFunc("POST /api/sync/reset", httpx.AuthMiddleware(syncsvc.HandleSyncReset))
		mux.HandleFunc("POST /api/sync/test", httpx.AuthMiddleware(syncsvc.HandleSyncTest))
		mux.HandleFunc("GET /api/sync/recovery", httpx.AuthMiddleware(syncsvc.HandleSyncRecoveryList))
		mux.HandleFunc("POST /api/sync/recovery/restore", httpx.AuthMiddleware(syncsvc.HandleSyncRecoveryRestore))
	} else {
		mux.HandleFunc("/api/sync", syncUnavailable)
		mux.HandleFunc("/api/sync/", syncUnavailable)
	}
	mux.HandleFunc("GET /custom.css", handleCustomCSS)
	return mux
}

// syncUnavailable is the one stable response for every cloud-sync route on a
// runtime without cloud sync. It is deliberately not wrapped in
// authMiddleware: availability is a property of the runtime, not of the
// caller, so the answer is identical for every request.
func syncUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.WriteErr(w, http.StatusNotFound, "cloud sync is not available on this runtime")
}

// handleCustomCSS serves the user-supplied stylesheet (via --css flag).
// Always 200 with text/css so the frontend's unconditional <link> tag
// doesn't log a 404 when no CSS is configured. Cache-Control: no-cache lets the
// browser store it but forces revalidation, so edits show up on the next reload.
func handleCustomCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if appstate.CSSFile == "" {
		return
	}
	data, err := os.ReadFile(appstate.CSSFile)
	if err != nil {
		return
	}
	w.Write(data)
}
