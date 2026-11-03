package workflow

import (
	"testing"
)

func TestResolvePackages_Empty(t *testing.T) {
	e := &Engine{Workspace: "/project", RepoRoot: "/project"}
	got := e.ResolvePackages(nil)
	if len(got) != 1 || got[0] != "./..." {
		t.Errorf("ResolvePackages(nil) = %v, want [./...]", got)
	}
}

func TestResolvePackages_RelativePattern(t *testing.T) {
	e := &Engine{Workspace: "/project", RepoRoot: "/project"}
	got := e.ResolvePackages([]string{"./pkg/foo/..."})
	if len(got) != 1 || got[0] != "./pkg/foo/..." {
		t.Errorf("ResolvePackages(./pkg/foo/...) = %v, want [./pkg/foo/...]", got)
	}
}

func TestResolvePackages_ImportPath(t *testing.T) {
	e := &Engine{Workspace: "/project", RepoRoot: "/project"}
	got := e.ResolvePackages([]string{"example.com/foo/bar/..."})
	if len(got) != 1 || got[0] != "example.com/foo/bar/..." {
		t.Errorf("ResolvePackages(import path) = %v, want [example.com/foo/bar/...]", got)
	}
}

func TestResolvePackages_AbsoluteInsideRepoRoot(t *testing.T) {
	e := &Engine{Workspace: "/project/pkg/foo", RepoRoot: "/project"}
	got := e.ResolvePackages([]string{"/project/pkg/bar"})
	if len(got) != 1 || got[0] != "./pkg/bar/..." {
		t.Errorf("ResolvePackages(/project/pkg/bar) = %v, want [./pkg/bar/...]", got)
	}
}

func TestResolvePackages_AbsoluteOutsideRepoRoot(t *testing.T) {
	e := &Engine{Workspace: "/project", RepoRoot: "/project"}
	got := e.ResolvePackages([]string{"/other/project"})
	// Should be dropped, falling back to ./...
	if len(got) != 1 || got[0] != "./..." {
		t.Errorf("ResolvePackages(outside) = %v, want [./...]", got)
	}
}

func TestResolvePackages_AbsoluteAtRepoRoot(t *testing.T) {
	e := &Engine{Workspace: "/project", RepoRoot: "/project"}
	got := e.ResolvePackages([]string{"/project"})
	// filepath.Rel("/project", "/project") = ".", so pattern = "./" + "." + "/..." = "././..."
	if len(got) != 1 || got[0] != "././..." {
		t.Errorf("ResolvePackages(/project) = %v, want [././...]", got)
	}
}

func TestResolvePackages_Mixed(t *testing.T) {
	e := &Engine{Workspace: "/project/cmd", RepoRoot: "/project"}
	got := e.ResolvePackages([]string{
		"./...",
		"example.com/foo",
		"/project/pkg/bar",
		"/outside",
	})
	// Expect: ./..., example.com/foo, ./pkg/bar/...
	// /outside is dropped
	if len(got) != 3 {
		t.Fatalf("ResolvePackages(mixed) = %v, want 3 entries", got)
	}
	if got[0] != "./..." {
		t.Errorf("got[0] = %q, want ./...", got[0])
	}
	if got[1] != "example.com/foo" {
		t.Errorf("got[1] = %q, want example.com/foo", got[1])
	}
	if got[2] != "./pkg/bar/..." {
		t.Errorf("got[2] = %q, want ./pkg/bar/...", got[2])
	}
}

func TestResolvePackages_RepoRootFallback(t *testing.T) {
	// When RepoRoot is empty, should fall back to Workspace.
	e := &Engine{Workspace: "/project", RepoRoot: ""}
	got := e.ResolvePackages([]string{"/project/pkg/foo"})
	if len(got) != 1 || got[0] != "./pkg/foo/..." {
		t.Errorf("ResolvePackages(empty RepoRoot) = %v, want [./pkg/foo/...]", got)
	}
}

// noTools is a ToolResolver reporting every tool as unavailable, so tests
// do not depend on what is installed on the host.
func noTools(string) []string { return nil }

func TestResolveTool_MissingNotResolvedViaGoTool(t *testing.T) {
	// A name that is neither a module tool nor on PATH must resolve to
	// nil, not to a dead "go tool <name>" invocation.
	if got := ResolveTool(t.TempDir(), "governor-no-such-tool-xyz"); got != nil {
		t.Errorf("ResolveTool = %v, want nil", got)
	}
}

func TestEngine_ResolveToolUsesInjectedResolver(t *testing.T) {
	e := &Engine{ToolResolver: func(name string) []string { return []string{"/stub/" + name} }}
	if got := e.resolveTool("dupl"); len(got) != 1 || got[0] != "/stub/dupl" {
		t.Errorf("resolveTool = %v", got)
	}
	e.ToolResolver = noTools
	if got := e.resolveTool("dupl"); got != nil {
		t.Errorf("resolveTool with noTools = %v, want nil", got)
	}
}
