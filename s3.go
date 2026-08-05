package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// imageConfigFile is where the settings panel persists the S3 config. It is a
// per-build package var (same seam as sessionFile): CLI web server stores it in
// the data dir, Wails stores it in the OS user-config dir so cloud-synced data
// folders never carry long-lived credentials.
var imageConfigFile string

// imageCleanupConfig controls the periodic orphan-image cleanup. It defaults to
// disabled; enabling it is an explicit, warned choice in the settings panel.
type imageCleanupConfig struct {
	Enabled bool `json:"enabled"`
}

// imageS3Config is the server-side S3 image host configuration. The JSON form
// is what <dataDir>/.image-config.json (or the Wails config file) stores.
type imageS3Config struct {
	Provider       string              `json:"provider"` // "local" or "s3"
	Endpoint       string              `json:"endpoint,omitempty"`
	Region         string              `json:"region,omitempty"`
	Bucket         string              `json:"bucket,omitempty"`
	Prefix         string              `json:"prefix,omitempty"`
	PublicBaseURL  string              `json:"publicBaseUrl,omitempty"`
	AccessKey      string              `json:"accessKey,omitempty"`
	SecretKey      string              `json:"secretKey,omitempty"`
	ForcePathStyle bool                `json:"forcePathStyle"`
	Cleanup        *imageCleanupConfig `json:"cleanup,omitempty"`
}

// imageTransferError preserves the actionable failure category across the Go
// proxy boundary. The frontend must not have to infer S3 auth/config failures
// from a generic 502 response.
type imageTransferError struct {
	Code string
	Err  error
}

func (e *imageTransferError) Error() string { return e.Err.Error() }
func (e *imageTransferError) Unwrap() error { return e.Err }

// CLI flag overrides (bound in main_cli.go). They have the highest priority.
var (
	imageS3Endpoint  string
	imageS3Region    string
	imageS3Bucket    string
	imageS3Prefix    string
	imageS3PublicURL string
	imageS3AccessKey string
	imageS3SecretKey string
)

func loadImageConfigFile() (imageS3Config, bool) {
	var cfg imageS3Config
	if imageConfigFile == "" {
		return cfg, false
	}
	data, err := os.ReadFile(imageConfigFile)
	if err != nil {
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, false
	}
	// Older config files omitted false-valued booleans. Missing means the
	// documented/default path-style behavior; newly written files always carry
	// the field so an explicit false survives reloads.
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) == nil {
		if _, present := fields["forcePathStyle"]; !present {
			cfg.ForcePathStyle = true
		}
	}
	return cfg, true
}

func saveImageConfigFile(cfg imageS3Config) error {
	if imageConfigFile == "" {
		return fmt.Errorf("image config file is not configured for this build")
	}
	if err := os.MkdirAll(filepath.Dir(imageConfigFile), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(imageConfigFile), "image-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, imageConfigFile)
}

func imageConfigEnv(key string) string {
	if v := os.Getenv("MEMODUMP_IMAGE_S3_" + key); v != "" {
		return v
	}
	if v := parseEnvFile(".env")["IMAGE_S3_"+key]; v != "" {
		return v
	}
	return ""
}

func overlayImageConfigValue(cfg *imageS3Config, key, val string) {
	if val == "" {
		return
	}
	switch key {
	case "ENDPOINT":
		cfg.Endpoint = val
	case "REGION":
		cfg.Region = val
	case "BUCKET":
		cfg.Bucket = val
	case "PREFIX":
		cfg.Prefix = val
	case "PUBLIC_URL":
		cfg.PublicBaseURL = val
	case "ACCESS_KEY":
		cfg.AccessKey = val
	case "SECRET_KEY":
		cfg.SecretKey = val
	case "FORCE_PATH_STYLE":
		cfg.ForcePathStyle = val == "1" || strings.EqualFold(val, "true")
	}
}

// effectiveImageS3Config resolves the config per request with precedence
// flags → env → .env → data-dir file, so changing the persisted file applies
// immediately without a restart.
func effectiveImageS3Config() imageS3Config {
	cfg, hasFile := loadImageConfigFile()
	if !hasFile {
		cfg.ForcePathStyle = true
	}
	for _, key := range []string{
		"ENDPOINT", "REGION", "BUCKET", "PREFIX", "PUBLIC_URL", "ACCESS_KEY",
		"SECRET_KEY", "FORCE_PATH_STYLE",
	} {
		overlayImageConfigValue(&cfg, key, imageConfigEnv(key))
	}
	overlayImageConfigValue(&cfg, "ENDPOINT", imageS3Endpoint)
	overlayImageConfigValue(&cfg, "REGION", imageS3Region)
	overlayImageConfigValue(&cfg, "BUCKET", imageS3Bucket)
	overlayImageConfigValue(&cfg, "PREFIX", imageS3Prefix)
	overlayImageConfigValue(&cfg, "PUBLIC_URL", imageS3PublicURL)
	overlayImageConfigValue(&cfg, "ACCESS_KEY", imageS3AccessKey)
	overlayImageConfigValue(&cfg, "SECRET_KEY", imageS3SecretKey)
	// A persisted `provider: local` must not suppress a higher-priority S3
	// configuration supplied by flags/env/.env.
	if imageConfigHasHigherOverride() {
		cfg.Provider = "s3"
	}
	_ = normalizeImageURLs(&cfg, true)
	return cfg
}

// imageConfigHasHigherOverride reports whether flags/env/.env supply any S3
// config, in which case the settings panel must be read-only.
func imageConfigHasHigherOverride() bool {
	flagSet := imageS3Endpoint != "" || imageS3Region != "" || imageS3Bucket != "" ||
		imageS3Prefix != "" || imageS3PublicURL != "" || imageS3AccessKey != "" || imageS3SecretKey != ""
	if flagSet {
		return true
	}
	for _, key := range []string{
		"ENDPOINT", "REGION", "BUCKET", "PREFIX", "PUBLIC_URL", "ACCESS_KEY",
		"SECRET_KEY", "FORCE_PATH_STYLE",
	} {
		if imageConfigEnv(key) != "" {
			return true
		}
	}
	return false
}

func s3Active(cfg imageS3Config) bool {
	return cfg.Provider != "local" &&
		cfg.Endpoint != "" && cfg.Bucket != "" &&
		cfg.AccessKey != "" && cfg.SecretKey != "" && cfg.PublicBaseURL != ""
}

// imageTargetID is a secret-free revision of the effective destination. It is
// persisted in each browser outbox entry and checked again by the upload
// endpoint, so a retry can never silently use a newly changed server config.
func imageTargetID(cfg imageS3Config) string {
	if !s3Active(cfg) {
		return "local"
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	identity := strings.Join([]string{
		strings.TrimRight(cfg.Endpoint, "/"), region, cfg.Bucket, cfg.Prefix,
		cfg.PublicBaseURL, fmt.Sprintf("%t", cfg.ForcePathStyle),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "s3:" + hex.EncodeToString(sum[:])
}

// cleanupEnabled reports whether the periodic image cleanup is switched on.
func cleanupEnabled(cfg imageS3Config) bool {
	return cfg.Cleanup != nil && cfg.Cleanup.Enabled
}

// normalizeImageURLs trims fields and canonicalizes publicBaseUrl and prefix.
// publicBaseUrl keeps any path (e.g. https://cdn.example.com/images) but loses
// the trailing slash; prefix loses both slashes; the final image URL is joined
// with exactly one slash each side.
func normalizeImageURLs(cfg *imageS3Config, allowInsecure bool) error {
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Prefix = strings.Trim(strings.TrimSpace(cfg.Prefix), "/")
	cfg.Region = strings.TrimSpace(cfg.Region)

	base := strings.TrimSpace(cfg.PublicBaseURL)
	base = strings.TrimRight(base, "/")
	if base != "" {
		u, err := url.Parse(base)
		if err != nil {
			return fmt.Errorf("publicBaseUrl is not a valid URL")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("publicBaseUrl must be an absolute http(s) URL")
		}
		if u.Host == "" {
			return fmt.Errorf("publicBaseUrl must include a host")
		}
		if u.User != nil {
			return fmt.Errorf("publicBaseUrl must not include userinfo")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("publicBaseUrl must not include a query or fragment")
		}
		if u.Scheme == "http" && !allowInsecure {
			return fmt.Errorf("publicBaseUrl must use https")
		}
		// Rebuild from the parsed URL so any trailing slashes in the path are
		// normalized away consistently.
		u.Path = strings.TrimRight(u.Path, "/")
		base = strings.TrimRight(u.String(), "/")
	}
	cfg.PublicBaseURL = base
	return nil
}

// buildImageURL returns the final public URL for a key.
func buildImageURL(cfg imageS3Config, key string) string {
	url := cfg.PublicBaseURL + "/"
	if cfg.Prefix != "" {
		url += cfg.Prefix + "/"
	}
	return url + key
}

func newMinioClient(cfg imageS3Config) (*minio.Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	secure := u.Scheme == "https" || u.Scheme == ""
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	lookup := minio.BucketLookupDNS
	if cfg.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}
	return minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       region,
		BucketLookup: lookup,
	})
}

// s3PutImage uploads the verified temp file to S3 and then verifies that the
// object is publicly readable at the final URL. The client is told success only
// when both steps pass, closing the "PUT ok but URL 403s" window.
func s3PutImage(cfg imageS3Config, key, tmpPath string, size int64, contentType string) error {
	client, err := newMinioClient(cfg)
	if err != nil {
		return &imageTransferError{Code: "invalid_config", Err: err}
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("upload_failed: %w", err)
	}
	defer f.Close()

	objectName := objectNameForImage(cfg, key)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.PutObject(ctx, cfg.Bucket, objectName, f, size, minio.PutObjectOptions{
		ContentType: contentType,
	}); err != nil {
		return classifyS3TransferError(err)
	}
	if err := verifyPublicImageURL(cfg, key); err != nil {
		return &imageTransferError{Code: "verify_failed", Err: err}
	}
	return nil
}

func classifyS3TransferError(err error) error {
	resp := minio.ToErrorResponse(err)
	code := "server"
	switch resp.Code {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidToken", "ExpiredToken":
		code = "auth"
	case "AccessDenied", "AllAccessDisabled":
		code = "permission"
	case "NoSuchBucket", "InvalidBucketName", "AuthorizationHeaderMalformed",
		"PermanentRedirect", "InvalidRegion", "InvalidRequest":
		code = "invalid_config"
	default:
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			code = "auth"
		case http.StatusForbidden:
			code = "permission"
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict:
			code = "invalid_config"
		}
	}
	return &imageTransferError{Code: code, Err: err}
}

func objectNameForImage(cfg imageS3Config, key string) string {
	if cfg.Prefix == "" {
		return key
	}
	return cfg.Prefix + "/" + key
}

// verifyPublicImageURL performs an anonymous HEAD (falling back to a ranged
// GET) of the final public URL.
func verifyPublicImageURL(cfg imageS3Config, key string) error {
	target := buildImageURL(cfg, key)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Head(target)
	if err == nil {
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300
		_ = resp.Body.Close()
		if ok {
			return nil
		}
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("public image URL is invalid: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")
	rangeResp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("public image URL is not reachable: %w", err)
	}
	defer rangeResp.Body.Close()
	if rangeResp.StatusCode >= 200 && rangeResp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("public image URL returned %s; check bucket read policy and publicBaseUrl", rangeResp.Status)
}

func imageProbeKey() string {
	body := []byte("memodump-probe")
	sum := sha256.Sum256(body)
	return ".memodump-probe/" + hex.EncodeToString(sum[:]) + ".txt"
}

// testImageS3Config probes a candidate S3 config: PUT a probe object, verify an
// anonymous GET of its public URL, then best-effort DELETE. DELETE failure
// becomes a warning, not a failure.
func testImageS3Config(cfg imageS3Config) (warnings []string, err error) {
	client, err := newMinioClient(cfg)
	if err != nil {
		return nil, err
	}
	probeKey := imageProbeKey()
	probeObjectName := objectNameForImage(cfg, probeKey)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.PutObject(ctx, cfg.Bucket, probeObjectName, strings.NewReader("memodump-probe"), int64(len("memodump-probe")), minio.PutObjectOptions{
		ContentType: "text/plain",
	}); err != nil {
		return nil, fmt.Errorf("probe upload failed: %w", err)
	}
	if err := verifyPublicImageURL(cfg, probeKey); err != nil {
		return nil, fmt.Errorf("probe not publicly readable: %w", err)
	}
	if err := client.RemoveObject(ctx, cfg.Bucket, probeObjectName, minio.RemoveObjectOptions{}); err != nil {
		warnings = append(warnings, fmt.Sprintf("probe object could not be removed (bucket policy may not allow DELETE): %v", err))
	}
	return warnings, nil
}

func imageConfigPublic(cfg imageS3Config) map[string]any {
	if !s3Active(cfg) {
		return map[string]any{
			"provider":   "local",
			"targetId":   "local",
			"configured": false,
			"editable":   !imageConfigHasHigherOverride(),
			"cleanup":    map[string]any{"enabled": cleanupEnabled(cfg)},
		}
	}
	return map[string]any{
		"provider":      "s3",
		"targetId":      imageTargetID(cfg),
		"bucket":        cfg.Bucket,
		"publicBaseUrl": cfg.PublicBaseURL,
		"prefix":        cfg.Prefix,
		"configured":    true,
		"editable":      !imageConfigHasHigherOverride(),
		"cleanup":       map[string]any{"enabled": cleanupEnabled(cfg)},
	}
}

// imageConfigEditorPublic is returned only from the authenticated image-config
// endpoint. It includes every non-secret field required to edit and save the
// config after a reload; secretKey remains server-only.
func imageConfigEditorPublic(cfg imageS3Config) map[string]any {
	result := imageConfigPublic(cfg)
	if s3Active(cfg) {
		result["endpoint"] = cfg.Endpoint
		result["region"] = cfg.Region
		result["accessKey"] = cfg.AccessKey
		result["forcePathStyle"] = cfg.ForcePathStyle
	}
	return result
}

func handleImageConfigGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, imageConfigEditorPublic(effectiveImageS3Config()))
}

// handleImageConfigSave persists the S3 settings from the panel. Secrets are
// stored server-side and never returned; an empty secretKey means "unchanged"
// unless the endpoint/bucket/accessKey identity changed, in which case the
// secret must be provided again.
func handleImageConfigSave(w http.ResponseWriter, r *http.Request) {
	var req imageS3Config
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", "Request format error")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider != "local" && req.Provider != "s3" {
		writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", "provider must be local or s3")
		return
	}

	if req.Provider == "s3" {
		if err := normalizeImageURLs(&req, true); err != nil {
			writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
		if req.Endpoint == "" || req.Bucket == "" || req.AccessKey == "" || req.PublicBaseURL == "" {
			writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", "endpoint, bucket, accessKey and publicBaseUrl are required")
			return
		}
		stored, hasStored := loadImageConfigFile()
		identityUnchanged := hasStored && stored.Provider == "s3" &&
			stored.Endpoint == req.Endpoint && stored.Bucket == req.Bucket && stored.AccessKey == req.AccessKey
		if req.SecretKey == "" {
			if !identityUnchanged || stored.SecretKey == "" {
				writeImageErrorCode(w, http.StatusBadRequest, "invalid_config",
					"secretKey is required when creating a config or changing endpoint, bucket or accessKey")
				return
			}
			req.SecretKey = stored.SecretKey
		}
	} else {
		// Reverting to the local vault clears all stored S3 settings, but the
		// cleanup preference is orthogonal and survives a provider switch.
		req = imageS3Config{Provider: "local", ForcePathStyle: true, Cleanup: req.Cleanup}
	}

	if err := saveImageConfigFile(req); err != nil {
		writeImageErrorCode(w, http.StatusInternalServerError, "save_failed", "Failed to save image config")
		return
	}
	writeJSON(w, http.StatusOK, imageConfigEditorPublic(effectiveImageS3Config()))
}

// handleImageConfigTest runs the server-side probe against the effective config
// (or a candidate config from the request body, merged over the effective one).
func handleImageConfigTest(w http.ResponseWriter, r *http.Request) {
	cfg := effectiveImageS3Config()
	original := cfg
	var req imageS3Config
	if r.ContentLength != 0 {
		var raw map[string]json.RawMessage
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(body, &req) != nil || json.Unmarshal(body, &raw) != nil {
			writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", "Request format error")
			return
		}
		_, forcePathStylePresent := raw["forcePathStyle"]
		mergeImageConfig(&cfg, req, forcePathStylePresent)
	}
	if err := normalizeImageURLs(&cfg, true); err != nil {
		writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	if req.SecretKey == "" && (cfg.Endpoint != original.Endpoint || cfg.Bucket != original.Bucket || cfg.AccessKey != original.AccessKey) {
		writeImageErrorCode(w, http.StatusBadRequest, "invalid_config",
			"secretKey is required when changing endpoint, bucket or accessKey")
		return
	}
	if !s3Active(cfg) {
		writeImageErrorCode(w, http.StatusBadRequest, "invalid_config", "S3 is not configured")
		return
	}
	warnings, err := testImageS3Config(cfg)
	if err != nil {
		writeImageErrorCode(w, http.StatusBadRequest, "test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warnings": warnings})
}

func mergeImageConfig(dst *imageS3Config, src imageS3Config, forcePathStylePresent bool) {
	if src.Endpoint != "" {
		dst.Endpoint = src.Endpoint
	}
	if src.Region != "" {
		dst.Region = src.Region
	}
	if src.Bucket != "" {
		dst.Bucket = src.Bucket
	}
	if src.Prefix != "" || dst.Prefix != "" {
		// An explicitly empty prefix is a valid value; only skip when the
		// field was omitted entirely. JSON can't distinguish here, so treat
		// empty as "keep" unless the whole provider field says s3 with an
		// endpoint (a full candidate config).
		if src.Endpoint != "" {
			dst.Prefix = src.Prefix
		}
	}
	if src.PublicBaseURL != "" {
		dst.PublicBaseURL = src.PublicBaseURL
	}
	if src.AccessKey != "" {
		dst.AccessKey = src.AccessKey
	}
	if src.SecretKey != "" {
		dst.SecretKey = src.SecretKey
	}
	if forcePathStylePresent {
		dst.ForcePathStyle = src.ForcePathStyle
	}
}
