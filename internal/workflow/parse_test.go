package workflow

import (
	"strings"
	"testing"
)

// --- parseTestOutput ---

func TestParseTestOutput_AllPass(t *testing.T) {
	input := lines(
		`{"Action":"run","Package":"pkg","Test":"TestA"}`,
		`{"Action":"output","Package":"pkg","Test":"TestA","Output":"ok\n"}`,
		`{"Action":"pass","Package":"pkg","Test":"TestA","Elapsed":0.1}`,
		`{"Action":"pass","Package":"pkg","Elapsed":0.2}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "PASS" {
		t.Errorf("Status = %q, want PASS", s.Status)
	}
	if s.Total != 1 {
		t.Errorf("Total = %d, want 1", s.Total)
	}
	if s.Passed != 1 {
		t.Errorf("Passed = %d, want 1", s.Passed)
	}
	if s.Failed != 0 {
		t.Errorf("Failed = %d, want 0", s.Failed)
	}
}

func TestParseTestOutput_OneFail(t *testing.T) {
	input := lines(
		`{"Action":"run","Package":"pkg","Test":"TestA"}`,
		`{"Action":"output","Package":"pkg","Test":"TestA","Output":"--- FAIL: TestA\n"}`,
		`{"Action":"fail","Package":"pkg","Test":"TestA","Elapsed":0.1}`,
		`{"Action":"fail","Package":"pkg","Elapsed":0.2}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "FAIL" {
		t.Errorf("Status = %q, want FAIL", s.Status)
	}
	if s.Failed != 1 {
		t.Errorf("Failed = %d, want 1", s.Failed)
	}
	if len(s.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1", len(s.Errors))
	}
	if s.Errors[0].Test != "TestA" {
		t.Errorf("Errors[0].Test = %q, want TestA", s.Errors[0].Test)
	}
	if s.Errors[0].Package != "pkg" {
		t.Errorf("Errors[0].Package = %q, want pkg", s.Errors[0].Package)
	}
}

func TestParseTestOutput_Mixed(t *testing.T) {
	input := lines(
		`{"Action":"pass","Package":"pkg","Test":"TestA"}`,
		`{"Action":"fail","Package":"pkg","Test":"TestB"}`,
		`{"Action":"skip","Package":"pkg","Test":"TestC"}`,
		`{"Action":"fail","Package":"pkg"}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "FAIL" {
		t.Errorf("Status = %q, want FAIL", s.Status)
	}
	if s.Total != 3 {
		t.Errorf("Total = %d, want 3", s.Total)
	}
	if s.Passed != 1 {
		t.Errorf("Passed = %d, want 1", s.Passed)
	}
	if s.Failed != 1 {
		t.Errorf("Failed = %d, want 1", s.Failed)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", s.Skipped)
	}
}

func TestParseTestOutput_PackageLevelFailOnly(t *testing.T) {
	input := lines(
		`{"Action":"fail","Package":"pkg"}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "FAIL" {
		t.Errorf("Status = %q, want FAIL", s.Status)
	}
	if s.Total != 0 {
		t.Errorf("Total = %d, want 0", s.Total)
	}
}

func TestParseTestOutput_MalformedLines(t *testing.T) {
	input := lines(
		`not json at all`,
		`{"Action":"pass","Package":"pkg","Test":"TestA"}`,
		`{broken`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "PASS" {
		t.Errorf("Status = %q, want PASS", s.Status)
	}
	if s.Total != 1 {
		t.Errorf("Total = %d, want 1", s.Total)
	}
}

func TestParseTestOutput_Empty(t *testing.T) {
	s := parseTestOutput(nil)
	if s.Status != "PASS" {
		t.Errorf("Status = %q, want PASS", s.Status)
	}
	if s.Total != 0 {
		t.Errorf("Total = %d, want 0", s.Total)
	}
}

func TestParseTestOutput_AllSkip(t *testing.T) {
	input := lines(
		`{"Action":"skip","Package":"pkg","Test":"TestA"}`,
		`{"Action":"skip","Package":"pkg","Test":"TestB"}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "PASS" {
		t.Errorf("Status = %q, want PASS", s.Status)
	}
	if s.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", s.Skipped)
	}
}

// --- parseTestOutput: build errors ---

func TestParseTestOutput_BuildFailure(t *testing.T) {
	input := lines(
		`{"ImportPath":"./...","Action":"build-output","Output":"# ./...\n"}`,
		`{"ImportPath":"./...","Action":"build-output","Output":"pattern ./...: directory prefix . does not contain main module\n"}`,
		`{"ImportPath":"./...","Action":"build-fail"}`,
		`{"Time":"2026-02-08T00:59:18.567774+01:00","Action":"start","Package":"./..."}`,
		`{"Time":"2026-02-08T00:59:18.568482+01:00","Action":"output","Package":"./...","Output":"FAIL\t./... [setup failed]\n"}`,
		`{"Time":"2026-02-08T00:59:18.568488+01:00","Action":"fail","Package":"./...","Elapsed":0.001,"FailedBuild":"./..."}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "FAIL" {
		t.Errorf("Status = %q, want FAIL", s.Status)
	}
	if len(s.BuildErrors) != 1 {
		t.Fatalf("BuildErrors = %d, want 1", len(s.BuildErrors))
	}
	if s.BuildErrors[0].ImportPath != "./..." {
		t.Errorf("ImportPath = %q, want ./...", s.BuildErrors[0].ImportPath)
	}
	if !strings.Contains(s.BuildErrors[0].Output, "directory prefix") {
		t.Errorf("expected build error output to contain 'directory prefix', got:\n%s", s.BuildErrors[0].Output)
	}
	if s.Total != 0 {
		t.Errorf("Total = %d, want 0", s.Total)
	}
	if len(s.Errors) != 0 {
		t.Errorf("Errors = %d, want 0", len(s.Errors))
	}
}

func TestParseTestOutput_BuildFailureCompileError(t *testing.T) {
	input := lines(
		`{"ImportPath":"example.com/pkg","Action":"build-output","Output":"# example.com/pkg\n"}`,
		`{"ImportPath":"example.com/pkg","Action":"build-output","Output":"./main.go:10:2: undefined: foo\n"}`,
		`{"ImportPath":"example.com/pkg","Action":"build-fail"}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "FAIL" {
		t.Errorf("Status = %q, want FAIL", s.Status)
	}
	if len(s.BuildErrors) != 1 {
		t.Fatalf("BuildErrors = %d, want 1", len(s.BuildErrors))
	}
	if s.BuildErrors[0].ImportPath != "example.com/pkg" {
		t.Errorf("ImportPath = %q, want example.com/pkg", s.BuildErrors[0].ImportPath)
	}
	if !strings.Contains(s.BuildErrors[0].Output, "undefined: foo") {
		t.Errorf("expected 'undefined: foo' in output, got:\n%s", s.BuildErrors[0].Output)
	}
}

func TestParseTestOutput_BuildFailureMixedWithTests(t *testing.T) {
	input := lines(
		`{"Action":"run","Package":"pkg/a","Test":"TestA"}`,
		`{"Action":"output","Package":"pkg/a","Test":"TestA","Output":"--- FAIL: TestA\n"}`,
		`{"Action":"fail","Package":"pkg/a","Test":"TestA"}`,
		`{"Action":"fail","Package":"pkg/a"}`,
		`{"ImportPath":"pkg/b","Action":"build-output","Output":"./b.go:5: syntax error\n"}`,
		`{"ImportPath":"pkg/b","Action":"build-fail"}`,
	)
	s := parseTestOutput([]byte(input))
	if s.Status != "FAIL" {
		t.Errorf("Status = %q, want FAIL", s.Status)
	}
	if s.Failed != 1 {
		t.Errorf("Failed = %d, want 1", s.Failed)
	}
	if len(s.Errors) != 1 {
		t.Errorf("Errors = %d, want 1", len(s.Errors))
	}
	if len(s.BuildErrors) != 1 {
		t.Fatalf("BuildErrors = %d, want 1", len(s.BuildErrors))
	}
	if s.BuildErrors[0].ImportPath != "pkg/b" {
		t.Errorf("ImportPath = %q, want pkg/b", s.BuildErrors[0].ImportPath)
	}
}

// --- TestSummary.String ---

func TestTestSummary_String_Pass(t *testing.T) {
	s := &TestSummary{
		Status: "PASS",
		Total:  3,
		Passed: 3,
	}
	out := s.String()
	if !strings.Contains(out, "Status: PASS") {
		t.Errorf("expected Status: PASS, got:\n%s", out)
	}
	if !strings.Contains(out, "All 3 tests passed") {
		t.Errorf("expected pass summary, got:\n%s", out)
	}
}

func TestTestSummary_String_Failure(t *testing.T) {
	s := &TestSummary{
		Status: "FAIL",
		Total:  1,
		Failed: 1,
		Errors: []TestFailure{{Test: "TestA", Package: "pkg", Output: "short error\n"}},
	}
	out := s.String()
	if !strings.Contains(out, "Status: FAIL") {
		t.Errorf("expected Status: FAIL, got:\n%s", out)
	}
	if !strings.Contains(out, "Failed 1 of 1 tests") {
		t.Errorf("expected failure count, got:\n%s", out)
	}
}

func TestTestSummary_String_BuildErrors(t *testing.T) {
	s := &TestSummary{
		Status:      "FAIL",
		BuildErrors: []BuildError{{ImportPath: "example.com/pkg", Output: "./main.go:10: undefined: foo"}},
	}
	out := s.String()
	if !strings.Contains(out, "Build errors:") {
		t.Errorf("expected 'Build errors:' header, got:\n%s", out)
	}
	if !strings.Contains(out, "example.com/pkg") {
		t.Errorf("expected import path in output, got:\n%s", out)
	}
	if !strings.Contains(out, "undefined: foo") {
		t.Errorf("expected error message in output, got:\n%s", out)
	}
}

// --- parseLintOutput ---

func TestParseLintOutput_WithIssues(t *testing.T) {
	input := `{"Issues":[{"FromLinter":"errcheck","Text":"unchecked error","Pos":{"Filename":"foo.go","Line":10,"Column":5}}]}`
	s := parseLintOutput([]byte(input), nil)
	if len(s.Issues) != 1 {
		t.Fatalf("Issues = %d, want 1", len(s.Issues))
	}
	if s.Issues[0].File != "foo.go" {
		t.Errorf("File = %q, want foo.go", s.Issues[0].File)
	}
	if s.Issues[0].Line != 10 {
		t.Errorf("Line = %d, want 10", s.Issues[0].Line)
	}
	if s.Issues[0].Linter != "errcheck" {
		t.Errorf("Linter = %q, want errcheck", s.Issues[0].Linter)
	}
	if s.Issues[0].Message != "unchecked error" {
		t.Errorf("Message = %q, want 'unchecked error'", s.Issues[0].Message)
	}
}

func TestParseLintOutput_NoIssues(t *testing.T) {
	input := `{"Issues":[]}`
	s := parseLintOutput([]byte(input), nil)
	if len(s.Issues) != 0 {
		t.Errorf("Issues = %d, want 0", len(s.Issues))
	}
}

func TestParseLintOutput_InvalidJSON(t *testing.T) {
	s := parseLintOutput([]byte("{broken"), nil)
	if len(s.Issues) != 0 {
		t.Errorf("Issues = %d, want 0 for invalid JSON", len(s.Issues))
	}
}

func TestParseLintOutput_Empty(t *testing.T) {
	s := parseLintOutput(nil, nil)
	if len(s.Issues) != 0 {
		t.Errorf("Issues = %d, want 0 for empty input", len(s.Issues))
	}
}

// --- truncateLines ---

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		wantSub  string
		wantFull bool
	}{
		{"under limit", "a\nb\nc", 5, "", true},
		{"at limit", "a\nb\nc", 3, "", true},
		{"over limit", "a\nb\nc\nd\ne", 2, "... (3 more lines)", false},
		{"empty", "", 5, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateLines(tt.input, tt.max)
			if tt.wantFull {
				if got != tt.input {
					t.Errorf("truncateLines() = %q, want %q", got, tt.input)
				}
			} else if !strings.Contains(got, tt.wantSub) {
				t.Errorf("truncateLines() = %q, want to contain %q", got, tt.wantSub)
			}
		})
	}
}

// lines joins strings with newlines.
func lines(ss ...string) string {
	return strings.Join(ss, "\n")
}
