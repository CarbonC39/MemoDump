package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// imageVaultDir is the image vault directory name inside the data dir. It is
	// dot-prefixed so the folder-tree APIs hide it, and reserved so user folders
	// can never shadow it.
	imageVaultDir  = ".images"
	imageSizeLimit = 20 << 20 // 20 MiB
	imagePrefixLen = 4096
)

// imageKeyRe matches content-hash image keys. The canonical extension set has
// no .jpeg: JPEG content always maps to .jpg, so identical content always
// produces the identical key (dedupe).
var imageKeyRe = regexp.MustCompile(`^[a-f0-9]{64}\.(png|jpg|gif|webp|avif)$`)

// imageFormat is a validated image container. The canonical extension is the
// single source of truth: keys, Content-Type and magic-byte validation all
// derive from it, never from user-supplied filenames or MIME types.
type imageFormat struct {
	ext         string // canonical extension, e.g. ".jpg"
	contentType string
}

var imageFormats = map[string]imageFormat{
	"png":  {ext: ".png", contentType: "image/png"},
	"jpg":  {ext: ".jpg", contentType: "image/jpeg"},
	"gif":  {ext: ".gif", contentType: "image/gif"},
	"webp": {ext: ".webp", contentType: "image/webp"},
	"avif": {ext: ".avif", contentType: "image/avif"},
}

var (
	pngSig     = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	gif87a     = []byte("GIF87a")
	gif89a     = []byte("GIF89a")
	webpRIFF   = []byte("RIFF")
	webpTag    = []byte("WEBP")
	ftypBox    = []byte("ftyp")
	avifBrands = map[string]bool{"avif": true, "avis": true}
)

// detectImageFormat validates the magic bytes of a limited header prefix and
// returns the canonical format. AVIF is an ISO-BMFF container: the first ftyp
// box is parsed (with bounds checks, within the prefix cap) and either the
// major brand or any compatible brand must be avif/avis — a major-brand-only
// check would reject valid files that carry the brand in compatible brands.
func detectImageFormat(header []byte) (imageFormat, error) {
	if len(header) >= 8 && bytes.Equal(header[:8], pngSig) {
		return imageFormats["png"], nil
	}
	if len(header) >= 3 && header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF {
		return imageFormats["jpg"], nil
	}
	if len(header) >= 6 && (bytes.Equal(header[:6], gif87a) || bytes.Equal(header[:6], gif89a)) {
		return imageFormats["gif"], nil
	}
	if len(header) >= 12 && bytes.Equal(header[:4], webpRIFF) && bytes.Equal(header[8:12], webpTag) {
		return imageFormats["webp"], nil
	}
	if f, ok := detectAvif(header); ok {
		return f, nil
	}
	return imageFormat{}, fmt.Errorf("unsupported or unrecognized image format")
}

func detectAvif(header []byte) (imageFormat, bool) {
	if len(header) < 12 || !bytes.Equal(header[4:8], ftypBox) {
		return imageFormat{}, false
	}
	boxSize := binary.BigEndian.Uint32(header[:4])
	if boxSize < 8 || int(boxSize) > len(header) || int(boxSize) > imagePrefixLen {
		return imageFormat{}, false
	}
	if avifBrands[string(header[8:12])] {
		return imageFormats["avif"], true
	}
	// Compatible brands start after the 16-byte box header and are 4 bytes each.
	for i := 16; i+4 <= int(boxSize); i += 4 {
		if avifBrands[string(header[i:i+4])] {
			return imageFormats["avif"], true
		}
	}
	return imageFormat{}, false
}

// containsReservedSegment reports whether any path segment is the reserved
// image vault directory name.
func containsReservedSegment(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == imageVaultDir {
			return true
		}
	}
	return false
}

func writeImageError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

// handleImagePut stores a raw image body under a content-hash key. The key is
// client-supplied but strictly validated: its hash segment must equal the
// SHA-256 of the body (content↔key binding, preventing arbitrary overwrite of
// the images namespace), and its extension must match the format detected from
// the magic bytes.
func handleImagePut(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !imageKeyRe.MatchString(key) {
		writeImageError(w, http.StatusBadRequest, "Invalid image key")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, imageSizeLimit)

	vaultDir := filepath.Join(dataDir, imageVaultDir)
	if err := os.MkdirAll(vaultDir, 0755); err != nil {
		writeImageError(w, http.StatusInternalServerError, "Failed to prepare image storage")
		return
	}

	tmp, err := os.CreateTemp(vaultDir, "img_*.tmp")
	if err != nil {
		writeImageError(w, http.StatusInternalServerError, "Failed to create temporary file")
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // no-op after a successful rename
	}()

	// Capture the first imagePrefixLen bytes for format detection while the
	// whole body streams into the temp file and the hasher.
	hasher := sha256.New()
	prefix := make([]byte, imagePrefixLen)
	n, readErr := io.ReadFull(r.Body, prefix)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		if _, ok := readErr.(*http.MaxBytesError); ok {
			writeImageError(w, http.StatusRequestEntityTooLarge, "Image too large (max 20 MiB)")
			return
		}
		writeImageError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	prefix = prefix[:n]
	if _, err := tmp.Write(prefix); err != nil {
		writeImageError(w, http.StatusInternalServerError, "Failed to write data")
		return
	}
	_, _ = hasher.Write(prefix)

	written := int64(n)
	count, err := io.Copy(io.MultiWriter(tmp, hasher), r.Body)
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			writeImageError(w, http.StatusRequestEntityTooLarge, "Image too large (max 20 MiB)")
			return
		}
		writeImageError(w, http.StatusInternalServerError, "Failed to write data")
		return
	}
	written += count

	format, err := detectImageFormat(prefix)
	if err != nil {
		writeImageError(w, http.StatusBadRequest, "Unsupported or unrecognized image format")
		return
	}
	if format.ext != filepath.Ext(key) {
		writeImageError(w, http.StatusBadRequest, "Image content does not match the requested key extension")
		return
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != key[:64] {
		writeImageError(w, http.StatusBadRequest, "Image hash does not match the requested key")
		return
	}

	if err := tmp.Close(); err != nil {
		writeImageError(w, http.StatusInternalServerError, "Failed to finalize image")
		return
	}

	target := filepath.Join(vaultDir, key)
	repaired := false
	if info, err := os.Stat(target); err == nil {
		if info.Size() == written {
			// Same key, same size: the content-hash invariant guarantees the
			// stored object already matches, so this is an idempotent no-op.
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key})
			return
		}
		// The stored object violates the invariant (e.g. overwritten or
		// truncated by direct filesystem access). Repair it with the verified
		// copy so a re-paste actually fixes the object.
		log.Printf("image vault: repairing corrupt object %s (stored %d bytes, verified %d)", key, info.Size(), written)
		if err := os.Remove(target); err != nil {
			writeImageError(w, http.StatusInternalServerError, "Failed to replace corrupt image")
			return
		}
		repaired = true
	}
	if err := os.Rename(tmpPath, target); err != nil {
		writeImageError(w, http.StatusInternalServerError, "Failed to save image")
		return
	}
	if repaired {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok", "key": key})
}

// handleImageGet serves a stored image. Content-Type comes from the canonical
// format map (keys are content-verified at PUT time), and responses are
// private (the endpoint is authenticated) and immutable (content-hash URLs).
func handleImageGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !imageKeyRe.MatchString(key) {
		writeImageError(w, http.StatusBadRequest, "Invalid image key")
		return
	}
	format, ok := imageFormats[strings.TrimPrefix(filepath.Ext(key), ".")]
	if !ok {
		writeImageError(w, http.StatusBadRequest, "Invalid image key")
		return
	}

	f, err := os.Open(filepath.Join(dataDir, imageVaultDir, key))
	if err != nil {
		if os.IsNotExist(err) {
			writeImageError(w, http.StatusNotFound, "Image not found")
			return
		}
		writeImageError(w, http.StatusInternalServerError, "Failed to read image")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", format.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if _, err := io.Copy(w, f); err != nil {
		// Headers are already sent; the connection will simply be truncated.
		return
	}
}
