package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// LintSummary holds parsed lint results.
type LintSummary struct {
	Issues []LintIssue
}

// LintIssue holds a single lint finding.
type LintIssue struct {
	Package string
	File    string
	Line    int
	Column  int
	Linter  string
	Message string
}

func (s *LintSummary) String() string {
	var b strings.Builder

	if len(s.Issues) == 0 {
		fmt.Fprintln(&b, "Status: OK")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "No lint issues found.")
	} else {
		fmt.Fprintf(&b, "Status: %d issues found\n", len(s.Issues))
		fmt.Fprintln(&b)
		for _, issue := range s.Issues {
			fmt.Fprintf(&b, "%s:%d:%d (%s): %s\n", issue.File, issue.Line, issue.Column, issue.Linter, issue.Message)
		}
	}
	return b.String()
}

func (e *Engine) runLint(ctx context.Context, packages []string) (*LintSummary, error) {
	argv := e.resolveTool("golangci-lint")
	if argv == nil {
		return nil, NewErrToolUnavailable("golangci-lint")
	}

	pkgs, err := e.Packages(ctx, packages, e.Config.Lint.Exclude)
	if err != nil {
		return nil, err
	}

	argv = slices.Concat(argv, []string{"run"}, e.lintJSONOutputFlags(ctx))
	if e.Config.Lint.Config != "" {
		argv = append(argv, "--config", e.Config.Lint.Config)
	}
	argv = append(argv, e.Config.Lint.Args...)
	argv = append(argv, e.dirPatterns(ctx, pkgs)...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing golangci-lint: %w", err)
	}

	summary := parseLintOutput(result.Stdout, result.Stderr)
	if result.ExitCode != 0 && len(summary.Issues) == 0 {
		// A failing run with no parsed issue means golangci-lint itself
		// failed (bad flags, version-manager shim), not a clean run.
		return nil, fmt.Errorf("golangci-lint failed (exit %d): %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)+string(result.Stdout)))
	}
	if len(summary.Issues) > 0 {
		dirs := e.packageDirs(ctx, pkgs)
		for i := range summary.Issues {
			summary.Issues[i].Package, summary.Issues[i].File = e.locate(dirs, summary.Issues[i].File)
		}
	}
	return summary, nil
}

// lintVersion matches the version in `golangci-lint --version` output,
// "golangci-lint has version 2.14.0 built with ..." in v1 and v2 alike.
var lintVersion = regexp.MustCompile(`version v?(\d+)\.`)

// lintJSONOutputFlags selects the flags that make golangci-lint print JSON
// on stdout. v2 removed --out-format in favour of --output.json.path. Only
// a version that reads as v1 gets the old flag: v2 is current, so a probe
// that fails or prints something else gets the v2 flag.
func (e *Engine) lintJSONOutputFlags(ctx context.Context) []string {
	v2 := []string{"--output.json.path", "stdout"}
	result, err := e.Runner.Run(ctx, slices.Concat(e.resolveTool("golangci-lint"), []string{"--version"}), "")
	if err != nil || result.ExitCode != 0 {
		return v2
	}
	m := lintVersion.FindSubmatch(append(result.Stdout, result.Stderr...))
	if m != nil && string(m[1]) == "1" {
		return []string{"--out-format", "json"}
	}
	return v2
}

// dirPatterns rewrites import-path patterns of workspace modules as
// directory patterns relative to the workspace. golangci-lint v2 takes
// every argument as a directory, so example.com/m/a/... becomes ./a/...
// Relative patterns and paths outside the workspace modules pass through.
func (e *Engine) dirPatterns(ctx context.Context, pkgs []string) []string {
	var needed bool
	for _, p := range pkgs {
		if !isRelativePattern(p) {
			needed = true
		}
	}
	if !needed {
		return pkgs
	}
	res, err := e.Runner.Run(ctx, []string{"go", "list", "-m", "-f", "{{.Path}}\t{{.Dir}}"}, "")
	if err != nil || res.ExitCode != 0 {
		return pkgs
	}
	var mods []lintModule
	for _, line := range nonEmptyLines(res.Stdout) {
		if path, dir, ok := strings.Cut(line, "\t"); ok && dir != "" {
			mods = append(mods, lintModule{path, dir})
		}
	}
	// Longest module path first: a nested module wins over its parent.
	slices.SortFunc(mods, func(a, b lintModule) int { return len(b.path) - len(a.path) })

	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, e.dirPattern(p, mods))
	}
	return out
}

type lintModule struct{ path, dir string }

func (e *Engine) dirPattern(p string, mods []lintModule) string {
	if isRelativePattern(p) {
		return p
	}
	base, recursive := strings.CutSuffix(p, "/...")
	for _, m := range mods {
		rest, ok := strings.CutPrefix(base, m.path)
		if !ok || (rest != "" && rest[0] != '/') {
			continue
		}
		rel, err := filepath.Rel(e.Workspace, filepath.Join(m.dir, filepath.FromSlash(rest)))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p
		}
		pattern := "./" + filepath.ToSlash(rel)
		if rel == "." {
			pattern = "."
		}
		if recursive {
			pattern = strings.TrimSuffix(pattern, "/.") + "/..."
		}
		return pattern
	}
	return p
}

func isRelativePattern(p string) bool {
	return p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

// golangciLintOutput is the top-level JSON output from golangci-lint.
type golangciLintOutput struct {
	Issues []golangciLintIssue `json:"Issues"`
}

type golangciLintIssue struct {
	FromLinter string          `json:"FromLinter"`
	Text       string          `json:"Text"`
	Pos        golangciLintPos `json:"Pos"`
}

type golangciLintPos struct {
	Filename string `json:"Filename"`
	Line     int    `json:"Line"`
	Column   int    `json:"Column"`
}

func parseLintOutput(stdout, stderr []byte) *LintSummary {
	s := &LintSummary{}

	var out golangciLintOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		return s
	}

	for _, issue := range out.Issues {
		s.Issues = append(s.Issues, LintIssue{
			File:    issue.Pos.Filename,
			Line:    issue.Pos.Line,
			Column:  issue.Pos.Column,
			Linter:  issue.FromLinter,
			Message: issue.Text,
		})
	}

	return s
}
