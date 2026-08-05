package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testImageKey builds a valid content-hash key from a hex char repeated 64×.
func testImageKey(char byte, ext string) string {
	return strings.Repeat(string(char), 64) + ext
}

func setOldMtime(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestCollectReferencedImageKeys(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()

	k1 := testImageKey('a', ".png")
	k2 := testImageKey('b', ".jpg")
	k3 := testImageKey('c', ".gif")
	k4 := testImageKey('d', ".webp")

	mustWrite(t, filepath.Join(dataDir, "note1.md"), "![x](/api/images/"+k1+")")
	mustWrite(t, filepath.Join(dataDir, "sub", "note2.md"), "img https://cdn.example.com/img/"+k2)
	// Non-note files must never contribute references.
	mustWrite(t, filepath.Join(dataDir, "draft.txt"), "/api/images/"+k3)
	// Dot-prefixed notes are still notes: their references must protect images.
	mustWrite(t, filepath.Join(dataDir, ".hidden.md"), "/api/images/"+k4)

	keys := collectReferencedImageKeys()
	for _, want := range []string{k1, k2, k4} {
		if !keys[want] {
			t.Errorf("expected key %s to be collected", want)
		}
	}
	if keys[k3] {
		t.Errorf("key %s from a non-note file must not be collected", k3)
	}
}

func TestCollectReferencedImageKeysSkipsSyncMetadata(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()

	kStray := testImageKey('f', ".png")
	kHidden := testImageKey('e', ".png")
	// A stray .md inside .memodump must never protect an image from GC, while a
	// hidden note at the vault root still does.
	mustWrite(t, filepath.Join(dataDir, ".memodump", "stray.md"), "/api/images/"+kStray)
	mustWrite(t, filepath.Join(dataDir, ".hidden.md"), "/api/images/"+kHidden)

	keys := collectReferencedImageKeys()
	if keys[kStray] {
		t.Errorf("reference from .memodump must not be collected")
	}
	if !keys[kHidden] {
		t.Errorf("hidden-note reference must still be collected")
	}
}

func TestGCVaultImages(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()

	vault := filepath.Join(dataDir, imageVaultDir)
	mustWrite(t, filepath.Join(vault, "a"+strings.Repeat("a", 63)+".png"), "x")
	mustWrite(t, filepath.Join(vault, "b"+strings.Repeat("b", 63)+".jpg"), "x")
	mustWrite(t, filepath.Join(vault, "c"+strings.Repeat("c", 63)+".webp"), "x")
	mustWrite(t, filepath.Join(vault, "notes.txt"), "not an image")
	setOldMtime(t, filepath.Join(vault, "b"+strings.Repeat("b", 63)+".jpg"))
	setOldMtime(t, filepath.Join(vault, "notes.txt"))

	// a*.png is referenced; b*.jpg is an old orphan; c*.webp is a recent orphan.
	ref := testImageKey('a', ".png")
	keys := map[string]bool{ref: true}

	removed, _, err := gcVaultImages(keys, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("gcVaultImages: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removal, got %d", removed)
	}
	for _, remain := range []string{
		filepath.Join(vault, "a"+strings.Repeat("a", 63)+".png"),
		filepath.Join(vault, "c"+strings.Repeat("c", 63)+".webp"),
		filepath.Join(vault, "notes.txt"),
	} {
		if _, err := os.Stat(remain); err != nil {
			t.Errorf("file %s should have been kept: %v", remain, err)
		}
	}
	if _, err := os.Stat(filepath.Join(vault, "b"+strings.Repeat("b", 63)+".jpg")); !os.IsNotExist(err) {
		t.Errorf("orphan image should have been removed")
	}
}

func TestGCVaultImagesMissingVault(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	removed, _, err := gcVaultImages(map[string]bool{}, time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("expected no-op on missing vault, got removed=%d err=%v", removed, err)
	}
}

func TestS3GarbageDecision(t *testing.T) {
	cutoff := time.Now().Add(-time.Hour)
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()
	k := testImageKey('e', ".png")
	keys := map[string]bool{k: true}

	cases := []struct {
		name   string
		object string
		prefix string
		time   time.Time
		keys   map[string]bool
		want   bool
	}{
		{"probe old", ".memodump-probe/x.png", "", old, nil, true},
		{"probe recent", ".memodump-probe/x.png", "", recent, nil, false},
		{"orphan old", k, "", old, nil, true},
		{"orphan recent", k, "", recent, nil, false},
		{"referenced", k, "", old, keys, false},
		{"non-key file", "reports/finance.pdf", "", old, nil, false},
		{"non-key under prefix", "img/logo.png", "img", old, nil, false},
		{"key under prefix", "img/" + k, "img", old, nil, true},
		{"key under prefix recent", "img/" + k, "img", recent, nil, false},
	}
	for _, c := range cases {
		if got := s3GarbageDecision(c.object, c.prefix, c.time, c.keys, cutoff); got != c.want {
			t.Errorf("%s: s3GarbageDecision = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestImageCleanupEndpointGated(t *testing.T) {
	oldDataDir, oldFile := dataDir, imageConfigFile
	dataDir = t.TempDir()
	imageConfigFile = filepath.Join(dataDir, ".image-config.json")
	defer func() { dataDir, imageConfigFile = oldDataDir, oldFile }()

	orphan := testImageKey('f', ".jpg")
	mustWrite(t, filepath.Join(dataDir, imageVaultDir, orphan), "x")
	setOldMtime(t, filepath.Join(dataDir, imageVaultDir, orphan))

	// Disabled → 403, file untouched.
	req := httptest.NewRequest(http.MethodPost, "/api/images/gc", nil)
	rec := httptest.NewRecorder()
	handleImageCleanup(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled cleanup: got %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dataDir, imageVaultDir, orphan)); err != nil {
		t.Fatalf("orphan removed while cleanup disabled: %v", err)
	}

	// Enabled → 200, orphan removed.
	mustWrite(t, imageConfigFile, `{"provider":"local","cleanup":{"enabled":true}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/images/gc", nil)
	rec = httptest.NewRecorder()
	handleImageCleanup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled cleanup: got %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, imageVaultDir, orphan)); !os.IsNotExist(err) {
		t.Errorf("orphan should have been removed by enabled cleanup")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
