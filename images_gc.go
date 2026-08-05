package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"memodump/internal/vaultfs"
)

// imageKeyRefRe finds content-hash image keys anywhere in a note body. The same
// regex serves both vault GC (relative /api/images/<key>) and S3 GC (public URL
// + key): content-hash keys are provider-independent, so one reference set is
// shared. A false positive (prose that happens to match) only keeps an image —
// the safe direction; the pattern is specific enough that false negatives are
// not a practical risk.
var imageKeyRefRe = regexp.MustCompile(`[a-f0-9]{64}\.(png|jpg|gif|webp|avif)`)

// imageCleanupGrace is how old an unreferenced image must be before cleanup may
// remove it. It protects images referenced only by client outbox drafts whose
// note body has not reached disk yet (the server cannot see those drafts).
const imageCleanupGrace = 7 * 24 * time.Hour

// collectReferencedImageKeys walks every note (.md) in the data dir and returns
// the set of content-hash image keys referenced in any body. Dot-prefixed
// entries are not skipped: an image referenced only by a hidden note must still
// be protected, and over-collecting references is always safer than deleting a
// file that is still in use.
func collectReferencedImageKeys() map[string]bool {
	keys := make(map[string]bool)
	_ = filepath.Walk(dataDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		// The sync metadata directory is never a note location; a stray .md
		// there must not influence GC of real notes.
		if info.IsDir() {
			if vaultfs.IsSyncMetadataDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range imageKeyRefRe.FindAll(data, -1) {
			keys[string(m)] = true
		}
		return nil
	})
	return keys
}

// gcVaultImages removes unreferenced image files from the local vault. A file is
// a candidate only when its name is a valid content-hash key, it is older than
// grace, and its key is not in the referenced set.
func gcVaultImages(keys map[string]bool, grace time.Duration) (removed int, bytes int64, err error) {
	vaultDir := filepath.Join(dataDir, imageVaultDir)
	entries, err := os.ReadDir(vaultDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	cutoff := time.Now().Add(-grace)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !imageKeyRe.MatchString(name) {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil || info.ModTime().After(cutoff) || keys[name] {
			continue
		}
		if err := os.Remove(filepath.Join(vaultDir, name)); err != nil {
			// One failure should not abort the sweep.
			log.Printf("image gc: failed to remove vault image %s: %v", name, err)
			continue
		}
		removed++
		bytes += info.Size()
	}
	return removed, bytes, nil
}

// s3GarbageDecision classifies one listed object for the sweep. Probe leftovers
// (.memodump-probe/) are always MemoDump test artifacts and are garbage once
// past the cutoff; content-hash keys are garbage only when unreferenced and old
// enough; anything else under the prefix is never MemoDump's to delete. The
// objectKey is the full object name; prefix is the configured object prefix.
func s3GarbageDecision(objectKey, prefix string, lastModified time.Time, keys map[string]bool, cutoff time.Time) bool {
	key := objectKey
	if prefix != "" {
		key = strings.TrimPrefix(key, prefix+"/")
	}
	if strings.HasPrefix(key, ".memodump-probe/") {
		return !lastModified.After(cutoff)
	}
	if !imageKeyRe.MatchString(key) || keys[key] {
		return false
	}
	return !lastModified.After(cutoff)
}

// gcS3Objects removes unreferenced objects from the S3 bucket, scoped to the
// configured prefix and to content-hash keys (non-MemoDump files in a shared
// bucket are never touched). Probe leftovers (.memodump-probe/) are swept
// unconditionally once old enough. Deletion is remote and permanent.
func gcS3Objects(cfg imageS3Config, keys map[string]bool, grace time.Duration) (removed int, bytes int64, err error) {
	client, err := newMinioClient(cfg)
	if err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	objectPrefix := cfg.Prefix
	if objectPrefix != "" {
		objectPrefix += "/"
	}
	cutoff := time.Now().Add(-grace)

	for obj := range client.ListObjects(ctx, cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    objectPrefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			log.Printf("image gc: s3 list error: %v", obj.Err)
			continue
		}
		if !s3GarbageDecision(obj.Key, cfg.Prefix, obj.LastModified, keys, cutoff) {
			continue
		}
		if err := client.RemoveObject(ctx, cfg.Bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			log.Printf("image gc: failed to remove s3 object %s: %v", obj.Key, err)
			continue
		}
		removed++
		bytes += obj.Size
	}
	return removed, bytes, nil
}

// runImageCleanup executes one cleanup sweep for the current data dir and
// effective config: collect references once, then clean the vault and, when S3
// is active, the bucket. Callers must gate on cleanupEnabled().
func runImageCleanup() (vaultRemoved, s3Removed int, bytes int64, err error) {
	keys := collectReferencedImageKeys()
	vaultRemoved, bytes, err = gcVaultImages(keys, imageCleanupGrace)
	if err != nil {
		return vaultRemoved, s3Removed, bytes, err
	}
	if cfg := effectiveImageS3Config(); s3Active(cfg) {
		n, b, gcErr := gcS3Objects(cfg, keys, imageCleanupGrace)
		if gcErr != nil {
			return vaultRemoved, s3Removed, bytes, gcErr
		}
		s3Removed, bytes = n, bytes+b
	}
	return vaultRemoved, s3Removed, bytes, nil
}

// handleImageCleanup runs a cleanup sweep. Gated by the cleanup config: when
// disabled it returns 403 so an accidental call can never delete anything.
func handleImageCleanup(w http.ResponseWriter, r *http.Request) {
	if !cleanupEnabled(effectiveImageS3Config()) {
		writeImageErrorCode(w, http.StatusForbidden, "cleanup_disabled", "Image cleanup is disabled in settings")
		return
	}
	vaultRemoved, s3Removed, bytes, err := runImageCleanup()
	if err != nil {
		writeImageErrorCode(w, http.StatusInternalServerError, "cleanup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vaultRemoved": vaultRemoved,
		"s3Removed":    s3Removed,
		"bytes":        bytes,
	})
}

// startImageCleanupLoop runs the periodic cleanup sweep in the background: once
// shortly after startup, then every 24 hours. Each tick re-reads the current
// data dir and effective config, and is a no-op while cleanup is disabled.
func startImageCleanupLoop() {
	go func() {
		// Defer the first sweep so the server/UI is fully up first.
		time.Sleep(5 * time.Second)
		sweepOnce()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			sweepOnce()
		}
	}()
}

func sweepOnce() {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("image gc: panic during sweep: %v", rec)
		}
	}()
	if !cleanupEnabled(effectiveImageS3Config()) {
		return
	}
	vaultRemoved, s3Removed, bytes, err := runImageCleanup()
	if err != nil {
		log.Printf("image gc: sweep failed: %v", err)
		return
	}
	if vaultRemoved > 0 || s3Removed > 0 {
		log.Printf("image gc: removed %d vault image(s), %d s3 object(s), %d bytes", vaultRemoved, s3Removed, bytes)
	}
}
