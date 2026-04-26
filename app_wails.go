//go:build production || dev || bindings

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

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
		if d := parseEnvFile(".env")["DATA"]; d != "" {
			cfg.DataDir = d
		} else {
			cwd, _ := os.Getwd()
			cfg.DataDir = filepath.Join(cwd, "data")
		}
		saveWailsCfg(cfg)
	}

	noAuth = true
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		cwd, _ := os.Getwd()
		abs = filepath.Join(cwd, "data")
	}
	dataDir = abs
	os.MkdirAll(dataDir, 0755)
	sessionFile = filepath.Join(dataDir, ".sessions.json")
	loadSessions()
	startSessionCleanup()
}

// GetDataDir returns the active data directory path (shown in the sidebar).
func (a *App) GetDataDir() string {
	return dataDir
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
