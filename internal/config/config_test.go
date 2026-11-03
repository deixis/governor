package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FromRepoRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".governor"), []byte("version: 1\ntimeout: 10m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.RepoRoot != dir {
		t.Errorf("RepoRoot = %q, want %q", res.RepoRoot, dir)
	}
	if res.Config.Version != 1 {
		t.Errorf("Config.Version = %d, want 1", res.Config.Version)
	}
	if res.Config.RawTimeout != "10m" {
		t.Errorf("Config.RawTimeout = %q, want %q", res.Config.RawTimeout, "10m")
	}
}

func TestLoad_FromSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".governor"), []byte("version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(root, "pkg", "foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Load(sub)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.RepoRoot != root {
		t.Errorf("RepoRoot = %q, want %q", res.RepoRoot, root)
	}
	if res.Config.Version != 2 {
		t.Errorf("Config.Version = %d, want 2", res.Config.Version)
	}
}

func TestLoad_NoGoMod(t *testing.T) {
	dir := t.TempDir()

	res, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.RepoRoot != dir {
		t.Errorf("RepoRoot = %q, want %q (fallback to workspace)", res.RepoRoot, dir)
	}
	// Should return default config.
	if res.Config.RawTimeout != "" {
		t.Errorf("expected default config, got RawTimeout = %q", res.Config.RawTimeout)
	}
}

func TestLoad_YAMLSuffix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".governor.yaml"), []byte("timeout: 2m\nexclude: [\"./e2e/...\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := res.Config.RawTimeout; got != "2m" {
		t.Errorf("RawTimeout = %q, want 2m", got)
	}
	if got := res.Config.Exclude; len(got) != 1 || got[0] != "./e2e/..." {
		t.Errorf("Exclude = %v, want [./e2e/...]", got)
	}
}

func TestLoad_MultipleFilesIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".governor", ".governor.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), ".governor, .governor.yml") {
		t.Fatalf("Load = %v, want multiple config files error", err)
	}
}

func TestLoad_NoGovernorFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.RepoRoot != dir {
		t.Errorf("RepoRoot = %q, want %q", res.RepoRoot, dir)
	}
	// Should return default config with no error.
	if res.Config.Version != 0 {
		t.Errorf("expected default config, got Version = %d", res.Config.Version)
	}
}
