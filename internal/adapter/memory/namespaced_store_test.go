package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/dream"
)

func callerContext(subject, workspace string) context.Context {
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: subject})
	if workspace != "" {
		ctx = WithWorkspace(ctx, workspace)
	}
	return ctx
}

func createMemory(t *testing.T, ctx context.Context, store tool.MemoryStore, entry tool.MemoryEntry) tool.MemoryRecord {
	t.Helper()
	record, err := store.Remember(ctx, entry, tool.MemoryCurrent{})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestCallerStorePartitionsUserMemoryByVerifiedPrincipal(t *testing.T) {
	store := NewCallerStore(memmemory.New(), false)
	alice := callerContext("alice", "")
	bob := callerContext("bob", "")
	createMemory(t, alice, store, tool.MemoryEntry{Key: "user/preference", Value: "alice"})
	createMemory(t, bob, store, tool.MemoryEntry{Key: "user/preference", Value: "bob"})

	aliceEntry, found, err := store.Recall(alice, "user/preference")
	if err != nil || !found || aliceEntry.Value != "alice" {
		t.Fatalf("alice Recall = (%+v, %v, %v)", aliceEntry, found, err)
	}
	bobEntry, found, err := store.Recall(bob, "user/preference")
	if err != nil || !found || bobEntry.Value != "bob" {
		t.Fatalf("bob Recall = (%+v, %v, %v)", bobEntry, found, err)
	}
	if _, _, err := store.Recall(context.Background(), "user/preference"); !errors.Is(err, errCallerStoreIdentityRequired) {
		t.Fatalf("ownerless Recall = %v", err)
	}
}

func TestCallerStorePartitionsProjectMemoryByWorkspace(t *testing.T) {
	store := NewCallerStore(memmemory.New(), true)
	left := callerContext("alice", "/workspace/left")
	right := callerContext("alice", "/workspace/right")
	createMemory(t, left, store, tool.MemoryEntry{Key: "project/deploy", Value: "left"})
	createMemory(t, right, store, tool.MemoryEntry{Key: "project/deploy", Value: "right"})

	leftEntry, _, _ := store.Recall(left, "project/deploy")
	rightEntry, _, _ := store.Recall(right, "project/deploy")
	if leftEntry.Value != "left" || rightEntry.Value != "right" {
		t.Fatalf("workspace isolation failed: left=%+v right=%+v", leftEntry, rightEntry)
	}
	if _, _, err := store.Recall(callerContext("alice", ""), "project/deploy"); err == nil {
		t.Fatal("project Recall without workspace succeeded")
	}
}

func TestNamespacedStorePreservesCASAndLogicalKeys(t *testing.T) {
	base := memmemory.New()
	left := NewNamespacedStore(base, "left")
	right := NewNamespacedStore(base, "right")
	first := createMemory(t, context.Background(), left, tool.MemoryEntry{Key: "profile/editor", Value: "helix"})
	createMemory(t, context.Background(), right, tool.MemoryEntry{Key: "profile/editor", Value: "vim"})

	updated, err := left.Remember(context.Background(), tool.MemoryEntry{Key: "profile/editor", Value: "zed"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version})
	if err != nil || updated.Current.Key != "profile/editor" {
		t.Fatalf("namespaced update = (%+v, %v)", updated, err)
	}
	if _, err := left.Remember(context.Background(), tool.MemoryEntry{Key: "profile/editor", Value: "stale"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version}); err == nil {
		t.Fatal("stale namespaced update succeeded")
	} else {
		var conflict *tool.MemoryVersionConflictError
		if !errors.As(err, &conflict) || conflict.Key != "profile/editor" {
			t.Fatalf("logical conflict = %v", err)
		}
	}
	rightEntry, _, _ := right.Recall(context.Background(), "profile/editor")
	if rightEntry.Value != "vim" {
		t.Fatalf("left mutation crossed namespace: %+v", rightEntry)
	}
	forgotten, err := left.Forget(context.Background(), "profile/editor", updated.Current.Version)
	if err != nil || forgotten.Current.Status != tool.MemoryStatusDeleted {
		t.Fatalf("Forget = (%+v, %v)", forgotten, err)
	}
	restored, err := left.Undo(context.Background(), "profile/editor", forgotten.Current.Version)
	if err != nil || restored.Current.Value != "zed" {
		t.Fatalf("Undo = (%+v, %v)", restored, err)
	}
}

func TestWrappersPreserveAtomicConsolidationCapabilities(t *testing.T) {
	base, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !dream.SupportsReviewedPlan(NewNamespacedStore(base, "project")) {
		t.Fatal("namespaced wrapper dropped reviewed-plan atomic capabilities")
	}
	if !dream.SupportsReviewedPlan(NewCallerStore(base, false)) {
		t.Fatal("caller wrapper dropped reviewed-plan atomic capabilities")
	}
}
