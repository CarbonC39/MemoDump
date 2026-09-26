package syncsvc

import (
	"encoding/json"
	"io"
	"net/http"

	"memodump/internal/httpx"
	"memodump/internal/syncprovider/s3"
)

// HandleSyncConfigGet returns the note-sync S3 configuration for the settings
// panel. The secretKey never leaves the server; an empty secretKey on save
// means "unchanged".
func HandleSyncConfigGet(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, syncConfigEditorPublic(syncS3Config()))
}

// HandleSyncConfigSave persists the note-sync S3 settings from the panel,
// mirroring the image-config semantics: the secretKey is stored server-side
// and never returned; an empty secretKey means "unchanged" unless the
// endpoint/bucket/accessKey identity changed, in which case the secret must be
// provided again.
//
// It runs under syncOpMu with the other lifecycle operations (enable/run/
// disable/reset) so a save can never interleave with an Enable that builds its
// provider from a different configuration, and it REFUSES writes while the
// vault is connected: changing the provider behind a live connection pin would
// immediately trip a provider-changed pause. Disable or Reset first.
func HandleSyncConfigSave(w http.ResponseWriter, r *http.Request) {
	syncOpMu.Lock()
	defer syncOpMu.Unlock()

	if rec, err := syncReadConnected(); err == nil && rec != nil && rec.Connected {
		httpx.WriteErr(w, http.StatusConflict,
			"sync is connected; disable sync before changing the provider configuration")
		return
	}

	var req syncConfigJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "Request format error")
		return
	}
	candidate := s3.Config{
		Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket,
		Prefix: req.Prefix, AccessKey: req.AccessKey, SecretKey: req.SecretKey,
		ForcePathStyle: req.ForcePathStyle,
	}
	if candidate.Endpoint == "" || candidate.Bucket == "" || candidate.AccessKey == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "endpoint, bucket and accessKey are required")
		return
	}
	if _, err := s3.New(candidate); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}

	stored, hasStored := loadSyncConfigFile()
	identityUnchanged := hasStored && stored.Endpoint == req.Endpoint &&
		stored.Bucket == req.Bucket && stored.AccessKey == req.AccessKey
	if req.SecretKey == "" {
		if !identityUnchanged || stored.SecretKey == "" {
			httpx.WriteErr(w, http.StatusBadRequest,
				"secretKey is required when creating a config or changing endpoint, bucket or accessKey")
			return
		}
		req.SecretKey = stored.SecretKey
	}
	if err := saveSyncConfigFile(req); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "Failed to save sync config")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, syncConfigEditorPublic(syncS3Config()))
}

// HandleSyncConfigTest runs the server-side capability probe against the
// effective config (or a candidate config from the request body merged over
// the effective one). The probe exercises conditional writes and listing; it
// never prints credentials.
func HandleSyncConfigTest(w http.ResponseWriter, r *http.Request) {
	cfg := syncS3Config()
	original := cfg
	var req syncConfigJSON
	if r.ContentLength != 0 {
		var raw map[string]json.RawMessage
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(body, &req) != nil || json.Unmarshal(body, &raw) != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "Request format error")
			return
		}
		_, forcePathStylePresent := raw["forcePathStyle"]
		mergeSyncConfig(&cfg, req, forcePathStylePresent)
	}
	if req.SecretKey == "" && (cfg.Endpoint != original.Endpoint ||
		cfg.Bucket != original.Bucket || cfg.AccessKey != original.AccessKey) {
		httpx.WriteErr(w, http.StatusBadRequest,
			"secretKey is required when changing endpoint, bucket or accessKey")
		return
	}
	if !syncConfigConfigured(cfg) {
		httpx.WriteErr(w, http.StatusBadRequest, "S3 is not configured")
		return
	}
	client, err := s3.New(cfg)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	caps, err := client.Test(r.Context())
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "conditionalWrites": caps.ConditionalWrites, "pagedListing": caps.PagedListing,
	})
}
