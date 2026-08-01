package agentfs

import (
	"errors"
	"path/filepath"
	"testing"
)

func dirs(t *testing.T, sources []AgentSource) []string {
	t.Helper()
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		ds, ok := s.(DirSource)
		if !ok {
			t.Fatalf("resolver produced a non-DirSource: %T", s)
		}
		out = append(out, ds.Dir)
	}
	return out
}

func assertDirs(t *testing.T, got []AgentSource, want []string) {
	t.Helper()
	have := dirs(t, got)
	if len(have) != len(want) {
		t.Fatalf("resolved %d sources, want %d\n got: %v\nwant: %v", len(have), len(want), have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Errorf("source[%d] = %q, want %q", i, have[i], want[i])
		}
	}
}

func TestResolveSourcesOptInByDefault(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/u", nil },
	})
	if len(got) != 0 {
		t.Fatalf("zero options must resolve no sources, got %d: %v", len(got), got)
	}
}

func TestResolveSourcesConventionalPrecedenceOrder(t *testing.T) {
	const home = "/home/u"
	const ws = "/work/repo"
	got := resolveSourcesEnv(ResolveOptions{
		Explicit:           []string{"/explicit"},
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: true,
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return home, nil },
	})
	want := []string{
		"/explicit",
		filepath.Join(ws, ".mecatl", "agents"),
		filepath.Join(ws, ".claude", "agents"),
		filepath.Join(home, ".config", "mecatl", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	assertDirs(t, got, want)
}

func TestResolveSourcesHonorsXDGConfigHome(t *testing.T) {
	const home = "/home/u"
	const xdg = "/custom/xdg"
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return home, nil },
	})
	resolved := dirs(t, got)
	wantXDG := filepath.Join(xdg, "mecatl", "agents")
	found := false
	for _, d := range resolved {
		if d == wantXDG {
			found = true
		}
		if d == filepath.Join(home, ".config", "mecatl", "agents") {
			t.Errorf("~/.config fallback should be skipped when XDG_CONFIG_HOME is set: %v", resolved)
		}
	}
	if !found {
		t.Errorf("XDG_CONFIG_HOME not honoured: want %q in %v", wantXDG, resolved)
	}
}

func TestResolveSourcesNoHomeSkipsUser(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	})
	want := []string{
		filepath.Join("/ws", ".mecatl", "agents"),
		filepath.Join("/ws", ".claude", "agents"),
	}
	assertDirs(t, got, want)
}

// TestResolveSourcesUntrustedDropsProjectTier asserts the Phase-2a trust gate:
// with IncludeProjectTier=false (an UNTRUSTED workspace) the project-tier agent-def
// dirs are WITHHELD while the explicit and user-tier dirs stay active.
func TestResolveSourcesUntrustedDropsProjectTier(t *testing.T) {
	const home = "/home/u"
	const ws = "/work/repo"
	got := resolveSourcesEnv(ResolveOptions{
		Explicit:           []string{"/explicit"},
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: false, // untrusted
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return home, nil },
	})
	want := []string{
		"/explicit",
		filepath.Join(home, ".config", "mecatl", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	assertDirs(t, got, want)
	for _, d := range dirs(t, got) {
		if d == filepath.Join(ws, ".mecatl", "agents") || d == filepath.Join(ws, ".claude", "agents") {
			t.Errorf("untrusted workspace leaked a project-tier agents dir: %q", d)
		}
	}
}

// TestResolveSourcesTrustedKeepsProjectTier is the positive regression guard
// (mirrors skills): with IncludeProjectTier=true the project-tier agents dir IS
// admitted. Named so a future change that drops the project tier under trust fails
// loudly here.
func TestResolveSourcesTrustedKeepsProjectTier(t *testing.T) {
	const home = "/home/u"
	const ws = "/work/repo"
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: true,
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return home, nil },
	})
	found := false
	for _, d := range dirs(t, got) {
		if d == filepath.Join(ws, ".mecatl", "agents") {
			found = true
		}
	}
	if !found {
		t.Errorf("trusted workspace must admit the project-tier agents dir; got %v", dirs(t, got))
	}
}
