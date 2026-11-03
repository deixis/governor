package reexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	withTool := "asm\ncover\nvet\n" + ImportPath + "\nhonnef.co/go/tools/cmd/staticcheck\n"
	cases := []struct {
		name string
		p    probe
		want action
	}{
		{"tool directive wins", probe{goToolList: withTool, goVersion: "go1.30.0", self: "go1.25.1", modVersion: "v0.0.1"}, actionGoTool},
		{"tool directive wins even for devel", probe{goToolList: withTool, goVersion: "go1.25.1", self: "go1.25.1", modVersion: "(devel)"}, actionGoTool},
		{"same toolchain", probe{goVersion: "go1.25.1", self: "go1.25.1", modVersion: "v0.0.1"}, actionInProcess},
		{"older workspace", probe{goVersion: "go1.24.0", self: "go1.25.1", modVersion: "v0.0.1"}, actionInProcess},
		{"newer workspace", probe{goVersion: "go1.26.0", self: "go1.25.1", modVersion: "v0.0.1"}, actionCachedBuild},
		{"newer workspace, devel build", probe{goVersion: "go1.26.0", self: "go1.25.1", modVersion: "(devel)"}, actionInProcess},
		{"newer workspace, no build info", probe{goVersion: "go1.26.0", self: "go1.25.1"}, actionInProcess},
		{"probe failed", probe{goVersion: "", self: "go1.25.1", modVersion: "v0.0.1"}, actionInProcess},
		{"devel self", probe{goVersion: "go1.26.0", self: "devel go1.26-abc123", modVersion: "v0.0.1"}, actionInProcess},
		{"rc workspace", probe{goVersion: "go1.26rc1", self: "go1.25.1", modVersion: "v0.0.1"}, actionCachedBuild},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decide(c.p); got != c.want {
				t.Errorf("decide = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHasToolDirective(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{ImportPath + "\n", true},
		{"vet\n  " + ImportPath + "  \n", true},
		{"vet\ncover\n", false},
		{"github.com/deixis/governor\n", false},     // root package, not the command
		{ImportPath + "/v2\n", false},               // different module
		{"example.com/other/cmd/governor\n", false}, // same base name only
		{"", false},
	}
	for _, c := range cases {
		if got := hasToolDirective(c.out); got != c.want {
			t.Errorf("hasToolDirective(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

func TestCachedBinary(t *testing.T) {
	got := cachedBinary("/cache", "go1.26.0", "v0.0.1")
	want := filepath.Join("/cache", "governor", "go1.26.0", "v0.0.1", "governor")
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	if got != want {
		t.Errorf("cachedBinary = %q, want %q", got, want)
	}
}

func TestMaybe_GuardSkips(t *testing.T) {
	t.Setenv(guardEnv, "1")
	if err := Maybe(t.TempDir(), []string{"version"}); err != nil {
		t.Fatalf("Maybe with guard set = %v, want nil", err)
	}
}

// TestEnsureBuild_EndToEnd installs a published governor version under a
// different toolchain into the real user cache. Network and toolchain
// downloads make it unfit for the default run; enable with
// GOVERNOR_E2E_GOVERSION=go1.26.0 GOVERNOR_E2E_MODVERSION=v0.0.1.
func TestEnsureBuild_EndToEnd(t *testing.T) {
	goVersion, modVersion := os.Getenv("GOVERNOR_E2E_GOVERSION"), os.Getenv("GOVERNOR_E2E_MODVERSION")
	if goVersion == "" || modVersion == "" {
		t.Skip("set GOVERNOR_E2E_GOVERSION and GOVERNOR_E2E_MODVERSION")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	bin, err := ensureBuild(goBin, goVersion, modVersion)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(goBin, "version", "-m", bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), goVersion) || !strings.Contains(string(out), modVersion) {
		t.Errorf("built binary reports:\n%s\nwant toolchain %s and module %s", out, goVersion, modVersion)
	}
}

func TestMaybe_NoWorkspaceRunsInProcess(t *testing.T) {
	// An empty directory has no go.mod: `go tool` lists only built-ins and
	// GOVERSION is the host toolchain, which equals the one running the
	// test, so Maybe must return nil without exec'ing.
	if err := Maybe(t.TempDir(), []string{"version"}); err != nil {
		t.Fatalf("Maybe = %v, want nil", err)
	}
}
