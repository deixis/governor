package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deixis/governor/internal/report"
)

func (e *Engine) runVulncheck(ctx context.Context, packages []string) ([]report.Vuln, error) {
	argv := ResolveTool("govulncheck")
	if argv == nil {
		return nil, NewErrToolUnavailable("govulncheck")
	}

	argv = append(argv, "-json")
	argv = append(argv, e.Config.Audit.Vulncheck.Args...)
	argv = append(argv, e.ResolvePackages(packages)...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing govulncheck: %w", err)
	}

	return parseGovulncheckOutput(result.Stdout), nil
}

type govulncheckMessage struct {
	Finding *govulncheckFinding `json:"finding,omitempty"`
	OSV     *govulncheckOSV     `json:"osv,omitempty"`
}

type govulncheckFinding struct {
	OSV          string                  `json:"osv"`
	FixedVersion string                  `json:"fixed_version,omitempty"`
	Trace        []govulncheckTraceEntry `json:"trace,omitempty"`
}

type govulncheckTraceEntry struct {
	Module   string `json:"module,omitempty"`
	Package  string `json:"package,omitempty"`
	Function string `json:"function,omitempty"`
}

type govulncheckOSV struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

func parseGovulncheckOutput(data []byte) []report.Vuln {
	osvSummaries := make(map[string]string)
	var findings []*govulncheckFinding

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg govulncheckMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.OSV != nil {
			osvSummaries[msg.OSV.ID] = msg.OSV.Summary
		}
		if msg.Finding != nil {
			findings = append(findings, msg.Finding)
		}
	}

	seen := make(map[string]*report.Vuln)
	var vulns []report.Vuln

	for _, f := range findings {
		v, ok := seen[f.OSV]
		if !ok {
			v = &report.Vuln{
				ID:           f.OSV,
				Summary:      osvSummaries[f.OSV],
				FixedVersion: f.FixedVersion,
			}
			seen[f.OSV] = v
			vulns = append(vulns, *v)
		}

		for _, t := range f.Trace {
			if t.Package != "" && v.AffectedPackage == "" {
				v.AffectedPackage = t.Package
			}
			if t.Function != "" {
				v.Symbols = append(v.Symbols, t.Function)
			}
		}

		for i := range vulns {
			if vulns[i].ID == f.OSV {
				vulns[i] = *v
			}
		}
	}

	return vulns
}

// FormatVulncheckSummary formats vulnerability results for display.
func FormatVulncheckSummary(vulns []report.Vuln) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Vulnerabilities found: %d\n", len(vulns))

	for _, v := range vulns {
		fmt.Fprintf(&b, "    %s: %s", v.ID, v.Summary)
		if v.AffectedPackage != "" {
			fmt.Fprintf(&b, " (%s)", v.AffectedPackage)
		}
		if v.FixedVersion != "" {
			fmt.Fprintf(&b, " [fixed in %s]", v.FixedVersion)
		}
		fmt.Fprintln(&b)
	}

	return b.String()
}
