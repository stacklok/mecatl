package permconfig

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// countingWS wraps a memfs Workspace to count Read calls, so a test can assert the
// resolver reads project files ONCE per root (then serves the revalidated cache).
type countingWS struct {
	*memfs.Workspace
	mu    sync.Mutex
	reads int
}

func (c *countingWS) Read(ctx context.Context, p string) ([]byte, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.Workspace.Read(ctx, p)
}

func (c *countingWS) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// seed writes a file into the underlying workspace (the resolver reads through it).
func (c *countingWS) seed(t *testing.T, p, content string) {
	t.Helper()
	if err := c.Write(context.Background(), p, []byte(content)); err != nil {
		t.Fatalf("seed %s: %v", p, err)
	}
}

func newProjectWS(t *testing.T, root string, settings string) *countingWS {
	t.Helper()
	ws := &countingWS{Workspace: memfs.NewWorkspace(root)}
	if settings != "" {
		ws.seed(t, projectFileMecatl, settings)
	}
	return ws
}

// fakeEnv is an injectable environment with no user-global files and no home.
func fakeEnv() xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    func(string) ([]byte, error) { return nil, errors.New("not found") },
	}
}

const trustedAllowYAML = `
permissions:
  allow:
    - "Bash(go test:*)"
  deny:
    - "Bash(rm:*)"
`

// A trusted project's allow + deny both resolve; the cache reads the file once per
// root and serves subsequent calls from memory.
func TestResolveTrustedProjectAndCache(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	if r == nil {
		t.Fatal("resolver should be non-nil when Conventional is set")
	}
	ws := newProjectWS(t, "/repo", trustedAllowYAML)

	rules := r.Resolve(context.Background(), ws)
	if findRule(rules, "Bash", "go test*") == nil {
		t.Fatalf("trusted project allow should resolve: %+v", rules)
	}
	if findRule(rules, "Bash", "rm*") == nil {
		t.Fatalf("project deny should resolve: %+v", rules)
	}
	readsAfterFirst := ws.readCount()
	if readsAfterFirst == 0 {
		t.Fatal("expected the resolver to read the project file at least once")
	}
	// Second Resolve for the SAME root hits the cache: revalidation is Stat-only
	// (not counted), so no further Reads.
	_ = r.Resolve(context.Background(), ws)
	if ws.readCount() != readsAfterFirst {
		t.Fatalf("cache miss: reads grew from %d to %d on a repeated unchanged Resolve", readsAfterFirst, ws.readCount())
	}
}

// An UNTRUSTED project drops its ALLOW rules but keeps deny/ask.
func TestResolveUntrustedProjectDropsAllowKeepsDeny(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, TrustProject: false}, fakeEnv())
	ws := newProjectWS(t, "/repo", trustedAllowYAML)

	rules := r.Resolve(context.Background(), ws)
	if findRule(rules, "Bash", "go test*") != nil {
		t.Fatalf("untrusted project allow must be DROPPED: %+v", rules)
	}
	if findRule(rules, "Bash", "rm*") == nil {
		t.Fatalf("project deny must be kept even when untrusted: %+v", rules)
	}
}

// Two different roots resolve independently (different files → different rules).
func TestResolvePerRootIndependent(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	wsA := newProjectWS(t, "/a", "permissions:\n  allow:\n    - \"Bash(go test:*)\"\n")
	wsB := newProjectWS(t, "/b", "permissions:\n  deny:\n    - \"Bash(go test:*)\"\n")

	ra := r.Resolve(context.Background(), wsA)
	rb := r.Resolve(context.Background(), wsB)
	if got := findRule(ra, "Bash", "go test*"); got == nil || got.Effect != governance.Allow {
		t.Fatalf("/a should resolve an allow, got %+v", ra)
	}
	if got := findRule(rb, "Bash", "go test*"); got == nil || got.Effect != governance.Deny {
		t.Fatalf("/b should resolve a deny, got %+v", rb)
	}
}

// Conventional OFF (and no explicit files) → New returns a nil resolver: nothing
// is discovered.
func TestResolveConventionalOffYieldsNilResolver(t *testing.T) {
	if r := newWithEnv(Options{Conventional: false}, fakeEnv()); r != nil {
		t.Fatalf("expected a nil resolver when nothing is configured, got %#v", r)
	}
}

// A nil workspace yields only the user-global rules (no project to read).
func TestResolveNilWorkspace(t *testing.T) {
	env := fakeEnv()
	env.ReadFile = func(_ string) ([]byte, error) {
		return []byte("permissions:\n  deny:\n    - \"Bash(curl:*)\"\n"), nil
	}
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/perms.yaml"}}, env)
	if r == nil {
		t.Fatal("explicit files should produce a non-nil resolver")
	}
	rules := r.Resolve(context.Background(), nil)
	if got := findRule(rules, "Bash", "curl*"); got == nil || got.Scope != governance.ScopeCLI {
		t.Fatalf("explicit-file rule should resolve at ScopeCLI even with a nil ws: %+v", rules)
	}
}

// Scope assignment per tier (issue #13): explicit (--permission-config) → ScopeCLI;
// .mecatl/settings.local.yaml → ScopeLocalProject; .mecatl/settings.yaml →
// ScopeSharedProject; user-global → ScopeUser.
func TestResolveScopeAssignmentPerTier(t *testing.T) {
	env := fakeEnv()
	env.ReadFile = func(_ string) ([]byte, error) {
		return []byte("permissions:\n  deny:\n    - \"Bash(curl:*)\"\n"), nil
	}
	r := newWithEnv(Options{
		Conventional:  true,
		TrustProject:  true,
		ExplicitFiles: []string{"/etc/mecatl/perms.yaml"},
	}, env)
	ws := newProjectWS(t, "/repo", "permissions:\n  deny:\n    - \"Bash(rm:*)\"\n")
	ws.seed(t, projectFileMecatlLocal, "permissions:\n  deny:\n    - \"Bash(sudo:*)\"\n")

	rules := r.Resolve(context.Background(), ws)
	if got := findRule(rules, "Bash", "rm*"); got == nil || got.Scope != governance.ScopeSharedProject {
		t.Fatalf("shared project rule should be ScopeSharedProject: %+v", rules)
	}
	if got := findRule(rules, "Bash", "sudo*"); got == nil || got.Scope != governance.ScopeLocalProject {
		t.Fatalf("local project rule should be ScopeLocalProject: %+v", rules)
	}
	if got := findRule(rules, "Bash", "curl*"); got == nil || got.Scope != governance.ScopeCLI {
		t.Fatalf("explicit (CLI) rule should be ScopeCLI: %+v", rules)
	}
}

// Cache REVALIDATION (issue #13 fix #4): a deny added to the config mid-process
// takes effect on the NEXT Resolve — the per-root cache is invalidated by the
// file's mtime/size change, not held until restart.
func TestResolveCacheRevalidatesOnEdit(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	ws := newProjectWS(t, "/repo", "permissions:\n  allow:\n    - \"Bash(go test:*)\"\n")

	first := r.Resolve(context.Background(), ws)
	if findRule(first, "Bash", "rm*") != nil {
		t.Fatalf("rm deny should not exist yet: %+v", first)
	}
	// Edit the config: add a deny. memfs stamps a fresh modTime + a new size on
	// Write, so the cached entry's fingerprint no longer matches.
	ws.seed(t, projectFileMecatl, "permissions:\n  allow:\n    - \"Bash(go test:*)\"\n  deny:\n    - \"Bash(rm:*)\"\n")

	second := r.Resolve(context.Background(), ws)
	if findRule(second, "Bash", "rm*") == nil {
		t.Fatalf("the newly-added deny must take effect on the next Resolve (cache went stale): %+v", second)
	}
}

// Cache MISS on a DISTINCT root: a second root must trigger its own Read (a bug
// that collapsed the cache key to a constant would skip this and fail).
func TestResolveCacheMissOnDistinctRoot(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	wsA := newProjectWS(t, "/a", "permissions:\n  deny:\n    - \"Bash(a:*)\"\n")
	wsB := newProjectWS(t, "/b", "permissions:\n  deny:\n    - \"Bash(b:*)\"\n")

	_ = r.Resolve(context.Background(), wsA)
	if wsB.readCount() != 0 {
		t.Fatalf("ws-b should not have been read yet, got %d", wsB.readCount())
	}
	_ = r.Resolve(context.Background(), wsB)
	if wsB.readCount() == 0 {
		t.Fatal("ws-b should have been read on its first (distinct-root) Resolve — cache key collapsed?")
	}
}

// Concurrent Resolve across goroutines (and two roots) so `go test -race`
// exercises the RWMutex around the per-root cache. A dropped write-lock would be
// caught by the race detector here.
func TestResolveConcurrent(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	wsA := newProjectWS(t, "/a", "permissions:\n  deny:\n    - \"Bash(a:*)\"\n")
	wsB := newProjectWS(t, "/b", "permissions:\n  deny:\n    - \"Bash(b:*)\"\n")

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ws := wsA
			want := "a*"
			if i%2 == 0 {
				ws, want = wsB, "b*"
			}
			rules := r.Resolve(context.Background(), ws)
			if findRule(rules, "Bash", want) == nil {
				t.Errorf("concurrent Resolve missing rule %q: %+v", want, rules)
			}
		}(i)
	}
	wg.Wait()
}

// Partial-malformed fail-soft (issue #13 QA): a BAD shared .mecatl/settings.yaml
// must NOT suppress a GOOD .claude/settings.json under the same root — each file
// is parsed independently and a bad one is skipped (warned), not fatal.
func TestResolvePartialMalformedFailSoft(t *testing.T) {
	r := newWithEnv(Options{Conventional: true, ImportClaude: true, TrustProject: true}, fakeEnv())
	ws := newProjectWS(t, "/repo", "permissions: [this is: not: valid")
	ws.seed(t, projectFileClaude, `{"permissions":{"deny":["Bash(rm:*)"]}}`)

	rules := r.Resolve(context.Background(), ws)
	if findRule(rules, "Bash", "rm*") == nil {
		t.Fatalf("the good Claude file must still load despite the bad YAML sibling: %+v", rules)
	}
}

// TestResolveEmitsFailSafeWarnThroughInjectedSink pins BOTH the diagnostics wiring
// (the injected port.Diagnostics is actually consulted — the very thing the soul-gate
// site was caught NOT doing) AND the level (a silent WARN→Info downgrade fails here).
// It feeds an unparseable project YAML and asserts the fail-safe "skipping" line
// reaches the injected sink at WARN.
func TestResolveEmitsFailSafeWarnThroughInjectedSink(t *testing.T) {
	var buf bytes.Buffer
	// minLevel=Info so an accidental WARN→Info downgrade would STILL be captured;
	// the assertion then pins level=WARN, so the downgrade fails the test.
	diag := slogdiag.New(&buf, false, port.LevelInfo)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	ws := newProjectWS(t, "/repo", "permissions: [this is: not: valid")

	_ = r.Resolve(context.Background(), ws)

	out := buf.String()
	if !strings.Contains(out, "project YAML invalid; skipping") {
		t.Fatalf("fail-safe parse-skip line did not reach the injected sink; got: %s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("fail-safe parse-skip line must be WARN (no silent downgrade); got: %s", out)
	}
}

// TestGoccyYAMLMigration_Scenario5_PermissionReloadFailsSafeWithoutPartialPolicy
// pins the fail-safe reload boundary: a strict nested-schema failure discards the
// complete file, reports preserved lost-rule counts, and never reflects YAML data.
func TestGoccyYAMLMigration_Scenario5_PermissionReloadFailsSafeWithoutPartialPolicy(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelInfo)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	const secret = "attacker-controlled-secret-should-not-leak"
	ws := newProjectWS(t, "/repo", "permissions:\n  deny: [Bash(rm:*)]\n  "+secret+": [credential-shaped-value]\n")

	if rules := r.Resolve(context.Background(), ws); len(rules) != 0 {
		t.Fatalf("strict-invalid config applied partial policy: %+v", rules)
	}

	out := buf.String()
	if !strings.Contains(out, "project YAML invalid; skipping") || !strings.Contains(out, "lost_deny=1") || !strings.Contains(out, "counts_known=true") {
		t.Fatalf("reload warning = %q, want value-free skip with known lost count", out)
	}
	for _, forbidden := range []string{secret, "credential-shaped-value"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("reload warning leaked YAML-derived content %q: %q", forbidden, out)
		}
	}
}

func TestGoccyYAMLMigration_Scenario6_SourceMatrixSafeErrorsAndLogAttributes(t *testing.T) {
	const yamlSecret = "yaml-content-secret-should-not-leak"
	const explicitPath = "/operator/token=path-identifier-is-permitted.yaml"
	const userConfigDir = "/user/token=path-identifier-is-permitted"
	config := "permissions:\n  deny: [Read]\n  allow: [\"Bash(" + yamlSecret + "\"]\nposture: " + yamlSecret + "\n"

	for _, tc := range []struct {
		name      string
		new       func(port.Diagnostics) (*Resolver, *countingWS)
		wantScope governance.Scope
	}{
		{
			name: "explicit CLI file",
			new: func(diag port.Diagnostics) (*Resolver, *countingWS) {
				env := fakeEnv()
				env.ReadFile = func(string) ([]byte, error) { return []byte(config), nil }
				return newWithEnv(Options{ExplicitFiles: []string{explicitPath}, Diagnostics: diag}, env), nil
			},
			wantScope: governance.ScopeCLI,
		},
		{
			name: "user-global XDG settings",
			new: func(diag port.Diagnostics) (*Resolver, *countingWS) {
				env := fakeEnv()
				env.Getenv = func(string) string { return userConfigDir }
				env.ReadFile = func(string) ([]byte, error) { return []byte(config), nil }
				return newWithEnv(Options{Conventional: true, Diagnostics: diag}, env), nil
			},
			wantScope: governance.ScopeUser,
		},
		{
			name: "shared-project settings",
			new: func(diag port.Diagnostics) (*Resolver, *countingWS) {
				r := newWithEnv(Options{Conventional: true, Diagnostics: diag}, fakeEnv())
				return r, newProjectWS(t, "/repo", config)
			},
			wantScope: governance.ScopeSharedProject,
		},
		{
			name: "local-project settings",
			new: func(diag port.Diagnostics) (*Resolver, *countingWS) {
				r := newWithEnv(Options{Conventional: true, Diagnostics: diag}, fakeEnv())
				ws := newProjectWS(t, "/repo", "")
				ws.seed(t, projectFileMecatlLocal, config)
				return r, ws
			},
			wantScope: governance.ScopeLocalProject,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			diag := slogdiag.New(&buf, false, port.LevelInfo)
			r, ws := tc.new(diag)
			if r == nil {
				t.Fatal("resolver is nil")
			}

			var rules []governance.Rule
			if ws == nil {
				rules = r.Resolve(context.Background(), nil)
			} else {
				rules = r.Resolve(context.Background(), ws)
			}
			if got := findRule(rules, "Read", ""); got == nil || got.Scope != tc.wantScope || got.Effect != governance.Deny {
				t.Fatalf("rules = %#v, want the %v deny from this source", rules, tc.wantScope)
			}

			out := buf.String()
			if strings.Contains(out, yamlSecret) {
				t.Fatalf("diagnostics leaked YAML-derived content %q: %q", yamlSecret, out)
			}
		})
	}
}

func TestGoccyYAMLMigration_Scenario6_SourceTierTrustAndOperatorOnlyMatrix(t *testing.T) {
	const configDir = "/config"
	const explicitPath = "/operator/permissions.yaml"
	userPath := filepath.Join(configDir, userSubdirMecatl)
	env := fakeEnv()
	env.Getenv = func(string) string { return configDir }
	env.ReadFile = func(path string) ([]byte, error) {
		switch path {
		case explicitPath:
			return []byte("permissions:\n  deny: [Bash(cli-deny:*)]\n  allow: [Read]\nposture: strict\n"), nil
		case userPath:
			return []byte("permissions:\n  deny: [Bash(user-deny:*)]\n  allow: [Grep]\nposture: yolo\n"), nil
		default:
			return nil, errors.New("not found")
		}
	}

	r := newWithEnv(Options{Conventional: true, ExplicitFiles: []string{explicitPath}}, env)
	ws := newProjectWS(t, "/repo", "permissions:\n  deny: [Bash(shared-deny:*)]\n  allow: [Write]\nposture: yolo\n")
	ws.seed(t, projectFileMecatlLocal, "permissions:\n  deny: [Bash(local-deny:*)]\n  allow: [Edit]\nposture: yolo\n")

	rules := r.Resolve(context.Background(), ws)
	for _, want := range []struct {
		tool    string
		pattern string
		scope   governance.Scope
	}{
		{"Bash", "cli-deny*", governance.ScopeCLI},
		{"Bash", "user-deny*", governance.ScopeUser},
		{"Bash", "shared-deny*", governance.ScopeSharedProject},
		{"Bash", "local-deny*", governance.ScopeLocalProject},
	} {
		got := findRule(rules, want.tool, want.pattern)
		if got == nil || got.Effect != governance.Deny || got.Scope != want.scope {
			t.Fatalf("rules = %#v, want deny %#v", rules, want)
		}
	}
	for _, toolName := range []string{"Read", "Grep"} {
		if got := findRule(rules, toolName, ""); got == nil || got.Effect != governance.Allow {
			t.Fatalf("operator allow %q missing from %#v", toolName, rules)
		}
	}
	for _, toolName := range []string{"Write", "Edit"} {
		if got := findRule(rules, toolName, ""); got != nil {
			t.Fatalf("untrusted project allow %q was honoured: %#v", toolName, rules)
		}
	}
	if got := r.OperatorPosture(); got != "strict" {
		t.Fatalf("operator posture = %q, want CLI posture %q; project values must be ignored", got, "strict")
	}
}

// TestGoccyYAMLMigration_Scenario6_CredentialShapedPathnameIsPermittedButContentIsNot
// pins the diagnostic boundary: a source pathname may identify the file, but a
// credential-shaped YAML scalar must not cross into its warning.
func TestGoccyYAMLMigration_Scenario6_CredentialShapedPathnameIsPermittedButContentIsNot(t *testing.T) {
	const path = "/operator/token=path-identifier-is-permitted.yaml"
	const secret = "token=yaml-content-must-not-leak"
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelInfo)
	env := fakeEnv()
	env.ReadFile = func(string) ([]byte, error) {
		return []byte("permissions: [" + secret), nil
	}
	_ = newWithEnv(Options{ExplicitFiles: []string{path}, Diagnostics: diag}, env)

	out := buf.String()
	if !strings.Contains(out, path) {
		t.Fatalf("diagnostics = %q, want supplied source path %q", out, path)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("diagnostics leaked YAML-derived content %q: %q", secret, out)
	}
}

// Compile-time: a *countingWS is a tool.Workspace (so the resolver accepts it).
var _ tool.Workspace = (*countingWS)(nil)
