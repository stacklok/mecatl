package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/dream"
)

func memoryArgs(raw string) json.RawMessage { return json.RawMessage(raw) }

// noFSTestEnv is a stand-in Environment for these tests' memory tools, none of
// which touch a filesystem.
var noFSTestEnv = tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "test"}, nofs.New(), nil)

func callerContext(subject, workspace string) context.Context {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: subject})
	if workspace != "" {
		ctx = WithWorkspace(ctx, workspace)
	}
	return ctx
}

func TestCallerSeparation_Scenario2_UserModelMemoryIsCallerPartitioned(t *testing.T) {
	dir := t.TempDir()
	base, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := NewCallerStore(base, false)
	if _, _, err := store.Recall(context.Background(), "user/preference"); err == nil {
		t.Fatal("ownerless user-model Recall succeeded")
	}
	aliceCtx := callerContext("alice", "")
	bobCtx := callerContext("bob", "")

	if err := store.RememberEntry(aliceCtx, tool.MemoryEntry{Key: "user/preference", Value: "alice value", Description: "alice preference"}); err != nil {
		t.Fatalf("alice RememberEntry: %v", err)
	}
	if err := store.RememberEntry(bobCtx, tool.MemoryEntry{Key: "user/preference", Value: "bob value", Description: "bob preference"}); err != nil {
		t.Fatalf("bob RememberEntry: %v", err)
	}

	// A fresh process opens the same local user-model directory. The caller
	// namespace is persisted with the entry, not held in an in-memory cache.
	reopenedBase, err := New(dir)
	if err != nil {
		t.Fatalf("reopen user-model store: %v", err)
	}
	reopened := NewCallerStore(reopenedBase, false)

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "alice", ctx: aliceCtx, want: "alice value"},
		{name: "bob", ctx: bobCtx, want: "bob value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ok, rerr := reopened.Recall(tc.ctx, "user/preference")
			if rerr != nil || !ok || e.Value != tc.want {
				t.Fatalf("Recall = (%+v, %t, %v), want value %q", e, ok, rerr, tc.want)
			}
			idx, ierr := reopened.Index(tc.ctx)
			if ierr != nil || len(idx) != 1 || idx[0].Key != "user/preference" {
				t.Fatalf("Index = (%+v, %v), want only own logical key", idx, ierr)
			}
		})
	}
}

func TestCallerSeparation_Scenario2_ProjectMemoryIsCallerPartitioned(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := NewCallerStore(base, true)
	aliceCtx := callerContext("alice", "workspace-a")
	aliceElsewhereCtx := callerContext("alice", "workspace-b")
	bobCtx := callerContext("bob", "workspace-a")

	if err := store.RememberEntry(aliceCtx, tool.MemoryEntry{Key: "deploy", Value: "alice deploy procedure", Description: "alice deploy"}); err != nil {
		t.Fatalf("alice RememberEntry: %v", err)
	}
	if err := store.RememberEntry(bobCtx, tool.MemoryEntry{Key: "deploy", Value: "bob deploy procedure", Description: "bob deploy"}); err != nil {
		t.Fatalf("bob RememberEntry: %v", err)
	}
	if _, ok, err := store.Recall(aliceElsewhereCtx, "deploy"); err != nil || ok {
		t.Fatalf("Alice workspace-b Recall = (%t, %v), want absent", ok, err)
	}

	search, err := store.Search(aliceCtx, "deploy", 10)
	if err != nil || len(search) != 1 || search[0].Key != "deploy" || strings.Contains(search[0].Description, "bob") {
		t.Fatalf("alice Search = (%+v, %v), want only Alice's entry", search, err)
	}
	if _, err := dream.New(store, nil, dream.Config{}).Consolidate(aliceCtx); err != nil {
		t.Fatalf("alice consolidation: %v", err)
	}
	if err := store.Forget(aliceCtx, "deploy"); err != nil {
		t.Fatalf("alice Forget: %v", err)
	}
	if _, ok, rerr := store.Recall(aliceCtx, "deploy"); rerr != nil || ok {
		t.Fatalf("alice Recall after delete = (%t, %v), want absent", ok, rerr)
	}
	entry, ok, rerr := store.Recall(bobCtx, "deploy")
	if rerr != nil || !ok || entry.Value != "bob deploy procedure" {
		t.Fatalf("bob Recall after Alice delete = (%+v, %t, %v), want Bob's unchanged entry", entry, ok, rerr)
	}
}

func TestCallerSeparation_Scenario3_ModelFacingMemoryToolsAreOwnerChecked(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := NewCallerStore(base, true)
	aliceCtx := callerContext("alice", "workspace-a")
	bobCtx := callerContext("bob", "workspace-a")
	aliceTools := Tools(store)
	bobTools := Tools(store)

	remember := aliceTools[0]
	result, err := remember.Execute(aliceCtx, session.NewToolCall("remember-alice", RememberToolName, memoryArgs(`{"key":"same-key","value":"alice secret","description":"alice fact"}`)), noFSTestEnv)
	if err != nil || result.IsError {
		t.Fatalf("Alice Remember = (%+v, %v)", result, err)
	}
	foreignRecall, err := bobTools[1].Execute(bobCtx, session.NewToolCall("recall-bob", RecallToolName, memoryArgs(`{"key":"same-key"}`)), noFSTestEnv)
	if err != nil || foreignRecall.IsError || strings.Contains(foreignRecall.Content, "alice secret") || !strings.Contains(foreignRecall.Content, "No memory found") {
		t.Fatalf("Bob foreign Recall = (%+v, %v), want an absent result without Alice's value", foreignRecall, err)
	}
	foreignSearch, err := bobTools[2].Execute(bobCtx, session.NewToolCall("search-bob", SearchMemoryToolName, memoryArgs(`{"query":"alice"}`)), noFSTestEnv)
	if err != nil || foreignSearch.IsError || strings.Contains(foreignSearch.Content, "alice") && !strings.Contains(foreignSearch.Content, "No memory entries") {
		t.Fatalf("Bob foreign Search = (%+v, %v), want no Alice entry", foreignSearch, err)
	}
	result, err = bobTools[0].Execute(bobCtx, session.NewToolCall("remember-bob", RememberToolName, memoryArgs(`{"key":"same-key","value":"bob value","description":"bob fact"}`)), noFSTestEnv)
	if err != nil || result.IsError {
		t.Fatalf("Bob Remember = (%+v, %v)", result, err)
	}
	if err := store.Forget(bobCtx, "same-key"); err != nil {
		t.Fatalf("Bob Forget: %v", err)
	}
	entry, ok, rerr := store.Recall(aliceCtx, "same-key")
	if rerr != nil || !ok || entry.Value != "alice secret" {
		t.Fatalf("Alice value after Bob write/delete = (%+v, %t, %v), want unchanged", entry, ok, rerr)
	}

	userBase, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New user-model store: %v", err)
	}
	userStore := NewCallerStore(userBase, false)
	aliceUserTools := NewUserModelTools(userStore)
	bobUserTools := NewUserModelTools(userStore)
	result, err = aliceUserTools[0].Execute(aliceCtx, session.NewToolCall("remember-user-alice", RememberUserToolName, memoryArgs(`{"key":"preference","value":"alice user value"}`)), noFSTestEnv)
	if err != nil || result.IsError {
		t.Fatalf("Alice RememberUser = (%+v, %v)", result, err)
	}
	foreignUserRecall, err := bobUserTools[1].Execute(bobCtx, session.NewToolCall("recall-user-bob", RecallUserToolName, memoryArgs(`{"key":"user/preference"}`)), noFSTestEnv)
	if err != nil || foreignUserRecall.IsError || strings.Contains(foreignUserRecall.Content, "alice user value") || !strings.Contains(foreignUserRecall.Content, "No memory found") {
		t.Fatalf("Bob foreign RecallUser = (%+v, %v), want an absent result without Alice's value", foreignUserRecall, err)
	}
	result, err = bobUserTools[0].Execute(bobCtx, session.NewToolCall("remember-user-bob", RememberUserToolName, memoryArgs(`{"key":"preference","value":"bob user value"}`)), noFSTestEnv)
	if err != nil || result.IsError {
		t.Fatalf("Bob RememberUser = (%+v, %v)", result, err)
	}
	if err := userStore.Forget(bobCtx, "user/preference"); err != nil {
		t.Fatalf("Bob user-model Forget: %v", err)
	}
	entry, ok, rerr = userStore.Recall(aliceCtx, "user/preference")
	if rerr != nil || !ok || entry.Value != "alice user value" {
		t.Fatalf("Alice user-model value after Bob write/delete = (%+v, %t, %v), want unchanged", entry, ok, rerr)
	}
}

type baseOnlyMemoryStore struct{ tool.MemoryStore }
type convergenceOnlyMemoryStore struct {
	tool.MemoryStore
	tool.MemoryConvergenceStore
}

func TestCallerStoresPreserveOptionalLifecycleCapability(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]tool.MemoryStore{
		"namespace": NewNamespacedStore(base, "test"),
		"caller":    NewCallerStore(base, false),
	} {
		if _, ok := store.(tool.MemoryLifecycleStore); !ok {
			t.Errorf("%s wrapper dropped MemoryLifecycleStore", name)
		}
		if _, ok := store.(tool.MemoryConvergenceStore); !ok {
			t.Errorf("%s wrapper dropped MemoryConvergenceStore", name)
		}
		if _, ok := store.(duplicateRetirementStore); !ok {
			t.Errorf("%s wrapper dropped atomic duplicate retirement", name)
		}
	}

	convergenceOnly := convergenceOnlyMemoryStore{MemoryStore: base, MemoryConvergenceStore: base}
	for name, store := range map[string]tool.MemoryStore{
		"namespace": NewNamespacedStore(convergenceOnly, "test"),
		"caller":    NewCallerStore(convergenceOnly, false),
	} {
		if _, ok := store.(duplicateRetirementStore); ok {
			t.Errorf("%s wrapper advertised unsupported atomic duplicate retirement", name)
		}
	}

	baseOnly := baseOnlyMemoryStore{MemoryStore: base}
	for name, store := range map[string]tool.MemoryStore{
		"namespace": NewNamespacedStore(baseOnly, "test"),
		"caller":    NewCallerStore(baseOnly, false),
	} {
		if _, ok := store.(tool.MemoryLifecycleStore); ok {
			t.Errorf("%s wrapper advertised unsupported MemoryLifecycleStore", name)
		}
	}
}

func TestCallerLifecycleProfileDetailAndHistoryAreIsolated(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := NewCallerStore(base, false)
	lifecycle, ok := store.(tool.MemoryLifecycleStore)
	if !ok {
		t.Fatal("caller wrapper did not preserve lifecycle capability")
	}
	aliceCtx := callerContext("alice", "")
	bobCtx := callerContext("bob", "")

	aliceFirst, err := lifecycle.RememberVersioned(aliceCtx, tool.MemoryEntry{Key: "user/preference", Value: "alice-v1", Description: "alice detail"}, "")
	if err != nil {
		t.Fatal(err)
	}
	aliceSecond, err := lifecycle.RememberVersioned(aliceCtx, tool.MemoryEntry{Key: "user/preference", Value: "alice-v2", Description: "alice current"}, aliceFirst.Current.Version)
	if err != nil {
		t.Fatal(err)
	}
	bobRecord, err := lifecycle.RememberVersioned(bobCtx, tool.MemoryEntry{Key: "user/preference", Value: "bob-v1", Description: "bob detail"}, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name        string
		ctx         context.Context
		wantValue   string
		wantDetails []string
		wantHistory int
	}{
		{name: "alice", ctx: aliceCtx, wantValue: "alice-v2", wantDetails: []string{"alice detail", "alice current"}, wantHistory: 2},
		{name: "bob", ctx: bobCtx, wantValue: "bob-v1", wantDetails: []string{"bob detail"}, wantHistory: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, listErr := store.List(tc.ctx, "user/")
			if listErr != nil || len(profile) != 1 || profile[0].Value != tc.wantValue {
				t.Fatalf("profile List = (%+v, %v), want only %q", profile, listErr, tc.wantValue)
			}
			record, found, inspectErr := lifecycle.Inspect(tc.ctx, "user/preference")
			if inspectErr != nil || !found || record.Current.Value != tc.wantValue || len(record.Revisions) != tc.wantHistory {
				t.Fatalf("detail/history = (%+v, %t, %v)", record, found, inspectErr)
			}
			for i, revision := range record.Revisions {
				if revision.Key != "user/preference" || revision.Description != tc.wantDetails[i] {
					t.Fatalf("history[%d] = %+v, want logical key and detail %q", i, revision, tc.wantDetails[i])
				}
			}
		})
	}

	_, err = lifecycle.RememberVersioned(aliceCtx, tool.MemoryEntry{Key: "user/preference", Value: "bad"}, bobRecord.Current.Version)
	var conflict *tool.MemoryVersionConflictError
	if !errors.As(err, &conflict) || conflict.Key != "user/preference" || strings.Contains(conflict.Key, "caller/") {
		t.Fatalf("caller-scoped conflict = %#v (%v), want logical key only", conflict, err)
	}
	if aliceSecond.Current.Value != "alice-v2" {
		t.Fatalf("unexpected Alice current record: %+v", aliceSecond.Current)
	}
}

type convergenceOnlyStore struct {
	tool.MemoryStore
	tool.MemoryConvergenceStore
}

func TestNamespacedReviewedSynthesisCapabilityAndKeyTranslation(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrapped := NewNamespacedStore(base, "review")
	synthesis, ok := wrapped.(synthesisStore)
	if !ok {
		t.Fatal("synthesis capability was not preserved")
	}
	for _, entry := range []tool.MemoryEntry{{Key: "a", Value: "one"}, {Key: "b", Value: "two"}} {
		if err := wrapped.RememberEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	lifecycle := wrapped.(tool.MemoryLifecycleStore)
	survivor, _, _ := lifecycle.Inspect(context.Background(), "a")
	source, _, _ := lifecycle.Inspect(context.Background(), "b")
	updated, err := synthesis.SynthesizeReplacement(context.Background(), tool.MemoryEntry{Key: "a", Value: "combined", Description: "reviewed"}, survivor.Current.Version, []string{"b"}, []tool.MemoryVersion{source.Current.Version})
	if err != nil || updated.Current.Key != "a" || updated.Current.Value != "combined" {
		t.Fatalf("logical synthesis = (%+v, %v)", updated, err)
	}
	physicalSurvivor, _, _ := base.Inspect(context.Background(), "review/a")
	physicalSource, _, _ := base.Inspect(context.Background(), "review/b")
	if physicalSurvivor.Current.Value != "combined" || physicalSource.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("physical records survivor=%+v source=%+v", physicalSurvivor, physicalSource)
	}

	withoutCapability := NewNamespacedStore(&convergenceOnlyStore{MemoryStore: base, MemoryConvergenceStore: base}, "plain")
	if _, advertised := withoutCapability.(synthesisStore); advertised {
		t.Fatal("wrapper advertised synthesis absent from backing store")
	}
}
