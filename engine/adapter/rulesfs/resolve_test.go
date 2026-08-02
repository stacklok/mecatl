package rulesfs

import (
	"path/filepath"
	"testing"
)

func dirs(t *testing.T, sources []RuleSource) []string {
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

func assertDirs(t *testing.T, got []RuleSource, want []string) {
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

func fakeEnv(home, xdg string) ResolveEnv {
	return ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return home, nil },
	}
}

// TestResolveSourcesConventionalOffResolvesNothing asserts the test-isolation
// lane: Conventional=false yields NO sources (there is no explicit tier).
func TestResolveSourcesConventionalOffResolvesNothing(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, fakeEnv("/home/u", ""))
	if len(got) != 0 {
		t.Fatalf("Conventional=false must resolve no sources, got %v", dirs(t, got))
	}
}

// TestResolveSourcesPrecedenceOrder asserts project > user, mecatl before
// claude within each tier.
func TestResolveSourcesPrecedenceOrder(t *testing.T) {
	const home = "/home/u"
	const ws = "/work/repo"
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: true,
	}, fakeEnv(home, ""))
	want := []string{
		filepath.Join(ws, ".mecatl", "rules"),
		filepath.Join(ws, ".claude", "rules"),
		filepath.Join(home, ".config", "mecatl", "rules"),
		filepath.Join(home, ".claude", "rules"),
	}
	assertDirs(t, got, want)
}

// TestResolveSourcesUntrustedWithholdsProjectTier asserts the trust gate:
// IncludeProjectTier=false drops BOTH project dirs while the user tier stays
// active.
func TestResolveSourcesUntrustedWithholdsProjectTier(t *testing.T) {
	const home = "/home/u"
	const ws = "/work/repo"
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: false, // untrusted
	}, fakeEnv(home, ""))
	want := []string{
		filepath.Join(home, ".config", "mecatl", "rules"),
		filepath.Join(home, ".claude", "rules"),
	}
	assertDirs(t, got, want)
	for _, d := range dirs(t, got) {
		if d == filepath.Join(ws, ".mecatl", "rules") || d == filepath.Join(ws, ".claude", "rules") {
			t.Errorf("untrusted workspace leaked a project-tier rules dir: %q", d)
		}
	}
}

// TestResolveSourcesTrustedKeepsProjectTier is the positive regression guard:
// IncludeProjectTier=true admits the project tier.
func TestResolveSourcesTrustedKeepsProjectTier(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, fakeEnv("/home/u", ""))
	found := false
	for _, d := range dirs(t, got) {
		if d == filepath.Join("/ws", ".mecatl", "rules") {
			found = true
		}
	}
	if !found {
		t.Errorf("trusted workspace must admit the project-tier rules dir; got %v", dirs(t, got))
	}
}

// TestResolveSourcesHonorsXDGConfigHome asserts $XDG_CONFIG_HOME wins over the
// ~/.config fallback for the mecatl user dir.
func TestResolveSourcesHonorsXDGConfigHome(t *testing.T) {
	const home = "/home/u"
	const xdg = "/custom/xdg"
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, fakeEnv(home, xdg))
	resolved := dirs(t, got)
	wantXDG := filepath.Join(xdg, "mecatl", "rules")
	found := false
	for _, d := range resolved {
		if d == wantXDG {
			found = true
		}
		if d == filepath.Join(home, ".config", "mecatl", "rules") {
			t.Errorf("~/.config fallback must be skipped when XDG_CONFIG_HOME is set: %v", resolved)
		}
	}
	if !found {
		t.Errorf("XDG_CONFIG_HOME not honoured: want %q in %v", wantXDG, resolved)
	}
}

// TestResolveSourcesEmptyWorkspaceSkipsProjectTier asserts an empty Workspace
// resolves only the user tier (no project dirs to point at).
func TestResolveSourcesEmptyWorkspaceSkipsProjectTier(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "",
		IncludeProjectTier: true,
	}, fakeEnv("/home/u", ""))
	want := []string{
		filepath.Join("/home/u", ".config", "mecatl", "rules"),
		filepath.Join("/home/u", ".claude", "rules"),
	}
	assertDirs(t, got, want)
}

// TestResolveSourcesTierStamping asserts each resolved DirSource carries the
// right admission tier label.
func TestResolveSourcesTierStamping(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, fakeEnv("/home/u", ""))
	for _, s := range got {
		ds := s.(DirSource)
		switch ds.Label {
		case "project(.mecatl)", "project(.claude)":
			if ds.Tier != "project" {
				t.Errorf("%s Tier = %q, want project", ds.Label, ds.Tier)
			}
		case "user(xdg)", "user(.claude)":
			if ds.Tier != "user" {
				t.Errorf("%s Tier = %q, want user", ds.Label, ds.Tier)
			}
		default:
			t.Errorf("unexpected label %q", ds.Label)
		}
	}
}
