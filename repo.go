package main

import (
	"log"

	"memodump/internal/vaultfs"
)

// repo is the shared filesystem note repository. It is the only component
// allowed to materialize changes in the vault: every HTTP handler that reads or
// writes notes or folders goes through it so revision CAS, front-matter
// preservation, path locks and atomic writes apply uniformly.
//
// It is (re)created whenever dataDir is finalized — at startup for both entry
// points, and by tests that set dataDir directly.
var repo *vaultfs.Repository

// initRepo constructs repo from the current dataDir. It must be called after
// dataDir is resolved to its final absolute value.
func initRepo() {
	r, err := vaultfs.New(dataDir)
	if err != nil {
		log.Fatalf("Failed to initialize note repository: %v", err)
	}
	repo = r
}
