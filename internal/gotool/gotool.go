// Package gotool parses the output of `go tool` run with no arguments.
package gotool

import (
	"path"
	"strings"
)

// ModuleTools returns the import paths of the module tool directives in
// `go tool` output. Built-in tools (vet, cover, ...) print as a bare name
// and are left out. Go 1.27 prints a module tool as "name (import/path)"
// when the name is unique, and as the bare import path when it collides
// with a built-in or another module tool; earlier versions always print
// the bare import path. All forms are accepted.
func ModuleTools(goToolOutput string) []string {
	var paths []string
	for line := range strings.SplitSeq(goToolOutput, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, " ("); i >= 0 && strings.HasSuffix(line, ")") {
			line = line[i+2 : len(line)-1]
		}
		if !strings.Contains(line, "/") {
			continue
		}
		paths = append(paths, line)
	}
	return paths
}

// Find returns the import path of the first module tool in `go tool`
// output that cmd/go would run as `go tool name`, or "" when there is
// none. Built-in tools never match. Callers should invoke the tool by
// the returned import path: `go tool name` fails when name is ambiguous
// and runs the built-in when name shadows one.
func Find(goToolOutput, name string) string {
	for _, importPath := range ModuleTools(goToolOutput) {
		if ExecName(importPath) == name {
			return importPath
		}
	}
	return ""
}

// ExecName returns the name cmd/go gives a tool at importPath: the last
// path element, or the one before it when the last is a major version
// suffix (example.com/mycmd/v2 is mycmd).
func ExecName(importPath string) string {
	dir, elem := path.Split(importPath)
	if dir != "" && isVersionElement(elem) {
		elem = path.Base(strings.TrimSuffix(dir, "/"))
	}
	return elem
}

// isVersionElement reports whether s is a major version element (v2,
// v3, ...), matching cmd/go/internal/load.
func isVersionElement(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] == '0' || s[1] == '1' && len(s) == 2 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || '9' < s[i] {
			return false
		}
	}
	return true
}
