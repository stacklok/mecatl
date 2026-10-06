package jsonlstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type inventoryObservedWork struct {
	catalogRows  int
	snapshotRead int
	rebuilds     int
}

func observeInventoryWork(st *Store) *inventoryObservedWork {
	work := &inventoryObservedWork{}
	st.inventoryWorkObserver = func(kind inventoryWorkKind) {
		switch kind {
		case inventoryWorkCatalogRow:
			work.catalogRows++
		case inventoryWorkSnapshotRead:
			work.snapshotRead++
		case inventoryWorkRebuild:
			work.rebuilds++
		}
	}
	return work
}

func installInventoryFixture(tb testing.TB, count int, owner *session.Principal) *Store {
	tb.Helper()
	st, err := New(tb.TempDir())
	if err != nil {
		tb.Fatalf("New Store: %v", err)
	}
	rows := make([]port.SessionDiscoveryMeta, 0, count)
	base := time.Unix(1_800_000_000, 0).UTC()
	for i := 0; i < count; i++ {
		id := session.SessionID(fmt.Sprintf("session-%04d", i))
		path := st.resolver.currentSnapshotPath(id)
		f, createErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if createErr != nil {
			tb.Fatalf("create sparse snapshot %q: %v", id, createErr)
		}
		// 15 GiB / 1024 gives the 807-row fixture a 12-GiB-equivalent logical
		// payload while sparse allocation keeps the offline test small.
		if truncateErr := f.Truncate(15 << 20); truncateErr != nil {
			_ = f.Close()
			tb.Fatalf("truncate sparse snapshot %q: %v", id, truncateErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			tb.Fatalf("close sparse snapshot %q: %v", id, closeErr)
		}
		modified := base.Add(-time.Duration(i/2) * time.Second)
		if chtimesErr := os.Chtimes(path, modified, modified); chtimesErr != nil {
			tb.Fatalf("Chtimes sparse snapshot %q: %v", id, chtimesErr)
		}
		rows = append(rows, port.SessionDiscoveryMeta{
			ID: id, ModifiedAt: modified, State: session.StateCompleted,
			Kind: session.SessionKindMain, Owner: owner,
		})
	}
	fingerprint, err := st.inventoryFingerprint()
	if err != nil {
		tb.Fatalf("inventoryFingerprint: %v", err)
	}
	if err := st.writeInventoryCatalog(fingerprint, map[string]inventoryCatalogSource{}, rows); err != nil {
		tb.Fatalf("writeInventoryCatalog: %v", err)
	}
	return st
}

func TestSessionStorageContinuity_Scenario2_PageWorkBounded(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice"}
	st := installInventoryFixture(t, 240, owner)
	work := observeInventoryWork(st)
	request := port.SessionMetadataPageRequest{Limit: 25, OwnershipEnforced: true, Owner: owner}

	first, err := st.PageSessionMetadata(context.Background(), request)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	firstRows := work.catalogRows
	if len(first.Sessions) != 25 || first.NextCursor == nil {
		t.Fatalf("first page = %d rows cursor=%v, want 25 and cursor", len(first.Sessions), first.NextCursor)
	}
	if firstRows > 26 || work.snapshotRead != 0 || work.rebuilds != 0 {
		t.Fatalf("first page work = %+v, want <=26 catalog rows and no snapshot reads/rebuild", *work)
	}

	work.catalogRows = 0
	second, err := st.PageSessionMetadata(context.Background(), port.SessionMetadataPageRequest{
		Limit: 25, OwnershipEnforced: true, Owner: owner, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Sessions) != 25 || work.catalogRows > 26 || work.snapshotRead != 0 || work.rebuilds != 0 {
		t.Fatalf("second page work = %+v rows=%d, want direct <=26-row continuation", *work, len(second.Sessions))
	}
}

func TestSessionStorageContinuity_Scenario2_AdapterOpaqueCursor(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice"}
	st := installInventoryFixture(t, 4, owner)
	ctx := context.Background()
	request := port.SessionMetadataPageRequest{Limit: 2, OwnershipEnforced: true, Owner: owner}

	jsonlPage, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("jsonl first page: %v", err)
	}
	if jsonlPage.NextCursor == nil || jsonlPage.NextCursor.Continuation == "" {
		t.Fatalf("jsonl cursor = %+v, want opaque continuation", jsonlPage.NextCursor)
	}
	jsonlSecond, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: owner, Cursor: jsonlPage.NextCursor,
	})
	if err != nil || len(jsonlSecond.Sessions) != 2 {
		t.Fatalf("jsonl continuation: rows=%d err=%v", len(jsonlSecond.Sessions), err)
	}

	mem := memstore.New(memstore.WithNow(func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }))
	for i := 0; i < 4; i++ {
		sess := session.New(session.SessionID(fmt.Sprintf("memory-%04d", i)), session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
		if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatalf("label memory session: %v", err)
		}
		if err := mem.Save(ctx, sess); err != nil {
			t.Fatalf("save memory session: %v", err)
		}
	}
	memoryPage, err := mem.PageSessionMetadata(ctx, request)
	if err != nil || memoryPage.NextCursor == nil || memoryPage.NextCursor.Continuation == "" {
		t.Fatalf("memory cursor = %+v err=%v, want opaque continuation", memoryPage.NextCursor, err)
	}
	memorySecond, err := mem.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: owner, Cursor: memoryPage.NextCursor,
	})
	if err != nil || len(memorySecond.Sessions) != 2 {
		t.Fatalf("memory continuation: rows=%d err=%v", len(memorySecond.Sessions), err)
	}

	foreign := *jsonlPage.NextCursor
	foreign.Continuation = memoryPage.NextCursor.Continuation
	if _, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: owner, Cursor: &foreign,
	}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("jsonl with memory continuation error = %v, want restart", err)
	}

	foreign = *memoryPage.NextCursor
	foreign.Continuation = jsonlPage.NextCursor.Continuation
	if _, err := mem.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: owner, Cursor: &foreign,
	}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("memory with jsonl continuation error = %v, want restart", err)
	}
}

func TestSessionStorageContinuity_Scenario2_GenerationBoundCursor(t *testing.T) {
	alice := &session.Principal{Issuer: "issuer", Subject: "alice"}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob"}
	st := installInventoryFixture(t, 4, alice)
	ctx := context.Background()
	first, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 2, OwnershipEnforced: true, Owner: alice})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := []session.SessionID{first.Sessions[0].ID, first.Sessions[1].ID}; got[0] != "session-0000" || got[1] != "session-0001" {
		t.Fatalf("equal-time ordering = %v, want session id ascending", got)
	}

	_, err = st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: bob, Cursor: first.NextCursor,
	})
	if !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("owner-mismatched cursor error = %v, want restart", err)
	}

	path := st.resolver.currentSnapshotPath("new-generation")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write generation change: %v", err)
	}
	_, err = st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 2, OwnershipEnforced: true, Owner: alice, Cursor: first.NextCursor,
	})
	if !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("stale cursor error = %v, want restart", err)
	}
}

func TestSessionStorageContinuity_Scenario2_LargeInventoryWorkCounters(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "large-owner"}
	st := installInventoryFixture(t, 807, owner)
	work := observeInventoryWork(st)
	ctx := context.Background()
	first, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 100, OwnershipEnforced: true, Owner: owner})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Sessions) != 100 || work.catalogRows > 101 || work.snapshotRead != 0 || work.rebuilds != 0 {
		t.Fatalf("first large page rows=%d work=%+v", len(first.Sessions), *work)
	}
	work.catalogRows = 0
	second, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 100, OwnershipEnforced: true, Owner: owner, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Sessions) != 100 || work.catalogRows > 101 || work.snapshotRead != 0 || work.rebuilds != 0 {
		t.Fatalf("second large page traversed prior work: rows=%d work=%+v", len(second.Sessions), *work)
	}
}

func BenchmarkSessionStorageContinuity_LargeInventory(b *testing.B) {
	owner := &session.Principal{Issuer: "issuer", Subject: "benchmark"}
	st := installInventoryFixture(b, 807, owner)
	request := port.SessionMetadataPageRequest{Limit: 100, OwnershipEnforced: true, Owner: owner}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, err := st.PageSessionMetadata(ctx, request)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Sessions) != 100 {
			b.Fatalf("page rows = %d", len(page.Sessions))
		}
	}
}
