// Package gotool parses the output of `go tool` run with no arguments.
package gotool

import "strings"

// ModuleTools returns the import paths of the module tool directives in
// `go tool` output. Built-in tools (vet, cover, ...) print as a bare name
// and are left out. Go 1.27 prints a module tool as
// "name (import/path)", earlier versions as the bare import path; both
// forms are accepted.
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
