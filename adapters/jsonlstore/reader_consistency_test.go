package jsonlstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func readerFixture(t *testing.T) (*Store, *Reader, *session.Session) {
	t.Helper()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var s *session.Session
	for _, id := range []session.SessionID{"a", "b", "c"} {
		s = lineageTestSession(t, id, session.SessionKindMain, session.SessionRelationship{})
		if err := st.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.MetaList(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(st.resolver.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st, r, s
}

func TestOpenReaderMissingIndexLocksAreNotCreated(t *testing.T) {
	for _, index := range []string{"catalog", "lineage"} {
		t.Run(index, func(t *testing.T) {
			_, r, s := readerFixture(t)
			path := filepath.Join(r.store.inventoryCatalogDir(), inventoryCatalogLockName)
			if index == "lineage" {
				path = lineageLockPath(r.store.lineageRecordPath(s.ID))
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			var err error
			if index == "catalog" {
				_, err = r.MetaList(t.Context())
			} else {
				_, err = r.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: s.ID, RootIncarnation: s.Incarnation(), Limit: 10})
			}
			if err == nil {
				t.Fatal("missing lock accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("lock created: %v", err)
			}
		})
	}
}

func TestOpenReaderRejectsInvalidationDuringRead(t *testing.T) {
	for _, operation := range []string{"meta", "first-page", "next-page", "stale-cursor"} {
		t.Run(operation, func(t *testing.T) {
			writer, r, s := readerFixture(t)
			req := port.SessionMetadataPageRequest{Limit: 1}
			if operation == "next-page" || operation == "stale-cursor" {
				page, err := r.PageSessionMetadata(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				req.Cursor = page.NextCursor
			}
			mutate := func() {
				if err := writer.Save(t.Context(), s); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "stale-cursor" {
				mutate()
			} else {
				r.store.inventoryWorkObserver = func(kind inventoryWorkKind) {
					if kind == inventoryWorkCatalogRow {
						r.store.inventoryWorkObserver = nil
						mutate()
					}
				}
			}
			var err error
			if operation == "meta" {
				_, err = r.MetaList(t.Context())
			} else {
				_, err = r.PageSessionMetadata(t.Context(), req)
			}
			if err == nil {
				t.Fatal("returned an invalidated catalog")
			}
			if req.Cursor != nil && !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
				t.Fatalf("cursor invalidation: %v", err)
			}
		})
	}
}

func TestOpenReaderTruncatedScopeDoesNotClaimCompleteness(t *testing.T) {
	for _, nextPage := range []bool{false, true} {
		for _, damage := range []string{"truncated", "corrupt", "missing"} {
			t.Run(damage+map[bool]string{false: "-first", true: "-next"}[nextPage], func(t *testing.T) {
				_, r, _ := readerFixture(t)
				req := port.SessionMetadataPageRequest{Limit: 10}
				if nextPage {
					first, err := r.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 1})
					if err != nil {
						t.Fatal(err)
					}
					req.Cursor = first.NextCursor
				}
				catalog, err := r.store.readOnlyInventoryManifest(nil)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(r.store.inventoryCatalogDir(), catalog.Scopes[inventoryGlobalScope].File)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.SplitAfter(string(data), "\n")
				switch damage {
				case "truncated":
					n := 1
					if nextPage {
						n = 2
					}
					err = os.WriteFile(path, []byte(strings.Join(lines[:n], "")), 0o600)
				case "corrupt":
					err = os.WriteFile(path, []byte(strings.Join(lines[:1], "")+"broken\n"), 0o600)
				case "missing":
					err = os.Remove(path)
				}
				if err != nil {
					t.Fatal(err)
				}
				page, err := r.PageSessionMetadata(t.Context(), req)
				if err == nil || len(page.Sessions) != 0 {
					t.Fatalf("damaged scope returned page %+v, %v", page, err)
				}
			})
		}
	}
}

func TestOpenReaderPagesRemainBounded(t *testing.T) {
	writer := installInventoryFixture(t, 100, nil)
	// The fixture writes its catalog directly; prepare only the writer's lock.
	if err := writer.withInventoryCatalogLock(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(writer.resolver.dir)
	if err != nil {
		t.Fatal(err)
	}
	work := observeInventoryWork(r.store)
	req := port.SessionMetadataPageRequest{Limit: 2}
	for range 2 {
		page, err := r.PageSessionMetadata(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) != 2 || page.NextCursor == nil {
			t.Fatalf("page: %+v", page)
		}
		req.Cursor = page.NextCursor
	}
	if work.catalogRows != 6 || work.snapshotRead != 0 || work.rebuilds != 0 {
		t.Fatalf("unbounded work: %+v", work)
	}
}

// A deterministic cancellation context lets tests interrupt decoding between
// records rather than depending on scheduling or a large fixture.
type cancelAfterChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestOpenReaderCancellationDuringTraversal(t *testing.T) {
	_, r, s := readerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List: %v", err)
	}
	if _, err := r.List(&cancelAfterChecks{Context: t.Context(), remaining: 3}); !errors.Is(err, context.Canceled) {
		t.Fatalf("List traversal: %v", err)
	}
	for _, meta := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		r.store.inventoryWorkObserver = func(inventoryWorkKind) { cancel() }
		var err error
		if meta {
			_, err = r.MetaList(ctx)
		} else {
			_, err = r.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 2})
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("metadata traversal: %v", err)
		}
	}
	query := port.SessionLineageQuery{RootID: s.ID, RootIncarnation: s.Incarnation(), Limit: 10}
	if _, err := r.ReadSessionLineage(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("lineage: %v", err)
	}
	_, _, err := readLineagePartition(&cancelAfterChecks{Context: t.Context(), remaining: 2}, r.store.lineageRecordPath(s.ID), lineageRecordFormat, 10, true, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lineage decode: %v", err)
	}
}

func TestOpenReaderCancellationDuringEventReplay(t *testing.T) {
	writer, r, s := readerFixture(t)
	for range 3 {
		if err := writer.Append(t.Context(), s.ID, session.Event{Type: session.EvResult}); err != nil {
			t.Fatal(err)
		}
	}
	for _, cursor := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		count := 0
		var got error
		if cursor {
			for _, err := range r.ReadAfter(ctx, s.ID, "", port.ReadOptions{}) {
				if err != nil {
					got = err
					break
				}
				count++
				cancel()
			}
		} else {
			for _, err := range r.Read(ctx, s.ID) {
				if err != nil {
					got = err
					break
				}
				count++
				cancel()
			}
		}
		cancel()
		if count != 1 || !errors.Is(got, context.Canceled) {
			t.Fatalf("cursor=%v count=%d err=%v", cursor, count, got)
		}
	}
	f, size, err := r.store.openEventFileLocked(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _, err = decodeEventPage(&cancelAfterChecks{Context: t.Context(), remaining: 2}, s.ID, f, logBasis{}, 0, size, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("event decode: %v", err)
	}
}
