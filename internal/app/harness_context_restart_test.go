package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func TestADR_0359_HarnessContext_Scenario4_RestartRebindsCurrentPolicy(t *testing.T) {
	root, storeDir, userDir, memoryDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(t.Context(), owner)
	makeConfig := func(id, marker string, allowed bool, provider port.LLMProvider) Config {
		kinds := harnessEmptyKinds()
		kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{id}, Mode: "combine"}
		cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{id}, Kinds: kinds})
		cfg.Workspace = root
		cfg.StoreDir = storeDir
		cfg.UserModelDir = userDir
		cfg.MemoryDir = memoryDir
		cfg.OwnershipEnforced = true
		cfg.AllowAllTools = true
		cfg.MockProvider = provider
		cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: HarnessSourceID(id), Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			if !scope.Principal.SameIdentity(owner) {
				return nil, nil, fmt.Errorf("wrong authoritative source owner")
			}
			if !allowed {
				return nil, nil, fs.ErrPermission
			}
			return hcAssembler(marker), nil, nil
		}}}
		return cfg
	}
	var oldRequests []port.LLMRequest
	firstProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { oldRequests = append(oldRequests, r) })}, mockllm.ToolCallTurn(session.ToolCall{ID: "create", Name: "Schedule", Args: json.RawMessage(`{"verb":"create","name":"context-proof","prompt":"scheduled task","cron":"@every 1h"}`)}), mockllm.TextTurn("scheduled"))
	first, err := buildIsolated(t, ctx, makeConfig("old", "OLD-CONTEXT", true, firstProvider))
	if err != nil {
		t.Fatal(err)
	}
	id := harnessCreate(t, first, ctx)
	for _, event := range harnessRun(t, first, ctx, id, "schedule a task") {
		if event.ToolResult != nil && event.ToolResult.IsError {
			first.Close()
			t.Fatalf("schedule setup=%s", event.ToolResult.Content)
		}
	}
	if len(oldRequests) == 0 || !strings.Contains(harnessRequestText(oldRequests[0]), "OLD-CONTEXT") {
		first.Close()
		t.Fatal("old source positive control missing")
	}
	schedule, err := first.Service.GetSchedule(ctx, "context-proof")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if !schedule.Spec.Owner.SameIdentity(owner) {
		first.Close()
		t.Fatal("schedule lost authoritative owner")
	}
	first.Close()
	var currentRequests []port.LLMRequest
	currentProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { currentRequests = append(currentRequests, r) })}, mockllm.TextTurn("resumed"), mockllm.TextTurn("fired"))
	current, err := buildIsolated(t, ctx, makeConfig("current", "CURRENT-CONTEXT", true, currentProvider))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	harnessRun(t, current, ctx, id, "after restart")
	scheduleStore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	fire := func(b *Built) (port.ScheduleFire, error) {
		runner := scheduler.New(scheduler.Config{Store: scheduleStore.ScheduleStore(), Clock: testWallClock{}, Diagnostics: port.NopDiagnostics{}, TickInterval: time.Hour})
		runner.SetFire(makeFireFunc(b.Service, scheduleStore.ScheduleStore(), defaultFireTimeout, nil))
		b.Service.SetScheduler(runner)
		defer func() { _ = runner.Stop() }()
		return b.Service.FireNow(ctx, "context-proof")
	}
	result, err := fire(current)
	if err != nil || result.Stop == session.StopError {
		t.Fatalf("fire after restart=%+v,%v", result, err)
	}
	if len(currentRequests) != 2 {
		t.Fatalf("expected resumed session and scheduled fire, got %d requests", len(currentRequests))
	}
	for _, request := range currentRequests {
		body := harnessRequestText(request)
		if !strings.Contains(body, "CURRENT-CONTEXT") || strings.Contains(body, "OLD-CONTEXT") {
			t.Fatalf("restart retained stale context: %s", body)
		}
	}
	current.Close()
	var unauthorizedCalls int
	revokedProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { unauthorizedCalls++ })}, mockllm.TextTurn("must not run"))
	revoked, err := buildIsolated(t, ctx, makeConfig("current", "CURRENT-CONTEXT", false, revokedProvider))
	if err != nil {
		t.Fatal(err)
	}
	defer revoked.Close()
	if run, err := revoked.Service.StartRun(ctx, id, "revoked"); err == nil {
		for range run.Events() {
		}
		revoked.Service.FinishRun(id, run)
		t.Fatal("resumed session retained revoked source authority")
	}
	result, err = fire(revoked)
	if err == nil && result.Stop != session.StopError {
		t.Fatalf("schedule retained revoked source authority: %+v", result)
	}
	if unauthorizedCalls != 0 {
		t.Fatal("model called after source authorization was revoked")
	}
}
