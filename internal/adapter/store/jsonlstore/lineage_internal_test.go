package jsonlstore

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLineageTombstoneContainsNoPrincipalPII(t *testing.T) {
	ctx := t.Context()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := session.New("owned", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	s.Owner = &session.Principal{Issuer: "secret-issuer.example", Subject: "private-subject", GrantType: session.GrantTypeUser, Name: "Private Person"}
	if err := st.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(st.lineageIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{s.Owner.Issuer, s.Owner.Subject, s.Owner.Name} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("lineage tombstone retained Principal PII %q: %s", forbidden, body)
		}
	}
}

func TestLineageCrashBoundaryReconciliation(t *testing.T) {
	ctx := t.Context()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := st.Save(ctx, original); err != nil {
		t.Fatal(err)
	}

	// Snapshot published before its retained-index update: removing the index
	// simulates that crash and the next read must reconstruct it.
	if err := os.Remove(st.lineageIndexPath()); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: original.ID, RootIncarnation: original.Incarnation(), Limit: 10})
	if err != nil || len(got.Records) != 1 || got.Records[0].State != port.SessionLineageRetained {
		t.Fatalf("missing-index reconciliation = %+v, %v", got, err)
	}

	// Tombstone is ordered before snapshot deletion. If deletion crashes, the
	// still-present matching incarnation must remain pruned, never resurrect.
	if err := st.withLineageLock(ctx, func() error { return st.pruneLineageLocked(original.ID) }); err != nil {
		t.Fatal(err)
	}
	got, err = st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: original.ID, RootIncarnation: original.Incarnation(), Limit: 10})
	if err != nil || len(got.Records) != 1 || got.Records[0].State != port.SessionLineagePruned {
		t.Fatalf("tombstone/snapshot reconciliation = %+v, %v", got, err)
	}

	// A later recreation with the same storage key has a distinct incarnation.
	// Re-install the stale tombstone after publishing the replacement snapshot to
	// simulate a crash before the retained-index update.
	stale, err := readLineageIndex(st.lineageIndexPath())
	if err != nil {
		t.Fatal(err)
	}
	replacement := session.New(original.ID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0))
	if err := st.Save(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := st.writeLineageRows(stale); err != nil {
		t.Fatal(err)
	}
	got, err = st.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: original.ID, RootIncarnation: replacement.Incarnation(), Limit: 10})
	if err != nil || len(got.Records) != 2 || got.Records[0].State != port.SessionLineageRetained || got.Records[0].Incarnation != string(replacement.Incarnation()) || got.Records[1].State != port.SessionLineagePruned || got.Records[1].Incarnation != string(original.Incarnation()) {
		t.Fatalf("recreation reconciliation = %+v, %v", got, err)
	}
}

func TestLineageIndexCorruptionFailsLoud(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.lineageIndexPath(), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: "root", RootIncarnation: session.NewIncarnationID(), Limit: 1}); err == nil {
		t.Fatal("ReadSessionLineage accepted a corrupt index")
	}
}
