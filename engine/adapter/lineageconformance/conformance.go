// Package lineageconformance validates optional durable session-lineage readers.
package lineageconformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type lineageStore interface {
	port.SessionStore
	port.SessionCreator
	port.PrunableStore
	port.SessionLineageReader
}

type fixture struct {
	root, child, scheduled *session.Session
	alice, bob             *session.Principal
}

// Run validates the common durable-lineage contract.
func Run(t *testing.T, st lineageStore) {
	t.Helper()
	assertContentFree(t)
	f := seedAndCheckDiscovery(t, st)
	checkCollisionDeleteRecreate(t, st, f)
	checkBoundsAndConcurrency(t, st, f.root)
	checkRootRecreationBoundary(t, st, f)
}

func seedAndCheckDiscovery(t *testing.T, st lineageStore) fixture {
	t.Helper()
	ctx := context.Background()
	f := fixture{
		alice: &session.Principal{Issuer: "issuér/原文", Subject: "alice", GrantType: session.GrantTypeUser, Name: "Alice"},
		bob:   &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser, Name: "Bob"},
	}
	f.root = mustSession(t, "root", session.SessionKindMain, session.SessionRelationship{}, f.alice)
	f.child = mustSession(t, "z-child", session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: f.root.ID, ParentIncarnation: f.root.Incarnation(), CallID: "call"}, f.alice)
	f.scheduled = mustSession(t, "a-scheduled", session.SessionKindScheduled, session.SessionRelationship{ScheduleName: "nightly", OriginSessionID: f.root.ID, OriginIncarnation: f.root.Incarnation()}, f.bob)
	unrelated := mustSession(t, "subagent-root-fake", session.SessionKindMain, session.SessionRelationship{}, f.bob)
	for _, s := range []*session.Session{f.root, f.child, f.scheduled, unrelated} {
		if err := st.Create(ctx, s); err != nil {
			t.Fatalf("Create(%s): %v", s.ID, err)
		}
	}

	result, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: f.root.ID, RootIncarnation: f.root.Incarnation(), Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Records) != 2 || result.Records[0].ID != f.root.ID || result.Records[1].ID != f.scheduled.ID {
		t.Fatalf("bounded order = %+v, truncated=%t", result.Records, result.Truncated)
	}
	result = readAll(t, st, f.root)
	if len(result.Records) != 3 || result.Records[0].ID != f.root.ID || result.Records[1].ID != f.scheduled.ID || result.Records[2].ID != f.child.ID {
		t.Fatalf("direct records = %+v", result.Records)
	}
	if result.Records[0].OwnerScope != session.PrincipalScopeHash(f.alice) || result.Records[1].OwnerScope != session.PrincipalScopeHash(f.bob) {
		t.Fatalf("owner scopes not preserved: %+v", result.Records)
	}
	if result.Records[0].Incarnation == "" || result.Records[1].Incarnation == "" {
		t.Fatalf("incarnations not preserved: %+v", result.Records)
	}
	checkExactLookup(t, st, f)
	return f
}

func checkExactLookup(t *testing.T, st lineageStore, f fixture) {
	t.Helper()
	ctx := context.Background()
	exact, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{
		RootID: f.root.ID, RootIncarnation: f.root.Incarnation(),
		RecordID: f.child.ID, RecordIncarnation: f.child.Incarnation(), Limit: 1,
	})
	if err != nil || exact.Truncated || len(exact.Records) != 1 || exact.Records[0].ID != f.child.ID || exact.Records[0].Incarnation != string(f.child.Incarnation()) {
		t.Fatalf("exact direct edge = %+v, %v", exact, err)
	}
	missing, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{
		RootID: f.scheduled.ID, RootIncarnation: f.scheduled.Incarnation(),
		RecordID: f.child.ID, RecordIncarnation: f.child.Incarnation(), Limit: 1,
	})
	if err != nil || len(missing.Records) != 0 || missing.Truncated {
		t.Fatalf("unrelated exact edge = %+v, %v", missing, err)
	}
}

func checkCollisionDeleteRecreate(t *testing.T, st lineageStore, f fixture) {
	t.Helper()
	ctx := context.Background()
	if err := st.Create(ctx, mustSession(t, f.root.ID, session.SessionKindMain, session.SessionRelationship{}, f.bob)); !errors.Is(err, port.ErrSessionAlreadyExists) {
		t.Fatalf("collision = %v", err)
	}
	if err := st.Delete(ctx, f.child.ID); err != nil {
		t.Fatal(err)
	}
	deleted := readAll(t, st, f.root).Records[2]
	if deleted.State != port.SessionLineagePruned || deleted.DeletedAt.IsZero() || deleted.OwnerScope != session.PrincipalScopeHash(f.alice) {
		t.Fatalf("tombstone = %+v", deleted)
	}
	oldIncarnation := deleted.Incarnation
	if err := st.Create(ctx, mustSession(t, f.child.ID, session.SessionKindSubagent, f.child.Relationship, f.bob)); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	recreated := readAll(t, st, f.root).Records
	if len(recreated) != 4 {
		t.Fatalf("recreated lineage length = %d, want 4: %+v", len(recreated), recreated)
	}
	var current, historical *port.SessionLineageRecord
	for i := range recreated {
		if recreated[i].ID != f.child.ID {
			continue
		}
		if recreated[i].State == port.SessionLineageRetained {
			current = &recreated[i]
		} else if recreated[i].Incarnation == oldIncarnation {
			historical = &recreated[i]
		}
	}
	if current == nil || current.OwnerScope != session.PrincipalScopeHash(f.bob) || current.Incarnation == oldIncarnation || historical == nil || historical.DeletedAt.IsZero() {
		t.Fatalf("recreated lineage did not preserve current and historical incarnations: %+v", recreated)
	}
}

func checkRootRecreationBoundary(t *testing.T, st lineageStore, f fixture) {
	t.Helper()
	ctx := context.Background()
	oldIncarnation := f.root.Incarnation()
	if err := st.Delete(ctx, f.root.ID); err != nil {
		t.Fatal(err)
	}
	replacement := mustSession(t, f.root.ID, session.SessionKindMain, session.SessionRelationship{}, f.alice)
	if replacement.CreatedAt != f.root.CreatedAt || !replacement.Owner.SameIdentity(f.root.Owner) || replacement.Incarnation() == oldIncarnation {
		t.Fatalf("recreated root did not hold identical ID/time/owner with a new incarnation")
	}
	if err := st.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	current := readAll(t, st, replacement)
	for _, row := range current.Records {
		if row.ID != replacement.ID {
			t.Fatalf("recreated root inherited old descendant: %+v", row)
		}
	}
	if len(current.Records) != 2 || current.Records[0].State != port.SessionLineageRetained || current.Records[1].State != port.SessionLineagePruned || current.Records[1].Incarnation != string(oldIncarnation) {
		t.Fatalf("root tombstone was not preserved across recreation: %+v", current.Records)
	}
	old, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: f.root.ID, RootIncarnation: oldIncarnation, Limit: 10})
	if err != nil || len(old.Records) < 4 {
		t.Fatalf("old incarnation lineage was not preserved: records=%+v err=%v", old.Records, err)
	}
}

func checkBoundsAndConcurrency(t *testing.T, st lineageStore, root *session.Session) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: port.MaxSessionLineageRecords + 1}); !errors.Is(err, port.ErrInvalidSessionLineageQuery) {
		t.Fatalf("over-limit query = %v", err)
	}
	if _, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), RecordID: "child", Limit: 1}); !errors.Is(err, port.ErrInvalidSessionLineageQuery) {
		t.Fatalf("partial exact query = %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.Save(ctx, root); err != nil {
				errs <- fmt.Errorf("save: %w", err)
			}
			if _, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10}); err != nil {
				errs <- fmt.Errorf("read: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	final := readAll(t, st, root)
	if len(final.Records) == 0 || final.Records[0].ID != root.ID || final.Records[0].State != port.SessionLineageRetained || final.Records[0].Incarnation != string(root.Incarnation()) {
		t.Fatalf("final lineage contents = %+v", final)
	}
}

func readAll(t *testing.T, st lineageStore, root *session.Session) port.SessionLineageResult {
	t.Helper()
	result, err := st.ReadSessionLineage(context.Background(), port.SessionLineageQuery{RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustSession(t *testing.T, id session.SessionID, kind session.SessionKind, rel session.SessionRelationship, owner *session.Principal) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := s.RestoreSessionMetadata(kind, rel); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	return s
}

func assertContentFree(t *testing.T) {
	t.Helper()
	typ := reflect.TypeFor[port.SessionLineageRecord]()
	allowed := map[string]bool{"ID": true, "Kind": true, "Relationship": true, "OwnerScope": true, "Incarnation": true, "State": true, "DeletedAt": true}
	for i := range typ.NumField() {
		if !allowed[typ.Field(i).Name] {
			t.Fatalf("unexpected lineage field %s", typ.Field(i).Name)
		}
	}
	if typ.NumField() != len(allowed) {
		t.Fatalf("lineage fields=%d want=%d", typ.NumField(), len(allowed))
	}
}
