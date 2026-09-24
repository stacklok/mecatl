package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestADR_0357_HarnessContext_Scenario4_PrincipalScopedBindingIsolation(t *testing.T) {
	t.Run("concurrent retry and retirement", TestHarnessCommandBindingConcurrentRetryAndRetirement)
	t.Run("replacement survives stale cleanup", TestHarnessGenerationOldReleaseCannotEvictReplacement)
	t.Run("scope and current authorization", TestHarnessGenerationActivationRechecksScopeAndAuthorization)
	t.Run("owner-authorized service reload", TestHarnessGenerationServiceReloadOwnerAuthorization)
	t.Run("process shutdown waits", TestHarnessCommandShutdownWaitsForBorrowers)
	file := filepath.Join(t.TempDir(), "settings.yaml")
	body := `harness_context:
  enabled_sources: [tenant]
  kinds:
    instructions: {sources: [tenant], mode: combine}
    commands: {sources: [tenant], mode: combine}
    rules: {sources: [tenant], mode: combine}
    skills: {sources: [tenant], mode: combine}
    agent_defs: {sources: [tenant], mode: combine}
`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var binds [5]atomic.Int32
	expectedSubject := "alice"
	check := func(scope HarnessSourceScope, index int) {
		t.Helper()
		if scope.Principal == nil || scope.Principal.Subject != expectedSubject {
			t.Errorf("binder scope=%+v", scope)
		}
		binds[index].Add(1)
	}
	var requests []port.LLMRequest
	cfg := Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), PermissionConfigs: []string{file}, UseMock: true, OwnershipEnforced: true,
		MockProvider: mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("done"), mockllm.TextTurn("second")),
		HarnessInstructionSources: []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, s HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			check(s, 0)
			return hcAssembler("TENANT-INSTRUCTIONS-" + s.Principal.Subject), nil, nil
		}}},
		HarnessCommandSources: []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, s HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			check(s, 1)
			return &hcCommands{values: map[string]string{"review": "TENANT-COMMAND-" + s.Principal.Subject}}, nil, nil
		}}},
		HarnessRulesSources: []HarnessSourceRegistration[prompt.RulesSource]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, s HarnessSourceScope) (prompt.RulesSource, func() error, error) {
			check(s, 2)
			return sourceconformance.NewRuleFixtureSource(), nil, nil
		}}},
		HarnessSkillSources: []HarnessSourceRegistration[tool.SkillSource]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, s HarnessSourceScope) (tool.SkillSource, func() error, error) {
			check(s, 3)
			return sourceconformance.NewFixtureSource(), nil, nil
		}}},
		HarnessAgentDefSources: []HarnessSourceRegistration[tool.AgentDefSource]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, s HarnessSourceScope) (tool.AgentDefSource, func() error, error) {
			check(s, 4)
			return &resolvedAgentSource{defs: []tool.AgentDef{sourceconformance.AgentFixture[0]}}, nil, nil
		}}},
	}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	for i := range binds {
		if binds[i].Load() != 0 {
			t.Fatalf("principal source %d bound at startup", i)
		}
	}
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := built.Service.ListCommandsForSession(ctx, sess.ID)
	if err != nil || len(commands) != 1 || commands[0].Name != "review" {
		t.Fatalf("commands=%v,%v", commands, err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "/review")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	for i := range binds {
		if binds[i].Load() != 1 {
			t.Errorf("source %d bound %d times", i, binds[i].Load())
		}
	}
	if len(requests) != 1 {
		t.Fatalf("requests=%d", len(requests))
	}
	var text strings.Builder
	for _, m := range requests[0].Messages {
		text.WriteString(m.Text)
	}
	for _, marker := range []string{"TENANT-INSTRUCTIONS", "TENANT-COMMAND", sourceconformance.RuleFixture[0].Body} {
		if !strings.Contains(text.String(), marker) {
			t.Errorf("missing %q in actual engine request", marker)
		}
	}
	var skill bool
	for _, spec := range requests[0].Tools {
		skill = skill || spec.Name == "Skill"
	}
	if !skill {
		t.Fatal("principal skill catalog missing")
	}
	expectedSubject = "bob"
	bob := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	other := harnessCreate(t, built, bob)
	harnessRun(t, built, bob, other, "/review")
	for i := range binds {
		if binds[i].Load() != 2 {
			t.Errorf("source %d reused another owner's binding", i)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	var otherText strings.Builder
	for _, message := range requests[1].Messages {
		otherText.WriteString(message.Text)
	}
	if strings.Contains(otherText.String(), "TENANT-INSTRUCTIONS-alice") || strings.Contains(otherText.String(), "TENANT-COMMAND-alice") {
		t.Fatal("principal source leaked across owners")
	}
	if !strings.Contains(otherText.String(), "TENANT-INSTRUCTIONS-bob") || !strings.Contains(otherText.String(), "TENANT-COMMAND-bob") {
		t.Fatal("second owner did not receive its sources")
	}
}
