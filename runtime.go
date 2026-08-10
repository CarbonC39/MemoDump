package main

// cloudSyncCapable reports whether THIS runtime owns cloud synchronization
// (R6.0). The CLI Web server is not a cloud-sync product target: all of its
// browser clients already share the server's one canonical data directory, so
// it starts no scheduler and every /api/sync route returns a single stable
// unavailable response. Wails owns the reviewed R0–R5 Go engine and scheduler.
//
// The default follows the build tags: the CLI build (no Wails tag) leaves it
// false (the zero value); the Wails build (production/dev/bindings) sets it
// true in runtime_wails.go. Tests select each runtime mode explicitly by
// assigning this variable, so one test binary can prove the whole matrix
// instead of relying on ambient build-tag globals.
var cloudSyncCapable bool
