// Package reexec hands execution over to a governor binary built with the
// workspace's Go toolchain when the running one is too old for it.
//
// Governor type-checks in process with go/types, so its export-data and
// language support are fixed at build time. A workspace whose go.mod
// selects a newer toolchain than the one that built this binary cannot be
// loaded. The fix mirrors what the go command does for toolchains: detect
// the mismatch and run a matching build instead, without touching any
// editor or agent configuration.
//
// Resolution order, from the workspace directory:
//
//  1. The module declares governor as a tool directive: run `go tool`.
//     The workspace pins both the governor version and the toolchain.
//  2. The workspace toolchain is not newer than this binary's: run in
//     process.
//  3. Otherwise install this binary's module version under the workspace
//     toolchain into the user cache and run that. Builds are cached per
//     (toolchain, governor version), so only the first run pays.
//  4. Development builds have no module version to install; they run in
//     process.
package reexec

import (
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/deixis/governor"
	"github.com/deixis/governor/internal/gotool"
)

// ImportPath is the main package of the governor binary.
const ImportPath = "github.com/deixis/governor/cmd/governor"

// guardEnv marks a process that was started by Maybe so the child does
// not evaluate the same decision again.
const guardEnv = "GOVERNOR_NO_REEXEC"

type action int

const (
	actionInProcess action = iota
	actionGoTool
	actionCachedBuild
)

// probe holds everything the decision depends on, so decide stays pure.
type probe struct {
	// goToolList is the output of `go tool` run in the workspace.
	goToolList string
	// goVersion is the toolchain the workspace selects, as reported by
	// `go env GOVERSION` run in the workspace (for example "go1.26.0").
	goVersion string
	// self is runtime.Version() of this binary.
	self string
	// modVersion is this binary's module version, "(devel)" for source
	// builds.
	modVersion string
}

func decide(p probe) action {
	if hasToolDirective(p.goToolList) {
		return actionGoTool
	}
	// A failed probe or a devel toolchain string ("devel go1.26-abc") is
	// not comparable; stay in process and let the workspace load speak.
	if !version.IsValid(p.goVersion) || !version.IsValid(p.self) {
		return actionInProcess
	}
	if version.Compare(p.goVersion, p.self) <= 0 {
		return actionInProcess
	}
	// A build from a modified checkout carries "+dirty": go install at
	// that version fails, and at the clean version it would build the
	// published commit, not the local changes.
	if p.modVersion == "" || p.modVersion == "(devel)" || strings.Contains(p.modVersion, "+dirty") {
		return actionInProcess
	}
	return actionCachedBuild
}

// hasToolDirective reports whether `go tool` output lists governor's main
// package as a module tool directive.
func hasToolDirective(goToolOutput string) bool {
	return slices.Contains(gotool.ModuleTools(goToolOutput), ImportPath)
}

// cachedBinary returns where a governor build for the given toolchain and
// module version lives under cacheDir.
func cachedBinary(cacheDir, goVersion, modVersion string) string {
	name := "governor"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(cacheDir, "governor", goVersion, modVersion, name)
}

// Maybe inspects the workspace in dir and, when a different governor build
// should serve it, replaces the current process with that build running
// args. It returns nil when the caller should continue in process. A
// non-nil error means the handover was attempted and failed; the caller
// should still continue in process, since the go command will then report
// the underlying mismatch on its own terms.
func Maybe(dir string, args []string) error {
	if os.Getenv(guardEnv) != "" {
		return nil
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}

	p := probe{self: runtime.Version(), modVersion: governor.Version}
	if out, err := run(goBin, dir, nil, "tool"); err == nil {
		p.goToolList = out
	}
	if out, err := run(goBin, dir, nil, "env", "GOVERSION"); err == nil {
		p.goVersion = strings.TrimSpace(out)
	}

	switch decide(p) {
	case actionInProcess:
		return nil
	case actionGoTool:
		argv := append([]string{goBin, "tool", ImportPath}, args...)
		return execve(argv, childEnv(nil))
	default:
		bin, err := ensureBuild(goBin, p.goVersion, p.modVersion)
		if err != nil {
			return err
		}
		return execve(append([]string{bin}, args...), childEnv(nil))
	}
}

// ensureBuild returns the path of a governor binary built with goVersion,
// installing it into the user cache on first use.
func ensureBuild(goBin, goVersion, modVersion string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating user cache: %w", err)
	}
	bin := cachedBinary(cacheDir, goVersion, modVersion)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "governor: building %s@%s with %s (this binary was built with %s)\n",
		ImportPath, modVersion, goVersion, runtime.Version())
	env := []string{"GOTOOLCHAIN=" + goVersion, "GOBIN=" + filepath.Dir(bin)}
	// Run from the cache directory: `go install pkg@version` must not see
	// the workspace module, and the cache directory holds no go.mod.
	if out, err := run(goBin, filepath.Dir(bin), env, "install", ImportPath+"@"+modVersion); err != nil {
		return "", fmt.Errorf("building governor with %s: %w\n%s", goVersion, err, strings.TrimSpace(out))
	}
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("building governor with %s: binary not found at %s", goVersion, bin)
	}
	return bin, nil
}

// run executes goBin with args in dir and returns its combined output.
// extraEnv is appended to the current environment.
func run(goBin, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command(goBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// childEnv returns the current environment with the re-exec guard set and
// extra entries appended.
func childEnv(extra []string) []string {
	env := append(os.Environ(), guardEnv+"=1")
	return append(env, extra...)
}
