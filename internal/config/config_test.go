package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFileDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("BILL_SNAP_TEST_DOTENV=from-file\nBILL_SNAP_TEST_OTHER=\"quoted value\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BILL_SNAP_TEST_DOTENV", "from-env")
	t.Cleanup(func() { os.Unsetenv("BILL_SNAP_TEST_OTHER") })
	os.Unsetenv("BILL_SNAP_TEST_OTHER")

	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("BILL_SNAP_TEST_DOTENV"); got != "from-env" {
		t.Fatalf("env should win, got %q", got)
	}
	if got := os.Getenv("BILL_SNAP_TEST_OTHER"); got != "quoted value" {
		t.Fatalf("quoted value: %q", got)
	}
}

func TestLoadRequiresNothingForMCP(t *testing.T) {
	t.Setenv("APP_TZ", "UTC")
	t.Setenv("DB_PATH", "data/test.db")
	t.Setenv("UPLOAD_TOKEN", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Location.String() != "UTC" {
		t.Fatal(cfg.Location)
	}
	if err := cfg.ValidateForServer(); err == nil {
		t.Fatal("empty token should fail server validation")
	}
}

func TestLoadRejectsBadLimit(t *testing.T) {
	t.Setenv("APP_TZ", "UTC")
	t.Setenv("MAX_UPLOAD_BYTES", "nope")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_UPLOAD_BYTES") {
		t.Fatalf("got %v", err)
	}
}
