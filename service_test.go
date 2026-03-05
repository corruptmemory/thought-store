package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePaths(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "thought-store")
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(binPath, []byte("binary"), 0755)
	os.WriteFile(cfgPath, []byte("config"), 0644)

	absBin, absCfg, err := ResolvePaths(binPath, cfgPath)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	if !filepath.IsAbs(absBin) {
		t.Errorf("expected absolute binary path, got %s", absBin)
	}
	if !filepath.IsAbs(absCfg) {
		t.Errorf("expected absolute config path, got %s", absCfg)
	}
}

func TestResolvePathsMissing(t *testing.T) {
	_, _, err := ResolvePaths("/nonexistent/binary", "/nonexistent/config")
	if err == nil {
		t.Fatal("expected error for missing files")
	}
}
