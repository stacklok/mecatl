package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// permissionModeCfg is the offline Build fixture for the permission-mode tests: an
// explicit --permission-mode token over the mock provider.
func permissionModeCfg(t *testing.T, token string, diag port.Diagnostics) Config {
	t.Helper()
	return Config{
		Workspace:             t.TempDir(),
		Model:                 "mock",
		UseMock:               true,
		NoSoul:                true,
		PermissionMode:        token,
		PermissionModeFlagSet: true,
		Diagnostics:           diag,
	}
}

// builtDefaultSessionMode reports the mode a CreateSession that leaves mode
// unspecified starts in: the session half of the token.
func builtDefaultSessionMode(t *testing.T, built *Built) session.PermissionMode {
	t.Helper()
	sess, err := built.Service.CreateSession(context.Background(), "", defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess.Mode
}

func TestTokenTableResolvesExactPairs(t *testing.T) {
	want := map[string]struct {
		posture Posture
		mode    session.PermissionMode
	}{
		"plan":         {PostureStrict, session.ModePlan},
		"default":      {PostureStrict, session.ModeDefault},
		"accept-edits": {PostureStrict, session.ModeAccept},
		"auto":         {PostureAuto, session.ModeAuto},
		"yolo":         {PostureYolo, session.ModeYolo},
	}
	if got := PermissionModeNames(); len(got) != 5 {
		t.Fatalf("token set = %v, want exactly the 5 tokens", got)
	}
	for name, pair := range want {
		tok, err := ParsePermissionMode(name)
		if err != nil {
			t.Fatalf("ParsePermissionMode(%q): %v", name, err)
		}
		if tok.Posture != pair.posture || tok.SessionMode != pair.mode {
			t.Errorf("%s resolves to (%s, %s), want (%s, %s)", name, tok.Posture, tok.SessionMode, pair.posture, pair.mode)
		}

		// Through the real Build: the posture echo and the session default.
		cfg := permissionModeCfg(t, name, port.NopDiagnostics{})
		cfg.GuardrailsDisabled = pair.posture >= PostureAuto
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build %s: %v", name, err)
		}
		if got := postureEchoFromBuild(t, built); got != pair.posture.String() {
			t.Errorf("%s: echoed posture %q, want %q", name, got, pair.posture)
		}
		if got := builtDefaultSessionMode(t, built); got != pair.mode {
			t.Errorf("%s: unspecified-mode session started in %q, want %q", name, got, pair.mode)
		}
		// The ceiling: every strict token admits all three strict modes, and no
		// token admits a mode above its own allow-all tier.
		for _, mode := range []session.PermissionMode{session.ModePlan, session.ModeDefault, session.ModeAccept, session.ModeYolo} {
			_, err := built.Service.CreateSession(context.Background(), mode, defaultLimits())
			if wantOK := mode != session.ModeYolo || pair.posture == PostureYolo; (err == nil) != wantOK {
				t.Errorf("%s: CreateSession(%s) err = %v, want admitted=%v", name, mode, err, wantOK)
			}
		}
		built.Close()
	}

	_, err := ParsePermissionMode("autonomous")
	if err == nil {
		t.Fatal("an unknown token must be an error")
	}
	for _, name := range PermissionModeNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("unknown-token error must name the valid set; missing %q in %v", name, err)
		}
	}
	if _, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "autonomous", port.NopDiagnostics{})); err == nil || !strings.Contains(err.Error(), "accept-edits") {
		t.Fatalf("Build with an unknown token must fail naming the valid set; got %v", err)
	}
}

func TestTrustedKeepsCurrentMeaning(t *testing.T) {
	// The deprecated --posture trusted still resolves to PostureTrusted.
	legacy := Config{Workspace: t.TempDir(), Model: "mock", UseMock: true, NoSoul: true, Posture: PostureTrusted, PostureFlagSet: true}
	built, err := buildIsolated(t, context.Background(), legacy)
	if err != nil {
		t.Fatalf("--posture trusted: Build: %v", err)
	}
	defer built.Close()
	if got := postureEchoFromBuild(t, built); got != "trusted" {
		t.Errorf("--posture trusted: posture %q, want trusted", got)
	}
	if got := builtDefaultSessionMode(t, built); got != session.ModeDefault {
		t.Errorf("--posture trusted: session mode %q, want default", got)
	}
}

func TestAllowAllTokenRequiresChecker(t *testing.T) {
	// The checker gate is now a runtime WARN, not a startup refusal.
	// Build succeeds; entering auto mode without a checker is refused at SetMode time.
	for _, token := range []string{"auto", "yolo"} {
		rec := slogdiagBuffer(t)
		cfg := permissionModeCfg(t, token, rec.diag)
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("%s with no checker must still start (checker gate is now a WARN): %v", token, err)
		}
		built.Close()
		if !strings.Contains(rec.String(), "no guardrails checker") {
			t.Errorf("%s without checker must WARN about missing checker; log:\n%s", token, rec.String())
		}
		// Either declared choice keeps the WARN from appearing.
		off := permissionModeCfg(t, token, port.NopDiagnostics{})
		off.GuardrailsDisabled = true
		built2, err := buildIsolated(t, context.Background(), off)
		if err != nil {
			t.Fatalf("%s with --guardrails off must start: %v", token, err)
		}
		built2.Close()
		checked := permissionModeCfg(t, token, port.NopDiagnostics{})
		checked.GuardrailsModel = "mock-checker"
		built3, err := buildIsolated(t, context.Background(), checked)
		if err != nil {
			t.Fatalf("%s with a configured checker must start: %v", token, err)
		}
		built3.Close()
	}
}

func TestCheckerRefusalNamesEveryFix(t *testing.T) {
	// Checker absence is now a startup WARN (not a refusal); the WARN must name every fix.
	rec := slogdiagBuffer(t)
	cfg := permissionModeCfg(t, "auto", rec.diag)
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("auto with no checker must start (WARN only); got error: %v", err)
	}
	built.Close()
	for _, want := range []string{"no guardrails checker", "--guardrails-model", "`guardrail` model slot", "--guardrails off"} {
		if !strings.Contains(rec.String(), want) {
			t.Errorf("checker-absent WARN must name %q; log:\n%s", want, rec.String())
		}
	}
}

func TestGateFreeTokenStatesCheckerIsAdvisoryOnly(t *testing.T) {
	properties := []string{"pre-tool veto", "approve-once human ask", "fail-closed"}

	rec := slogdiagBuffer(t)
	cfg := permissionModeCfg(t, "yolo", rec.diag)
	cfg.GuardrailsModel = "mock-checker"
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	line := rec.lineContaining(`msg="permission mode"`)
	if !strings.Contains(line, "guardrails_checker=advisory") {
		t.Fatalf("yolo startup line must report the checker as advisory; line: %q", line)
	}
	for _, p := range properties {
		if !strings.Contains(line, p) {
			t.Errorf("yolo startup line must name %q; line: %q", p, line)
		}
	}
}

func TestNonAllowAllTokensRequireNoChecker(t *testing.T) {
	for _, token := range []string{"plan", "default", "accept-edits"} {
		built, err := buildIsolated(t, context.Background(), permissionModeCfg(t, token, port.NopDiagnostics{}))
		if err != nil {
			t.Fatalf("%s must start with no checker: %v", token, err)
		}
		built.Close()
	}
}

func TestStartupLineNamesCheckerState(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"enforcing", Config{Posture: PostureAuto, GuardrailsModel: "m"}, "enforcing"},
		{"advisory at yolo", Config{Posture: PostureYolo, MaxAllowedMode: session.ModeYolo, GuardrailsModel: "m"}, "advisory"},
		{"kill-switch", Config{Posture: PostureAuto, GuardrailsDisabled: true}, "disabled"},
		{"none at strict", Config{}, "disabled"},
	}
	for _, tc := range cases {
		configured := tc.cfg.GuardrailsModel != ""
		if got, _ := checkerState(tc.cfg, configured); got != tc.want {
			t.Errorf("%s: checker state %q, want %q", tc.name, got, tc.want)
		}
	}

	rec := slogdiagBuffer(t)
	cfg := permissionModeCfg(t, "auto", rec.diag)
	cfg.GuardrailsModel = "mock-checker"
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	if line := rec.lineContaining(`msg="permission mode"`); !strings.Contains(line, "guardrails_checker=enforcing") {
		t.Fatalf("an auto deployment with a checker must report it enforcing; line: %q", line)
	}
}

// headlessCfg is a headless root with no trust source.
func headlessCfg(t *testing.T, token string, diag port.Diagnostics) Config {
	t.Helper()
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
	cfg := permissionModeCfg(t, token, diag)
	cfg.Headless = true
	return cfg
}

func TestHeadlessTrustTokenRefused(t *testing.T) {
	// The trust-raising posture (PostureTrusted) on a headless root with no trust
	// source is refused. Reachable via the deprecated --posture trusted flag.
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
	cfg := Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		UseMock:        true,
		NoSoul:         true,
		Headless:       true,
		Posture:        PostureTrusted,
		PostureFlagSet: true,
		Diagnostics:    port.NopDiagnostics{},
	}
	_, err := buildIsolated(t, context.Background(), cfg)
	if err == nil {
		t.Fatal("--posture trusted on a headless root with no trust source must refuse")
	}
	for _, want := range []string{"--trust-project", "trustedWorkspaces", "default or auto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q; got: %v", want, err)
		}
	}
}

func TestHeadlessTrustTokenAcceptsEveryTrustSource(t *testing.T) {
	baseHeadlessTrusted := func(t *testing.T) Config {
		t.Helper()
		withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
		return Config{
			Workspace:      t.TempDir(),
			Model:          "mock",
			UseMock:        true,
			NoSoul:         true,
			Headless:       true,
			Posture:        PostureTrusted,
			PostureFlagSet: true,
			Diagnostics:    port.NopDiagnostics{},
		}
	}
	t.Run("explicit --trust-project", func(t *testing.T) {
		cfg := baseHeadlessTrusted(t)
		cfg.TrustProject = true
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("explicit trust must satisfy the gate: %v", err)
		}
		built.Close()
	})
	t.Run("declared trustedWorkspaces", func(t *testing.T) {
		ws := t.TempDir()
		withTrustEnv(t, trustSettingsEnv(t.TempDir(), []byte("trustedWorkspaces:\n  - "+ws+"\n")))
		cfg := Config{
			Workspace:      ws,
			Model:          "mock",
			UseMock:        true,
			NoSoul:         true,
			Headless:       true,
			Posture:        PostureTrusted,
			PostureFlagSet: true,
			Diagnostics:    port.NopDiagnostics{},
		}
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("declared trust must satisfy the gate: %v", err)
		}
		built.Close()
	})
}

func TestHeadlessAllowAllStartsWithoutTrust(t *testing.T) {
	for _, token := range []string{"auto", "yolo"} {
		rec := slogdiagBuffer(t)
		cfg := headlessCfg(t, token, rec.diag)
		cfg.GuardrailsDisabled = true
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("%s on a headless root must start: %v", token, err)
		}
		built.Close()
		if line := rec.lineContaining(`msg="operator posture"`); !strings.Contains(line, "project_ingestion=false") {
			t.Errorf("%s: headless without trust must admit no project ingestion; line: %q", token, line)
		}
		if !strings.Contains(rec.String(), "project trust WITHHELD") {
			t.Errorf("%s: headless allow-all without trust must WARN naming the withheld trust; log:\n%s", token, rec.String())
		}
	}
}

func TestInteractiveTrustTokenGrantsTrust(t *testing.T) {
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
	rec := slogdiagBuffer(t)
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		UseMock:        true,
		NoSoul:         true,
		Posture:        PostureTrusted,
		PostureFlagSet: true,
		Diagnostics:    rec.diag,
	})
	if err != nil {
		t.Fatalf("interactive trusted must start: %v", err)
	}
	defer built.Close()
	if line := rec.lineContaining(`msg="operator posture"`); !strings.Contains(line, "trust_project=true") {
		t.Errorf("interactive trusted must grant project trust; line: %q", line)
	}
	if strings.Contains(rec.String(), "WITHHELD") {
		t.Errorf("interactive trusted must not WARN about withheld trust; log:\n%s", rec.String())
	}
}

func TestAllowAllNarratesSubagentAsymmetry(t *testing.T) {
	rec := slogdiagBuffer(t)
	cfg := permissionModeCfg(t, "auto", rec.diag)
	cfg.GuardrailsDisabled = true
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built.Close()
	line := rec.lineContaining("KEEP the command-substitution guard")
	if line == "" || !strings.Contains(line, "--subagent-ask-reviewer") {
		t.Fatalf("auto must narrate the subagent asymmetry and name the reviewer; log:\n%s", rec.String())
	}
}

// headlessAllowAll is a headless auto deployment with the checker declared off.
func headlessAllowAll(t *testing.T, diag port.Diagnostics) Config {
	t.Helper()
	cfg := headlessCfg(t, "auto", diag)
	cfg.GuardrailsDisabled = true
	return cfg
}

func TestHeadlessAllowAllDefaultsReviewerOn(t *testing.T) {
	rec := slogdiagBuffer(t)
	built, err := buildIsolated(t, context.Background(), headlessAllowAll(t, rec.diag))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built.Close()
	if !strings.Contains(rec.String(), "subagent ask reviewer ON by default") {
		t.Fatalf("headless allow-all must engage the reviewer by default; log:\n%s", rec.String())
	}

	// The model resolves through the existing slot, then the parent model.
	cfg := Config{Posture: PostureAuto, MaxAllowedMode: session.ModeAuto, Model: "parent-model", Diagnostics: port.NopDiagnostics{}}
	cfg = resolveAskReviewerDefault(cfg)
	if !cfg.askReviewerDefaultOn {
		t.Fatal("resolveAskReviewerDefault must engage the reviewer")
	}
	deps, ok := askAdjudicatorDeps(cfg, nil, nil, "", "parent-model")
	if !ok || deps.Model != "parent-model" {
		t.Fatalf("default reviewer must fall back to the parent model; ok=%t model=%q", ok, deps.Model)
	}
	cfg.ModelSlots = map[string]string{slotAskReviewer: "reviewer-model"}
	deps, ok = askAdjudicatorDeps(cfg, nil, nil, "", "parent-model")
	if !ok || deps.Model != "reviewer-model" {
		t.Fatalf("default reviewer must resolve through the ask-reviewer slot; ok=%t model=%q", ok, deps.Model)
	}
}

func TestReviewerDefaultIsOptOutAndNarrated(t *testing.T) {
	rec := slogdiagBuffer(t)
	built, err := buildIsolated(t, context.Background(), headlessAllowAll(t, rec.diag))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built.Close()
	on := rec.lineContaining("subagent ask reviewer ON by default")
	for _, want := range []string{"a model may approve", "spends tokens", "--subagent-ask-reviewer off"} {
		if !strings.Contains(on, want) {
			t.Errorf("default-on narration must say %q; line: %q", want, on)
		}
	}

	rec = slogdiagBuffer(t)
	cfg := headlessAllowAll(t, rec.diag)
	cfg.SubagentAskReviewerModel = "off"
	built, err = buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build with --subagent-ask-reviewer off: %v", err)
	}
	built.Close()
	if strings.Contains(rec.String(), "ON by default") {
		t.Fatalf("the opt-out must restore today's behaviour; log:\n%s", rec.String())
	}
	if line := rec.lineContaining(`msg="permission mode"`); !strings.Contains(line, "off (--subagent-ask-reviewer off)") {
		t.Fatalf("the startup line must state the opt-out is in effect; line: %q", line)
	}
}

func TestReviewerDefaultDegradesToTodayBehaviour(t *testing.T) {
	rec := slogdiagBuffer(t)
	cfg := resolveAskReviewerDefault(Config{Posture: PostureAuto, MaxAllowedMode: session.ModeAuto, Diagnostics: rec.diag})
	if cfg.askReviewerDefaultOn {
		t.Fatal("with no resolvable reviewer model the default must stay off")
	}
	if _, ok := askAdjudicatorDeps(cfg, nil, nil, "", ""); ok {
		t.Fatal("no reviewer must be built, so behaviour is today's denial")
	}
	if !strings.Contains(rec.String(), "--subagent-ask-reviewer <model>") {
		t.Fatalf("a WARN must name the fix; log:\n%s", rec.String())
	}
}

func TestReviewerDefaultIsConfinedToHeadlessAllowAll(t *testing.T) {
	for name, cfg := range map[string]Config{
		"interactive auto": {Posture: PostureAuto, MaxAllowedMode: session.ModeAuto, Interactive: true, Model: "m"},
		"headless strict":  {Posture: PostureStrict, Model: "m"},
		"headless trusted": {Posture: PostureTrusted, Model: "m"},
	} {
		cfg.Diagnostics = port.NopDiagnostics{}
		if resolveAskReviewerDefault(cfg).askReviewerDefaultOn {
			t.Errorf("%s must not engage the reviewer default", name)
		}
	}
}

func TestReviewerAndCheckerNarratedSeparately(t *testing.T) {
	rec := slogdiagBuffer(t)
	cfg := headlessCfg(t, "auto", rec.diag)
	cfg.GuardrailsModel = "mock-checker"
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built.Close()
	line := rec.lineContaining(`msg="permission mode"`)
	if !strings.Contains(line, "guardrails_checker=") || !strings.Contains(line, "subagent_ask_reviewer=") {
		t.Fatalf("the startup line must carry the checker and the reviewer as separate items; line: %q", line)
	}
	on := strings.ToLower(rec.lineContaining("subagent ask reviewer ON by default"))
	if strings.Contains(on, "guardrail") || strings.Contains(on, "checker") {
		t.Errorf("the reviewer narration must not describe itself in the checker's terms; line: %q", on)
	}
	for _, s := range []string{"enforcing", "advisory", "disabled"} {
		if strings.Contains(s, "review") {
			t.Errorf("checker state %q must not describe itself in the reviewer's terms", s)
		}
	}
}

func TestStartupLineReportsTokenBothHalvesCheckerAndReviewer(t *testing.T) {
	rec := slogdiagBuffer(t)
	cfg := permissionModeCfg(t, "auto", rec.diag)
	cfg.GuardrailsModel = "mock-checker"
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built.Close()
	line := rec.lineContaining(`msg="permission mode"`)
	for _, want := range []string{
		"permission_mode=auto",
		"posture=auto",
		"session_mode=auto",
		"process-wide",
		"a session may change it",
		"guardrails_checker=enforcing",
		"subagent_ask_reviewer=",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("startup line must carry %q; line: %q", want, line)
		}
	}
	if n := strings.Count(rec.String(), `msg="permission mode"`); n != 1 {
		t.Errorf("the permission-mode line must be emitted exactly once per Build, got %d", n)
	}
}

func TestPermissionModeIsOperatorTierOnly(t *testing.T) {
	operatorFile := func(t *testing.T, token string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "operator.yaml")
		if err := os.WriteFile(path, []byte("permissionMode: "+token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := func(t *testing.T) Config {
		return Config{Workspace: t.TempDir(), Model: "mock", UseMock: true, NoSoul: true, Diagnostics: port.NopDiagnostics{}}
	}

	t.Run("CLI tier honoured", func(t *testing.T) {
		cfg := base(t)
		cfg.PermissionConfigs = []string{operatorFile(t, "accept-edits")}
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if got := postureEchoFromBuild(t, built); got != "strict" {
			t.Errorf("posture %q, want strict", got)
		}
		if got := builtDefaultSessionMode(t, built); got != session.ModeAccept {
			t.Errorf("session mode %q, want acceptEdits", got)
		}
	})
	t.Run("user-global tier honoured", func(t *testing.T) {
		env := trustSettingsEnv(t.TempDir(), []byte("permissionMode: plan\n"))
		withTrustEnv(t, env)
		cfg := base(t)
		cfg.PermissionsConventional = true
		cfg.permConfigEnv = &env
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if got := builtDefaultSessionMode(t, built); got != session.ModePlan {
			t.Errorf("session mode %q, want plan", got)
		}
	})
	t.Run("explicit flag outranks the key", func(t *testing.T) {
		cfg := permissionModeCfg(t, "plan", port.NopDiagnostics{})
		cfg.PermissionConfigs = []string{operatorFile(t, "default")}
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if got := postureEchoFromBuild(t, built); got != "strict" {
			t.Errorf("posture %q, want strict from the flag", got)
		}
		if got := builtDefaultSessionMode(t, built); got != session.ModePlan {
			t.Errorf("session mode %q, want plan from the flag", got)
		}
	})
	t.Run("project tier ignored with a WARN", func(t *testing.T) {
		// Composed: a trusted workspace's project permissionMode: never becomes the
		// mode; only --trust-project's own trusted posture applies.
		cfg := base(t)
		cfg.TrustProject = true
		mkdirProjectSettings(t, cfg.Workspace, "permissionMode: yolo\n")
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if got := postureEchoFromBuild(t, built); got != "trusted" {
			t.Errorf("a project-tier permissionMode must be ignored; posture %q", got)
		}
		// The WARN fires where the project file is read: the resolver's lazy
		// per-root Resolve.
		isolateUserConfig(t)
		rec := slogdiagBuffer(t)
		ws := memfs.NewWorkspace("/repo")
		if err := ws.Write(context.Background(), ".mecatl/settings.yaml", []byte("permissionMode: yolo\n")); err != nil {
			t.Fatal(err)
		}
		res := permconfig.New(permconfig.Options{Conventional: true, TrustProject: true, Diagnostics: rec.diag})
		_ = res.Resolve(context.Background(), ws)
		if got := res.OperatorPermissionMode(); got != "" {
			t.Errorf("a project-tier permissionMode must not become the operator value; got %q", got)
		}
		if !strings.Contains(rec.String(), "IGNORING a project-tier permissionMode") {
			t.Errorf("a project-tier permissionMode must WARN; log:\n%s", rec.String())
		}
	})
}

// TestInvariant_NoWireOrDomainSurfaceChanged pins that:
//   - the five session mode wire strings are stable;
//   - the proto enum has exactly the six approved values (UNSPECIFIED, DEFAULT,
//     PLAN, ACCEPT_EDITS, AUTO, YOLO — AUTO and YOLO added with the unified
//     permission-mode architecture, intentional additive wire change);
//   - the token table only writes the five known session modes.
//
// `task api:check` covers the engine API surface itself.
func TestInvariant_NoWireOrDomainSurfaceChanged(t *testing.T) {
	modes := []session.PermissionMode{
		session.ModeDefault, session.ModePlan, session.ModeAccept,
		session.ModeAuto, session.ModeYolo,
	}
	want := []session.PermissionMode{"default", "plan", "acceptEdits", "auto", "yolo"}
	if !slices.Equal(modes, want) {
		t.Fatalf("session mode wire strings changed: %v", modes)
	}
	names := make([]string, 0, len(mecatlv1.PermissionMode_value))
	for name := range mecatlv1.PermissionMode_value {
		names = append(names, name)
	}
	slices.Sort(names)
	// AUTO and YOLO are intentional additive additions (no break: existing clients
	// receive unknown enum values and must ignore them per the proto compatibility
	// contract; they do not need to send or recognise them to stay correct).
	wantProto := []string{
		"PERMISSION_MODE_ACCEPT_EDITS",
		"PERMISSION_MODE_AUTO",
		"PERMISSION_MODE_DEFAULT",
		"PERMISSION_MODE_PLAN",
		"PERMISSION_MODE_UNSPECIFIED",
		"PERMISSION_MODE_YOLO",
	}
	if !slices.Equal(names, wantProto) {
		t.Fatalf("proto PermissionMode values = %v, want %v", names, wantProto)
	}
	knownModes := []session.PermissionMode{
		session.ModeDefault, session.ModePlan, session.ModeAccept,
		session.ModeAuto, session.ModeYolo,
	}
	for _, tok := range permissionModeTable {
		if !slices.Contains(knownModes, tok.SessionMode) {
			t.Errorf("token %s writes an unknown session mode %q", tok.Name, tok.SessionMode)
		}
	}
}

// TestAuthoritativePostureFoldsPermissionModeKey pins that the cmd
// fast paths (root refusal, pre-launch WARN) see an operator-YAML-only
// permissionMode: allow-all token, not only an explicit flag.
func TestAuthoritativePostureFoldsPermissionModeKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.yaml")
	if err := os.WriteFile(path, []byte("permissionMode: yolo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workspace: t.TempDir(), PermissionConfigs: []string{path}, Diagnostics: port.NopDiagnostics{}}
	if got := ResolveAuthoritativePosture(cfg); got != PostureYolo {
		t.Fatalf("authoritative posture = %s, want yolo from the permissionMode: key", got)
	}
	if PostureRefusalReason(ResolveAuthoritativePosture(cfg), true) == nil {
		t.Fatal("a YAML-only permissionMode: yolo must hit the root/no-sandbox refusal fast path")
	}
}
