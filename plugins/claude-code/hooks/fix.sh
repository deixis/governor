#!/bin/sh
# PostToolUse hook: run `governor fix` on the Go file an edit tool just
# touched. Reads the hook event on stdin, prints governor's output so it
# reaches the agent as context, and always exits 0 so a diagnostic never
# blocks the edit itself.

set -u

event=$(cat)

if command -v jq >/dev/null 2>&1; then
  file=$(printf '%s' "$event" | jq -r '.tool_input.file_path // empty')
else
  # Minimal fallback: first "file_path":"..." value. Paths with escaped
  # quotes are not expected from edit tools.
  file=$(printf '%s' "$event" | sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
fi

case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac

if ! command -v governor >/dev/null 2>&1; then
  echo "governor is not installed; gov_fix skipped for $file."
  echo "Install: go install github.com/deixis/governor/cmd/governor@latest"
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
governor fix -q "$file"
exit 0
