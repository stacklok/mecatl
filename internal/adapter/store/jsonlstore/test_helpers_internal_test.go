package jsonlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func newInternalStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st
}

func snapshotLine(t *testing.T, id session.SessionID, title string) []byte {
	t.Helper()
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "current"}, session.Limits{}, time.Unix(1, 0).UTC())
	s.SetTitle(title)
	line, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatalf("sessnap.Marshal: %v", err)
	}
	return line
}

func writeBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func collectEvents(t *testing.T, st *Store, id session.SessionID) []session.Event {
	t.Helper()
	var events []session.Event
	for ev, err := range st.Read(context.Background(), id) {
		if err != nil {
			t.Fatalf("Read(%q): %v", id, err)
		}
		events = append(events, ev)
	}
	return events
}

func eventRecordLine(t *testing.T, ev session.Event) []byte {
	t.Helper()
	header, err := json.Marshal(eventLogRecord{V: eventLogGenerationTag, G: "test-generation"})
	if err != nil {
		t.Fatalf("marshal generation header: %v", err)
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	line, err := json.Marshal(eventLogRecord{V: eventLogFormat, Ev: evJSON})
	if err != nil {
		t.Fatalf("marshal event record: %v", err)
	}
	return append(append(header, '\n'), line...)
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s bytes changed: got %q want %q", path, got, want)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists or stat failed unexpectedly: %v", path, err)
	}
}
