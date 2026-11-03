#!/bin/sh
# PostToolUse hook: run `governor fix` on the Go file an edit tool just
# touched. Reads the hook event on stdin. Output is fed back to the agent
# on stderr with exit code 2, the only PostToolUse channel the agent sees
# (exit 0 stdout only reaches the transcript). The edit itself has already
# happened, so exit 2 never blocks it.

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
  echo "governor is not installed; gov_fix skipped for $file." >&2
  echo "Install: go install github.com/deixis/governor/cmd/governor@latest" >&2
  exit 2
fi

# Run from the file's directory so the go command resolves the module that
# owns it, not the project root (which may hold no go.mod in a monorepo).
cd "$(dirname "$file")" || exit 0
out=$(governor fix -q "$file" 2>&1)
if [ -n "$out" ]; then
  printf '%s\n' "$out" >&2
  exit 2
fi
exit 0
