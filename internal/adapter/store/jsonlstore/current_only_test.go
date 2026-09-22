package jsonlstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestNonCurrentSnapshotArtifactsAreIgnoredUntouched(t *testing.T) {
	dir := t.TempDir()
	id := session.SessionID("poison")
	oldFiles := map[string][]byte{
		filepath.Join(dir, "legacy.session.json"):                                      []byte("old-root-snapshot"),
		filepath.Join(dir, "legacy.events.jsonl"):                                      []byte("old-root-events"),
		filepath.Join(dir, "legacy.tools.jsonl"):                                       []byte("old-root-tools"),
		filepath.Join(dir, canonicalDirName, encodeSessionToken(id)+sessionFileSuffix): []byte("old-canonical-snapshot"),
	}
	for path, contents := range oldFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	st, err := New(dir)
	if err != nil {
		t.Fatalf("New with old artifacts: %v", err)
	}
	if _, err := st.Load(context.Background(), id); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("Load old-only id = %v, want not found", err)
	}
	if rows, err := st.List(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("List with old artifacts = (%+v, %v), want empty", rows, err)
	}
	current := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/current", Revision: "current"}, session.Limits{}, time.Unix(2, 0).UTC())
	if err := st.Create(context.Background(), current); err != nil {
		t.Fatalf("Create same id in current namespace: %v", err)
	}
	if _, err := st.AppendEvent(context.Background(), id, session.Event{Type: session.EvResult}); err != nil {
		t.Fatalf("AppendEvent in current namespace: %v", err)
	}
	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	loaded, err := reopened.Load(context.Background(), id)
	if err != nil || loaded.EnvironmentRef != current.EnvironmentRef {
		t.Fatalf("Load current after reopen = (%+v, %v)", loaded, err)
	}
	if rows, err := reopened.List(context.Background()); err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("List current after reopen = (%+v, %v)", rows, err)
	}
	for path, want := range oldFiles {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("old artifact %q changed: bytes=%q err=%v", path, got, err)
		}
	}
}

func TestCurrentSnapshotCorruptionFailsListAndLoad(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := session.SessionID("current-corrupt")
	if err := os.WriteFile(st.resolver.currentSnapshotPath(id), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(context.Background(), id); err == nil {
		t.Fatal("Load accepted corrupt current snapshot")
	}
	if _, err := st.List(context.Background()); err == nil {
		t.Fatal("List hid corrupt current snapshot")
	}
}

func TestCurrentUnversionedEventLogIsRejectedUntouched(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := session.SessionID("old-event-log")
	path := st.resolver.canonicalPath(id, kindEvents)
	original := []byte(`{"v":"eventlog-json/1","ev":{"type":"result"}}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write old event log: %v", err)
	}
	var eventReadErr error
	for _, err := range st.Read(context.Background(), id) {
		if err != nil {
			eventReadErr = err
			break
		}
	}
	if eventReadErr == nil || !strings.Contains(eventReadErr.Error(), "unsupported unversioned event log") {
		t.Fatalf("Read error = %v", eventReadErr)
	}
	_, err = st.AppendEvent(context.Background(), id, session.Event{Type: session.EvResult})
	if err == nil || !strings.Contains(err.Error(), "unsupported unversioned event log") {
		t.Fatalf("AppendEvent error = %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("rejected append changed event log: bytes=%q err=%v", got, readErr)
	}
}
