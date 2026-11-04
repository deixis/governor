package shellguard

import (
	"strings"
)

const editBlocked = "Blocked: in-place edit of a Go file with a text tool. Edit each site with the Edit or Write tool, or use ast-grep (sg) for a repeated pattern. After changing a definition, run gov_fix on its file and gov_diagnostics for the workspace: they list every site that must change."

// blocked returns the reason a command must not run, or "". goFiles says
// whether the command, or the shell input around it, names a Go file:
// in a loop the file arrives through a variable.
func blocked(name string, args []string, goFiles bool) string {
	if !goFiles {
		return ""
	}
	switch name {
	case "sed", "gsed":
		for _, a := range args {
			if strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
				return editBlocked
			}
		}
	case "perl":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--") {
				continue
			}
			// -i, -pi, -0pi, -i.bak: the i switch anywhere in a cluster.
			if strings.Contains(a, "i") {
				return editBlocked
			}
		}
	case "sd":
		for _, a := range args {
			if a == "-p" || a == "--preview" {
				return ""
			}
		}
		return editBlocked
	case "awk", "gawk":
		for i, a := range args {
			if a == "--inplace" || strings.HasPrefix(a, "--inplace=") {
				return editBlocked
			}
			if a == "-i" && i+1 < len(args) && args[i+1] == "inplace" {
				return editBlocked
			}
		}
	}
	return ""
}

// rewrite returns the governor command that replaces name args, and why,
// or nil when the command runs as is.
func rewrite(name string, args []string) ([]string, string) {
	switch name {
	case "go":
		if len(args) == 0 {
			return nil, ""
		}
		switch args[0] {
		case "test":
			return goTest(args[1:])
		case "vet":
			return check(here(packages(args[1:], goVetValueFlags)), false, ""), "gov_check runs vet through golangci-lint"
		case "tool":
			if len(args) > 1 && args[1] == "staticcheck" {
				return check(here(packages(args[2:], staticcheckValueFlags)), false, ""), "gov_check runs staticcheck"
			}
		}
	case "staticcheck":
		return check(here(packages(args, staticcheckValueFlags)), false, ""), "gov_check runs staticcheck"
	case "golangci-lint":
		if len(args) == 0 || args[0] != "run" {
			return nil, ""
		}
		fix := false
		for _, a := range args[1:] {
			if a == "--fix" {
				fix = true
			}
		}
		// golangci-lint run with no argument lints ./..., as governor does.
		return check(packages(args[1:], golangciValueFlags), fix, ""), "gov_check runs golangci-lint"
	case "goimports", "gofmt", "gofumpt":
		return format(args)
	}
	return nil, ""
}

// goTestValueFlags are go test flags that take their value as the next
// argument when not written with =.
var goTestValueFlags = set("run", "bench", "fuzz", "skip", "timeout", "count", "cpu", "parallel", "tags", "p",
	"exec", "o", "coverprofile", "covermode", "coverpkg", "cpuprofile", "memprofile", "blockprofile",
	"mutexprofile", "trace", "outputdir", "shuffle", "benchtime", "fuzztime", "fuzzminimizetime",
	"ldflags", "gcflags", "asmflags", "gccgoflags", "mod", "modfile", "overlay", "pgo", "toolexec",
	"buildmode", "buildvcs", "compiler", "installsuffix", "memprofilerate", "blockprofilerate")

// targetedTestFlags mark a go test run that governor check cannot stand
// in for: a subset of tests, a benchmark, a fuzz target, a profile.
var targetedTestFlags = set("run", "bench", "fuzz", "skip", "coverprofile", "cpuprofile", "memprofile",
	"blockprofile", "mutexprofile", "trace", "json", "exec", "o", "c", "benchtime", "fuzztime", "list")

var (
	goVetValueFlags       = set("tags", "mod", "modfile", "overlay", "vettool", "ldflags", "gcflags")
	staticcheckValueFlags = set("checks", "tags", "go", "f", "explain", "matrix")
	golangciValueFlags    = set("c", "config", "E", "enable", "D", "disable", "timeout", "out-format",
		"output.text.path", "build-tags", "j", "concurrency", "max-issues-per-linter", "max-same-issues",
		"presets", "path-prefix", "exclude", "e", "exclude-use-default", "new-from-rev", "new-from-patch",
		"modules-download-mode", "go", "sort-order", "show-stats", "allow-parallel-runners")
)

func goTest(args []string) ([]string, string) {
	var timeout string
	verbose := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, value, hasValue := splitFlag(a)
		if flag == "" {
			continue
		}
		if targetedTestFlags[flag] {
			return nil, ""
		}
		if flag == "v" {
			verbose = true
		}
		if goTestValueFlags[flag] && !hasValue && i+1 < len(args) {
			value = args[i+1]
			i++
		}
		if flag == "timeout" {
			timeout = value
		}
	}
	pkgs := here(packages(args, goTestValueFlags))
	cmd := check(pkgs, false, timeout)
	if verbose {
		cmd = append([]string{"governor", "check", "-v"}, cmd[2:]...)
	}
	return cmd, "gov_check runs the tests with the project's configured flags, then lint and staticcheck"
}

// here keeps the go tool's default of the current package when no
// package was named; governor check alone would mean ./...
func here(pkgs []string) []string {
	if len(pkgs) == 0 {
		return []string{"."}
	}
	return pkgs
}

func check(pkgs []string, fix bool, timeout string) []string {
	cmd := []string{"governor", "check"}
	if fix {
		cmd = append(cmd, "-fix")
	}
	if timeout != "" {
		cmd = append(cmd, "-timeout", timeout)
	}
	return append(cmd, pkgs...)
}

// format rewrites an in-place formatter run over Go files to governor
// fix. Listing or diff modes and directory arguments run as is.
func format(args []string) ([]string, string) {
	write := false
	var files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, _, hasValue := splitFlag(a)
		switch {
		case flag == "w":
			write = true
		case flag == "l", flag == "d", flag == "e", flag == "extra":
			return nil, ""
		case flag == "local" && !hasValue:
			i++
		case flag != "":
		case isGoFile(a):
			files = append(files, a)
		default:
			return nil, ""
		}
	}
	if !write || len(files) == 0 {
		return nil, ""
	}
	return append([]string{"governor", "fix"}, files...), "gov_fix formats with gofumpt, repairs imports and reports diagnostics"
}

// packages returns the non-flag arguments, skipping the values of flags
// that take one as a separate argument.
func packages(args []string, valueFlags map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, _, hasValue := splitFlag(a)
		if flag == "" {
			if a != "--" {
				out = append(out, a)
			}
			continue
		}
		if valueFlags[flag] && !hasValue {
			i++
		}
	}
	return out
}

// splitFlag parses -name, --name, -name=value and --name=value. A
// non-flag argument yields flag "".
func splitFlag(arg string) (flag, value string, hasValue bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
		return "", "", false
	}
	arg = strings.TrimLeft(arg, "-")
	if i := strings.IndexByte(arg, '='); i >= 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
