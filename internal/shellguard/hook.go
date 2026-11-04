package shellguard

import (
	"encoding/json"
	"fmt"
	"io"
)

// hookInput is the part of a Claude Code PreToolUse event the hook reads.
type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type bashInput struct {
	Command string `json:"command"`
}

// hookOutput is the PreToolUse response. updatedInput replaces the whole
// tool input, so the original fields travel along with the new command.
type hookOutput struct {
	HookSpecificOutput hookSpecific `json:"hookSpecificOutput"`
}

type hookSpecific struct {
	HookEventName            string         `json:"hookEventName"`
	PermissionDecision       string         `json:"permissionDecision"`
	PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             map[string]any `json:"updatedInput,omitempty"`
	AdditionalContext        string         `json:"additionalContext,omitempty"`
}

// ClaudeCodeHook reads a PreToolUse event from r and writes the decision
// to w. Nothing is written when the command runs as is, which Claude Code
// treats as pass-through. Events for other tools are ignored.
func ClaudeCodeHook(r io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading hook event: %w", err)
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("parsing hook event: %w", err)
	}
	if in.ToolName != "Bash" {
		return nil
	}
	var bash bashInput
	if err := json.Unmarshal(in.ToolInput, &bash); err != nil {
		return fmt.Errorf("parsing tool input: %w", err)
	}
	d := Inspect(bash.Command)
	if !d.Changed() {
		return nil
	}

	out := hookOutput{HookSpecificOutput: hookSpecific{HookEventName: "PreToolUse"}}
	if d.Blocked != "" {
		out.HookSpecificOutput.PermissionDecision = "deny"
		out.HookSpecificOutput.PermissionDecisionReason = d.Blocked
	} else {
		var fields map[string]any
		if err := json.Unmarshal(in.ToolInput, &fields); err != nil {
			return fmt.Errorf("parsing tool input: %w", err)
		}
		fields["command"] = d.Command
		out.HookSpecificOutput.PermissionDecision = "allow"
		out.HookSpecificOutput.UpdatedInput = fields
		out.HookSpecificOutput.AdditionalContext = d.Context()
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
