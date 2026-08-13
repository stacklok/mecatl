package memconformance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// RunLifecycle executes the shared MemoryLifecycleStore conformance suite. The
// factory must return a fresh store implementing both the legacy and lifecycle
// contracts over the same records.
func RunLifecycle(t *testing.T, newStore func(t *testing.T) (tool.MemoryStore, tool.MemoryLifecycleStore)) {
	t.Helper()
	ctx := context.Background()

	t.Run("remember supersedes and preserves attribution", func(t *testing.T) {
		legacy, lifecycle := newStore(t)
		attributed := tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{
			Writer: tool.MemoryWriterModel,
			Origin: tool.MemoryOriginExplicit,
			Source: tool.MemorySource{SessionID: "session-1"},
		})
		first, err := lifecycle.RememberVersioned(attributed, tool.MemoryEntry{Key: "profile/editor", Value: "helix", Description: "editor"}, "")
		if err != nil {
			t.Fatalf("RememberVersioned create: %v", err)
		}
		if first.Current.Version == "" || first.Current.Status != tool.MemoryStatusActive {
			t.Fatalf("first current = %+v", first.Current)
		}
		if first.Current.Writer != tool.MemoryWriterModel || first.Current.Source.SessionID != "session-1" {
			t.Fatalf("first attribution = %+v", first.Current)
		}
		second, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/editor", Value: "vim"}, first.Current.Version)
		if err != nil {
			t.Fatalf("RememberVersioned overwrite: %v", err)
		}
		if second.Current.Value != "vim" || len(second.Revisions) != 2 {
			t.Fatalf("second record = %+v", second)
		}
		if second.Revisions[0].Status != tool.MemoryStatusSuperseded || second.Revisions[1] != second.Current {
			t.Fatalf("revision statuses = %+v", second.Revisions)
		}
		got, found, err := legacy.Recall(ctx, "profile/editor")
		if err != nil || !found || got.Value != "vim" {
			t.Fatalf("legacy Recall after lifecycle overwrite = (%+v, %v, %v)", got, found, err)
		}
		second.Revisions[0].Value = "caller mutation"
		again, _, _ := lifecycle.Inspect(ctx, "profile/editor")
		if again.Revisions[0].Value != "helix" {
			t.Fatal("Inspect returned aliased revision history")
		}
	})

	t.Run("empty expected version overwrites unconditionally", func(t *testing.T) {
		_, lifecycle := newStore(t)
		first, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/overwrite", Value: "one"}, "")
		if err != nil {
			t.Fatal(err)
		}
		second, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/overwrite", Value: "two"}, "")
		if err != nil || second.Current.Value != "two" || second.Current.Version == first.Current.Version || len(second.Revisions) != 2 {
			t.Fatalf("unconditional overwrite: first=%+v second=%+v err=%v", first, second, err)
		}
		if _, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/overwrite", Value: "stale"}, first.Current.Version); err == nil {
			t.Fatal("stale explicit expected version succeeded")
		}
	})

	t.Run("compare-version forget conflicts and deletion remains inspectable", func(t *testing.T) {
		legacy, lifecycle := newStore(t)
		created, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/shell", Value: "fish"}, "")
		if err != nil {
			t.Fatal(err)
		}
		newer, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/shell", Value: "nu"}, created.Current.Version)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycle.ForgetVersioned(ctx, "profile/shell", created.Current.Version); err == nil {
			t.Fatal("stale ForgetVersioned succeeded")
		} else {
			var conflict *tool.MemoryVersionConflictError
			if !errors.As(err, &conflict) || conflict.Actual != newer.Current.Version {
				t.Fatalf("stale ForgetVersioned = %v", err)
			}
		}
		if got, found, _ := legacy.Recall(ctx, "profile/shell"); !found || got.Value != "nu" {
			t.Fatalf("stale forget removed newer value: %+v found=%v", got, found)
		}
		deleted, err := lifecycle.ForgetVersioned(ctx, "profile/shell", newer.Current.Version)
		if err != nil {
			t.Fatalf("ForgetVersioned: %v", err)
		}
		if deleted.Current.Status != tool.MemoryStatusDeleted || deleted.Current.Version == newer.Current.Version {
			t.Fatalf("deleted current = %+v", deleted.Current)
		}
		if _, found, _ := legacy.Recall(ctx, "profile/shell"); found {
			t.Fatal("legacy Recall found deleted record")
		}
		inspected, found, err := lifecycle.Inspect(ctx, "profile/shell")
		if err != nil || !found || inspected.Current.Status != tool.MemoryStatusDeleted {
			t.Fatalf("Inspect deleted = (%+v, %v, %v)", inspected, found, err)
		}
		if _, err := lifecycle.ForgetVersioned(ctx, "profile/shell", inspected.Current.Version); !errors.Is(err, tool.ErrMemoryNotFound) {
			t.Fatalf("forget existing tombstone = %v, want ErrMemoryNotFound", err)
		}
	})

	t.Run("undo appends compensation", func(t *testing.T) {
		legacy, lifecycle := newStore(t)
		first, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "dark"}, "")
		second, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/theme", Value: "light"}, first.Current.Version)
		undone, err := lifecycle.UndoLatest(ctx, "profile/theme", second.Current.Version)
		if err != nil {
			t.Fatalf("UndoLatest overwrite: %v", err)
		}
		if undone.Current.Value != "dark" || undone.Current.Status != tool.MemoryStatusActive || len(undone.Revisions) != 3 {
			t.Fatalf("undo overwrite = %+v", undone)
		}
		if undone.Current.Origin != tool.MemoryOriginUndo {
			t.Fatalf("undo origin = %q, want undo", undone.Current.Origin)
		}
		deleted, _ := lifecycle.ForgetVersioned(ctx, "profile/theme", undone.Current.Version)
		restored, err := lifecycle.UndoLatest(ctx, "profile/theme", deleted.Current.Version)
		if err != nil || restored.Current.Value != "dark" || restored.Current.Status != tool.MemoryStatusActive {
			t.Fatalf("undo delete = (%+v, %v)", restored, err)
		}
		fresh, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/one-shot", Value: "x"}, "")
		removed, err := lifecycle.UndoLatest(ctx, "profile/one-shot", fresh.Current.Version)
		if err != nil || removed.Current.Status != tool.MemoryStatusDeleted {
			t.Fatalf("undo creation = (%+v, %v)", removed, err)
		}
		if _, found, _ := legacy.Recall(ctx, "profile/one-shot"); found {
			t.Fatal("undo creation left legacy value active")
		}

		walkFirst, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/walk", Value: "one"}, "")
		walkSecond, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/walk", Value: "two"}, walkFirst.Current.Version)
		walkBack, err := lifecycle.UndoLatest(ctx, "profile/walk", walkSecond.Current.Version)
		if err != nil || walkBack.Current.Value != "one" {
			t.Fatalf("first walking undo = (%+v, %v)", walkBack, err)
		}
		walkDeleted, err := lifecycle.UndoLatest(ctx, "profile/walk", walkBack.Current.Version)
		if err != nil || walkDeleted.Current.Status != tool.MemoryStatusDeleted {
			t.Fatalf("second walking undo = (%+v, %v)", walkDeleted, err)
		}
		if len(walkDeleted.Revisions) != 4 {
			t.Fatalf("walking undo history = %+v", walkDeleted.Revisions)
		}
	})

	t.Run("undo stops at truncated predecessor boundary", func(t *testing.T) {
		legacy, lifecycle := newStore(t)
		var current tool.MemoryRecord
		for i := range 70 {
			var err error
			current, err = lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/bounded", Value: fmt.Sprintf("v-%d", i)}, "")
			if err != nil {
				t.Fatal(err)
			}
		}
		succeeded := 0
		for ; succeeded < 100; succeeded++ {
			before := current.Current
			next, err := lifecycle.UndoLatest(ctx, "profile/bounded", before.Version)
			if err == nil {
				current = next
				continue
			}
			if succeeded == 0 {
				t.Fatalf("first bounded undo failed: %v", err)
			}
			after, found, inspectErr := lifecycle.Inspect(ctx, "profile/bounded")
			if inspectErr != nil || !found || after.Current != before {
				t.Fatalf("failed boundary undo mutated current: before=%+v after=%+v found=%v inspect=%v undo=%v", before, after.Current, found, inspectErr, err)
			}
			entry, found, recallErr := legacy.Recall(ctx, "profile/bounded")
			if recallErr != nil || !found || entry.Value != before.Value {
				t.Fatalf("failed boundary undo changed legacy view: entry=%+v found=%v recall=%v", entry, found, recallErr)
			}
			return
		}
		t.Fatal("undo crossed truncated history boundary")
	})

	t.Run("concurrent undo reverses once", func(t *testing.T) {
		_, lifecycle := newStore(t)
		first, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/concurrent", Value: "one"}, "")
		second, _ := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/concurrent", Value: "two"}, first.Current.Version)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = lifecycle.UndoLatest(ctx, "profile/concurrent", second.Current.Version)
			}()
		}
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
			t.Fatalf("concurrent undo successes=%d conflicts=%d errors=%v", successes, conflicts, errs)
		}
		record, _, _ := lifecycle.Inspect(ctx, "profile/concurrent")
		if len(record.Revisions) != 3 || record.Current.Value != "one" {
			t.Fatalf("concurrent undo record = %+v", record)
		}
	})

	t.Run("concurrent update races are single-winner CAS", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			second func(tool.MemoryLifecycleStore, tool.MemoryVersion) error
		}{
			{name: "update-update", second: func(s tool.MemoryLifecycleStore, version tool.MemoryVersion) error {
				_, err := s.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/race", Value: "three"}, version)
				return err
			}},
			{name: "update-forget", second: func(s tool.MemoryLifecycleStore, version tool.MemoryVersion) error {
				_, err := s.ForgetVersioned(ctx, "profile/race", version)
				return err
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, lifecycle := newStore(t)
				first, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/race", Value: "one"}, "")
				if err != nil {
					t.Fatal(err)
				}
				start := make(chan struct{})
				errs := make([]error, 2)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					_, errs[0] = lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/race", Value: "two"}, first.Current.Version)
				}()
				go func() { defer wg.Done(); <-start; errs[1] = tc.second(lifecycle, first.Current.Version) }()
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
				record, _, err := lifecycle.Inspect(ctx, "profile/race")
				if err != nil || successes != 1 || conflicts != 1 || len(record.Revisions) != 2 {
					t.Fatalf("successes=%d conflicts=%d revisions=%d errors=%v inspect=%v", successes, conflicts, len(record.Revisions), errs, err)
				}
			})
		}
	})

	t.Run("new lifecycle writes validate key and secrets without changing legacy grammar", func(t *testing.T) {
		legacy, lifecycle := newStore(t)
		if err := legacy.RememberEntry(ctx, tool.MemoryEntry{Key: "Legacy Key/É", Value: "imported"}); err != nil {
			t.Fatalf("legacy RememberEntry grammar changed: %v", err)
		}
		if _, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "Legacy Key/É", Value: "new"}, ""); !errors.Is(err, tool.ErrInvalidMemoryKey) {
			t.Fatalf("lifecycle invalid key = %v", err)
		}
		if _, err := lifecycle.RememberVersioned(ctx, tool.MemoryEntry{Key: "profile/token", Value: "ghp_0123456789abcdefghijklmnop"}, ""); !errors.Is(err, tool.ErrSecretMemoryValue) {
			t.Fatalf("lifecycle secret = %v", err)
		}
	})
}
