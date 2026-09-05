package imagesvc

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"memodump/internal/httpx"

	"memodump/internal/appstate"
)

func imageHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func pngBody() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{0x00}, 16)...)
}

func jpgBody() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, bytes.Repeat([]byte{0x00}, 16)...)
}

func gifBody() []byte {
	return append([]byte("GIF89a"), bytes.Repeat([]byte{0x00}, 16)...)
}

func webpBody() []byte {
	body := make([]byte, 24)
	copy(body[0:4], "RIFF")
	copy(body[8:12], "WEBP")
	return body
}

// avifBody builds an ISO-BMFF ftyp sample. When compatible is true the major
// brand is generic ("isom") and the avif brand sits in the compatible brands;
// otherwise the major brand itself is "avif".
func avifBody(compatible bool) []byte {
	if compatible {
		box := make([]byte, 20)
		binary.BigEndian.PutUint32(box[0:4], 20)
		copy(box[4:8], "ftyp")
		copy(box[8:12], "isom")
		copy(box[16:20], "avif")
		return box
	}
	box := make([]byte, 16)
	binary.BigEndian.PutUint32(box[0:4], 16)
	copy(box[4:8], "ftyp")
	copy(box[8:12], "avif")
	return box
}

func imageTestMux(t *testing.T) http.Handler {
	t.Helper()
	oldDataDir, oldNoAuth := appstate.DataDir, appstate.NoAuth
	appstate.DataDir = t.TempDir()
	appstate.NoAuth = true
	t.Cleanup(func() {
		appstate.DataDir = oldDataDir
		appstate.NoAuth = oldNoAuth
	})
	appstate.InitRepo()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/images/{key}", HandleImagePut)
	mux.HandleFunc("GET /api/images/{key}", HandleImageGet)
	mux.HandleFunc("POST /api/images/gc", HandleImageCleanup)
	mux.HandleFunc("GET /api/config/image", HandleImageConfigGet)
	mux.HandleFunc("PUT /api/config/image", HandleImageConfigSave)
	mux.HandleFunc("POST /api/config/image/test", HandleImageConfigTest)
	return mux
}

func putImage(t *testing.T, mux http.Handler, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/images/"+key, bytes.NewReader(body))
	req.Header.Set("X-MemoDump-Image-Target", imageTargetID(EffectiveImageS3Config()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func getImage(t *testing.T, mux http.Handler, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/images/"+key, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestImagePutGetRoundTrip(t *testing.T) {
	mux := imageTestMux(t)
	body := pngBody()
	key := imageHash(body) + ".png"

	if rec := putImage(t, mux, key, body); rec.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	rec := getImage(t, mux, key)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Cache-Control"); !bytes.Contains([]byte(got), []byte("private")) {
		t.Fatalf("Cache-Control = %q, want private", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatal("GET body does not match uploaded body")
	}

	entries, err := os.ReadDir(filepath.Join(appstate.DataDir, imageVaultDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != key {
		t.Fatalf("vault entries = %v, want exactly %s (no leftover tmp files)", entries, key)
	}
}

func TestImagePutIsIdempotent(t *testing.T) {
	mux := imageTestMux(t)
	body := jpgBody()
	key := imageHash(body) + ".jpg"

	if rec := putImage(t, mux, key, body); rec.Code != http.StatusCreated {
		t.Fatalf("first PUT status = %d, want 201", rec.Code)
	}
	if rec := putImage(t, mux, key, body); rec.Code != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200 (idempotent)", rec.Code)
	}
}

func TestImagePutRepairsCorruptObject(t *testing.T) {
	mux := imageTestMux(t)
	body := pngBody()
	key := imageHash(body) + ".png"

	vaultDir := filepath.Join(appstate.DataDir, imageVaultDir)
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Manually corrupt the object with a different size, violating the
	// content-hash invariant as if it had been truncated on disk.
	if err := os.WriteFile(filepath.Join(vaultDir, key), []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}

	if rec := putImage(t, mux, key, body); rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (repair); body=%s", rec.Code, rec.Body.String())
	}
	stored, err := os.ReadFile(filepath.Join(vaultDir, key))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, body) {
		t.Fatal("corrupt object was not repaired")
	}
}

func TestImagePutRejectsHashMismatch(t *testing.T) {
	mux := imageTestMux(t)
	key := imageHash([]byte("some other content")) + ".png"
	if rec := putImage(t, mux, key, pngBody()); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestImagePutRejectsStaleTargetAfterSwitchToLocal(t *testing.T) {
	mux := imageTestMux(t)
	body := pngBody()
	key := imageHash(body) + ".png"
	req := httptest.NewRequest(http.MethodPut, "/api/images/"+key, bytes.NewReader(body))
	req.Header.Set("X-MemoDump-Image-Target", "s3:old-destination")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want stale-target conflict; body=%s", rec.Code, rec.Body.String())
	}
}

func TestImagePutRejectsExtensionMismatch(t *testing.T) {
	mux := imageTestMux(t)
	body := pngBody()
	key := imageHash(body) + ".jpg"
	if rec := putImage(t, mux, key, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestImagePutRejectsUnrecognizedFormat(t *testing.T) {
	mux := imageTestMux(t)
	body := bytes.Repeat([]byte{0xAA}, 64)
	key := imageHash(body) + ".png"
	if rec := putImage(t, mux, key, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestImagePutFormats(t *testing.T) {
	cases := []struct {
		name        string
		body        func() []byte
		ext         string
		contentType string
	}{
		{"png", pngBody, ".png", "image/png"},
		{"jpeg", jpgBody, ".jpg", "image/jpeg"},
		{"gif", gifBody, ".gif", "image/gif"},
		{"webp", webpBody, ".webp", "image/webp"},
		{"avif-major-brand", func() []byte { return avifBody(false) }, ".avif", "image/avif"},
		{"avif-compatible-brand", func() []byte { return avifBody(true) }, ".avif", "image/avif"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := imageTestMux(t)
			body := tc.body()
			key := imageHash(body) + tc.ext
			if rec := putImage(t, mux, key, body); rec.Code != http.StatusCreated {
				t.Fatalf("PUT status = %d, want 201; body=%s", rec.Code, rec.Body.String())
			}
			if rec := getImage(t, mux, key); rec.Header().Get("Content-Type") != tc.contentType {
				t.Fatalf("Content-Type = %q, want %q", rec.Header().Get("Content-Type"), tc.contentType)
			}
		})
	}
}

func TestImagePutRejectsInvalidKeys(t *testing.T) {
	body := pngBody()
	hash := imageHash(body)
	cases := []string{
		hash + ".jpeg",               // non-canonical extension
		hash + ".PNG",                // uppercase extension
		hash + ".svg",                // excluded format
		"..%2F" + hash[:60] + ".png", // traversal attempt
		"..",                         // plain traversal
		"abc.png",                    // not hex
		hash[:63] + ".png",           // truncated hash
		hash + ".png/extra",          // slash inside key
	}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			mux := imageTestMux(t)
			rec := putImage(t, mux, key, body)
			// Routing rejects some keys before the handler (307 clean-redirect
			// for "..", 404 for multi-segment keys); the handler rejects the
			// rest with 400. Any non-success is a valid rejection.
			if rec.Code < 400 && rec.Code != http.StatusTemporaryRedirect {
				t.Fatalf("key %q: status = %d, want rejection", key, rec.Code)
			}
		})
	}
}

func TestImageGetNotFound(t *testing.T) {
	mux := imageTestMux(t)
	body := pngBody()
	if rec := getImage(t, mux, imageHash(body)+".png"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestImageRequiresAuth(t *testing.T) {
	oldDataDir, oldNoAuth := appstate.DataDir, appstate.NoAuth
	appstate.DataDir = t.TempDir()
	appstate.NoAuth = false
	t.Cleanup(func() {
		appstate.DataDir = oldDataDir
		appstate.NoAuth = oldNoAuth
	})
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/images/{key}", httpx.AuthMiddleware(HandleImagePut))
	mux.HandleFunc("GET /api/images/{key}", httpx.AuthMiddleware(HandleImageGet))

	body := pngBody()
	key := imageHash(body) + ".png"
	if rec := putImage(t, mux, key, body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec := getImage(t, mux, key); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET status = %d, want 401", rec.Code)
	}
}

func TestImagePutTooLarge(t *testing.T) {
	mux := imageTestMux(t)
	body := append(pngBody(), make([]byte, imageSizeLimit)...) // 1 byte over the limit
	key := imageHash(body) + ".png"
	if rec := putImage(t, mux, key, body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
