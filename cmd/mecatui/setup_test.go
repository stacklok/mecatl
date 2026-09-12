package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

type setupRouteNativeHost struct {
	ids       []string
	login     []string
	status    map[string]llmendpoint.Status
	closeCall int
}

func (h *setupRouteNativeHost) EndpointIDs() []string { return append([]string(nil), h.ids...) }
func (h *setupRouteNativeHost) Login(_ context.Context, id string) error {
	h.login = append(h.login, id)
	return nil
}
func (h *setupRouteNativeHost) Status(_ context.Context, id string) llmendpoint.Status {
	return h.status[id]
}
func (*setupRouteNativeHost) Logout(context.Context, string) error { return nil }
func (h *setupRouteNativeHost) Close() error {
	h.closeCall++
	return nil
}

func TestMecatuiLocalProviderSetup_Scenario1_PassiveAggregateStatus(t *testing.T) {
	rows := []providerStatus{
		{ID: "z-custom", Kind: "configured", CredentialSource: "auth file", FilePresent: true, Model: "z/model"},
		{ID: "openai", Kind: "builtin", CredentialSource: "OPENAI_API_KEY", FilePresent: true, Default: true, Model: "fast"},
	}
	var out bytes.Buffer
	writeAggregateStatus(&out, rows, []string{"native-b"}, map[string]string{"native-b": "native-model"}, true)
	got := out.String()
	for _, want := range []string{"Keyed providers:", "Native endpoints:", "ToolHive:", "verification: not checked", "shadowed auth file: present", "selected default: yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "openai") > strings.Index(got, "z-custom") {
		t.Fatalf("provider rows not ID sorted:\n%s", got)
	}
}

func TestMecatuiLocalProviderSetup_Scenario1_StatusHasNoActiveSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY"} {
		t.Setenv(name, "")
	}
	authPath := filepath.Join(home, "missing-auth.yaml")
	out, err := os.CreateTemp(t.TempDir(), "status-output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	forbidden := func() { t.Fatal("aggregate status crossed an active setup boundary") }
	deps := defaultSetupDeps()
	deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		forbidden()
		return "", nil
	}
	deps.updateDefaults = func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
		forbidden()
		return "", nil
	}
	deps.nativeLogin = func(context.Context, string) error { forbidden(); return nil }
	deps.nativeUsable = func(context.Context, string) (bool, error) { forbidden(); return false, nil }
	deps.start = func(string) error { forbidden(); return nil }
	res := resolveLLMCommand([]string{"status", "--auth-file", authPath})
	if res.err != nil {
		t.Fatal(res.err)
	}
	hostCalls := 0
	services := llmCommandDeps{setup: deps, openNative: func(context.Context, bool, io.Writer) (nativeLLMHost, error) {
		hostCalls++
		return nil, errors.New("must not initialize native host")
	}}
	if err := runLLMCommandWith(res, os.Stdin, out, io.Discard, services); err != nil {
		t.Fatal(err)
	}
	if hostCalls != 0 {
		t.Fatalf("aggregate status initialized native host %d times", hostCalls)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "toolhive", "config.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passive status created or opened ToolHive state unexpectedly: %v", err)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "ToolHive:\n  not configured") {
		t.Fatalf("empty host fabricated ToolHive status:\n%s", body)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "setup.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenCalls := map[string]bool{"app.Build": true, "http.Get": true, "http.Post": true, "exec.Command": true, "openNativeLLMHost": true, "newNativeLLMHost": true}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "runAggregateStatus" && fn.Name.Name != "loadSetupSnapshot") {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				if base, ok := fun.X.(*ast.Ident); ok {
					name = base.Name + "." + fun.Sel.Name
				}
			}
			if forbiddenCalls[name] {
				t.Errorf("%s directly calls active boundary %s", fn.Name.Name, name)
			}
			return true
		})
	}
}

func TestMecatuiLocalProviderSetup_Scenario1_EndpointCompatibility(t *testing.T) {
	for _, args := range [][]string{{"status", "--auth-file", "x", "native"}, {"status", "native", "--auth-file", "x"}, {"status", "--auth-file", "x", "--auth-file", "y"}, {"status", "--bad"}, {"status", "a", "b"}} {
		if got := resolveLLMCommand(args); got.err == nil {
			t.Errorf("resolveLLMCommand(%q) accepted", args)
		}
	}
	got := resolveLLMCommand([]string{"status", "--auth-file", "x"})
	if got.err != nil || got.llmAuthFile != "x" || got.llmEndpoint != "" {
		t.Fatalf("aggregate parse = %+v", got)
	}
}

func TestMecatuiLocalProviderSetup_Scenario2_ProviderChoicesAndLifecycleHandoffs(t *testing.T) {
	s := setupSnapshot{Rows: []providerStatus{{ID: "openai", Mutable: true}, {ID: "custom", Mutable: true}, {ID: "none", Mutable: false}}, NativeIDs: []string{"corp"}, ToolHive: true}
	got := mutableProviderIDs(s)
	if strings.Join(got, ",") != "custom,openai" {
		t.Fatalf("mutable choices = %v", got)
	}
	called := false
	deps := setupDeps{nativeLogin: func(context.Context, string) error { called = true; return nil }}
	if err := handoffNative(t.Context(), "corp", false, true, io.Discard, deps); err != nil || !called {
		t.Fatalf("native handoff: called=%v err=%v", called, err)
	}
	called = false
	if err := handoffNative(t.Context(), "corp", true, true, io.Discard, deps); err == nil || called {
		t.Fatalf("override native handoff: called=%v err=%v", called, err)
	}
	checks := 0
	nativeSnapshot := setupSnapshot{NativeIDs: []string{"corp"}, ProviderDefaults: map[string]string{"corp": "native-default"}}
	r := setupRunner{out: io.Discard, deps: setupDeps{nativeUsable: func(context.Context, string) (bool, error) { checks++; return true, nil }}, snapshot: nativeSnapshot, readLine: scriptedLines("corp", "n")}
	if err := r.chooseDefault(t.Context()); err != nil || checks != 0 {
		t.Fatalf("declined native status consent: checks=%d err=%v", checks, err)
	}
	r.readLine = scriptedLines("corp", "y", "native-default", "n")
	if err := r.chooseDefault(t.Context()); err != nil || checks != 1 {
		t.Fatalf("consented native status: checks=%d err=%v", checks, err)
	}
}

func TestInvariant_mecatui_setup_secret_never_observable(t *testing.T) {
	const secret = "SYNTHETIC-SECRET-DO-NOT-PRINT"
	var out bytes.Buffer
	mutated := false
	deps := setupDeps{isTerminal: func(int) bool { return true }, readPassword: func(int) ([]byte, error) { return []byte(secret), nil }, updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		mutated = true
		return authfile.CommitDurable, nil
	}}
	r := setupRunner{inFD: 1, outFD: 2, out: &out, deps: deps, readLine: scriptedLines("1", "openai", "n", "4"), snapshot: setupSnapshot{Rows: []providerStatus{{ID: "openai", CredentialSource: "missing", Mutable: true}}}}
	if err := r.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("declined credential mutated file")
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("secret reached output")
	}
	for _, want := range []string{"provider console", "API/developer key", "consumer subscription", "same-UID", "agent Shell", "charges"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing disclosure %q", want)
		}
	}
	for _, tty := range []func(int) bool{
		func(fd int) bool { return fd != 1 },
		func(fd int) bool { return fd != 2 },
	} {
		read := false
		noTTY := setupRunner{inFD: 1, outFD: 2, out: io.Discard, deps: setupDeps{isTerminal: tty, readPassword: func(int) ([]byte, error) { read = true; return nil, nil }}, readLine: scriptedLines("1")}
		if err := noTTY.run(t.Context()); err == nil || read {
			t.Fatalf("non-terminal setup: err=%v readSecret=%v", err, read)
		}
	}
	oversized := setupRunner{inFD: 1, outFD: 2, out: io.Discard, deps: setupDeps{isTerminal: func(int) bool { return true }, readPassword: func(int) ([]byte, error) { return bytes.Repeat([]byte("x"), maxSetupAPIKeyBytes+1), nil }, updateKey: deps.updateKey}, readLine: scriptedLines("1", "openai"), snapshot: r.snapshot}
	if err := oversized.run(t.Context()); err == nil || mutated {
		t.Fatalf("oversized input: err=%v mutated=%v", err, mutated)
	}
}

func TestMecatuiLocalProviderSetup_OpenCodeCredentialDisclosure(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     []string
		absent   string
	}{
		{
			provider: "opencode",
			want:     []string{"OpenCode API key with an active Go subscription", "built-in uses the Go endpoint, not Zen pay-as-you-go", "same-UID", "agent Shell", "charges"},
			absent:   "API/developer key, not a consumer subscription",
		},
		{
			provider: "openai",
			want:     []string{"API/developer key, not a consumer subscription"},
		},
		{
			provider: "anthropic",
			want:     []string{"API/developer key, not a consumer subscription"},
		},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var out bytes.Buffer
			r := setupRunner{
				inFD: 1, outFD: 2, out: &out,
				deps:     setupDeps{isTerminal: func(int) bool { return true }, readPassword: func(int) ([]byte, error) { return []byte("test-key"), nil }},
				readLine: scriptedLines("1", tc.provider, "n", "4"),
				snapshot: setupSnapshot{Rows: []providerStatus{{ID: tc.provider, CredentialSource: credentialSourceMissing, Mutable: true}}},
			}
			if err := r.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("disclosure missing %q:\n%s", want, got)
				}
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("disclosure unexpectedly contains %q:\n%s", tc.absent, got)
			}
		})
	}
}

func TestPanelRepair_SecretReadAndPostReadFailuresNeverWrite(t *testing.T) {
	const secret = "SYNTHETIC-POST-READ-SENTINEL"
	for name, readPassword := range map[string]func(int) ([]byte, error){
		"read error":    func(int) ([]byte, error) { return nil, errors.New("terminal read failed") },
		"EOF after key": func(int) ([]byte, error) { return []byte(secret), nil },
	} {
		t.Run(name, func(t *testing.T) {
			writes := 0
			var out bytes.Buffer
			r := setupRunner{
				inFD: 1, outFD: 2, out: &out,
				deps: setupDeps{isTerminal: func(int) bool { return true }, readPassword: readPassword, updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
					writes++
					return authfile.CommitDurable, nil
				}},
				readLine: scriptedLines("1", "openai"),
				snapshot: setupSnapshot{Rows: []providerStatus{{ID: "openai", CredentialSource: credentialSourceMissing, Mutable: true}}},
			}
			if err := r.run(t.Context()); err == nil {
				t.Fatal("pre-confirmation failure succeeded")
			}
			if writes != 0 || strings.Contains(out.String(), secret) {
				t.Fatalf("failure leaked or wrote: writes=%d output=%q", writes, out.String())
			}
		})
	}
}

func TestMecatuiLocalProviderSetup_Scenario2_ExistingCredentialPrecedence(t *testing.T) {
	status := cliconfig.ResolveCredentialSource("openrouter", true, func(name string) string { return map[string]string{"OPENAI_API_KEY": "env"}[name] })
	if status.Source != "auth file" || !status.FilePresent {
		t.Fatalf("openrouter source = %+v", status)
	}
	status = cliconfig.ResolveCredentialSource("openrouter", true, func(name string) string {
		return map[string]string{"OPENROUTER_API_KEY": "own", "OPENAI_API_KEY": "fallback"}[name]
	})
	if status.Source != "OPENROUTER_API_KEY" || !status.FilePresent {
		t.Fatalf("openrouter own source = %+v", status)
	}
	if got := cliconfig.ResolveCredentialSource("custom", false, func(name string) string { return map[string]string{"CUSTOM_API_KEY": "x"}[name] }); got.Source != "missing" {
		t.Fatalf("custom inferred env: %+v", got)
	}
}

func TestMecatuiLocalProviderSetup_Scenario2_IndependentConfirmationsAndStart(t *testing.T) {
	if got, ok := resolveSetupModel("fast", map[string]string{"fast": "openai/gpt-5"}); !ok || got != "openai/gpt-5" {
		t.Fatalf("alias = %q,%v", got, ok)
	}
	models := setupModelChoices("existing", []modelChoice{{ID: "z", Name: "Z", ToolCapable: true}, {ID: "a", Name: "A", ToolCapable: true}, {ID: "x", Name: "X", ToolCapable: false}, {ID: "b", Name: "B", ToolCapable: true}, {ID: "c", Name: "C", ToolCapable: true}, {ID: "d", Name: "D", ToolCapable: true}})
	if len(models) != 5 || models[0].ID != "existing" || models[1].ID != "a" {
		t.Fatalf("model choices = %+v", models)
	}
	if got, ok := resolveSetupModel("vendor/model", nil); !ok || got != "vendor/model" {
		t.Fatalf("manual model = %q,%v", got, ok)
	}
	var order []string
	deps := setupDeps{
		isTerminal:   func(int) bool { return true },
		readPassword: func(int) ([]byte, error) { return []byte("fixture-key"), nil },
		updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
			order = append(order, "credential")
			return authfile.CommitDurable, nil
		},
		updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
			order = append(order, "defaults")
			return authfile.CommitDurable, nil
		},
		start: func(path string) error { order = append(order, "start:"+path); return nil },
	}
	r := setupRunner{inFD: 1, outFD: 2, out: io.Discard, deps: deps, authPath: "chosen-auth", settingsPath: "settings", readLine: scriptedLines("1", "openai", "y", "y", "fast", "y", "y"), snapshot: setupSnapshot{Rows: []providerStatus{{ID: "openai", CredentialSource: "missing", Mutable: true}}, Aliases: map[string]string{"fast": "openai/gpt-5"}, ProviderDefaults: map[string]string{"openai": "openai/gpt-5"}}}
	if err := r.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "credential,defaults,start:chosen-auth" {
		t.Fatalf("lifecycle order = %s", got)
	}
}

func TestMecatuiLocalProviderSetup_Scenario2_AC24_ManualBareDeclaredDefaultUsesAuthoritativeGate(t *testing.T) {
	var saved defaultSelection
	writes := 0
	deps := setupDeps{updateDefaults: func(_ context.Context, _ string, d defaultSelection) (authfile.CommitState, error) {
		writes++
		saved = d
		return authfile.CommitDurable, nil
	}}
	snapshot := setupSnapshot{
		Rows:             []providerStatus{{ID: "custom", Kind: "configured", CredentialSource: credentialSourceAuthFile, Mutable: true}},
		ProviderDefaults: map[string]string{"custom": "model"},
	}

	r := setupRunner{out: io.Discard, deps: deps, snapshot: snapshot, readLine: scriptedLines("custom", "model", "y", "n")}
	if err := r.chooseDefault(t.Context()); err != nil {
		t.Fatalf("declared bare default rejected: %v", err)
	}
	if writes != 1 || saved != (defaultSelection{Provider: "custom", Model: "model"}) {
		t.Fatalf("declared bare default was not confirmed and saved: writes=%d saved=%+v", writes, saved)
	}

	r = setupRunner{out: io.Discard, deps: deps, snapshot: snapshot, readLine: scriptedLines("custom", "unknown", "y")}
	if err := r.chooseDefault(t.Context()); err == nil {
		t.Fatal("unknown bare selector accepted")
	}
	if writes != 1 {
		t.Fatalf("unknown bare selector wrote settings %d times", writes)
	}
}

func TestMecatuiLocalProviderSetup_Scenario4_TruthfulPartialCommit(t *testing.T) {
	for _, state := range []authfile.CommitState{authfile.CommitNotApplied, authfile.CommitReplacementAppliedDurabilityUnknown} {
		settingsCalled := false
		deps := setupDeps{updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
			return state, errors.New("injected persistence fault")
		}, updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
			settingsCalled = true
			return authfile.CommitDurable, nil
		}}
		var out bytes.Buffer
		next := &defaultSelection{Provider: "openai", Model: "model"}
		if err := commitCredentialThenDefaults(t.Context(), "a", "s", "openai", "secret", next, &out, deps); err == nil {
			t.Fatalf("state %s succeeded", state)
		}
		if settingsCalled {
			t.Fatalf("settings written after credential state %s", state)
		}
		if !strings.Contains(out.String(), string(state)) || !strings.Contains(out.String(), "run `mecatui llm status`") {
			t.Fatalf("state %s untruthful report: %s", state, out.String())
		}
	}
	var order []string
	deps := setupDeps{updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		order = append(order, "key")
		return authfile.CommitDurable, nil
	}, updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
		order = append(order, "settings")
		return authfile.CommitNoop, nil
	}}
	if err := commitCredentialThenDefaults(t.Context(), "a", "s", "openai", "secret", &defaultSelection{Provider: "openai", Model: "model"}, io.Discard, deps); err != nil || strings.Join(order, ",") != "key,settings" {
		t.Fatalf("safe ordered commits: order=%v err=%v", order, err)
	}
}

func TestFinalReview_RemoveRejectsReplacementLosingItsOnlyCredential(t *testing.T) {
	for _, model := range []string{"same-model", "different-model"} {
		t.Run(model, func(t *testing.T) {
			settingsWrites, keyDeletes := 0, 0
			r := setupRunner{
				out: io.Discard,
				deps: setupDeps{
					updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
						settingsWrites++
						return authfile.CommitDurable, nil
					},
					updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
						keyDeletes++
						return authfile.CommitDurable, nil
					},
				},
				readLine: scriptedLines("openai", "y", "openai", model, "y"),
				snapshot: setupSnapshot{
					Rows:             []providerStatus{{ID: "openai", CredentialSource: credentialSourceAuthFile, Mutable: true}},
					Default:          defaultSelection{Provider: "openai", Model: "same-model"},
					Models:           map[string][]modelChoice{"openai": {{ID: "different-model", ToolCapable: true}}},
					ProviderDefaults: map[string]string{"openai": "same-model"},
				},
			}
			if err := r.remove(t.Context()); err == nil {
				t.Fatal("replacement retained only the credential being deleted")
			}
			if settingsWrites != 0 || keyDeletes != 0 {
				t.Fatalf("unsafe replacement wrote: settings=%d key-deletes=%d", settingsWrites, keyDeletes)
			}
		})
	}
}

func TestFinalReview_RemoveAcceptsSurvivingReplacementAndEnvironmentSource(t *testing.T) {
	t.Run("other provider survives", func(t *testing.T) {
		var order []string
		r := setupRunner{
			out: io.Discard,
			deps: setupDeps{
				updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
					order = append(order, "settings")
					return authfile.CommitDurable, nil
				},
				updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
					order = append(order, "key")
					return authfile.CommitDurable, nil
				},
			},
			readLine: scriptedLines("openai", "y", "anthropic", "claude", "y"),
			snapshot: setupSnapshot{
				Rows: []providerStatus{
					{ID: "openai", CredentialSource: credentialSourceAuthFile, Mutable: true},
					{ID: "anthropic", CredentialSource: credentialSourceAuthFile, Mutable: true},
				},
				Default:          defaultSelection{Provider: "openai", Model: "gpt"},
				ProviderDefaults: map[string]string{"anthropic": "claude"},
			},
		}
		if err := r.remove(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(order, ","); got != "settings,key" {
			t.Fatalf("commit order = %s", got)
		}
	})

	t.Run("environment credential avoids replacement", func(t *testing.T) {
		settingsWrites, keyDeletes := 0, 0
		r := setupRunner{
			out: io.Discard,
			deps: setupDeps{
				updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
					settingsWrites++
					return authfile.CommitDurable, nil
				},
				updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
					keyDeletes++
					return authfile.CommitDurable, nil
				},
			},
			readLine: scriptedLines("openai", "y"),
			snapshot: setupSnapshot{
				Rows:    []providerStatus{{ID: "openai", CredentialSource: "OPENAI_API_KEY", FilePresent: true, Mutable: true}},
				Default: defaultSelection{Provider: "openai", Model: "gpt"},
			},
		}
		if err := r.remove(t.Context()); err != nil {
			t.Fatal(err)
		}
		if settingsWrites != 0 || keyDeletes != 1 {
			t.Fatalf("environment-backed removal: settings=%d key-deletes=%d", settingsWrites, keyDeletes)
		}
	})
}

func TestMecatuiLocalProviderSetup_Scenario4_RemoveWithoutSilentDefaultReset(t *testing.T) {
	replacement := &defaultSelection{Provider: "anthropic", Model: "m"}
	removed := false
	blocked := setupDeps{updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
		return authfile.CommitNotApplied, errors.New("blocked")
	}, updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		removed = true
		return authfile.CommitDurable, nil
	}}
	if err := removeCredential(t.Context(), "a", "s", "openai", replacement, io.Discard, blocked); err == nil || removed {
		t.Fatalf("stranding removal: removed=%v err=%v", removed, err)
	}
	blocked = setupDeps{updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
		return authfile.CommitNoop, nil
	}, updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		removed = true
		return authfile.CommitDurable, nil
	}}
	if err := removeCredential(t.Context(), "a", "s", "openai", replacement, io.Discard, blocked); err == nil || removed {
		t.Fatalf("no-op replacement did not prove durability: removed=%v err=%v", removed, err)
	}

	for _, tc := range []struct {
		state authfile.CommitState
		want  string
	}{
		{authfile.CommitNotApplied, "default moved; the old key may remain"},
		{authfile.CommitReplacementAppliedDurabilityUnknown, "replacement default is durable; key removal may have applied"},
	} {
		var out bytes.Buffer
		calls := 0
		deps := setupDeps{updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
			return authfile.CommitDurable, nil
		}, updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
			calls++
			return tc.state, errors.New("injected removal fault")
		}}
		if err := removeCredential(t.Context(), "a", "s", "openai", replacement, &out, deps); err == nil || calls != 1 || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("removal state %s: calls=%d err=%v output=%s", tc.state, calls, err, out.String())
		}
	}
}

func TestPanelRepair_LoadSnapshotKeepsProviderDefaults(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	mecatlDir := filepath.Join(configDir, "mecatl")
	if err := os.MkdirAll(mecatlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	settings := `providers:
  custom:
    base_url: https://custom.example/v1
    default_model: custom-default
    api_flavor: openai-responses
    auth:
      method: api_key
`
	if err := os.WriteFile(filepath.Join(mecatlDir, "settings.yaml"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(mecatlDir, "auth.yaml")
	if err := os.WriteFile(authPath, []byte("providers:\n  custom:\n    api_key: fixture-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadSetupSnapshot(authPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProviderDefaults["custom"] != "custom-default" {
		t.Fatalf("provider defaults = %+v native=%+v", snapshot.ProviderDefaults, snapshot.NativeDefaults)
	}
	var custom providerStatus
	for _, row := range snapshot.Rows {
		if row.ID == "custom" {
			custom = row
		}
	}
	if custom.Model != "custom-default" || custom.CredentialSource != "auth file" {
		t.Fatalf("custom row = %+v", custom)
	}
}

func TestPanelRepair_SetupPromptCancellationUnblocks(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, readErr := readLineContext(ctx, read, bufio.NewReader(read))
		done <- readErr
	}()
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got, context.Canceled) {
			t.Fatalf("cancel error = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled prompt remained blocked")
	}
}

func TestPanelRepair_SetupPreflightRejectsPhysicalAlias(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(auth, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "settings.yaml")
	if err := os.Link(auth, alias); err != nil {
		t.Fatal(err)
	}
	if err := preflightSetupPaths(auth, alias); err == nil {
		t.Fatal("hard-linked auth/settings targets accepted")
	}
}
func TestPanelRepair_DefaultSelectionRejectsUnstartableChoices(t *testing.T) {
	writes := 0
	deps := setupDeps{updateDefaults: func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
		writes++
		return authfile.CommitDurable, nil
	}}
	snapshot := setupSnapshot{
		Rows: []providerStatus{
			{ID: "openai", Kind: "builtin", CredentialSource: "missing", Mutable: true, DefaultModel: "gpt-5-mini"},
			{ID: "custom", Kind: "configured", CredentialSource: "auth file", Mutable: true, DefaultModel: "custom-model"},
			{ID: "anonymous", Kind: "none required", CredentialSource: "none required", DefaultModel: "anon-model"},
		},
		ProviderDefaults: map[string]string{"openai": "gpt-5-mini", "custom": "custom-model", "anonymous": "anon-model"},
	}
	for name, lines := range map[string][]string{
		"unknown-provider":     {"unknown"},
		"missing-credential":   {"openai"},
		"unknown-custom-model": {"custom", "typo-model"},
	} {
		t.Run(name, func(t *testing.T) {
			r := setupRunner{out: io.Discard, deps: deps, snapshot: snapshot, readLine: scriptedLines(lines...)}
			if err := r.chooseDefault(t.Context()); err == nil {
				t.Fatal("unstartable default accepted")
			}
		})
	}
	if writes != 0 {
		t.Fatalf("unstartable defaults wrote settings %d times", writes)
	}

	r := setupRunner{out: io.Discard, deps: deps, snapshot: snapshot, readLine: scriptedLines("custom", "custom-model", "y", "n")}
	if err := r.chooseDefault(t.Context()); err != nil || writes != 1 {
		t.Fatalf("configured custom default: writes=%d err=%v", writes, err)
	}
}

func TestPanelRepair_PerProviderDefaultsAndToolHiveIntent(t *testing.T) {
	rows := []providerStatus{{ID: "custom", DefaultModel: "custom-model"}, {ID: "native", DefaultModel: "native-model"}}
	if rows[0].DefaultModel != "custom-model" || rows[1].DefaultModel != "native-model" {
		t.Fatalf("provider defaults lost: %+v", rows)
	}
	if app.ToolhiveConfiguredPassive(filepath.Join(t.TempDir(), "missing.yaml")) {
		t.Fatal("missing ToolHive config reported configured")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("llm:\n  gateway_url: https://gateway.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !app.ToolhiveConfiguredPassive(path) {
		t.Fatal("present ToolHive intent not detected")
	}
}

func TestPanelRepair_UnsupportedSetupFailsBeforeInteraction(t *testing.T) {
	loaded, read, updated := false, false, false
	deps := setupDeps{
		writeSupported: func() bool { return false },
		load:           func(string, bool) (setupSnapshot, error) { loaded = true; return setupSnapshot{}, nil },
		isTerminal:     func(int) bool { return true },
		readPassword:   func(int) ([]byte, error) { read = true; return nil, nil },
		updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
			updated = true
			return authfile.CommitDurable, nil
		},
	}
	f, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := runSetupCommand(t.Context(), "auth", false, f, f, deps); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported setup error = %v", err)
	}
	if loaded || read || updated {
		t.Fatalf("unsupported setup crossed interaction boundary: load=%v read=%v update=%v", loaded, read, updated)
	}
}
func TestMecatuiLocalProviderSetup_Scenario4_StartupPointerOnly(t *testing.T) {
	err := validateEmbeddedProvider(config{transportMode: modeLocal})
	if err == nil || !strings.Contains(err.Error(), "run `mecatui llm setup`") {
		t.Fatalf("startup pointer = %v", err)
	}
	if got := resolveInvocation([]string{"mecatui", "connect", "host"}); got.mode != modeConnect {
		t.Fatalf("connect changed: %+v", got)
	}
}

func scriptedLines(lines ...string) func(string) (string, error) {
	i := 0
	return func(string) (string, error) {
		if i >= len(lines) {
			return "", io.EOF
		}
		s := lines[i]
		i++
		return s, nil
	}
}
