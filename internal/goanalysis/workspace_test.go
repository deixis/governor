package goanalysis

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyFixture copies testdata/<name> into a temp dir, stripping the .txt
// suffix that keeps fixtures out of the real build.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, strings.TrimSuffix(rel, ".txt"))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying fixture %s: %v", name, err)
	}
	// Resolve symlinks (macOS /var -> /private/var) so positions compare.
	if resolved, err := filepath.EvalSymlinks(dst); err == nil {
		dst = resolved
	}
	return dst
}

func TestCacheInvalidation(t *testing.T) {
	dir := copyFixture(t, "sample")
	ws := NewWorkspace(dir)
	ctx := context.Background()

	if _, err := ws.Diagnostics(ctx, nil); err != nil {
		t.Fatal(err)
	}
	first := ws.snap

	if _, err := ws.Search(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if ws.snap != first {
		t.Fatal("snapshot reloaded without changes")
	}

	// Introduce an error; mtime granularity may be coarse, so bump it.
	path := filepath.Join(dir, "client.go")
	data, _ := os.ReadFile(path)
	data = append(data, []byte("\nfunc broken() { undefined() }\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	diags, err := ws.Diagnostics(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ws.snap == first {
		t.Fatal("snapshot not reloaded after change")
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "undefined: undefined") {
		t.Errorf("diags = %+v", diags)
	}
}
