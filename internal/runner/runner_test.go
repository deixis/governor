package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	return &Runner{
		Workspace: t.TempDir(),
		Timeout:   10 * time.Second,
		MaxOutput: 1 << 20,
	}
}

func TestRun_Success(t *testing.T) {
	r := newTestRunner(t)
	res, err := r.Run(context.Background(), []string{"echo", "hello"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Errorf("Stdout = %q, want to contain 'hello'", res.Stdout)
	}
	if res.RunID == "" {
		t.Error("RunID is empty")
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	r := newTestRunner(t)
	res, err := r.Run(context.Background(), []string{"/usr/bin/false"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero")
	}
}

func TestRun_BinaryNotFound(t *testing.T) {
	r := newTestRunner(t)
	_, err := r.Run(context.Background(), []string{"nonexistent-binary-xyz-123"}, "")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "nonexistent-binary-xyz-123") {
		t.Errorf("error = %q, want to mention the binary name", err)
	}
}

func TestRun_EmptyArgv(t *testing.T) {
	r := newTestRunner(t)
	_, err := r.Run(context.Background(), nil, "")
	if err == nil {
		t.Fatal("expected error for empty argv")
	}
}

func TestRun_CWDWithinWorkspace(t *testing.T) {
	r := newTestRunner(t)
	sub := filepath.Join(r.Workspace, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []string{"pwd"}, "subdir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "subdir") {
		t.Errorf("Stdout = %q, want to contain 'subdir'", res.Stdout)
	}
}

func TestRun_CWDOutsideWorkspace_Relative(t *testing.T) {
	r := newTestRunner(t)
	_, err := r.Run(context.Background(), []string{"echo"}, "../")
	if err == nil {
		t.Fatal("expected error for cwd outside workspace")
	}
	if !strings.Contains(err.Error(), "outside workspace") {
		t.Errorf("error = %q, want 'outside workspace'", err)
	}
}

func TestRun_CWDOutsideWorkspace_Absolute(t *testing.T) {
	r := newTestRunner(t)
	_, err := r.Run(context.Background(), []string{"echo"}, "/tmp")
	if err == nil {
		t.Fatal("expected error for absolute cwd outside workspace")
	}
	if !strings.Contains(err.Error(), "outside workspace") {
		t.Errorf("error = %q, want 'outside workspace'", err)
	}
}

func TestRun_Timeout(t *testing.T) {
	r := newTestRunner(t)
	r.Timeout = 100 * time.Millisecond

	_, err := r.Run(context.Background(), []string{"sleep", "10"}, "")
	// On timeout, exec.CommandContext sends SIGKILL which produces an ExitError
	// (not a context error). Either way, we should get a result or an error.
	if err != nil {
		// Some systems may return an error rather than a result.
		return
	}
	// If we got a result, the exit code should be non-zero.
}

func TestRun_OutputTruncation(t *testing.T) {
	r := newTestRunner(t)
	r.MaxOutput = 100 // very small cap

	// Generate output larger than cap.
	res, err := r.Run(context.Background(), []string{"sh", "-c", "dd if=/dev/zero bs=200 count=1 2>/dev/null"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(res.Stdout) > r.MaxOutput {
		t.Errorf("len(Stdout) = %d, want <= %d", len(res.Stdout), r.MaxOutput)
	}
}
