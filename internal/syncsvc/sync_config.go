package syncsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"memodump/internal/syncprovider/s3"
)

// ConfigFile is where the Wails settings panel persists the note-sync S3
// configuration. It is a per-build package var (same seam as imagesvc.ConfigFile):
// the Wails desktop stores it in the OS user-config dir so cloud-synced data
// folders never carry long-lived credentials. The CLI Web server does not own
// cloud sync and never sets it. Environment variables (MEMODUMP_SYNC_*)
// override the file, matching the images precedence (env > file).
var ConfigFile string

// syncConfigJSON is the persisted shape of the note-sync S3 configuration. It
// mirrors s3.Config minus the non-serializable HTTPClient.
type syncConfigJSON struct {
	Endpoint       string `json:"endpoint"`
	Region         string `json:"region"`
	Bucket         string `json:"bucket"`
	Prefix         string `json:"prefix"`
	AccessKey      string `json:"accessKey"`
	SecretKey      string `json:"secretKey"`
	ForcePathStyle bool   `json:"forcePathStyle"`
}

func loadSyncConfigFile() (syncConfigJSON, bool) {
	var cfg syncConfigJSON
	if ConfigFile == "" {
		return cfg, false
	}
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, false
	}
	if !cfg.ForcePathStyle {
		// A missing forcePathStyle on older files means the documented default
		// path-style behavior; newly written files always carry the field.
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) == nil {
			if _, present := fields["forcePathStyle"]; !present {
				cfg.ForcePathStyle = true
			}
		}
	}
	return cfg, true
}

func saveSyncConfigFile(cfg syncConfigJSON) error {
	if ConfigFile == "" {
		return fmt.Errorf("sync config file is not configured for this build")
	}
	if err := os.MkdirAll(filepath.Dir(ConfigFile), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(ConfigFile), "sync-config-*.tmp")
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
	return os.Rename(tmpPath, ConfigFile)
}

// syncConfigHasEnvOverride reports whether any MEMODUMP_SYNC_* environment
// variable supplies configuration, in which case the settings panel must be
// read-only (env wins over the persisted file).
func syncConfigHasEnvOverride() bool {
	for _, key := range []string{
		"MEMODUMP_SYNC_ENDPOINT", "MEMODUMP_SYNC_REGION", "MEMODUMP_SYNC_BUCKET",
		"MEMODUMP_SYNC_PREFIX", "MEMODUMP_SYNC_ACCESS_KEY", "MEMODUMP_SYNC_SECRET_KEY",
		"MEMODUMP_SYNC_FORCE_PATH_STYLE",
	} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// syncS3Config reads the note-sync S3-provider configuration: the persisted
// file (when set) as the base, with MEMODUMP_SYNC_* environment variables
// overriding it. An empty endpoint or bucket means "no real provider
// configured".
func syncS3Config() s3.Config {
	cfgJSON, hasFile := loadSyncConfigFile()
	cfg := s3.Config{
		Endpoint:       cfgJSON.Endpoint,
		Region:         cfgJSON.Region,
		Bucket:         cfgJSON.Bucket,
		Prefix:         cfgJSON.Prefix,
		AccessKey:      cfgJSON.AccessKey,
		SecretKey:      cfgJSON.SecretKey,
		ForcePathStyle: cfgJSON.ForcePathStyle,
	}
	if !hasFile {
		cfg.ForcePathStyle = true
	}
	if v := os.Getenv("MEMODUMP_SYNC_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if v := os.Getenv("MEMODUMP_SYNC_REGION"); v != "" {
		cfg.Region = v
	}
	if v := os.Getenv("MEMODUMP_SYNC_BUCKET"); v != "" {
		cfg.Bucket = v
	}
	if v := os.Getenv("MEMODUMP_SYNC_PREFIX"); v != "" {
		cfg.Prefix = v
	}
	if v := os.Getenv("MEMODUMP_SYNC_ACCESS_KEY"); v != "" {
		cfg.AccessKey = v
	}
	if v := os.Getenv("MEMODUMP_SYNC_SECRET_KEY"); v != "" {
		cfg.SecretKey = v
	}
	if v := os.Getenv("MEMODUMP_SYNC_FORCE_PATH_STYLE"); v != "" {
		cfg.ForcePathStyle = v == "1"
	}
	return cfg
}

// syncConfigConfigured reports whether the effective config is complete enough
// to build a provider.
func syncConfigConfigured(cfg s3.Config) bool {
	return cfg.Endpoint != "" && cfg.Bucket != "" && cfg.AccessKey != "" && cfg.SecretKey != ""
}

// syncConfigEditorPublic returns the config for the settings panel. Secrets are
// server-side only: accessKey is returned (the panel can prefill it) but the
// secretKey is never sent to the frontend; an empty secretKey on save means
// "unchanged".
func syncConfigEditorPublic(cfg s3.Config) map[string]any {
	return map[string]any{
		"endpoint":       cfg.Endpoint,
		"region":         cfg.Region,
		"bucket":         cfg.Bucket,
		"prefix":         cfg.Prefix,
		"accessKey":      cfg.AccessKey,
		"forcePathStyle": cfg.ForcePathStyle,
		"configured":     syncConfigConfigured(cfg),
		"editable":       !syncConfigHasEnvOverride(),
	}
}

// mergeSyncConfig overlays a candidate (draft) config onto the effective one.
// Empty candidate fields keep the effective value, so the panel can send a
// partial draft (for example an unchanged secretKey) and only the filled
// fields take effect.
func mergeSyncConfig(dst *s3.Config, src syncConfigJSON, forcePathStylePresent bool) {
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
		if src.Endpoint != "" {
			dst.Prefix = src.Prefix
		}
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
