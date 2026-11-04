package shellguard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runHook(t *testing.T, event string) (string, map[string]any) {
	t.Helper()
	var out bytes.Buffer
	if err := ClaudeCodeHook(strings.NewReader(event), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return "", nil
	}
	var parsed struct {
		HookSpecificOutput map[string]any `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	return out.String(), parsed.HookSpecificOutput
}

func TestClaudeCodeHook_Rewrite(t *testing.T) {
	_, got := runHook(t, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test -race ./x","description":"Run tests","timeout":120000}}`)
	if got["hookEventName"] != "PreToolUse" || got["permissionDecision"] != "allow" {
		t.Errorf("output = %+v", got)
	}
	updated, _ := got["updatedInput"].(map[string]any)
	if updated["command"] != "governor check ./x" || updated["description"] != "Run tests" || updated["timeout"] != float64(120000) {
		t.Errorf("updatedInput = %+v", updated)
	}
	ctx, _ := got["additionalContext"].(string)
	if !strings.Contains(ctx, "rewrote") {
		t.Errorf("additionalContext = %q", ctx)
	}
}

func TestClaudeCodeHook_Block(t *testing.T) {
	_, got := runHook(t, `{"tool_name":"Bash","tool_input":{"command":"perl -pi -e 's/a/b/' a.go"}}`)
	if got["permissionDecision"] != "deny" {
		t.Errorf("output = %+v", got)
	}
	reason, _ := got["permissionDecisionReason"].(string)
	if !strings.HasPrefix(reason, "Blocked:") {
		t.Errorf("reason = %q", reason)
	}
	if _, ok := got["updatedInput"]; ok {
		t.Error("updatedInput present on deny")
	}
}

func TestClaudeCodeHook_PassThrough(t *testing.T) {
	if out, _ := runHook(t, `{"tool_name":"Bash","tool_input":{"command":"git status"}}`); out != "" {
		t.Errorf("unexpected output %q", out)
	}
	if out, _ := runHook(t, `{"tool_name":"Edit","tool_input":{"file_path":"a.go"}}`); out != "" {
		t.Errorf("unexpected output for Edit %q", out)
	}
}

func TestClaudeCodeHook_BadEvent(t *testing.T) {
	if err := ClaudeCodeHook(strings.NewReader("not json"), &bytes.Buffer{}); err == nil {
		t.Error("expected error")
	}
}
