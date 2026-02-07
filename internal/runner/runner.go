// Package runner provides safe command execution with workspace bounds,
// timeouts, and output size limits.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Runner executes commands safely within a workspace boundary.
type Runner struct {
	Workspace string
	Timeout   time.Duration
	MaxOutput int // bytes
}

// Run executes a command with the given argv. The first element is the
// binary name (resolved via PATH), and the rest are arguments.
// cwd is resolved relative to the workspace root and must remain within it.
func (r *Runner) Run(ctx context.Context, argv []string, cwd string) (*Result, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}

	// Resolve and validate cwd.
	dir, err := r.resolveDir(cwd)
	if err != nil {
		return nil, err
	}

	timeout := r.Timeout
	maxOutput := r.MaxOutput

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	runID := uuid.New().String()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{buf: &stdout, limit: maxOutput}
	cmd.Stderr = &limitWriter{buf: &stderr, limit: maxOutput}

	runErr := cmd.Run()

	truncated := stdout.Len() >= maxOutput || stderr.Len() >= maxOutput

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Binary not found or other exec error.
			return nil, fmt.Errorf("executing %s: %w", argv[0], runErr)
		}
	}

	return &Result{
		RunID:     runID,
		ExitCode:  exitCode,
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: truncated,
	}, nil
}

// resolveDir resolves cwd relative to the workspace and validates it
// is within the workspace boundary.
func (r *Runner) resolveDir(cwd string) (string, error) {
	if cwd == "" {
		return r.Workspace, nil
	}

	var dir string
	if filepath.IsAbs(cwd) {
		dir = filepath.Clean(cwd)
	} else {
		dir = filepath.Clean(filepath.Join(r.Workspace, cwd))
	}

	// Ensure dir is within workspace.
	rel, err := filepath.Rel(r.Workspace, dir)
	if err != nil {
		return "", fmt.Errorf("resolving cwd: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("cwd %q is outside workspace %q", cwd, r.Workspace)
	}
	return dir, nil
}

// limitWriter writes up to limit bytes to buf, then silently discards the rest.
type limitWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard
	}
	if len(p) > remaining {
		// Write only what fits, but report all bytes as consumed
		// to avoid short write errors from io.Copy.
		w.buf.Write(p[:remaining])
		return len(p), nil
	}
	return w.buf.Write(p)
}
