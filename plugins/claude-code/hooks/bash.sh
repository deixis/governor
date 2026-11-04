#!/bin/sh
# PreToolUse hook for Bash: governor inspects the command and either
# rewrites it (go test, go vet, golangci-lint, staticcheck, goimports -w
# become governor check or governor fix), refuses it (sed, perl, sd, awk
# editing a Go file in place) or lets it through. The decision is printed
# as JSON on stdout; no output means run as is.

set -u

if ! command -v governor >/dev/null 2>&1; then
  exit 0
fi

exec governor hook bash
