package skillfs

import (
	"errors"
	"path/filepath"
	"testing"
)

// dirs extracts the resolved DirSource directories (in precedence order) so a
// test can assert the ordered path set without reaching into Source internals.
func dirs(t *testing.T, sources []Source) []string {
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

func TestResolveSourcesOptInByDefault(t *testing.T) {
	// Zero options: strictly opt-in — no explicit dirs, conventional OFF.
	got := resolveSourcesEnv(ResolveOptions{}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/u", nil },
	})
	if len(got) != 0 {
		t.Fatalf("zero options must resolve no sources, got %d: %v", len(got), got)
	}
}

func TestResolveSourcesExplicitOnly(t *testing.T) {
	got := resolveSourcesEnv(ResolveOptions{
		Explicit: []string{"/one", "", "/two"}, // empty entries dropped
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/u", nil },
	})
	assertDirs(t, got, []string{"/one", "/two"})
	for _, s := range got {
		if ds := s.(DirSource); ds.Label != "explicit" {
			t.Errorf("explicit source mislabelled: %q", ds.Label)
		}
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
		Getenv:      func(string) string { return "" }, // no XDG_CONFIG_HOME -> ~/.config
		UserHomeDir: func() (string, error) { return home, nil },
	})
	// Precedence, highest first: explicit > project(.mecatl) > project(.claude)
	// > user(xdg ~/.config/mecatl) > user(~/.claude).
	want := []string{
		"/explicit",
		filepath.Join(ws, ".mecatl", "skills"),
		filepath.Join(ws, ".claude", "skills"),
		filepath.Join(home, ".config", "mecatl", "skills"),
		filepath.Join(home, ".claude", "skills"),
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
	wantXDG := filepath.Join(xdg, "mecatl", "skills")
	if !containsDir(resolved, wantXDG) {
		t.Errorf("XDG_CONFIG_HOME not honoured: want %q in %v", wantXDG, resolved)
	}
	// The ~/.config fallback must NOT appear when XDG_CONFIG_HOME is set.
	if containsDir(resolved, filepath.Join(home, ".config", "mecatl", "skills")) {
		t.Errorf("~/.config fallback should be skipped when XDG_CONFIG_HOME is set: %v", resolved)
	}
}

func TestResolveSourcesNoWorkspaceSkipsProject(t *testing.T) {
	const home = "/home/u"
	got := resolveSourcesEnv(ResolveOptions{
		Conventional: true,
		// Workspace empty: project-level sources are skipped, user-level remain.
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return home, nil },
	})
	want := []string{
		filepath.Join(home, ".config", "mecatl", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	assertDirs(t, got, want)
}

func TestResolveSourcesNoHomeSkipsUser(t *testing.T) {
	// No home and no XDG: user-level sources cannot be resolved, but project ones
	// (under an explicit workspace) still resolve.
	got := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          "/ws",
		IncludeProjectTier: true,
	}, ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errNoHome },
	})
	want := []string{
		filepath.Join("/ws", ".mecatl", "skills"),
		filepath.Join("/ws", ".claude", "skills"),
	}
	assertDirs(t, got, want)
}

// TestResolveSourcesUntrustedDropsProjectTier asserts the Phase-2a trust gate:
// with IncludeProjectTier=false (an UNTRUSTED workspace) the project-tier
// conventional dirs are WITHHELD while the explicit and user-tier dirs stay active
// — the "ask the human, not do nothing" degradation (R2.5).
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
	// Explicit (operator-supplied) + user tier stay; project tier is dropped.
	want := []string{
		"/explicit",
		filepath.Join(home, ".config", "mecatl", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	assertDirs(t, got, want)
	for _, d := range dirs(t, got) {
		if d == filepath.Join(ws, ".mecatl", "skills") || d == filepath.Join(ws, ".claude", "skills") {
			t.Errorf("untrusted workspace leaked a project-tier skills dir: %q", d)
		}
	}
}

// TestResolveSourcesTrustedKeepsProjectTier is the positive counterpart: with
// IncludeProjectTier=true the project tier IS admitted (current behaviour).
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
	if !containsDir(dirs(t, got), filepath.Join(ws, ".mecatl", "skills")) {
		t.Errorf("trusted workspace must admit the project tier; got %v", dirs(t, got))
	}
}

var errNoHome = errors.New("no home")

func assertDirs(t *testing.T, got []Source, want []string) {
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

func containsDir(dirs []string, target string) bool {
	for _, d := range dirs {
		if d == target {
			return true
		}
	}
	return false
}
