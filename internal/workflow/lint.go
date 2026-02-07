package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LintSummary holds parsed lint results.
type LintSummary struct {
	Issues []LintIssue
}

// LintIssue holds a single lint finding.
type LintIssue struct {
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
	argv := ResolveTool("golangci-lint")
	if argv == nil {
		return nil, NewErrToolUnavailable("golangci-lint")
	}

	pkgs := e.ResolvePackages(packages)

	argv = append(argv, "run", "--out-format", "json")
	if e.Config.Lint.Config != "" {
		argv = append(argv, "--config", e.Config.Lint.Config)
	}
	argv = append(argv, e.Config.Lint.Args...)
	argv = append(argv, pkgs...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing golangci-lint: %w", err)
	}

	summary := parseLintOutput(result.Stdout, result.Stderr)
	return summary, nil
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
