package jsonlstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario2_UnrelatedMutationNotBlocked(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New Store: %v", err)
	}
	ctx := context.Background()
	ids := []session.SessionID{"inventory-source", "save-target", "load-target", "event-target", "tool-target"}
	sessions := make(map[session.SessionID]*session.Session, len(ids))
	for _, id := range ids {
		sessions[id] = session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
		if err := st.Save(ctx, sessions[id]); err != nil {
			t.Fatalf("seed %q: %v", id, err)
		}
	}
	if _, err := st.MetaList(ctx); err != nil {
		t.Fatalf("prime catalog: %v", err)
	}
	if err := os.Remove(st.inventoryCatalogPath()); err != nil {
		t.Fatalf("remove inventory manifest: %v", err)
	}

	rebuildStarted := make(chan struct{})
	releaseRebuild := make(chan struct{})
	blocked := false
	st.inventoryWorkObserver = func(kind inventoryWorkKind) {
		if kind != inventoryWorkRebuild || blocked {
			return
		}
		blocked = true
		close(rebuildStarted)
		<-releaseRebuild
	}
	inventoryDone := make(chan error, 1)
	go func() {
		_, listErr := st.MetaList(ctx)
		inventoryDone <- listErr
	}()
	awaitSignal(t, rebuildStarted, "inventory rebuild did not start")

	operations := map[string]func(){
		"Save": func() {
			if saveErr := st.Save(ctx, sessions["save-target"]); saveErr != nil {
				t.Errorf("Save unrelated family: %v", saveErr)
			}
		},
		"Load": func() {
			if _, loadErr := st.Load(ctx, "load-target"); loadErr != nil {
				t.Errorf("Load unrelated family: %v", loadErr)
			}
		},
		"EventLog.Append": func() {
			if appendErr := st.Append(ctx, "event-target", session.Event{Type: session.EvResult}); appendErr != nil {
				t.Errorf("Append unrelated family: %v", appendErr)
			}
		},
		"ToolCall": func() {
			st.ToolCall("tool-target", session.ToolCall{ID: "call-1", Name: "Read"}, session.NewToolResult("call-1", "ok"), 0, 0)
		},
	}
	for name, operation := range operations {
		done := make(chan struct{})
		go func() {
			operation()
			close(done)
		}()
		awaitSignal(t, done, name+" was delayed by an unrelated inventory rebuild")
	}

	close(releaseRebuild)
	if err := awaitError(t, inventoryDone, "inventory rebuild did not finish"); err != nil {
		t.Fatalf("MetaList after unblock: %v", err)
	}
}

func TestSessionStorageContinuity_Scenario2_SameFamilyMutationSerialized(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T, *Store, session.SessionID)
		run    func(context.Context, *Store, session.SessionID) error
		verify func(*testing.T, *Store, session.SessionID)
	}{
		{name: "Save", run: func(ctx context.Context, st *Store, id session.SessionID) error {
			return st.Save(ctx, session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC()))
		}},
		{name: "Migration promotion", setup: func(t *testing.T, st *Store, id session.SessionID) {
			t.Helper()
			if err := st.Delete(ctx, id); err != nil {
				t.Fatalf("remove canonical seed: %v", err)
			}
			legacy := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/legacy", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_600_000_000, 0).UTC())
			payload, err := sessnap.Marshal(legacy)
			if err != nil {
				t.Fatalf("marshal legacy snapshot: %v", err)
			}
			if err := os.WriteFile(st.resolver.legacyPath(id, kindSnapshot), append(payload, '\n'), 0o600); err != nil {
				t.Fatalf("write legacy snapshot: %v", err)
			}
			if err := os.WriteFile(st.resolver.legacyPath(id, kindTools), []byte("legacy-tool\n"), 0o600); err != nil {
				t.Fatalf("write legacy sidecar: %v", err)
			}
		}, run: func(ctx context.Context, st *Store, id session.SessionID) error {
			return st.Save(ctx, session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC()))
		}, verify: func(t *testing.T, st *Store, id session.SessionID) {
			t.Helper()
			if _, err := os.Stat(st.resolver.canonicalPath(id, kindTools)); err != nil {
				t.Fatalf("promoted sidecar: %v", err)
			}
			if _, err := os.Stat(st.resolver.legacyPath(id, kindSnapshot)); !os.IsNotExist(err) {
				t.Fatalf("legacy snapshot remains after promotion: %v", err)
			}
		}},
		{name: "Delete", run: func(ctx context.Context, st *Store, id session.SessionID) error { return st.Delete(ctx, id) }},
		{name: "ConditionalDelete", run: func(ctx context.Context, st *Store, id session.SessionID) error {
			rows, err := st.rebuildInventoryRows()
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.ID == id {
					_, err = st.DeleteSessionIfUnchanged(ctx, row)
					return err
				}
			}
			return errors.New("conditional delete fixture row not found")
		}},
		{name: "EventLog.Append", run: func(ctx context.Context, st *Store, id session.SessionID) error {
			return st.Append(ctx, id, session.Event{Type: session.EvResult})
		}},
		{name: "ToolCall", run: func(_ context.Context, st *Store, id session.SessionID) error {
			st.ToolCall(id, session.ToolCall{ID: "call-1", Name: "Read"}, session.NewToolResult("call-1", "ok"), 0, 0)
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first, err := New(dir)
			if err != nil {
				t.Fatalf("New first Store: %v", err)
			}
			second, err := New(dir)
			if err != nil {
				t.Fatalf("New second Store: %v", err)
			}
			id := session.SessionID("same-family")
			otherID := session.SessionID("unrelated-family")
			if err := first.Save(ctx, session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())); err != nil {
				t.Fatalf("seed family: %v", err)
			}
			if err := first.Save(ctx, session.New(otherID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())); err != nil {
				t.Fatalf("seed unrelated family: %v", err)
			}
			if tc.setup != nil {
				tc.setup(t, first, id)
			}

			familyLock := flock.New(snapshotFamilyLockPath(first.resolver.currentSnapshotPath(id)), flock.SetPermissions(0o600))
			if err := familyLock.Lock(); err != nil {
				t.Fatalf("hold family lock: %v", err)
			}
			reachedLock := make(chan struct{}, 1)
			second.snapshotFamilyLockBlocked = func() {
				select {
				case reachedLock <- struct{}{}:
				default:
				}
			}
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, second, id) }()
			assertStillBlocked(t, reachedLock, done, tc.name+" escaped the same-family mutation lock")
			unrelatedDone := make(chan error, 1)
			go func() {
				unrelatedDone <- second.Append(ctx, otherID, session.Event{Type: session.EvResult})
			}()
			if err := awaitError(t, unrelatedDone, "unrelated family was delayed by a held family lock"); err != nil {
				t.Fatalf("append unrelated family: %v", err)
			}
			if err := familyLock.Unlock(); err != nil {
				t.Fatalf("release family lock: %v", err)
			}
			if err := familyLock.Close(); err != nil {
				t.Fatalf("close family lock: %v", err)
			}
			if err := awaitError(t, done, tc.name+" did not continue after family unlock"); err != nil {
				t.Fatalf("%s after family unlock: %v", tc.name, err)
			}
			if tc.verify != nil {
				tc.verify(t, second, id)
			}
		})
	}
}

func TestSessionFamilyOperationsHonorContextWhileLockIsHeld(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New Store: %v", err)
	}
	id := session.SessionID("context-lock-target")
	seed := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
	seed.SetTitle("before")
	if err := st.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 1}); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	snapshotPath := st.resolver.currentSnapshotPath(id)
	eventPath := st.resolver.canonicalPath(id, kindEvents)
	beforeSnapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read seed snapshot: %v", err)
	}
	beforeEvents, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatalf("read seed events: %v", err)
	}

	familyLock := flock.New(snapshotFamilyLockPath(snapshotPath), flock.SetPermissions(0o600))
	if err := familyLock.Lock(); err != nil {
		t.Fatalf("hold family lock: %v", err)
	}
	defer func() { _ = familyLock.Close() }()

	changed := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
	changed.SetTitle("after")
	operations := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "Save", run: func(ctx context.Context) error { return st.Save(ctx, changed) }},
		{name: "Load", run: func(ctx context.Context) error { _, err := st.Load(ctx, id); return err }},
		{name: "Delete", run: func(ctx context.Context) error { return st.Delete(ctx, id) }},
		{name: "Append", run: func(ctx context.Context) error {
			return st.Append(ctx, id, session.Event{Type: session.EvResult, Seq: 2})
		}},
		{name: "Read", run: func(ctx context.Context) error {
			for _, err := range st.Read(ctx, id) {
				return err
			}
			return nil
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			reachedLock := make(chan struct{}, 1)
			st.snapshotFamilyLockBlocked = func() { reachedLock <- struct{}{} }
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- operation.run(ctx) }()
			awaitSignal(t, reachedLock, "operation did not reach the held family lock")
			var opErr error
			select {
			case opErr = <-done:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("operation did not return after its context deadline")
			}
			if !errors.Is(opErr, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want context.DeadlineExceeded", opErr)
			}
			gotSnapshot, readErr := os.ReadFile(snapshotPath)
			if readErr != nil || string(gotSnapshot) != string(beforeSnapshot) {
				t.Fatalf("snapshot mutated while lock held: data changed=%v, err=%v", string(gotSnapshot) != string(beforeSnapshot), readErr)
			}
			gotEvents, readErr := os.ReadFile(eventPath)
			if readErr != nil || string(gotEvents) != string(beforeEvents) {
				t.Fatalf("events mutated while lock held: data changed=%v, err=%v", string(gotEvents) != string(beforeEvents), readErr)
			}
		})
	}

	st.toolCallLockTimeout = 30 * time.Millisecond
	started := time.Now()
	st.ToolCall(id, session.ToolCall{ID: "bounded-call", Name: "Read"}, session.NewToolResult("bounded-call", "ok"), 0, 0)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("ToolCall lock wait = %s, want bounded return", elapsed)
	}
	if _, err := os.Stat(st.resolver.canonicalPath(id, kindTools)); !os.IsNotExist(err) {
		t.Fatalf("ToolCall mutated tool log while family lock held: %v", err)
	}
}

func awaitSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func awaitError(t *testing.T, ch <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal(message)
		return nil
	}
}

func assertStillBlocked(t *testing.T, reached <-chan struct{}, ch <-chan error, message string) {
	t.Helper()
	awaitSignal(t, reached, message+": operation did not reach the held lock")
	select {
	case err := <-ch:
		t.Fatalf("%s: operation returned %v", message, err)
	default:
	}
}
