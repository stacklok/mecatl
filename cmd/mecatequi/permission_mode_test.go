package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
)

func TestDeprecatedAliasesStillResolve(t *testing.T) {
	for _, tc := range []struct{ posture, token string }{
		{"strict", "default"}, {"auto", "auto"}, {"yolo", "yolo"},
	} {
		t.Run(tc.posture, func(t *testing.T) {
			alias, err := parseFlags([]string{"--prompt", "x", "--posture", tc.posture})
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			ac := appConfig(alias, port.NopDiagnostics{}, observability{})
			tok, err := app.ParsePermissionMode(tc.token)
			if err != nil {
				t.Fatal(err)
			}
			if got := app.ResolveAuthoritativePosture(ac); got != tok.Posture || ac.DefaultSessionMode != "" {
				t.Fatalf("--posture %s = (%s, %q), want the %q token's (%s, default)", tc.posture, got, ac.DefaultSessionMode, tc.token, tok.Posture)
			}
			var buf bytes.Buffer
			warnDeprecatedPermissionFlags(slogdiag.NewFromLogger(slog.New(slog.NewTextHandler(&buf, nil))), alias)
			if n := strings.Count(buf.String(), "DEPRECATED"); n != 1 || !strings.Contains(buf.String(), "--permission-mode "+tc.token) {
				t.Fatalf("--posture %s logged %d deprecation WARNs, want exactly one naming --permission-mode %s: %s", tc.posture, n, tc.token, buf.String())
			}
		})
	}
	f, err := parseFlags([]string{"--prompt", "x", "--permission-mode", "auto"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	var buf bytes.Buffer
	warnDeprecatedPermissionFlags(slogdiag.NewFromLogger(slog.New(slog.NewTextHandler(&buf, nil))), f)
	if buf.Len() != 0 {
		t.Fatalf("--permission-mode emitted a deprecation WARN: %s", buf.String())
	}
	if ac := appConfig(f, port.NopDiagnostics{}, observability{}); !ac.PermissionModeFlagSet || ac.PermissionMode != "auto" {
		t.Fatalf("appConfig did not carry --permission-mode: set=%v mode=%q", ac.PermissionModeFlagSet, ac.PermissionMode)
	}
}

func TestPermissionModeFlagConflictsAndUnknownTokens(t *testing.T) {
	_, err := parseFlags([]string{"--prompt", "x", "--permission-mode", "auto", "--posture", "auto"})
	if err == nil || !strings.Contains(err.Error(), "pass only --permission-mode") {
		t.Fatalf("--permission-mode with --posture err = %v, want a pass-one startup error", err)
	}
	_, err = parseFlags([]string{"--prompt", "x", "--permission-mode", "bogus"})
	if err == nil {
		t.Fatal("unknown --permission-mode token parsed without error")
	}
	for _, name := range app.PermissionModeNames() {
		if !strings.Contains(err.Error(), name) || !strings.Contains(permissionModeHelp, name) {
			t.Fatalf("unknown-token error %q or help does not name %q", err, name)
		}
	}
	if !strings.Contains(permissionModeHelp, "process-wide") || !strings.Contains(permissionModeHelp, "ceiling") {
		t.Fatal("--permission-mode help does not say which half is the process-wide ceiling")
	}
	if strings.Contains(permissionModeHelp, "MECATL_SANDBOX") {
		t.Fatal("--permission-mode help claims a root refusal mecatequi does not enforce")
	}
}

// actionFile is the subset of the composite action.yml the checker-choice test reads.
type actionFile struct {
	Inputs map[string]struct {
		Default string `json:"default"`
	} `json:"inputs"`
	Runs struct {
		Steps []struct {
			Name string            `json:"name"`
			Env  map[string]string `json:"env"`
			Run  string            `json:"run"`
		} `json:"steps"`
	} `json:"runs"`
}

type workflowFile struct {
	Jobs map[string]struct {
		Steps []struct {
			Uses string         `json:"uses"`
			With map[string]any `json:"with"`
		} `json:"steps"`
	} `json:"jobs"`
}

var inputRef = regexp.MustCompile(`^\$\{\{\s*inputs\.([a-z0-9-]+)\s*\}\}$`)

// runActionScript executes the action's real "Run mecatequi" bash body with
// the given inputs (defaults fill the rest) against a fake binary that records
// its argv, and returns that argv plus the script's combined output.
func runActionScript(t *testing.T, action actionFile, with map[string]string, env ...string) ([]string, string, error) {
	t.Helper()
	idx := slices.IndexFunc(action.Runs.Steps, func(s struct {
		Name string            `json:"name"`
		Env  map[string]string `json:"env"`
		Run  string            `json:"run"`
	}) bool {
		return s.Name == "Run mecatequi"
	})
	if idx < 0 {
		t.Fatal("action.yml has no \"Run mecatequi\" step")
	}
	step := action.Runs.Steps[idx]
	dir := t.TempDir()
	argsOut := filepath.Join(dir, "argv")
	// NUL-separated, so a multi-line value (the default instructions) stays one arg.
	fake := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$ARGS_OUT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "mecatequi"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(prompt, []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	overrides := map[string]string{
		"prompt-file": prompt,
		"workspace":   dir,
		"out-diff":    filepath.Join(dir, "p.patch"),
		"out-summary": filepath.Join(dir, "s.json"),
		"out-events":  filepath.Join(dir, "e.jsonl"),
	}
	for k, v := range with {
		overrides[k] = v
	}
	env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"RUNNER_TEMP=" + dir,
		"GITHUB_OUTPUT=" + filepath.Join(dir, "github_output"),
		"ARGS_OUT=" + argsOut,
	}, env...)
	for name, expr := range step.Env {
		m := inputRef.FindStringSubmatch(expr)
		if m == nil {
			t.Fatalf("step env %s=%q is not an inputs reference", name, expr)
		}
		input, ok := action.Inputs[m[1]]
		if !ok {
			t.Fatalf("step env %s references undeclared input %q", name, m[1])
		}
		value := input.Default
		if v, ok := overrides[m[1]]; ok {
			value = v
		}
		env = append(env, name+"="+value)
	}
	cmd := exec.Command("bash", "-c", step.Run)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(out), err
	}
	raw, rerr := os.ReadFile(argsOut)
	if rerr != nil {
		t.Fatalf("fake mecatequi was not invoked: %v\n%s", rerr, out)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"), string(out), nil
}

// hasPair reports whether argv carries flag immediately followed by value.
func hasPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// hasGuardrailsFlag reports whether argv carries any guardrails checker flag.
func hasGuardrailsFlag(argv []string) bool {
	return slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--guardrails") })
}

// loadAction parses the composite action, skipping when bash is unavailable.
func loadAction(t *testing.T) actionFile {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is required to execute the composite action body")
	}
	raw, err := os.ReadFile("../../.github/actions/mecatequi/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action actionFile
	if err := yaml.Unmarshal(raw, &action); err != nil {
		t.Fatalf("parse action.yml: %v", err)
	}
	return action
}

// buildFromActionArgv runs the action body with the given inputs against a fake
// binary, then feeds the argv it produced (plus extra flags, for example a
// mounted operator settings file) through the real CLI composition with the
// mock provider. The workspace input is pointed at a hermetic git repo.
func buildFromActionArgv(t *testing.T, action actionFile, with map[string]string, env []string, extra ...string) (*app.Built, error) {
	t.Helper()
	repo := t.TempDir()
	initTestRepo(t, repo)
	inputs := map[string]string{"workspace": repo}
	for k, v := range with {
		inputs[k] = v
	}
	argv, out, err := runActionScript(t, action, inputs, env...)
	if err != nil {
		t.Fatalf("action body failed: %v\n%s", err, out)
	}
	f, err := parseFlags(append(argv, extra...))
	if err != nil {
		t.Fatalf("parseFlags(%q): %v", argv, err)
	}
	cfg := appConfig(f, port.NopDiagnostics{}, observability{})
	cfg.UseMock, cfg.NoSoul = true, true
	return buildIsolated(t, context.Background(), cfg)
}

// operatorSettings writes settings to where the action and binary look for the
// operator settings file, returning that path and the env that points there.
func operatorSettings(t *testing.T, settings string) (string, []string) {
	t.Helper()
	config := t.TempDir()
	path := filepath.Join(config, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, []string{"XDG_CONFIG_HOME=" + config}
}

// TestOmittedGuardrailsInputInheritsSettingsChecker covers the default action
// input (no guardrails flag) composed with operator settings: a checker
// configured there lets auto start enforcing, and with none auto refuses.
func TestOmittedGuardrailsInputInheritsSettingsChecker(t *testing.T) {
	action := loadAction(t)
	for name, settings := range map[string]string{
		"guardrails model": "guardrails:\n  model: mock-checker\n",
		"guardrail slot":   "models:\n  slots:\n    guardrail: mock-checker\n",
	} {
		t.Run(name, func(t *testing.T) {
			path, env := operatorSettings(t, settings)
			built, err := buildFromActionArgv(t, action, nil, env, "--permission-config", path)
			if err != nil {
				t.Fatalf("auto with an inherited %s checker must start: %v", name, err)
			}
			defer built.Close()
			ctx := context.Background()
			outcome, err := run(ctx, built.Service, session.Limits{}, "summarise the repo", io.Discard)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			sess, err := built.Service.GetSession(ctx, session.SessionID(outcome.Summary.SessionID))
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if sess.Mode != session.ModeAuto {
				t.Fatalf("session mode = %q, want auto", sess.Mode)
			}
		})
	}
	t.Run("settings without a checker refuses", func(t *testing.T) {
		path, env := operatorSettings(t, "permissions:\n  allow: []\n")
		built, err := buildFromActionArgv(t, action, nil, env, "--permission-config", path)
		if err == nil {
			built.Close()
			t.Fatal("auto with no inherited checker must refuse to start")
		}
		if !strings.Contains(err.Error(), "requires an enforcing guardrails checker") {
			t.Fatalf("refusal = %v, want the enforcing-checker error", err)
		}
	})
}

// TestActionAutoWithoutCheckerFailsTheStep pins the action's early refusal:
// auto with no named checker and no operator settings file, or with the
// explicit kill-switch, fails before the binary runs and names the fixes.
func TestActionAutoWithoutCheckerFailsTheStep(t *testing.T) {
	action := loadAction(t)
	_, env := operatorSettings(t, "guardrails:\n  model: mock-checker\n")
	for name, tc := range map[string]struct {
		with map[string]string
		env  []string
	}{
		"defaults with no settings file":      {nil, nil},
		"explicit auto with no settings file": {map[string]string{"permission-mode": "auto"}, nil},
		"off with no settings file":           {map[string]string{"guardrails-model": "off"}, nil},
		"off despite a settings checker":      {map[string]string{"guardrails-model": "off"}, env},
	} {
		t.Run(name, func(t *testing.T) {
			_, out, err := runActionScript(t, action, tc.with, tc.env...)
			if err == nil {
				t.Fatalf("auto without a checker must fail the step: %s", out)
			}
			for _, want := range []string{"::error::", "guardrails-model", "permission-mode: yolo"} {
				if !strings.Contains(out, want) {
					t.Fatalf("step output = %q, want it to contain %q", out, want)
				}
			}
		})
	}
	t.Run("yolo with off starts", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, map[string]string{"permission-mode": "yolo", "guardrails-model": "off"})
		if err != nil || !hasPair(argv, "--permission-mode", "yolo") {
			t.Fatalf("yolo with off: err=%v argv=%q\n%s", err, argv, out)
		}
	})
}

// TestShippedAllowAllDefaultsDeclareCheckerChoice pins the tri-state checker
// input of the composite action and this repo's live workflow: an empty
// guardrails-model passes no flag (the binary inherits the settings checker),
// "off" passes the kill-switch, and any other value names the checker. It
// executes the action's real bash body against a fake binary rather than
// grepping it.
func TestShippedAllowAllDefaultsDeclareCheckerChoice(t *testing.T) {
	action := loadAction(t)

	_, settingsEnv := operatorSettings(t, "guardrails:\n  model: mock-checker\n")
	t.Run("action defaults inherit the checker", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, nil, settingsEnv...)
		if err != nil {
			t.Fatalf("action body failed: %v\n%s", err, out)
		}
		if !hasPair(argv, "--permission-mode", "auto") || hasGuardrailsFlag(argv) || slices.Contains(argv, "--posture") {
			t.Fatalf("default action argv = %q, want --permission-mode auto with no guardrails flag and no --posture", argv)
		}
	})
	t.Run("checker model", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, map[string]string{"guardrails-model": "checker-model"})
		if err != nil {
			t.Fatalf("action body failed: %v\n%s", err, out)
		}
		if !hasPair(argv, "--guardrails-model", "checker-model") || slices.Contains(argv, "--guardrails") {
			t.Fatalf("argv = %q, want --guardrails-model checker-model and no kill-switch", argv)
		}
	})
	t.Run("explicit off", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, map[string]string{"permission-mode": "yolo", "guardrails-model": "off"})
		if err != nil {
			t.Fatalf("action body failed: %v\n%s", err, out)
		}
		if !hasPair(argv, "--guardrails", "off") || slices.Contains(argv, "--guardrails-model") {
			t.Fatalf("argv = %q, want --guardrails off and no --guardrails-model", argv)
		}
	})
	t.Run("deprecated posture input", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, map[string]string{"posture": "auto"})
		if err != nil {
			t.Fatalf("action body failed: %v\n%s", err, out)
		}
		if !hasPair(argv, "--posture", "auto") || slices.Contains(argv, "--permission-mode") || hasGuardrailsFlag(argv) {
			t.Fatalf("argv = %q, want the deprecated --posture auto alone with no guardrails flag", argv)
		}
		if !strings.Contains(out, "::warning::") || !strings.Contains(out, "permission-mode") {
			t.Fatalf("deprecated posture input did not warn naming permission-mode: %s", out)
		}
	})
	t.Run("both inputs fail", func(t *testing.T) {
		_, out, err := runActionScript(t, action, map[string]string{"posture": "auto", "permission-mode": "auto"})
		if err == nil || !strings.Contains(out, "pass only permission-mode") {
			t.Fatalf("both inputs err = %v, want a failed step naming permission-mode: %s", err, out)
		}
	})

	t.Run("live workflow", func(t *testing.T) {
		raw, err := os.ReadFile("../../.github/workflows/mecatequi.yml")
		if err != nil {
			t.Fatal(err)
		}
		var wf workflowFile
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("parse mecatequi.yml: %v", err)
		}
		found := 0
		for _, job := range wf.Jobs {
			for _, step := range job.Steps {
				if step.Uses != "./.github/actions/mecatequi" {
					continue
				}
				found++
				if _, ok := step.With["posture"]; ok {
					t.Fatalf("workflow still passes the deprecated posture input: %v", step.With)
				}
				with := map[string]string{}
				for _, k := range []string{"permission-mode", "guardrails-model", "subagent-ask-reviewer"} {
					if v, ok := step.With[k]; ok {
						with[k] = fmt.Sprint(v)
					}
				}
				// The live workflow mounts no operator settings, so its argv alone must
				// start: the real composition, not a flag-shape check, decides.
				if built, err := buildFromActionArgv(t, action, with, nil); err != nil {
					t.Fatalf("workflow inputs do not start mecatequi: %v", err)
				} else {
					built.Close()
				}
			}
		}
		if found == 0 {
			t.Fatal("mecatequi.yml does not use ./.github/actions/mecatequi")
		}
	})
}

// TestSettingsPermissionModeRefusesDeprecatedPosture drives parseFlags,
// appConfig, and app.Build with an operator settings.yaml carrying
// permissionMode: plan. The deprecated --posture cannot be combined with it;
// the token alone and --posture alone still build.
func TestSettingsPermissionModeRefusesDeprecatedPosture(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("permissionMode: plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func(t *testing.T, args ...string) (*app.Built, error) {
		t.Helper()
		repo := t.TempDir()
		initTestRepo(t, repo)
		f, err := parseFlags(append([]string{"--prompt", "x", "--workspace", repo}, args...))
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, port.NopDiagnostics{}, observability{})
		cfg.UseMock, cfg.NoSoul = true, true
		return buildIsolated(t, context.Background(), cfg)
	}
	built, err := build(t, "--permission-config", settings, "--posture", "strict")
	if err == nil {
		built.Close()
		t.Fatal("settings permissionMode plus --posture must refuse to build")
	}
	if !strings.Contains(err.Error(), "permissionMode: plan cannot be combined") || !strings.Contains(err.Error(), "--permission-mode default") {
		t.Fatalf("refusal must name the conflict and the migration; got %v", err)
	}
	for _, tc := range []struct {
		args []string
		want session.PermissionMode
	}{
		{[]string{"--permission-config", settings}, session.ModePlan},
		{[]string{"--posture", "strict"}, session.ModeDefault},
	} {
		built, err := build(t, tc.args...)
		if err != nil {
			t.Fatalf("%q: app.Build: %v", tc.args, err)
		}
		sess, err := built.Service.CreateSession(context.Background(), "", session.Limits{})
		built.Close()
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if sess.Mode != tc.want {
			t.Fatalf("%q: unspecified-mode session started in %q, want %q", tc.args, sess.Mode, tc.want)
		}
	}
}
