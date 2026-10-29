package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Oasis1i/bill-snap/internal/model"
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
	if cfg.MCPToken != "" {
		t.Fatalf("mcp token %q", cfg.MCPToken)
	}
}

func TestLoadMCPToken(t *testing.T) {
	t.Setenv("APP_TZ", "UTC")
	t.Setenv("UPLOAD_TOKEN", "")
	t.Setenv("MCP_TOKEN", "  remote-secret  ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPToken != "remote-secret" {
		t.Fatalf("mcp token %q", cfg.MCPToken)
	}
	if err := cfg.ValidateForServer(); err == nil {
		t.Fatal("mcp token does not replace UPLOAD_TOKEN")
	}
}

func TestLoadCategories(t *testing.T) {
	t.Setenv("APP_TZ", "UTC")
	t.Setenv("CATEGORIES", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Categories, model.DefaultCategories()) {
		t.Fatalf("defaults %v", cfg.Categories)
	}

	t.Setenv("CATEGORIES", "+宠物,-工资")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(cfg.Categories, "宠物") || containsString(cfg.Categories, "工资") || !containsString(cfg.Categories, "其他") {
		t.Fatalf("custom %v", cfg.Categories)
	}
	if model.NormalizeCategory("宠物") == "宠物" {
		t.Fatal("Load installed the category list; startup does that")
	}

	t.Setenv("CATEGORIES", "宠物")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Categories, []string{"宠物", "其他"}) {
		t.Fatalf("override %v", cfg.Categories)
	}

	t.Setenv("CATEGORIES", "-其他")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("got %v", err)
	}
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func TestLoadRejectsBadLimit(t *testing.T) {
	t.Setenv("APP_TZ", "UTC")
	t.Setenv("MAX_UPLOAD_BYTES", "nope")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_UPLOAD_BYTES") {
		t.Fatalf("got %v", err)
	}
}
