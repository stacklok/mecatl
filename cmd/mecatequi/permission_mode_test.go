package main

import (
	"bytes"
	"fmt"
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
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
)

func TestADR_0365_DeprecatedAliasesStillResolve(t *testing.T) {
	for _, tc := range []struct{ posture, token string }{
		{"strict", "default"}, {"trusted", "trusted"}, {"auto", "auto"}, {"yolo", "yolo"},
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

func TestADR_0365_PermissionModeFlagConflictsAndUnknownTokens(t *testing.T) {
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
	if !strings.Contains(permissionModeHelp, "process-wide") {
		t.Fatal("--permission-mode help does not say which half is process-wide")
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
func runActionScript(t *testing.T, action actionFile, with map[string]string) ([]string, string, error) {
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
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_OUT\"\n"
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
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"RUNNER_TEMP=" + dir,
		"GITHUB_OUTPUT=" + filepath.Join(dir, "github_output"),
		"ARGS_OUT=" + argsOut,
	}
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
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n"), string(out), nil
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

// TestADR_0365_ShippedAllowAllDefaultsDeclareCheckerChoice pins ADR 0365 AC2.3
// for the composite action and this repo's live workflow: whenever they run an
// allow-all mode they pass a checker model or the explicit --guardrails off, so
// the startup refusal never fires on a shipped default. It executes the action's
// real bash body against a fake binary rather than grepping it.
func TestADR_0365_ShippedAllowAllDefaultsDeclareCheckerChoice(t *testing.T) {
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

	t.Run("action defaults", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, nil)
		if err != nil {
			t.Fatalf("action body failed: %v\n%s", err, out)
		}
		if !hasPair(argv, "--permission-mode", "auto") || !hasPair(argv, "--guardrails", "off") || slices.Contains(argv, "--posture") {
			t.Fatalf("default action argv = %q, want --permission-mode auto with --guardrails off and no --posture", argv)
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
	t.Run("deprecated posture input", func(t *testing.T) {
		argv, out, err := runActionScript(t, action, map[string]string{"posture": "auto"})
		if err != nil {
			t.Fatalf("action body failed: %v\n%s", err, out)
		}
		if !hasPair(argv, "--posture", "auto") || slices.Contains(argv, "--permission-mode") || !hasPair(argv, "--guardrails", "off") {
			t.Fatalf("argv = %q, want the deprecated --posture auto alone with --guardrails off", argv)
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
				argv, out, err := runActionScript(t, action, with)
				if err != nil {
					t.Fatalf("workflow inputs failed the action body: %v\n%s", err, out)
				}
				if !hasPair(argv, "--permission-mode", "auto") {
					t.Fatalf("workflow argv = %q, want --permission-mode auto", argv)
				}
				if !hasPair(argv, "--guardrails", "off") && !slices.Contains(argv, "--guardrails-model") {
					t.Fatalf("workflow runs allow-all without a checker choice: %q", argv)
				}
			}
		}
		if found == 0 {
			t.Fatal("mecatequi.yml does not use ./.github/actions/mecatequi")
		}
	})
}
