package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/report"
)

// StaticcheckResult holds the parsed output from a staticcheck run.
type StaticcheckResult struct {
	Issues []report.StaticIssue
}

func (s *StaticcheckResult) String() string {
	var b strings.Builder

	if len(s.Issues) == 0 {
		fmt.Fprintln(&b, "Status: OK")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "No staticcheck issues found.")
	} else {
		fmt.Fprintf(&b, "Status: %d issues found\n", len(s.Issues))
		fmt.Fprintln(&b)
		for _, issue := range s.Issues {
			fmt.Fprintf(&b, "%s:%d:%d (%s): %s\n", issue.File, issue.Line, issue.Col, issue.Code, issue.Message)
		}
	}
	return b.String()
}

func (e *Engine) runStaticcheck(ctx context.Context, packages []string) (*StaticcheckResult, error) {
	argv := ResolveTool("staticcheck")
	if argv == nil {
		return nil, NewErrToolUnavailable("staticcheck")
	}

	argv = append(argv, "-f", "json")

	if len(e.Config.Staticcheck.Checks) > 0 {
		argv = append(argv, "-checks", strings.Join(e.Config.Staticcheck.Checks, ","))
	}
	argv = append(argv, e.Config.Staticcheck.Args...)
	argv = append(argv, packages...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing staticcheck: %w", err)
	}

	return parseStaticcheckOutput(result.Stdout), nil
}

// staticcheckEvent represents a single JSON line from `staticcheck -f json`.
type staticcheckEvent struct {
	Code     string              `json:"code"`
	Severity string              `json:"severity"`
	Message  string              `json:"message"`
	Location staticcheckLocation `json:"location"`
	End      staticcheckLocation `json:"end"`
}

type staticcheckLocation struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func parseStaticcheckOutput(data []byte) *StaticcheckResult {
	s := &StaticcheckResult{}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev staticcheckEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Code == "" {
			continue
		}

		pkg := derivePackageFromFile(ev.Location.File)

		s.Issues = append(s.Issues, report.StaticIssue{
			Package:  pkg,
			File:     ev.Location.File,
			Line:     ev.Location.Line,
			Col:      ev.Location.Column,
			EndLine:  ev.End.Line,
			EndCol:   ev.End.Column,
			Code:     ev.Code,
			Severity: ev.Severity,
			Message:  ev.Message,
		})
	}
	return s
}
