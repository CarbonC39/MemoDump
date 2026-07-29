package main

import (
	"testing"
	"testing/fstest"
)

func TestVerifyServerFrontend(t *testing.T) {
	serverBuild := fstest.MapFS{
		"build-mode.json": {Data: []byte(`{"mode":"server"}`)},
	}
	if err := verifyServerFrontend(serverBuild); err != nil {
		t.Fatalf("server build rejected: %v", err)
	}

	localBuild := fstest.MapFS{
		"build-mode.json": {Data: []byte(`{"mode":"local"}`)},
	}
	if err := verifyServerFrontend(localBuild); err == nil {
		t.Fatal("local frontend build was accepted for an embedded server")
	}
}
