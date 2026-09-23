package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

type previousWriterEnvelope struct {
	Snapshot struct {
		ID session.SessionID `json:"id"`
	} `json:"snapshot"`
}

func installPreviousWriterSnapshot(t *testing.T, st *jsonlstore.Store, dir, name string) session.SessionID {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "store", "jsonlstore", "testdata", "previous-writer", name))
	if err != nil {
		t.Fatalf("read previous-writer fixture: %v", err)
	}
	var envelope previousWriterEnvelope
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatalf("decode previous-writer fixture: %v", err)
	}
	placeholder := session.New(envelope.Snapshot.ID, session.ModeDefault, session.EnvironmentRef{
		Kind: session.EnvKindLocal, ID: "/fixture-workspace", Revision: "in-tree-v1",
	}, session.Limits{}, time.Unix(1, 0).UTC())
	if err := st.Save(context.Background(), placeholder); err != nil {
		t.Fatalf("seed current snapshot path: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "sid-v1", "*.session.json"))
	if err != nil {
		t.Fatalf("glob snapshots: %v", err)
	}
	for _, path := range paths {
		candidate, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var current previousWriterEnvelope
		if json.Unmarshal(candidate, &current) == nil && current.Snapshot.ID == envelope.Snapshot.ID {
			if err := os.WriteFile(path, fixture, 0o600); err != nil {
				t.Fatalf("install previous-writer fixture: %v", err)
			}
			return envelope.Snapshot.ID
		}
	}
	t.Fatalf("current snapshot path for %q not found", envelope.Snapshot.ID)
	return ""
}

// The fixtures were emitted unchanged by jsonlstore at
// ad1cfe3c89a640905b88fb69f9905df498ba5c9c, the writer immediately before the
// cleanup. They pin the real sessnap-current-json/2 envelope and payload.
func TestPreviousCurrentWriterSessionsListAndOpenTranscript(t *testing.T) {
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	fixtures := []string{
		"completed-spend.session.json",
		"retryable-failure.session.json",
		"permanent-failure.session.json",
		"zero-token-fresh.session.json",
	}
	ids := make(map[session.SessionID]bool, len(fixtures))
	for _, fixture := range fixtures {
		ids[installPreviousWriterSnapshot(t, st, dir, fixture)] = true
	}
	paths, err := filepath.Glob(filepath.Join(dir, "sid-v1", "*.session.json"))
	if err != nil {
		t.Fatalf("glob installed snapshots: %v", err)
	}
	before := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed snapshot: %v", err)
		}
		before[path] = string(data)
	}

	svc := listSessionsService(t, st)
	rows, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != len(fixtures) {
		t.Fatalf("ListSessions returned %d rows, want %d: %+v", len(rows), len(fixtures), rows)
	}
	for _, row := range rows {
		if !ids[session.SessionID(row.SessionID)] {
			t.Errorf("unexpected list row %q", row.SessionID)
		}
	}

	transcript, err := svc.GetTranscript(context.Background(), "previous-completed-spend")
	if err != nil {
		t.Fatalf("GetTranscript(previous current writer): %v", err)
	}
	if !transcript.Complete || len(transcript.Messages) != 2 || transcript.Messages[0].Text != "yesterday prompt" || transcript.Messages[1].Text != "yesterday answer" {
		t.Fatalf("transcript = %+v", transcript)
	}

	completed, err := st.Load(context.Background(), "previous-completed-spend")
	if err != nil {
		t.Fatalf("Load completed: %v", err)
	}
	wantUsage := session.Usage{InputTokens: 21, OutputTokens: 8, CacheReadTokens: 3}
	if got := completed.UsageFor(session.UsageKindMain); got != wantUsage {
		t.Fatalf("completed usage = %+v, want %+v", got, wantUsage)
	}
	for id, want := range map[session.SessionID]session.RetryDisposition{
		"previous-retryable-failure": session.RetryDispositionRetryable,
		"previous-permanent-failure": session.RetryDispositionPermanent,
	} {
		loaded, err := st.Load(context.Background(), id)
		if err != nil {
			t.Fatalf("Load(%s): %v", id, err)
		}
		if got := loaded.FailureMetadata().Disposition; got != want {
			t.Errorf("Load(%s) retry disposition = %v, want %v", id, got, want)
		}
	}
	fresh, err := st.Load(context.Background(), "previous-zero-token-fresh")
	if err != nil {
		t.Fatalf("Load zero-token fresh: %v", err)
	}
	if got := fresh.UsageFor(session.UsageKindMain); got != (session.Usage{}) {
		t.Fatalf("zero-token usage = %+v", got)
	}
	for path, want := range before {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read snapshot after load: %v", err)
		}
		if string(data) != want {
			t.Errorf("snapshot %s was rewritten while loading", filepath.Base(path))
		}
	}
}
