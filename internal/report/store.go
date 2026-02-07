// Package report provides structured persistence and retrieval of
// tool run results. Results are stored as typed structs and can be
// queried by package or symbol.
package report

import (
	"fmt"
	"strings"
)

// Kind identifies the type of a run.
type Kind string

const (
	// Check is a check run (test, lint, staticcheck).
	Check Kind = "check"
	// Audit is an audit run (coverage, complexity, deadcode, dupl, vulncheck).
	Audit Kind = "audit"
)

// Store persists and retrieves run results.
type Store interface {
	Save(result *RunResult) error
	Load(runID string) (*RunResult, error)
}

// RunResult holds the structured output from a tool run.
type RunResult struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`

	// Validation fields.
	AutoFixes    int           `json:"auto_fixes,omitempty"`
	FormatIssues []FormatIssue `json:"format_issues,omitempty"`
	BuildErrors  []BuildError  `json:"build_errors,omitempty"`
	TestFailures []TestFailure `json:"test_failures,omitempty"`
	LintIssues   []LintIssue   `json:"lint_issues,omitempty"`
	StaticIssues []StaticIssue `json:"static_issues,omitempty"`

	// Audit fields.
	Coverage   []CoverageEntry   `json:"coverage,omitempty"`
	Complexity []ComplexityEntry `json:"complexity,omitempty"`
	DeadFuncs  []DeadFunc        `json:"dead_funcs,omitempty"`
	Duplicates []Duplicate       `json:"duplicates,omitempty"`
	Vulns      []Vuln            `json:"vulns,omitempty"`
}

// Expect returns an error if the run's Kind does not match want.
func (r *RunResult) Expect(want Kind) error {
	if r.Kind != want {
		return fmt.Errorf("run %s is a %s run, not a %s run", r.ID, r.Kind, want)
	}
	return nil
}

// FormatIssue represents an unformatted file detected by gofumpt.
type FormatIssue struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Message string `json:"message"`
}

// BuildError represents a compilation error.
type BuildError struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}

// TestFailure represents a failed test.
type TestFailure struct {
	Package string `json:"package"`
	Test    string `json:"test"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

// LintIssue represents a linter finding.
type LintIssue struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Linter  string `json:"linter"`
	Message string `json:"message"`
}

// StaticIssue represents a staticcheck finding.
type StaticIssue struct {
	Package  string `json:"package"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	EndLine  int    `json:"end_line,omitempty"`
	EndCol   int    `json:"end_col,omitempty"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// CoverageEntry holds per-function test coverage data.
type CoverageEntry struct {
	Package  string  `json:"package"`
	File     string  `json:"file"`
	Function string  `json:"function"`
	Coverage float64 `json:"coverage"` // 0.0–100.0
}

// ComplexityEntry holds per-function cognitive complexity data.
type ComplexityEntry struct {
	Package    string `json:"package"`
	File       string `json:"file"`
	Function   string `json:"function"`
	Line       int    `json:"line"`
	Complexity int    `json:"complexity"`
}

// DeadFunc represents an unreachable function found by deadcode.
type DeadFunc struct {
	Package  string `json:"package"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
}

// Duplicate represents a pair of duplicated code blocks found by dupl.
type Duplicate struct {
	File1      string `json:"file_1"`
	StartLine1 int    `json:"start_line_1"`
	EndLine1   int    `json:"end_line_1"`
	File2      string `json:"file_2"`
	StartLine2 int    `json:"start_line_2"`
	EndLine2   int    `json:"end_line_2"`
	Tokens     int    `json:"tokens"`
}

// Vuln represents a vulnerability found by govulncheck.
type Vuln struct {
	ID              string   `json:"id"` // e.g. GO-2024-1234
	Summary         string   `json:"summary"`
	AffectedPackage string   `json:"affected_package"`
	FixedVersion    string   `json:"fixed_version,omitempty"`
	Symbols         []string `json:"symbols,omitempty"` // called vulnerable symbols
}

// Diagnostic is a uniform interface for all diagnostic types.
type Diagnostic struct {
	Source  string // "format", "build", "test", "lint", "staticcheck"
	Package string
	File    string
	Line    int
	Col     int
	Symbol  string // e.g. "TestAdd" for test failures
	Detail  string // linter name, staticcheck code, etc.
	Message string
	Output  string // full test output (test failures only)
}

// ByPackage returns all diagnostics for a given package import path.
func ByPackage(result *RunResult, pkg string) []Diagnostic {
	var out []Diagnostic
	for _, d := range toDiagnostics(result) {
		if d.Package == pkg {
			out = append(out, d)
		}
	}
	return out
}

// BySymbol returns diagnostics matching a Go-qualified symbol.
// If sym contains a "." after the last "/" segment, it is treated as
// package.Symbol (e.g. "example.com/foo.TestAdd"). Otherwise it is
// treated as a bare package path and returns all diagnostics.
func BySymbol(result *RunResult, sym string) []Diagnostic {
	pkg, name := splitSymbol(sym)
	if name == "" {
		return ByPackage(result, pkg)
	}

	var out []Diagnostic
	for _, d := range toDiagnostics(result) {
		if d.Package == pkg && d.Symbol == name {
			out = append(out, d)
		}
	}
	return out
}

// splitSymbol splits a Go-qualified symbol into package path and symbol name.
// "example.com/foo.TestAdd" → ("example.com/foo", "TestAdd")
// "example.com/foo" → ("example.com/foo", "")
func splitSymbol(sym string) (string, string) {
	lastSlash := strings.LastIndex(sym, "/")
	afterSlash := sym[lastSlash+1:]
	dotIdx := strings.Index(afterSlash, ".")
	if dotIdx < 0 {
		return sym, ""
	}
	pkg := sym[:lastSlash+1+dotIdx]
	name := afterSlash[dotIdx+1:]
	return pkg, name
}

func toDiagnostics(r *RunResult) []Diagnostic {
	var out []Diagnostic

	for _, f := range r.FormatIssues {
		out = append(out, Diagnostic{
			Source:  "format",
			Package: f.Package,
			File:    f.File,
			Message: f.Message,
		})
	}
	for _, b := range r.BuildErrors {
		out = append(out, Diagnostic{
			Source:  "build",
			Package: b.Package,
			File:    b.File,
			Line:    b.Line,
			Col:     b.Col,
			Message: b.Message,
		})
	}
	for _, t := range r.TestFailures {
		out = append(out, Diagnostic{
			Source:  "test",
			Package: t.Package,
			File:    t.File,
			Line:    t.Line,
			Symbol:  t.Test,
			Message: t.Message,
			Output:  t.Output,
		})
	}
	for _, l := range r.LintIssues {
		out = append(out, Diagnostic{
			Source:  "lint",
			Package: l.Package,
			File:    l.File,
			Line:    l.Line,
			Col:     l.Col,
			Detail:  l.Linter,
			Message: l.Message,
		})
	}
	for _, s := range r.StaticIssues {
		out = append(out, Diagnostic{
			Source:  "staticcheck",
			Package: s.Package,
			File:    s.File,
			Line:    s.Line,
			Col:     s.Col,
			Detail:  s.Code,
			Message: s.Message,
		})
	}

	// Audit diagnostics.
	for _, c := range r.Coverage {
		out = append(out, Diagnostic{
			Source:  "coverage",
			Package: c.Package,
			File:    c.File,
			Symbol:  c.Function,
			Message: fmt.Sprintf("%.1f%% coverage", c.Coverage),
		})
	}
	for _, c := range r.Complexity {
		out = append(out, Diagnostic{
			Source:  "complexity",
			Package: c.Package,
			File:    c.File,
			Line:    c.Line,
			Symbol:  c.Function,
			Message: fmt.Sprintf("cognitive complexity %d", c.Complexity),
		})
	}
	for _, d := range r.DeadFuncs {
		out = append(out, Diagnostic{
			Source:  "deadcode",
			Package: d.Package,
			File:    d.File,
			Line:    d.Line,
			Symbol:  d.Function,
			Message: "unreachable function",
		})
	}
	for _, d := range r.Duplicates {
		out = append(out, Diagnostic{
			Source:  "dupl",
			File:    d.File1,
			Line:    d.StartLine1,
			Message: fmt.Sprintf("duplicate of %s:%d-%d (%d tokens)", d.File2, d.StartLine2, d.EndLine2, d.Tokens),
		})
	}
	for _, v := range r.Vulns {
		msg := v.Summary
		if v.FixedVersion != "" {
			msg += " (fixed in " + v.FixedVersion + ")"
		}
		out = append(out, Diagnostic{
			Source:  "vulncheck",
			Package: v.AffectedPackage,
			Detail:  v.ID,
			Message: msg,
		})
	}

	return out
}
