package redisstore

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type metadataWorkCounts struct {
	loads int
	rows  int
}

func TestPageSessionMetadataWorkIsBoundedAndDoesNotLoadSnapshots(t *testing.T) {
	st, _ := newMetadataTestStore(t)
	ctx := context.Background()
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	for i := range 8 {
		s := session.New(session.SessionID("session-"+string(rune('a'+i))), session.ModeAccept, "/work", session.Limits{}, time.Now().UTC())
		if err := s.RestoreLabels(owner, ""); err != nil {
			t.Fatalf("RestoreLabels: %v", err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	work := &metadataWorkCounts{}
	st.metadataWorkObserver = func(kind metadataWorkKind) {
		switch kind {
		case metadataWorkLoad:
			work.loads++
		case metadataWorkRow:
			work.rows++
		}
	}
	request := port.SessionMetadataPageRequest{Limit: 2, OwnershipEnforced: true, Owner: owner}
	first, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("PageSessionMetadata(first): %v", err)
	}
	if work.loads != 0 || work.rows > request.Limit+1 {
		t.Fatalf("first-page work = loads:%d rows:%d, want zero loads and <= %d rows", work.loads, work.rows, request.Limit+1)
	}
	if len(first.Sessions) != request.Limit || first.NextCursor == nil || first.TotalCount != 8 {
		t.Fatalf("first page = %+v, want 2/8 rows plus cursor", first)
	}

	*work = metadataWorkCounts{}
	request.Cursor = first.NextCursor
	second, err := st.PageSessionMetadata(ctx, request)
	if err != nil {
		t.Fatalf("PageSessionMetadata(second): %v", err)
	}
	if work.loads != 0 || work.rows > request.Limit+1 {
		t.Fatalf("second-page work = loads:%d rows:%d, want zero loads and <= %d rows", work.loads, work.rows, request.Limit+1)
	}
	if len(second.Sessions) != request.Limit || second.Sessions[0].ID == first.Sessions[0].ID || second.Sessions[0].ID == first.Sessions[1].ID {
		t.Fatalf("second page traversed or repeated prior rows: first=%v second=%v", first.Sessions, second.Sessions)
	}
}

func TestMetadataMemberOrderingUsesModifiedDescThenIDAsc(t *testing.T) {
	modifiedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	members := make([]string, 0, 4)
	for _, id := range []session.SessionID{"aa", "b", "a"} {
		member, err := encodeMetadataMember(port.SessionDiscoveryMeta{ID: id, ModifiedAt: modifiedAt})
		if err != nil {
			t.Fatalf("encodeMetadataMember(%q): %v", id, err)
		}
		members = append(members, member)
	}
	newest, err := encodeMetadataMember(port.SessionDiscoveryMeta{ID: "z", ModifiedAt: modifiedAt.Add(time.Second)})
	if err != nil {
		t.Fatalf("encodeMetadataMember(newest): %v", err)
	}
	members = append(members, newest)
	slices.Sort(members)
	for i, want := range []session.SessionID{"z", "a", "aa", "b"} {
		row, err := decodeMetadataMember(members[i])
		if err != nil {
			t.Fatalf("decodeMetadataMember: %v", err)
		}
		if row.ID != want {
			t.Fatalf("ordered row %d = %q, want %q", i, row.ID, want)
		}
	}
}

func TestDeleteRemovesMetadataAndInvalidatesCursor(t *testing.T) {
	st, _ := newMetadataTestStore(t)
	ctx := context.Background()
	for _, id := range []session.SessionID{"delete-a", "delete-b"} {
		if err := st.Save(ctx, session.New(id, session.ModeAccept, "/work", session.Limits{}, time.Now().UTC())); err != nil {
			t.Fatalf("Save(%q): %v", id, err)
		}
	}
	first, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if err := st.Delete(ctx, first.Sessions[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("stale cursor error = %v, want restart", err)
	}
	page, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("PageSessionMetadata(after delete): %v", err)
	}
	if page.TotalCount != 1 || len(page.Sessions) != 1 {
		t.Fatalf("page after delete = %+v, want one row", page)
	}
}

func TestLegacyStoreReportsMetadataPagingUnsupported(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	mr.HSet(sessionKey("legacy"), fieldBlob, `{}`, fieldMtime, "1")
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.PageSessionMetadata(context.Background(), port.SessionMetadataPageRequest{Limit: 10}); !errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
		t.Fatalf("legacy PageSessionMetadata error = %v, want unsupported", err)
	}
}

func newMetadataTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, mr
}
