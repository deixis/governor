package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/deixis/governor/internal/report"
)

func (e *Engine) runDeadcode(ctx context.Context, packages []string) ([]report.DeadFunc, error) {
	argv := ResolveTool("deadcode")
	if argv == nil {
		return nil, NewErrToolUnavailable("deadcode")
	}

	pkgs := e.ResolvePackages(packages)

	argv = append(argv, "-json")
	argv = append(argv, e.Config.Audit.Deadcode.Args...)
	argv = append(argv, pkgs...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing deadcode: %w", err)
	}

	return parseDeadcodeOutput(result.Stdout), nil
}

// deadcodePackage matches the JSON schema from deadcode -json.
type deadcodePackage struct {
	Name  string             `json:"Name"`
	Path  string             `json:"Path"`
	Funcs []deadcodeFunction `json:"Funcs"`
}

type deadcodeFunction struct {
	Name     string           `json:"Name"`
	Position deadcodePosition `json:"Position"`
}

type deadcodePosition struct {
	File string `json:"File"`
	Line int    `json:"Line"`
	Col  int    `json:"Col"`
}

func parseDeadcodeOutput(data []byte) []report.DeadFunc {
	var pkgs []deadcodePackage
	if err := json.Unmarshal(data, &pkgs); err != nil {
		return nil
	}

	var funcs []report.DeadFunc
	for _, pkg := range pkgs {
		for _, f := range pkg.Funcs {
			funcs = append(funcs, report.DeadFunc{
				Package:  pkg.Path,
				File:     f.Position.File,
				Line:     f.Position.Line,
				Function: f.Name,
			})
		}
	}
	return funcs
}

// FormatDeadcodeSummary formats dead function results for display.
func FormatDeadcodeSummary(funcs []report.DeadFunc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Unreachable functions: %d\n", len(funcs))
	limit := 20
	for i, f := range funcs {
		if i >= limit {
			fmt.Fprintf(&b, "    ... and %d more\n", len(funcs)-limit)
			break
		}
		fmt.Fprintf(&b, "    %s.%s (%s:%d)\n", f.Package, f.Function, filepath.Base(f.File), f.Line)
	}
	return b.String()
}
