package jsonlstore_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventlogconformance"
	"github.com/stacklok/mecatl/engine/adapter/lineageconformance"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestJSONLStoreConformance runs the shared SessionStore conformance table
// against the append-only JSONL replay store (the osfs/fsconformance
// precedent for an internal adapter importing an engine/adapter test suite).
func TestJSONLStoreConformance(t *testing.T) {
	storeconformance.Run(t, func(t *testing.T) port.SessionStore {
		st, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	})
}

func TestJSONLStoreLineageConformance(t *testing.T) {
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	lineageconformance.Run(t, st)
	reopened, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := reopened.Load(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.ReadSessionLineage(t.Context(), port.SessionLineageQuery{RootID: "root", RootIncarnation: root.Incarnation(), Limit: 10})
	if err != nil || len(result.Records) != 2 || result.Records[0].ID != "root" {
		t.Fatalf("lineage after restart: records=%+v err=%v", result.Records, err)
	}
}

func TestJSONLStoreSessionCreatorConformance(t *testing.T) {
	storeconformance.RunSessionCreator(t, func(t *testing.T) (port.SessionStore, port.SessionStore) {
		dir := t.TempDir()
		first, err := jsonlstore.New(dir)
		if err != nil {
			t.Fatalf("jsonlstore.New(first): %v", err)
		}
		second, err := jsonlstore.New(dir)
		if err != nil {
			t.Fatalf("jsonlstore.New(second): %v", err)
		}
		return first, second
	})
}

// TestJSONLStorePrunableConformance runs the shared PrunableStore (retention
// seam) table against the JSONL replay store.
func TestJSONLStorePrunableConformance(t *testing.T) {
	storeconformance.RunPrunable(t, func(t *testing.T) port.SessionStore {
		st, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	})
}

func TestSessionContinuityUX_Scenario3_PagerConformance(t *testing.T) {
	storeconformance.RunMetadataPager(t, func(t *testing.T) port.SessionStore {
		st, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	})
}

func TestJSONLStoreConditionalPrunableConformance(t *testing.T) {
	storeconformance.RunConditionalPrunable(t, func(t *testing.T) port.SessionStore {
		st, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	})
}

// TestJSONLStoreEventLogConformance runs the shared EventLog conformance table
// against the JSONL replay store (the same Store that doubles as SessionStore):
// this is the LOCAL/reference half of the dual-path contract, run against the
// SAME suite the gRPC driver client passes over bufconn (cloud-native 3c).
func TestJSONLStoreEventLogConformance(t *testing.T) {
	eventlogconformance.Run(t, func(t *testing.T) port.EventLog {
		st, err := jsonlstore.New(t.TempDir())
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	})
}

// TestJSONLStoreCursorEventLogConformance runs the shared CursorEventLog table
// against the JSONL store — the on-disk half of ADR 0250's cross-process
// obligation, where cursors are byte offsets and follow is size-polling.
//
// NewPair returns two Stores over the SAME directory, which is what makes the
// cross-reader subtest meaningful: the two share no Go state whatsoever, so the
// only way the reader can observe the writer's appends is through the durable
// file. That is precisely the property a second replica needs.
func TestJSONLStoreCursorEventLogConformance(t *testing.T) {
	newStore := func(t *testing.T, dir string) port.CursorEventLog {
		t.Helper()
		st, err := jsonlstore.New(dir)
		if err != nil {
			t.Fatalf("jsonlstore.New: %v", err)
		}
		return st
	}
	eventlogconformance.RunCursor(t, eventlogconformance.CursorSuite{
		New: func(t *testing.T) port.CursorEventLog {
			return newStore(t, t.TempDir())
		},
		Reset: func(t *testing.T, log port.CursorEventLog, id session.SessionID) {
			t.Helper()
			// Deleting the session removes its event file; the next append mints
			// a fresh generation, exactly as a rebuilt log would.
			if err := log.(*jsonlstore.Store).Delete(context.Background(), id); err != nil {
				t.Fatalf("Delete(%q): %v", id, err)
			}
		},
		NewPair: func(t *testing.T) (port.CursorEventLog, port.CursorEventLog) {
			dir := t.TempDir()
			return newStore(t, dir), newStore(t, dir)
		},
	})
}
