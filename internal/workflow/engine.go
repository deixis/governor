// Package workflow provides the core execution engine for Governor's
// check and audit pipelines. It is consumed by both the MCP server
// and the CLI commands.
package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/deixis/governor/internal/config"
	"github.com/deixis/governor/internal/runner"
)

// CommandRunner executes commands within a workspace.
// Implemented by runner.Runner.
type CommandRunner interface {
	Run(ctx context.Context, argv []string, cwd string) (*runner.Result, error)
}

// Engine holds shared dependencies for all workflow operations.
type Engine struct {
	Config    *config.Config
	Runner    CommandRunner
	Workspace string // cwd — commands run from here, ./... scopes to here
	RepoRoot  string // module root — used for absolute-path resolution
}

// ResolvePackages normalises package arguments so that tools work
// identically regardless of how packages are specified. It accepts
// three input styles:
//
//   - Go import paths (e.g. "example.com/foo/bar/...") — passed through.
//   - Absolute directory paths (e.g. "/home/user/proj/bar") — converted
//     to a "./…" pattern relative to the repo root.
//   - Relative patterns (e.g. "./bar/...") — passed through unchanged.
//
// When the list is empty it defaults to "./..." (all packages in the
// workspace), matching the behaviour of `go test ./...`.
func (e *Engine) ResolvePackages(packages []string) []string {
	if len(packages) == 0 {
		return []string{"./..."}
	}

	resolved := make([]string, 0, len(packages))
	for _, p := range packages {
		if filepath.IsAbs(p) {
			// Convert absolute directory path to a repo-root-relative
			// pattern, so that paths anywhere in the module resolve
			// correctly regardless of cwd.
			base := e.RepoRoot
			if base == "" {
				base = e.Workspace
			}
			rel, err := filepath.Rel(base, p)
			if err != nil || strings.HasPrefix(rel, "..") {
				// Outside repo root — skip silently.
				continue
			}
			pattern := "./" + rel
			if !strings.HasSuffix(pattern, "...") {
				pattern += "/..."
			}
			resolved = append(resolved, pattern)
		} else {
			// Import path or relative pattern — pass through.
			resolved = append(resolved, p)
		}
	}

	if len(resolved) == 0 {
		return []string{"./..."}
	}
	return resolved
}

// ResolveTool returns the argv prefix for invoking a named tool.
// It checks "go tool <name>" first (Go 1.24+ tool directive in go.mod),
// then falls back to exec.LookPath on the system PATH.
// Returns nil if the tool is not available.
func ResolveTool(name string) []string {
	// Check if the tool is available via "go tool".
	goPath, err := exec.LookPath("go")
	if err == nil {
		// Probe with -h; exit code 0 or 2 (flag help) both indicate the tool exists.
		cmd := exec.Command(goPath, "tool", name, "-h")
		if err := cmd.Run(); err == nil {
			return []string{goPath, "tool", name}
		}
		// Some tools exit non-zero for -h but still exist. Check the specific
		// exit code — exec.ExitError with a code means the binary ran.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
			return []string{goPath, "tool", name}
		}
	}

	// Fallback to PATH lookup.
	toolPath, err := exec.LookPath(name)
	if err == nil {
		return []string{toolPath}
	}

	return nil
}

// toolInfo holds install metadata for a known tool.
type toolInfo struct {
	// ImportPath is the Go module path for go get -tool / go install.
	ImportPath string
	// AltInstall is an alternative install URL or instruction.
	AltInstall string
	// NoGoInstall is true if go get -tool / go install is not recommended.
	NoGoInstall bool
}

// knownTools maps tool binary names to their install metadata.
var knownTools = map[string]toolInfo{
	"gofumpt":       {ImportPath: "mvdan.cc/gofumpt@latest"},
	"staticcheck":   {ImportPath: "honnef.co/go/tools/cmd/staticcheck@latest"},
	"gocognit":      {ImportPath: "github.com/uudashr/gocognit/cmd/gocognit@latest"},
	"deadcode":      {ImportPath: "golang.org/x/tools/cmd/deadcode@latest"},
	"dupl":          {ImportPath: "github.com/mibk/dupl@latest"},
	"govulncheck":   {ImportPath: "golang.org/x/vuln/cmd/govulncheck@latest"},
	"golangci-lint": {AltInstall: "https://golangci-lint.run/welcome/install/", NoGoInstall: true},
	"gopls":         {ImportPath: "golang.org/x/tools/gopls@latest"},
}

// ErrToolUnavailable is returned when a required tool is not installed.
// It includes actionable install instructions when the tool is known.
type ErrToolUnavailable struct {
	Name string
	Info *toolInfo
}

func NewErrToolUnavailable(name string) ErrToolUnavailable {
	e := ErrToolUnavailable{Name: name}
	if info, ok := knownTools[name]; ok {
		e.Info = &info
	}
	return e
}

func (e ErrToolUnavailable) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is required but not installed.", e.Name)

	if e.Info == nil {
		return b.String()
	}

	fmt.Fprintln(&b)

	if e.Info.NoGoInstall {
		if e.Info.AltInstall != "" {
			fmt.Fprintf(&b, "\nInstall: %s", e.Info.AltInstall)
			fmt.Fprintf(&b, "\nNote: go get -tool and go install are not recommended for %s.", e.Name)
		}
	} else if e.Info.ImportPath != "" {
		importPath := strings.TrimSuffix(e.Info.ImportPath, "@latest")
		fmt.Fprintf(&b, "\nInstall:")
		fmt.Fprintf(&b, "\n  go get -tool %s   # adds to go.mod (recommended)", importPath)
		fmt.Fprintf(&b, "\n  go install %s     # installs globally", e.Info.ImportPath)
	}

	return b.String()
}

// derivePackageFromFile extracts a package-like path from a file path.
// This is best-effort; the caller may refine it with module info.
func derivePackageFromFile(file string) string {
	if file == "" {
		return ""
	}
	idx := strings.LastIndex(file, "/")
	if idx < 0 {
		return "."
	}
	return file[:idx]
}
