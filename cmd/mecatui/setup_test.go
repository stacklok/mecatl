package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func TestMecatuiLocalProviderSetup_Scenario1_PassiveAggregateStatus(t *testing.T) {
	rows := []providerStatus{
		{ID: "z-custom", Kind: "configured", CredentialSource: "auth file", FilePresent: true, Model: "z/model"},
		{ID: "openai", Kind: "builtin", CredentialSource: "OPENAI_API_KEY", FilePresent: true, Default: true, Model: "fast"},
	}
	var out bytes.Buffer
	writeAggregateStatus(&out, rows, []string{"native-b"}, true)
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
	calls := 0
	deps := setupDeps{load: func(string, bool) (setupSnapshot, error) {
		return setupSnapshot{Rows: []providerStatus{{ID: "openai"}}}, nil
	}, active: func() { calls++ }}
	var out bytes.Buffer
	if err := runAggregateStatus("", false, &out, deps); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("active side effects = %d", calls)
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

func TestMecatuiLocalProviderSetup_Scenario2_ExistingCredentialPrecedence(t *testing.T) {
	status := resolveCredentialStatus("openrouter", map[string]string{"OPENAI_API_KEY": "env"}, true)
	if status.Source != "auth file" || !status.FilePresent {
		t.Fatalf("openrouter source = %+v", status)
	}
	status = resolveCredentialStatus("openrouter", map[string]string{"OPENROUTER_API_KEY": "own", "OPENAI_API_KEY": "fallback"}, true)
	if status.Source != "OPENROUTER_API_KEY" || !status.FilePresent {
		t.Fatalf("openrouter own source = %+v", status)
	}
	if got := resolveCredentialStatus("custom", map[string]string{"CUSTOM_API_KEY": "x"}, false); got.Source != "missing" {
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
	if got, ok := resolveSetupModel("manualbare", nil); !ok || got != "manualbare" {
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
	r := setupRunner{inFD: 1, outFD: 2, out: io.Discard, deps: deps, authPath: "chosen-auth", settingsPath: "settings", readLine: scriptedLines("1", "openai", "y", "y", "fast", "y", "y"), snapshot: setupSnapshot{Rows: []providerStatus{{ID: "openai", CredentialSource: "missing", Mutable: true}}, Aliases: map[string]string{"fast": "openai/gpt-5"}}}
	if err := r.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "credential,defaults,start:chosen-auth" {
		t.Fatalf("lifecycle order = %s", got)
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
