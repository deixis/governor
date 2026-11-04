package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deixis/governor/internal/config"
	"github.com/deixis/governor/internal/report"
	"github.com/deixis/governor/internal/runner"
	"github.com/deixis/governor/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// setup creates a full Governor MCP server + client over in-memory transports.
// workspaceDir should be a prepared fixture directory.
func setup(t *testing.T, workspaceDir string, cfgOverride *config.Config) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	var cfg *config.Config
	if cfgOverride != nil {
		cfg = cfgOverride
	} else {
		var err error
		loaded, err := config.Load(workspaceDir)
		if err != nil {
			cfg = &config.Config{}
		} else {
			cfg = loaded.Config
		}
	}

	store := report.NewLRUStore(5, report.NewDiskStore())
	r := &runner.Runner{
		Workspace: workspaceDir,
		Timeout:   30 * time.Second,
		MaxOutput: cfg.MaxOutputBytes(),
	}

	server := NewServer(cfg, r, store, workspaceDir)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})

	return cs
}

// copyFixture copies a testdata fixture to a temp dir, renaming .txt extensions.
func copyFixture(t *testing.T, fixture string) string {
	t.Helper()
	srcDir := filepath.Join("testdata", fixture)
	dstDir := t.TempDir()

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", fixture, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("reading %s: %v", src, err)
		}
		name := e.Name()
		// Rename .go.txt -> .go, go.mod.txt -> go.mod
		name = strings.TrimSuffix(name, ".txt")
		dst := filepath.Join(dstDir, name)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatalf("writing %s: %v", dst, err)
		}
	}
	return dstDir
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func resultText(r *mcp.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// --- gov_workspace ---

func TestGovWorkspace(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_workspace", nil)
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	// The passing fixture has module "example.com/passing".
	if !strings.Contains(text, "Module:") {
		t.Errorf("expected Module: in output, got:\n%s", text)
	}
}

// --- gov_check ---

func TestGovCheck_Passing(t *testing.T) {
	dir := copyFixture(t, "passing")
	// Only run test step to avoid lint/staticcheck dependency.
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test"}},
	}
	cs := setup(t, dir, cfg)
	res := callTool(t, cs, "gov_check", nil)
	text := resultText(res)
	if !strings.Contains(text, "test: pass") {
		t.Errorf("expected test step to pass, got:\n%s", text)
	}
	if !strings.Contains(text, "Status: PASS") {
		t.Errorf("expected Status: PASS, got:\n%s", text)
	}
	if !strings.Contains(text, "Run:") {
		t.Errorf("expected Run: in output, got:\n%s", text)
	}
}

func TestGovCheck_TestFailure(t *testing.T) {
	dir := copyFixture(t, "failing")
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test"}},
	}
	cs := setup(t, dir, cfg)
	res := callTool(t, cs, "gov_check", nil)
	text := resultText(res)
	if !strings.Contains(text, "Status: FAIL") {
		t.Errorf("expected Status: FAIL, got:\n%s", text)
	}
	if !strings.Contains(text, "test: fail") {
		t.Errorf("expected test step to fail, got:\n%s", text)
	}
	if !strings.Contains(text, "Run:") {
		t.Errorf("expected Run: in output, got:\n%s", text)
	}
	// Should have failures section with Go-qualified symbols.
	if !strings.Contains(text, "Failures:") {
		t.Errorf("expected Failures: section, got:\n%s", text)
	}
	// Should have inspect hint.
	if !strings.Contains(text, "gov_inspect") {
		t.Errorf("expected gov_inspect hint, got:\n%s", text)
	}
}

func TestGovCheck_UnknownStep(t *testing.T) {
	dir := copyFixture(t, "passing")
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test", "build"}},
	}
	cs := setup(t, dir, cfg)
	res := callTool(t, cs, "gov_check", nil)
	text := resultText(res)
	if !strings.Contains(text, "Status: FAIL") {
		t.Errorf("expected Status: FAIL for unknown step, got:\n%s", text)
	}
	if !strings.Contains(text, "unknown step: build") {
		t.Errorf("expected 'unknown step: build' in output, got:\n%s", text)
	}
}

func TestGovCheck_BuildError(t *testing.T) {
	dir := copyFixture(t, "builderror")
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test"}},
	}
	cs := setup(t, dir, cfg)
	res := callTool(t, cs, "gov_check", nil)
	text := resultText(res)
	if !strings.Contains(text, "Status: FAIL") {
		t.Errorf("expected Status: FAIL, got:\n%s", text)
	}
}

// --- gov_inspect ---

func TestGovInspect_MissingRunID(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gov_inspect",
		Arguments: map[string]any{
			"symbol": "example.com/passing",
		},
	})
	if err == nil {
		t.Error("expected error for missing run_id")
	}
}

func TestGovInspect_NoSymbolReturnsAll(t *testing.T) {
	dir := copyFixture(t, "failing")
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test"}},
	}
	cs := setup(t, dir, cfg)

	valText := resultText(callTool(t, cs, "gov_check", nil))
	var runID string
	for line := range strings.SplitSeq(valText, "\n") {
		if id, ok := strings.CutPrefix(line, "Run: "); ok {
			runID = id
			break
		}
	}
	if runID == "" {
		t.Fatalf("no Run ID found in check output:\n%s", valText)
	}

	res := callTool(t, cs, "gov_inspect", map[string]any{"run_id": runID})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error from gov_inspect: %s", text)
	}
	if !strings.Contains(text, "all — ") || !strings.Contains(text, "[test]") {
		t.Errorf("expected all diagnostics, got:\n%s", text)
	}
}

func TestGovInspect_InvalidRunID(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_inspect", map[string]any{
		"run_id": "nonexistent-id",
		"symbol": "example.com/foo",
	})
	if !res.IsError {
		t.Error("expected IsError for invalid run_id")
	}
}

func TestGovInspect_AfterFailingCheck(t *testing.T) {
	dir := copyFixture(t, "failing")
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test"}},
	}
	cs := setup(t, dir, cfg)

	// Run validate to get a run ID.
	valRes := callTool(t, cs, "gov_check", nil)
	valText := resultText(valRes)

	// Extract run ID from "Run: <id>".
	var runID string
	for line := range strings.SplitSeq(valText, "\n") {
		if after, ok := strings.CutPrefix(line, "Run: "); ok {
			runID = after
			break
		}
	}
	if runID == "" {
		t.Fatalf("no Run ID found in validate output:\n%s", valText)
	}

	// Extract a package from the failures — use the failing fixture's module.
	// The failing fixture module is "testfailing".
	inspRes := callTool(t, cs, "gov_inspect", map[string]any{
		"run_id": runID,
		"symbol": "testfailing",
	})
	inspText := resultText(inspRes)
	if inspRes.IsError {
		t.Fatalf("unexpected error from gov_inspect: %s", inspText)
	}
	if strings.Contains(inspText, "No diagnostics") {
		t.Errorf("expected diagnostics for failing package, got:\n%s", inspText)
	}
}

// --- gov_check with lint ---

func TestGovCheck_WithLint(t *testing.T) {
	dir := copyFixture(t, "passing")
	if workflow.ResolveTool(dir, "golangci-lint") == nil {
		t.Skip("golangci-lint not available")
	}
	cfg := &config.Config{
		Check: config.CheckConfig{Steps: []string{"test", "lint"}},
	}
	cs := setup(t, dir, cfg)
	res := callTool(t, cs, "gov_check", nil)
	text := resultText(res)
	if !strings.Contains(text, "test: pass") {
		t.Errorf("expected test step to pass, got:\n%s", text)
	}
}

// gov_fix without files reports workspace diagnostics and writes nothing.
func TestGovFix_NoFilesIsDiagnostics(t *testing.T) {
	dir := copyFixture(t, "builderror")
	cs := setup(t, dir, nil)
	before, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	res := callTool(t, cs, "gov_fix", nil)
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "main.go:8:") {
		t.Errorf("expected a main.go:8 diagnostic, got:\n%s", text)
	}
	after, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("gov_fix without files changed main.go")
	}
}

func TestGovFix_NoFilesClean(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_fix", map[string]any{"files": []string{}})
	if text := resultText(res); res.IsError || text != "No diagnostics." {
		t.Errorf("unexpected output: %s", text)
	}
}

func TestGovSearch(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_search", map[string]any{"query": "add"})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "testpassing.Add") {
		t.Errorf("expected Add in output, got:\n%s", text)
	}
}

func TestGovOutline_API(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	for _, target := range []string{"testpassing", filepath.Join(dir, "main.go")} {
		res := callTool(t, cs, "gov_outline", map[string]any{"target": target, "api": true})
		text := resultText(res)
		if res.IsError {
			t.Fatalf("%s: unexpected error: %s", target, text)
		}
		if !strings.Contains(text, `// import "testpassing"`) || !strings.Contains(text, "func Add(a, b int) int") {
			t.Errorf("%s: expected Add signature, got:\n%s", target, text)
		}
	}
}

// Default sections: definition and references.
func TestGovSymbol_Default(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_symbol", map[string]any{
		"file":   filepath.Join(dir, "main.go"),
		"symbol": "Add",
	})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	for _, want := range []string{"Defined: main.go:4:6", "Add adds two integers.", "def main.go:4:6", "main_test.go:6:12"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "in TestAdd") {
		t.Errorf("callers shown without being asked:\n%s", text)
	}
}

func TestGovSymbol_Show(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_symbol", map[string]any{
		"file":   filepath.Join(dir, "main.go"),
		"symbol": "Add",
		"show":   []string{"callers", "implementations"},
	})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	// Callers apply; implementations do not apply to a function and say so
	// in place.
	if !strings.Contains(text, "main_test.go:6:12  in TestAdd") || !strings.Contains(text, "implementations: ") || !strings.Contains(text, "not a type or method") {
		t.Errorf("unexpected output:\n%s", text)
	}
	if strings.Contains(text, "Defined:") {
		t.Errorf("definition shown without being asked:\n%s", text)
	}

	res = callTool(t, cs, "gov_symbol", map[string]any{
		"file":   filepath.Join(dir, "main_test.go"),
		"symbol": "TestAdd",
		"show":   []string{"callees"},
	})
	text = resultText(res)
	if res.IsError || !strings.Contains(text, "Add") || !strings.Contains(text, "testing.common.Errorf") {
		t.Errorf("unexpected output:\n%s", text)
	}
}

func TestGovSymbol_Errors(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_symbol", map[string]any{"file": filepath.Join(dir, "main.go"), "symbol": "Nope"})
	if text := resultText(res); !res.IsError || !strings.Contains(text, "Nope") {
		t.Errorf("unknown symbol: %s", text)
	}
	res = callTool(t, cs, "gov_symbol", map[string]any{"file": filepath.Join(dir, "main.go"), "symbol": "Add", "show": []string{"users"}})
	if text := resultText(res); !res.IsError || !strings.Contains(text, `unknown show value "users"`) {
		t.Errorf("unknown show: %s", text)
	}
}

func TestGovFileContext(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_file_context", map[string]any{"file": filepath.Join(dir, "main_test.go")})
	text := resultText(res)
	if res.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "Add  main.go:4:6") {
		t.Errorf("expected Add use from main.go, got:\n%s", text)
	}
}

func TestGovPlan(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_plan", map[string]any{
		"op": "replace", "file": filepath.Join(dir, "main.go"), "symbol": "Add",
		"source": "// Add adds two integers.\nfunc Add(a, b, c int) int { return a + b + c }",
	})
	text := resultText(res)
	if res.IsError || !strings.HasPrefix(text, "OK: replace Add") || !strings.Contains(text, "Update the callers (1)") || !strings.Contains(text, "main_test.go:6:12  in TestAdd") {
		t.Errorf("unexpected output:\n%s", text)
	}

	res = callTool(t, cs, "gov_plan", map[string]any{"op": "add", "target": "testpassing", "source": "func Add() {}"})
	text = resultText(res)
	if res.IsError || !strings.HasPrefix(text, "BLOCKED: add func Add") || !strings.Contains(text, "Fix: use op replace") {
		t.Errorf("unexpected output:\n%s", text)
	}

	res = callTool(t, cs, "gov_plan", map[string]any{"op": "add", "target": "testpassing", "source": "func Add() {}", "force": true})
	text = resultText(res)
	if res.IsError || !strings.HasPrefix(text, "OK (forced): add func Add") {
		t.Errorf("unexpected output:\n%s", text)
	}

	res = callTool(t, cs, "gov_plan", map[string]any{"op": "rename"})
	if text := resultText(res); !res.IsError || !strings.Contains(text, "expected add, remove, replace or move") {
		t.Errorf("unexpected output:\n%s", text)
	}
}

func TestGovOutline(t *testing.T) {
	dir := copyFixture(t, "passing")
	cs := setup(t, dir, nil)
	res := callTool(t, cs, "gov_outline", map[string]any{"target": "testpassing"})
	text := resultText(res)
	if res.IsError || !strings.Contains(text, "func Add(a, b int) int") || !strings.Contains(text, "func TestAdd(t *testing.T)") {
		t.Errorf("unexpected output:\n%s", text)
	}
}
