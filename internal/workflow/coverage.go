package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/deixis/governor/internal/report"
)

func (e *Engine) runCoverage(ctx context.Context, packages []string) ([]report.CoverageEntry, error) {
	pkgs := e.ResolvePackages(packages)

	// Create a temp file for the cover profile.
	f, err := os.CreateTemp("", "governor-cover-*.out")
	if err != nil {
		return nil, fmt.Errorf("creating cover profile: %w", err)
	}
	coverFile := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(coverFile) }()

	// Run go test -coverprofile.
	argv := []string{"go", "test", "-coverprofile", coverFile}
	argv = append(argv, e.Config.Audit.Coverage.Args...)
	argv = append(argv, pkgs...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing go test -coverprofile: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("go test -coverprofile failed (exit %d): %s", result.ExitCode, string(result.Stderr))
	}

	// Run go tool cover -func to get per-function coverage.
	coverArgv := []string{"go", "tool", "cover", "-func", coverFile}
	coverResult, err := e.Runner.Run(ctx, coverArgv, "")
	if err != nil {
		return nil, fmt.Errorf("executing go tool cover -func: %w", err)
	}

	return parseCoverFunc(coverResult.Stdout), nil
}

// coverFuncLine matches lines from `go tool cover -func`:
//
//	github.com/foo/bar/baz.go:12:	FuncName		75.0%
//	total:					(statements)		62.4%
var coverFuncLine = regexp.MustCompile(`^(.+):(\d+):\s+(\S+)\s+(\d+\.\d+)%$`)

func parseCoverFunc(data []byte) []report.CoverageEntry {
	var entries []report.CoverageEntry

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		m := coverFuncLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		file := m[1]
		funcName := m[3]
		pct, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			continue
		}

		if file == "total" {
			continue
		}

		pkg := derivePackageFromFile(file)

		relFile := file
		if idx := strings.Index(file, "/"); idx >= 0 {
			relFile = file
		}

		entries = append(entries, report.CoverageEntry{
			Package:  pkg,
			File:     relFile,
			Function: funcName,
			Coverage: pct,
		})
	}

	return entries
}

// CoverageSummary holds aggregated stats for output formatting.
type CoverageSummary struct {
	Packages  int
	Functions int
	Total     float64
}

func SummariseCoverage(entries []report.CoverageEntry) CoverageSummary {
	pkgs := make(map[string]bool)
	var sum float64
	for _, e := range entries {
		pkgs[e.Package] = true
		sum += e.Coverage
	}
	var avg float64
	if len(entries) > 0 {
		avg = sum / float64(len(entries))
	}
	return CoverageSummary{
		Packages:  len(pkgs),
		Functions: len(entries),
		Total:     avg,
	}
}

// FormatCoverageSummary formats coverage entries for display.
func FormatCoverageSummary(entries []report.CoverageEntry) string {
	var b strings.Builder
	s := SummariseCoverage(entries)
	fmt.Fprintf(&b, "  Packages: %d\n", s.Packages)
	fmt.Fprintf(&b, "  Functions: %d\n", s.Functions)
	fmt.Fprintf(&b, "  Average function coverage: %.1f%%\n", s.Total)

	var uncovered []string
	for _, e := range entries {
		if e.Coverage == 0 {
			uncovered = append(uncovered, fmt.Sprintf("    %s.%s (%s)", e.Package, e.Function, filepath.Base(e.File)))
		}
	}
	if len(uncovered) > 0 {
		fmt.Fprintf(&b, "  Uncovered functions: %d\n", len(uncovered))
		limit := 10
		for i, u := range uncovered {
			if i >= limit {
				fmt.Fprintf(&b, "    ... and %d more\n", len(uncovered)-limit)
				break
			}
			fmt.Fprintln(&b, u)
		}
	}
	return b.String()
}
