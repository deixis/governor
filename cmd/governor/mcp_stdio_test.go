package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A stdio client that writes one request and closes its end of the pipe
// must still receive the response, and the server must exit cleanly.
// Reproduction from https://github.com/deixis/governor/issues/1.
func TestMCPStdio_ClientClosesAfterInitialize(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "governor")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building governor: %v\n%s", err, out)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/w\n\ngo 1.25.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		cmd := exec.Command(bin, "mcp")
		cmd.Dir = workspace
		cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}` + "\n")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			t.Fatalf("run %d: governor mcp exited with %v\nstderr: %s", i, err, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, `"id":1`) || !strings.Contains(out, `"result"`) || !strings.Contains(out, `"serverInfo"`) {
			t.Fatalf("run %d: no initialize result on stdout\nstdout: %s\nstderr: %s", i, out, stderr.String())
		}
		if strings.Contains(stderr.String(), "server is closing") {
			t.Fatalf("run %d: unexpected stderr: %s", i, stderr.String())
		}
	}
}
