// Package workflow provides the core execution engine for Governor's
// check and audit pipelines. It is consumed by both the MCP server
// and the CLI commands.
package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"path"
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

	// ToolResolver maps a tool name to the argv prefix that invokes it,
	// or nil when unavailable. Defaults to ResolveTool in Workspace.
	// Tests inject a stub so results do not depend on the host PATH.
	ToolResolver func(name string) []string
}

// resolveTool returns the argv prefix for name using ToolResolver, or
// ResolveTool against the workspace when none is set.
func (e *Engine) resolveTool(name string) []string {
	if e.ToolResolver != nil {
		return e.ToolResolver(name)
	}
	return ResolveTool(e.Workspace, name)
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

// Packages resolves package arguments like ResolvePackages and then
// subtracts the exclude patterns, which come from the calling step's
// config. Without excludes the patterns pass through untouched. With
// excludes both sides are expanded through `go list`, and the result is
// the explicit import paths that remain.
func (e *Engine) Packages(ctx context.Context, packages, exclude []string) ([]string, error) {
	pkgs := e.ResolvePackages(packages)
	if len(exclude) == 0 {
		return pkgs, nil
	}

	included, err := e.listImportPaths(ctx, pkgs)
	if err != nil {
		return nil, err
	}
	excluded, err := e.listImportPaths(ctx, exclude)
	if err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(excluded))
	for _, p := range excluded {
		skip[p] = true
	}
	kept := included[:0]
	for _, p := range included {
		if !skip[p] {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("no packages left after exclude %v", exclude)
	}
	return kept, nil
}

// listImportPaths expands package patterns to import paths with go list.
func (e *Engine) listImportPaths(ctx context.Context, patterns []string) ([]string, error) {
	argv := append([]string{"go", "list", "-f", "{{.ImportPath}}"}, patterns...)
	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("listing packages: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("go list %s failed (exit %d): %s", strings.Join(patterns, " "), res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nonEmptyLines(res.Stdout), nil
}

// goFilesTemplate makes go list print every Go file of a package, one
// absolute path per line, test files included.
const goFilesTemplate = `{{$d := .Dir}}` +
	`{{range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}` +
	`{{range .CgoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}` +
	`{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}` +
	`{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}`

// PackageFiles lists the Go source files of the given packages, so that
// file-based tools (gofumpt, dupl) cover exactly the package scope and
// not every .go file under the workspace, worktrees and vendored copies
// included.
func (e *Engine) PackageFiles(ctx context.Context, pkgs []string) ([]string, error) {
	argv := append([]string{"go", "list", "-f", goFilesTemplate}, pkgs...)
	res, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("listing package files: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("go list failed (exit %d): %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nonEmptyLines(res.Stdout), nil
}

func nonEmptyLines(out []byte) []string {
	var lines []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// ResolveTool returns the argv prefix for invoking a named tool from dir.
// It checks the module's tool directives first (Go 1.24+, listed by
// `go tool` with no arguments), then falls back to exec.LookPath on the
// system PATH. Returns nil if the tool is not available.
//
// Probing with `go tool <name> -h` cannot work: a missing tool and a
// present tool's help both exit non-zero.
func ResolveTool(dir, name string) []string {
	if goPath, err := exec.LookPath("go"); err == nil {
		cmd := exec.Command(goPath, "tool")
		cmd.Dir = dir
		if out, err := cmd.Output(); err == nil && hasModuleTool(string(out), name) {
			return []string{goPath, "tool", name}
		}
	}

	if toolPath, err := exec.LookPath(name); err == nil {
		return []string{toolPath}
	}
	return nil
}

// hasModuleTool reports whether the output of `go tool` lists name as a
// module tool directive. Built-in tools (vet, cover, ...) are listed by
// bare name, module tools by import path; only the latter are invocable
// as third-party tools, so a bare-name match is ignored.
func hasModuleTool(goToolOutput, name string) bool {
	for line := range strings.SplitSeq(goToolOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "/") {
			continue
		}
		if path.Base(line) == name {
			return true
		}
	}
	return false
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
