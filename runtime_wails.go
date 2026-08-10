//go:build production || dev || bindings

package main

// The Wails desktop app owns cloud sync: it starts the reviewed R5 scheduler
// in startup, exposes the full /api/sync surface, and keeps the 30-second
// status poll. This build-tag file flips the shared capability flag so
// buildAPIMux registers the sync routes and main_wails.go's scheduler lifecycle
// is the only place sync runs.
func init() {
	cloudSyncCapable = true
}
