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

// permissionModeCfg is the offline Build fixture for the ADR 0365 tests: an
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

func TestADR_0365_TokenTableResolvesExactPairs(t *testing.T) {
	want := map[string]struct {
		posture Posture
		mode    session.PermissionMode
	}{
		"plan":                 {PostureStrict, session.ModePlan},
		"default":              {PostureStrict, session.ModeDefault},
		"accept-edits":         {PostureStrict, session.ModeAccept},
		"trusted":              {PostureTrusted, session.ModeDefault},
		"trusted-accept-edits": {PostureTrusted, session.ModeAccept},
		"auto":                 {PostureAuto, session.ModeDefault},
		"yolo":                 {PostureYolo, session.ModeDefault},
	}
	if got := PermissionModeNames(); len(got) != len(want) {
		t.Fatalf("token set = %v, want exactly the %d ADR tokens", got, len(want))
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
	if _, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "autonomous", port.NopDiagnostics{})); err == nil || !strings.Contains(err.Error(), "trusted-accept-edits") {
		t.Fatalf("Build with an unknown token must fail naming the valid set; got %v", err)
	}
}

func TestADR_0365_TrustedKeepsCurrentMeaning(t *testing.T) {
	cfg := permissionModeCfg(t, "trusted", port.NopDiagnostics{})
	legacy := Config{Workspace: t.TempDir(), Model: "mock", UseMock: true, NoSoul: true, Posture: PostureTrusted, PostureFlagSet: true}
	for name, c := range map[string]Config{"token": cfg, "--posture trusted": legacy} {
		built, err := buildIsolated(t, context.Background(), c)
		if err != nil {
			t.Fatalf("%s: Build: %v", name, err)
		}
		if got := postureEchoFromBuild(t, built); got != "trusted" {
			t.Errorf("%s: posture %q, want trusted", name, got)
		}
		if got := builtDefaultSessionMode(t, built); got != session.ModeDefault {
			t.Errorf("%s: session mode %q, want default", name, got)
		}
		built.Close()
	}
}

func TestADR_0365_TrustedAcceptEditsIsNameable(t *testing.T) {
	built, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "trusted-accept-edits", port.NopDiagnostics{}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	if got := postureEchoFromBuild(t, built); got != "trusted" {
		t.Errorf("posture %q, want trusted", got)
	}
	if got := builtDefaultSessionMode(t, built); got != session.ModeAccept {
		t.Errorf("session mode %q, want acceptEdits", got)
	}
}

func TestADR_0365_AllowAllTokenRequiresChecker(t *testing.T) {
	for _, token := range []string{"auto", "yolo"} {
		_, err := buildIsolated(t, context.Background(), permissionModeCfg(t, token, port.NopDiagnostics{}))
		if err == nil {
			t.Fatalf("%s with no checker and no kill-switch must refuse to start", token)
		}
		// The deprecated surface reaches the same resolved posture and the same gate.
		legacy := Config{Workspace: t.TempDir(), Model: "mock", UseMock: true, NoSoul: true, AllowAllTools: true}
		if _, err := buildIsolated(t, context.Background(), legacy); err == nil {
			t.Fatal("--yolo with no checker must refuse to start too: the gate keys on the resolved posture")
		}
		// Either declared choice satisfies it.
		off := permissionModeCfg(t, token, port.NopDiagnostics{})
		off.GuardrailsDisabled = true
		built, err := buildIsolated(t, context.Background(), off)
		if err != nil {
			t.Fatalf("%s with --guardrails off must start: %v", token, err)
		}
		built.Close()
		checked := permissionModeCfg(t, token, port.NopDiagnostics{})
		checked.GuardrailsModel = "mock-checker"
		built, err = buildIsolated(t, context.Background(), checked)
		if err != nil {
			t.Fatalf("%s with a configured checker must start: %v", token, err)
		}
		built.Close()
	}
}

func TestADR_0365_CheckerRefusalNamesEveryFix(t *testing.T) {
	_, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "auto", port.NopDiagnostics{}))
	if err == nil {
		t.Fatal("auto with no checker must refuse")
	}
	for _, want := range []string{`"auto"`, "--guardrails-model", "`guardrail` model slot", "--guardrails off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q; got: %v", want, err)
		}
	}
}

func TestADR_0365_GateFreeTokenStatesCheckerIsAdvisoryOnly(t *testing.T) {
	properties := []string{"pre-tool veto", "approve-once human ask", "fail-closed"}

	_, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "yolo", port.NopDiagnostics{}))
	if err == nil {
		t.Fatal("yolo with no checker must refuse")
	}
	for _, p := range properties {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("yolo refusal must name %q as a property the checker lacks; got: %v", p, err)
		}
	}

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

func TestADR_0365_NonAllowAllTokensRequireNoChecker(t *testing.T) {
	for _, token := range []string{"plan", "default", "accept-edits", "trusted", "trusted-accept-edits"} {
		built, err := buildIsolated(t, context.Background(), permissionModeCfg(t, token, port.NopDiagnostics{}))
		if err != nil {
			t.Fatalf("%s must start with no checker: %v", token, err)
		}
		built.Close()
	}
}

func TestADR_0365_StartupLineNamesCheckerState(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"enforcing", Config{Posture: PostureAuto, GuardrailsModel: "m"}, "enforcing"},
		{"advisory at yolo", Config{Posture: PostureYolo, GuardrailsModel: "m"}, "advisory"},
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

func TestADR_0365_HeadlessTrustTokenRefused(t *testing.T) {
	for _, token := range []string{"trusted", "trusted-accept-edits"} {
		_, err := buildIsolated(t, context.Background(), headlessCfg(t, token, port.NopDiagnostics{}))
		if err == nil {
			t.Fatalf("%s on a headless root with no trust source must refuse", token)
		}
		for _, want := range []string{"--trust-project", "trustedWorkspaces", "default or auto"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s refusal must name %q; got: %v", token, want, err)
			}
		}
	}
}

func TestADR_0365_HeadlessTrustTokenAcceptsEveryTrustSource(t *testing.T) {
	t.Run("explicit --trust-project", func(t *testing.T) {
		cfg := headlessCfg(t, "trusted", port.NopDiagnostics{})
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
		cfg := permissionModeCfg(t, "trusted-accept-edits", port.NopDiagnostics{})
		cfg.Headless = true
		cfg.Workspace = ws
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("declared trust must satisfy the gate, because it runs after the trust fold: %v", err)
		}
		built.Close()
	})
}

func TestADR_0365_HeadlessAllowAllStartsWithoutTrust(t *testing.T) {
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

func TestADR_0365_InteractiveTrustTokenGrantsTrust(t *testing.T) {
	withTrustEnv(t, trustSettingsEnv(t.TempDir(), nil))
	rec := slogdiagBuffer(t)
	built, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "trusted", rec.diag))
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

func TestADR_0365_AllowAllNarratesSubagentAsymmetry(t *testing.T) {
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

func TestADR_0365_HeadlessAllowAllDefaultsReviewerOn(t *testing.T) {
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
	cfg := Config{Posture: PostureAuto, Model: "parent-model", Diagnostics: port.NopDiagnostics{}}
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

func TestADR_0365_ReviewerDefaultIsOptOutAndNarrated(t *testing.T) {
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

func TestADR_0365_ReviewerDefaultDegradesToTodayBehaviour(t *testing.T) {
	rec := slogdiagBuffer(t)
	cfg := resolveAskReviewerDefault(Config{Posture: PostureAuto, Diagnostics: rec.diag})
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

func TestADR_0365_ReviewerDefaultIsConfinedToHeadlessAllowAll(t *testing.T) {
	for name, cfg := range map[string]Config{
		"interactive auto": {Posture: PostureAuto, Interactive: true, Model: "m"},
		"headless strict":  {Posture: PostureStrict, Model: "m"},
		"headless trusted": {Posture: PostureTrusted, Model: "m"},
	} {
		cfg.Diagnostics = port.NopDiagnostics{}
		if resolveAskReviewerDefault(cfg).askReviewerDefaultOn {
			t.Errorf("%s must not engage the reviewer default", name)
		}
	}
}

func TestADR_0365_ReviewerAndCheckerNarratedSeparately(t *testing.T) {
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

func TestADR_0365_StartupLineReportsTokenBothHalvesCheckerAndReviewer(t *testing.T) {
	rec := slogdiagBuffer(t)
	built, err := buildIsolated(t, context.Background(), permissionModeCfg(t, "trusted-accept-edits", rec.diag))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	built.Close()
	line := rec.lineContaining(`msg="permission mode"`)
	for _, want := range []string{
		"permission_mode=trusted-accept-edits",
		"posture=trusted",
		"session_mode=acceptEdits",
		"process-wide",
		"a session may change it",
		"guardrails_checker=disabled",
		"subagent_ask_reviewer=off",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("startup line must carry %q; line: %q", want, line)
		}
	}
	if n := strings.Count(rec.String(), `msg="permission mode"`); n != 1 {
		t.Errorf("the permission-mode line must be emitted exactly once per Build, got %d", n)
	}
}

func TestADR_0365_PermissionModeIsOperatorTierOnly(t *testing.T) {
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
		cfg.PermissionConfigs = []string{operatorFile(t, "trusted-accept-edits")}
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if got := postureEchoFromBuild(t, built); got != "trusted" {
			t.Errorf("posture %q, want trusted", got)
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
		cfg.PermissionConfigs = []string{operatorFile(t, "trusted")}
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

// TestInvariant_NoWireOrDomainSurfaceChanged pins that the vocabulary added no
// session-selectable value (ADR 0365 decision 3): the domain keeps its three
// modes with their strings, the proto enum keeps its four values, and an old
// snapshot's mode string still round-trips. `task api:check` covers the engine
// API surface itself.
func TestInvariant_NoWireOrDomainSurfaceChanged(t *testing.T) {
	if got := []session.PermissionMode{session.ModeDefault, session.ModePlan, session.ModeAccept}; !slices.Equal(got, []session.PermissionMode{"default", "plan", "acceptEdits"}) {
		t.Fatalf("session modes changed: %v", got)
	}
	names := make([]string, 0, len(mecatlv1.PermissionMode_value))
	for name := range mecatlv1.PermissionMode_value {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"PERMISSION_MODE_ACCEPT_EDITS", "PERMISSION_MODE_DEFAULT", "PERMISSION_MODE_PLAN", "PERMISSION_MODE_UNSPECIFIED"}
	if !slices.Equal(names, want) {
		t.Fatalf("proto PermissionMode values = %v, want %v", names, want)
	}
	for _, tok := range permissionModeTable {
		if !slices.Contains([]session.PermissionMode{session.ModeDefault, session.ModePlan, session.ModeAccept}, tok.SessionMode) {
			t.Errorf("token %s writes a session mode outside the domain's three: %q", tok.Name, tok.SessionMode)
		}
	}
}
