// Package appstate holds the mutable process-wide state shared by the entry
// points (CLI and Wails), the HTTP handlers, and the sync/image feature
// packages. It is deliberately a small composition root, not a service
// container: the vars mirror the old flat package-main state so the move to
// feature packages changes call sites, not behavior.
package appstate

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"memodump/internal/vaultfs"
)

// General app configuration, resolved by the entry points (CLI flags/env/.env
// cascade or the Wails OS config) before any handler runs.
var (
	DataDir  string
	Username string
	Password string
	Port     int
	NoAuth   bool
	CSSFile  string
	// SyncRoot is the cloud-sync device-state root; "" = OS application data dir.
	// Only the Wails runtime owns cloud sync (the CLI Web server shares one
	// server vault), so the CLI never sets it.
	SyncRoot string
)

// Repo is the shared filesystem note repository. It is the only component
// allowed to materialize changes in the vault: every HTTP handler that reads or
// writes notes or folders goes through it so revision CAS, front-matter
// preservation, path locks and atomic writes apply uniformly.
//
// It is (re)created whenever DataDir is finalized — at startup for both entry
// points, and by tests that set DataDir directly.
var Repo *vaultfs.Repository

// InitRepo constructs Repo from the current DataDir. It must be called after
// DataDir is resolved to its final absolute value.
func InitRepo() {
	r, err := vaultfs.New(DataDir)
	if err != nil {
		log.Fatalf("Failed to initialize note repository: %v", err)
	}
	Repo = r
}

// CloudSyncCapable reports whether THIS runtime owns cloud synchronization
// (R6.0). The CLI Web server is not a cloud-sync product target: all of its
// browser clients already share the server's one canonical data directory, so
// it starts no scheduler and every /api/sync route returns a single stable
// unavailable response. Wails owns the reviewed R0–R5 Go engine and scheduler.
//
// The default follows the build tags: the CLI build (no Wails tag) leaves it
// false (the zero value); the Wails build (production/dev/bindings) sets it
// true from runtime_wails.go. Tests select each runtime mode explicitly by
// assigning this variable, so one test binary can prove the whole matrix
// instead of relying on ambient build-tag globals.
var CloudSyncCapable bool

// ParseEnvFile reads KEY=VALUE pairs from a .env file. A missing file is
// silently ignored.
func ParseEnvFile(path string) map[string]string {
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

		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}

		val = strings.TrimSpace(val)
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
