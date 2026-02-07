package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TestSummary holds parsed test results.
type TestSummary struct {
	Status      string // PASS or FAIL
	Total       int
	Passed      int
	Failed      int
	Skipped     int
	BuildErrors []BuildError
	Errors      []TestFailure
}

// BuildError holds a build failure from go test -json.
type BuildError struct {
	ImportPath string
	Output     string
}

// TestFailure holds a single test failure from go test -json.
type TestFailure struct {
	Test    string
	Package string
	Output  string
}

// maxFailureLines is the maximum number of output lines shown per test failure.
const maxFailureLines = 20

func (s *TestSummary) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Status: %s\n", s.Status)
	fmt.Fprintln(&b)

	if s.Status == "PASS" {
		fmt.Fprintf(&b, "All %d tests passed", s.Total)
		if s.Skipped > 0 {
			fmt.Fprintf(&b, " (%d skipped)", s.Skipped)
		}
		fmt.Fprintln(&b, ".")
	} else {
		if len(s.BuildErrors) > 0 {
			fmt.Fprintln(&b, "Build errors:")
			for _, be := range s.BuildErrors {
				fmt.Fprintf(&b, "  %s:\n", be.ImportPath)
				output := truncateLines(be.Output, maxFailureLines)
				for _, line := range strings.Split(output, "\n") {
					fmt.Fprintf(&b, "    %s\n", line)
				}
			}
			fmt.Fprintln(&b)
		}

		if s.Failed > 0 {
			fmt.Fprintf(&b, "Failed %d of %d tests.\n", s.Failed, s.Total)
			fmt.Fprintln(&b)

			byPkg := make(map[string][]TestFailure)
			for _, f := range s.Errors {
				byPkg[f.Package] = append(byPkg[f.Package], f)
			}
			for pkg, failures := range byPkg {
				fmt.Fprintf(&b, "FAIL %s (%d failures):\n", pkg, len(failures))
				for _, f := range failures {
					output := truncateLines(f.Output, maxFailureLines)
					fmt.Fprintf(&b, "  - %s\n", f.Test)
					if output != "" {
						for _, line := range strings.Split(output, "\n") {
							fmt.Fprintf(&b, "      %s\n", line)
						}
					}
				}
				fmt.Fprintln(&b)
			}
		} else if len(s.BuildErrors) == 0 {
			fmt.Fprintf(&b, "Failed %d of %d tests.\n", s.Failed, s.Total)
			fmt.Fprintln(&b)
		}
	}

	return b.String()
}

func (e *Engine) runTest(ctx context.Context, packages []string) (*TestSummary, error) {
	pkgs := e.ResolvePackages(packages)

	argv := []string{"go", "test", "-json"}
	argv = append(argv, pkgs...)
	argv = append(argv, e.Config.Test.Args...)

	result, err := e.Runner.Run(ctx, argv, "")
	if err != nil {
		return nil, fmt.Errorf("executing go test: %w", err)
	}

	summary := parseTestOutput(result.Stdout)
	return summary, nil
}

// test2jsonEvent represents a single event from `go test -json`.
type test2jsonEvent struct {
	Action     string  `json:"Action"`
	Package    string  `json:"Package"`
	Test       string  `json:"Test"`
	Output     string  `json:"Output"`
	Elapsed    float64 `json:"Elapsed"`
	ImportPath string  `json:"ImportPath"`
}

func parseTestOutput(data []byte) *TestSummary {
	s := &TestSummary{Status: "PASS"}

	type testKey struct{ pkg, test string }
	outputs := make(map[testKey]*strings.Builder)
	failedTests := make(map[testKey]bool)

	buildOutputs := make(map[string]*strings.Builder)
	failedBuilds := make(map[string]bool)

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev test2jsonEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		key := testKey{ev.Package, ev.Test}

		switch ev.Action {
		case "output":
			if ev.Test != "" {
				if _, ok := outputs[key]; !ok {
					outputs[key] = &strings.Builder{}
				}
				outputs[key].WriteString(ev.Output)
			}
		case "pass":
			if ev.Test != "" {
				s.Total++
				s.Passed++
			}
		case "fail":
			if ev.Test != "" {
				s.Total++
				s.Failed++
				s.Status = "FAIL"
				failedTests[key] = true
			} else if ev.Package != "" && ev.Test == "" {
				s.Status = "FAIL"
			}
		case "skip":
			if ev.Test != "" {
				s.Total++
				s.Skipped++
			}
		case "build-output":
			ip := ev.ImportPath
			if ip == "" {
				ip = ev.Package
			}
			if ip != "" {
				if _, ok := buildOutputs[ip]; !ok {
					buildOutputs[ip] = &strings.Builder{}
				}
				buildOutputs[ip].WriteString(ev.Output)
			}
		case "build-fail":
			ip := ev.ImportPath
			if ip == "" {
				ip = ev.Package
			}
			if ip != "" {
				failedBuilds[ip] = true
			}
			s.Status = "FAIL"
		}
	}

	for key := range failedTests {
		output := ""
		if b, ok := outputs[key]; ok {
			output = b.String()
		}
		s.Errors = append(s.Errors, TestFailure{
			Test:    key.test,
			Package: key.pkg,
			Output:  output,
		})
	}

	for ip := range failedBuilds {
		output := ""
		if b, ok := buildOutputs[ip]; ok {
			output = strings.TrimRight(b.String(), "\n")
		}
		s.BuildErrors = append(s.BuildErrors, BuildError{
			ImportPath: ip,
			Output:     output,
		})
	}

	return s
}

func truncateLines(s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	result := strings.Join(lines[:maxLines], "\n")
	result += fmt.Sprintf("\n... (%d more lines)", len(lines)-maxLines)
	return result
}
