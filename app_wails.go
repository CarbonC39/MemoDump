//go:build production || dev || bindings

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"memodump/internal/appstate"
	"memodump/internal/httpx"
	"memodump/internal/imagesvc"
	"memodump/internal/syncsvc"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type wailsCfg struct {
	DataDir string `json:"dataDir"`
}

func wailsCfgPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "memodump", "config.json")
}

func loadWailsCfg() wailsCfg {
	var cfg wailsCfg
	if data, err := os.ReadFile(wailsCfgPath()); err == nil {
		json.Unmarshal(data, &cfg)
	}
	return cfg
}

func saveWailsCfg(cfg wailsCfg) {
	path := wailsCfgPath()
	os.MkdirAll(filepath.Dir(path), 0755)
	if data, err := json.Marshal(cfg); err == nil {
		os.WriteFile(path, data, 0600)
	}
}

// App is the Wails application struct. Methods on App are bound to the frontend.
type App struct {
	ctx context.Context
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cfg := loadWailsCfg()
	if cfg.DataDir == "" {
		if d := appstate.ParseEnvFile(".env")["DATA"]; d != "" {
			cfg.DataDir = d
		} else {
			cwd, _ := os.Getwd()
			cfg.DataDir = filepath.Join(cwd, "data")
		}
		saveWailsCfg(cfg)
	}

	appstate.NoAuth = true
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		cwd, _ := os.Getwd()
		abs = filepath.Join(cwd, "data")
	}
	appstate.DataDir = abs
	os.MkdirAll(appstate.DataDir, 0755)
	appstate.InitRepo()
	httpx.SessionFile = filepath.Join(appstate.DataDir, ".sessions.json")
	if cfgDir, err := os.UserConfigDir(); err == nil {
		imagesvc.ConfigFile = filepath.Join(cfgDir, "memodump", "image-config.json")
	}
	httpx.LoadSessions()
	httpx.StartSessionCleanup()
	imagesvc.StartImageCleanupLoop()

	// Automatic cloud sync: a connected replica runs once after a 10s startup
	// delay and then every five minutes while the app is open. OnShutdown stops
	// and waits for it.
	syncsvc.StartSyncScheduler(ctx)
}

// shutdown stops the automatic sync scheduler and waits for any in-flight
// attempt to exit. Wails v2 invokes it with the app context on teardown.
func (a *App) shutdown(ctx context.Context) {
	syncsvc.StopSyncScheduler()
}

// GetDataDir returns the active data directory path (shown in the sidebar).
func (a *App) GetDataDir() string {
	return appstate.DataDir
}

// ChangeDataDir opens a folder picker; on confirmation saves the new path and
// tells the user to restart. Returns true if the user picked a folder.
func (a *App) ChangeDataDir() bool {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Choose MemoDump data folder",
	})
	if err != nil || dir == "" {
		return false
	}
	cfg := loadWailsCfg()
	cfg.DataDir = dir
	saveWailsCfg(cfg)
	runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:    runtime.InfoDialog,
		Title:   "MemoDump",
		Message: "Data folder updated. Please restart the app to apply the change.",
	})
	return true
}
