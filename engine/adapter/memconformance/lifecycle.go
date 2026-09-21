package memconformance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// RunLifecycle executes the mandatory versioned MemoryStore conformance suite.
func RunLifecycle(t *testing.T, newStore func(t *testing.T) tool.MemoryStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("create update and views", func(t *testing.T) {
		store := newStore(t)
		attributed := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{
			Writer: tool.MemoryWriterModel,
			Origin: tool.MemoryOriginExplicit,
			Source: tool.MemorySource{SessionID: "session-1", ProposalID: "proposal-1"},
		})
		first, err := store.Remember(attributed, tool.MemoryEntry{Key: "profile/editor", Value: "helix", Description: "editor"}, tool.MemoryCurrent{})
		if err != nil {
			t.Fatalf("Remember create: %v", err)
		}
		if first.Current.Version == "" || first.Current.Status != tool.MemoryStatusActive || first.Current.Writer != tool.MemoryWriterModel || first.Current.Source.SessionID != "session-1" {
			t.Fatalf("created record = %+v", first)
		}
		if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "stale"}, tool.MemoryCurrent{}); err == nil {
			t.Fatal("create-only Remember replaced an existing record")
		}
		second, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "vim"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version})
		if err != nil {
			t.Fatalf("Remember update: %v", err)
		}
		if second.Current.Value != "vim" || len(second.Revisions) != 2 || second.Revisions[0].Status != tool.MemoryStatusSuperseded {
			t.Fatalf("updated record = %+v", second)
		}
		if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "stale"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version}); err == nil {
			t.Fatal("stale Remember succeeded")
		}
		entry, found, err := store.Recall(ctx, "profile/editor")
		if err != nil || !found || entry.Value != "vim" {
			t.Fatalf("Recall = (%+v, %v, %v)", entry, found, err)
		}
		listed, err := store.List(ctx, "profile/")
		if err != nil || len(listed) != 1 || listed[0].Value != "vim" {
			t.Fatalf("List = (%+v, %v)", listed, err)
		}
		indexed, err := store.Index(ctx)
		if err != nil || len(indexed) != 1 || indexed[0].Value != "" || indexed[0].Description == "" {
			t.Fatalf("Index = (%+v, %v)", indexed, err)
		}
		searched, err := store.Search(ctx, "editor vim", 1)
		if err != nil || len(searched) != 1 || searched[0].Value != "" {
			t.Fatalf("Search = (%+v, %v)", searched, err)
		}
		inspected, found, err := store.Inspect(ctx, "profile/editor")
		if err != nil || !found || inspected.Current.Version != second.Current.Version {
			t.Fatalf("Inspect = (%+v, %v, %v)", inspected, found, err)
		}
	})

	t.Run("tombstone and undo require exact version", func(t *testing.T) {
		store := newStore(t)
		first, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "dark"}, tool.MemoryCurrent{})
		if err != nil {
			t.Fatal(err)
		}
		second, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "light"}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Forget(ctx, "profile/theme", first.Current.Version); err == nil {
			t.Fatal("stale Forget succeeded")
		}
		deleted, err := store.Forget(ctx, "profile/theme", second.Current.Version)
		if err != nil || deleted.Current.Status != tool.MemoryStatusDeleted {
			t.Fatalf("Forget = (%+v, %v)", deleted, err)
		}
		if _, found, _ := store.Recall(ctx, "profile/theme"); found {
			t.Fatal("Recall found tombstoned record")
		}
		if _, err := store.Undo(ctx, "profile/theme", second.Current.Version); err == nil {
			t.Fatal("stale Undo succeeded")
		}
		restored, err := store.Undo(ctx, "profile/theme", deleted.Current.Version)
		if err != nil || restored.Current.Status != tool.MemoryStatusActive || restored.Current.Value != "light" || restored.Current.Origin != tool.MemoryOriginUndo {
			t.Fatalf("Undo = (%+v, %v)", restored, err)
		}
	})

	t.Run("concurrent exact update has one winner", func(t *testing.T) {
		store := newStore(t)
		first, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/race", Value: "one"}, tool.MemoryCurrent{})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = store.Remember(ctx, tool.MemoryEntry{Key: "profile/race", Value: string(rune('a' + i))}, tool.MemoryCurrent{Exists: true, Version: first.Current.Version})
			}()
		}
		close(start)
		wg.Wait()
		successes, conflicts := 0, 0
		for _, err := range errs {
			if err == nil {
				successes++
				continue
			}
			var conflict *tool.MemoryVersionConflictError
			if errors.As(err, &conflict) {
				conflicts++
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successes=%d conflicts=%d errors=%v", successes, conflicts, errs)
		}
	})

	t.Run("write validation", func(t *testing.T) {
		store := newStore(t)
		if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "Legacy Key/É", Value: "invalid"}, tool.MemoryCurrent{}); !errors.Is(err, tool.ErrInvalidMemoryKey) {
			t.Fatalf("invalid key = %v", err)
		}
		if _, err := store.Remember(ctx, tool.MemoryEntry{Key: "profile/token", Value: "ghp_0123456789abcdefghijklmnop"}, tool.MemoryCurrent{}); !errors.Is(err, tool.ErrSecretMemoryValue) {
			t.Fatalf("secret value = %v", err)
		}
	})
}
