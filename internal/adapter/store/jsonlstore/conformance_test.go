package jsonlstore_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventlogconformance"
	"github.com/stacklok/mecatl/engine/adapter/storeconformance"
	"github.com/stacklok/mecatl/engine/port"
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
