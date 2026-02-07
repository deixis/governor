package workflow

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/deixis/governor/internal/report"
)

func (e *Engine) runDupl(ctx context.Context, packages []string) ([]report.Duplicate, error) {
	argv := ResolveTool("dupl")
	if argv == nil {
		return nil, NewErrToolUnavailable("dupl")
	}

	threshold := e.Config.DuplThreshold()
	argv = append(argv, "-plumbing", "-t", strconv.Itoa(threshold))
	argv = append(argv, e.Config.Audit.Dupl.Args...)

	// dupl operates on file paths, not import paths.
	// Pass "." to scan the workspace.
	argv = append(argv, ".")

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing dupl: %w", err)
	}

	return parseDuplOutput(result.Stdout, threshold), nil
}

// parseDuplOutput parses dupl -plumbing output.
var duplLine = regexp.MustCompile(`^(.+):(\d+)-(\d+)$`)

func parseDuplOutput(data []byte, threshold int) []report.Duplicate {
	var duplicates []report.Duplicate

	lines := strings.Split(string(data), "\n")

	var group []duplEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(group) >= 2 {
				duplicates = append(duplicates, groupToDuplicates(group, threshold)...)
			}
			group = nil
			continue
		}

		m := duplLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		group = append(group, duplEntry{
			File:      m[1],
			StartLine: start,
			EndLine:   end,
		})
	}

	if len(group) >= 2 {
		duplicates = append(duplicates, groupToDuplicates(group, threshold)...)
	}

	return duplicates
}

type duplEntry struct {
	File      string
	StartLine int
	EndLine   int
}

func groupToDuplicates(group []duplEntry, tokens int) []report.Duplicate {
	var out []report.Duplicate
	first := group[0]
	for _, other := range group[1:] {
		out = append(out, report.Duplicate{
			File1:      first.File,
			StartLine1: first.StartLine,
			EndLine1:   first.EndLine,
			File2:      other.File,
			StartLine2: other.StartLine,
			EndLine2:   other.EndLine,
			Tokens:     tokens,
		})
	}
	return out
}

// FormatDuplSummary formats duplicate results for display.
func FormatDuplSummary(duplicates []report.Duplicate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Duplicate blocks: %d\n", len(duplicates))
	limit := 10
	for i, d := range duplicates {
		if i >= limit {
			fmt.Fprintf(&b, "    ... and %d more\n", len(duplicates)-limit)
			break
		}
		fmt.Fprintf(&b, "    %s:%d-%d <> %s:%d-%d (%d tokens)\n",
			d.File1, d.StartLine1, d.EndLine1,
			d.File2, d.StartLine2, d.EndLine2,
			d.Tokens)
	}
	return b.String()
}
