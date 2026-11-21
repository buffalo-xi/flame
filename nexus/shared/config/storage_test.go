package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The data root is a platform-level setting — it is the root of tenants/, platform-assets/ (which belongs to no tenant)
// and plugins/ — so it must not be nested under an "agent" section. Pinning the key layout is worth a test because the
// failure mode is silent: yaml.v3 ignores unknown keys, so a rename that only touches the code would leave root_dir
// empty, fall back to ../data, and surface as "all the tenant data is gone" instead of a config error.
func TestStorageAndDownloadKeys(t *testing.T) {
	const body = "jwt:\n  secret_key: \"jwt-secret-key-0123456789-0123456789\"\n" +
		"storage:\n" +
		"  root_dir: /srv/flame/data\n" +
		"  driver: nfs\n" +
		"  s3:\n    bucket: b\n    prefix: p\n" +
		"download:\n" +
		"  x_accel_prefix: /__files\n" +
		"  presign_download: true\n" +
		"  presign_ttl: 60\n" +
		"  max_download_size: 8\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Storage.RootDir != "/srv/flame/data" {
		t.Errorf("storage.root_dir = %q, want /srv/flame/data", cfg.Storage.RootDir)
	}
	if cfg.Storage.Driver != "nfs" {
		t.Errorf("storage.driver = %q, want nfs", cfg.Storage.Driver)
	}
	if cfg.Storage.S3.Prefix != "p" {
		t.Errorf("storage.s3.prefix = %q, want p (the s3 root sits at the same level as root_dir)", cfg.Storage.S3.Prefix)
	}
	if cfg.Download.XAccelPrefix != "/__files" {
		t.Errorf("download.x_accel_prefix = %q, want /__files", cfg.Download.XAccelPrefix)
	}
	if !cfg.Download.PresignDownload {
		t.Error("download.presign_download = false, want true")
	}
	if got := cfg.Download.PresignTTLDuration(); got != time.Minute {
		t.Errorf("presign ttl = %v, want 1m", got)
	}
	if got := cfg.Download.MaxDownloadBytes(); got != 8<<20 {
		t.Errorf("max download = %d, want %d", got, 8<<20)
	}
}

// StorageRootDir is the nil-safe entry point every command uses; an unloaded configuration must report "" instead of
// panicking (see the comment on the function).
func TestStorageRootDir(t *testing.T) {
	prev := Cfg
	t.Cleanup(func() { Cfg = prev })

	Cfg = nil
	if got := StorageRootDir(); got != "" {
		t.Errorf("unloaded configuration: root = %q, want empty", got)
	}

	Cfg = &Config{Storage: StorageConfig{RootDir: "/srv/flame/data"}}
	if got := StorageRootDir(); got != "/srv/flame/data" {
		t.Errorf("root = %q, want /srv/flame/data", got)
	}
}
