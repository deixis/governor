package workflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/deixis/governor/internal/report"
)

func (e *Engine) runComplexity(ctx context.Context, packages []string) ([]report.ComplexityEntry, error) {
	argv := ResolveTool("gocognit")
	if argv == nil {
		return nil, NewErrToolUnavailable("gocognit")
	}

	pkgs := e.ResolvePackages(packages)

	argv = append(argv, e.Config.Audit.Complexity.Args...)
	argv = append(argv, pkgs...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing gocognit: %w", err)
	}

	return parseGocognitOutput(result.Stdout), nil
}

// parseGocognitOutput parses the default gocognit output format:
//
//	<complexity> <package> <function> <file>:<line>:<col>
func parseGocognitOutput(data []byte) []report.ComplexityEntry {
	var entries []report.ComplexityEntry

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		complexity, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		pkg := fields[1]
		pos := fields[len(fields)-1]
		funcName := strings.Join(fields[2:len(fields)-1], " ")
		file, lineNum := parsePosition(pos)

		entries = append(entries, report.ComplexityEntry{
			Package:    pkg,
			File:       file,
			Function:   funcName,
			Line:       lineNum,
			Complexity: complexity,
		})
	}

	return entries
}

// parsePosition extracts file and line from "file:line:col".
func parsePosition(pos string) (string, int) {
	parts := strings.Split(pos, ":")
	if len(parts) < 2 {
		return pos, 0
	}
	file := parts[0]
	lineNum, _ := strconv.Atoi(parts[1])
	return file, lineNum
}

// FormatComplexitySummary formats complexity entries for display.
func FormatComplexitySummary(entries []report.ComplexityEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Functions analysed: %d\n", len(entries))

	if len(entries) == 0 {
		return b.String()
	}

	highest := entries[0]
	for _, e := range entries[1:] {
		if e.Complexity > highest.Complexity {
			highest = e
		}
	}
	fmt.Fprintf(&b, "  Highest: %s.%s (%d)\n", highest.Package, highest.Function, highest.Complexity)

	buckets := []struct {
		label string
		min   int
		max   int
	}{
		{"1-5", 1, 5},
		{"6-10", 6, 10},
		{"11-15", 11, 15},
		{"16+", 16, 1<<31 - 1},
	}

	fmt.Fprintf(&b, "  Distribution:")
	for i, bucket := range buckets {
		count := 0
		for _, e := range entries {
			if e.Complexity >= bucket.min && e.Complexity <= bucket.max {
				count++
			}
		}
		if i > 0 {
			fmt.Fprint(&b, ",")
		}
		fmt.Fprintf(&b, " %d (%s)", count, bucket.label)
	}
	fmt.Fprintln(&b)

	return b.String()
}
