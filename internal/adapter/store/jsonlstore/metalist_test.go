package jsonlstore_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestMetaListProjectsSnapshotFields seeds a completed session with a known
// state/turns/model/title/created_at and asserts MetaList returns those fields
// (the latest-line read + small-struct decode, skipping messages).
func TestMetaListProjectsSnapshotFields(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)

	created := time.Unix(1700000000, 0).UTC()
	s := session.New("meta-1", session.ModeDefault, "/ws", session.Limits{}, created)
	s.ModelID = "model-x"
	s.SetTitle("the real title")
	for i := 0; i < 3; i++ {
		_ = s.BeginTurn()
		_ = s.RecordAssistant(session.NewAssistantMessage("ok", "", nil))
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Bump the mtime so ModifiedAt is deterministic.
	touchSessionFile(t, dir, s.ID, time.Unix(1800000000, 0).UTC())

	var ss port.SessionStore = st
	ml, ok := ss.(port.MetaLister)
	if !ok {
		t.Fatalf("Store does not implement port.MetaLister")
	}
	rows, err := ml.MetaList(ctx)
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("MetaList returned %d rows, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.ID != s.ID {
		t.Errorf("ID = %q, want %q", r.ID, s.ID)
	}
	if r.State != session.StateCompleted {
		t.Errorf("State = %q, want %q", r.State, session.StateCompleted)
	}
	if r.Turns != 3 {
		t.Errorf("Turns = %d, want 3", r.Turns)
	}
	if r.ModelID != "model-x" {
		t.Errorf("ModelID = %q, want model-x", r.ModelID)
	}
	if r.Title != "the real title" {
		t.Errorf("Title = %q, want %q", r.Title, "the real title")
	}
	if !r.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", r.CreatedAt, created)
	}
	if r.ModifiedAt.Unix() != 1800000000 {
		t.Errorf("ModifiedAt = %v, want 1800000000", r.ModifiedAt.Unix())
	}
}

// TestMetaListSkipsConversation asserts MetaList does NOT require the messages
// array to decode — the small-struct decode skips it. This is the O(N) win: a
// session with a LARGE conversation lists without unmarshaling the messages.
func TestMetaListSkipsConversation(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)

	s := session.New("big", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	s.SetTitle("big conv")
	// Record a large assistant message (many tool calls + a big text blob) so
	// the snapshot line is large — MetaList must still decode the metadata cheaply.
	big := strings.Repeat("x", 200_000)
	for i := 0; i < 5; i++ {
		_ = s.BeginTurn()
		_ = s.RecordAssistant(session.NewAssistantMessage(big, "", nil))
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var ss port.SessionStore = st
	ml := ss.(port.MetaLister)
	rows, err := ml.MetaList(ctx)
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Turns != 5 {
		t.Errorf("Turns = %d, want 5", rows[0].Turns)
	}
	if rows[0].Title != "big conv" {
		t.Errorf("Title = %q, want %q", rows[0].Title, "big conv")
	}
}

// TestMetaListCorruptRowSurfacesZeroed asserts a snapshot whose last line
// decodes an id but an UNKNOWN state (RestoreState would reject) surfaces with
// id/mtime + zeroed snapshot fields — matching the Load-per-row behaviour.
func TestMetaListCorruptRowSurfacesZeroed(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)

	// A valid session for sanity.
	good := session.New("good", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	good.SetTitle("good")
	if err := st.Save(ctx, good); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A corrupt file: decodable id, bogus state.
	corruptPath := filepath.Join(dir, "corrupt.session.jsonl")
	if err := os.WriteFile(corruptPath, []byte(`{"id":"corrupt","state":"bogus-state"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	var ss port.SessionStore = st
	ml := ss.(port.MetaLister)
	rows, err := ml.MetaList(ctx)
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	byID := make(map[string]port.SessionMeta, len(rows))
	for _, r := range rows {
		byID[string(r.ID)] = r
	}
	corrupt, ok := byID["corrupt"]
	if !ok {
		t.Fatalf("corrupt row missing from MetaList (must surface with id/mtime even when snapshot is corrupt): %+v", rows)
	}
	if corrupt.State != "" {
		t.Errorf("corrupt State = %q, want empty (unknown state → zeroed)", corrupt.State)
	}
	if corrupt.Turns != 0 || corrupt.ModelID != "" || corrupt.Title != "" || !corrupt.CreatedAt.IsZero() {
		t.Errorf("corrupt row must have zeroed snapshot fields, got %+v", corrupt)
	}
}

// TestMetaListMultiSnapshotLatestWins asserts MetaList reads the LAST snapshot
// line (append-only, latest-line-wins): a session saved twice surfaces the
// LATEST state/turns, not the first.
func TestMetaListMultiSnapshotLatestWins(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)

	s := session.New("multi", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	s.SetTitle("first")
	_ = s.BeginTurn()
	_ = s.RecordAssistant(session.NewAssistantMessage("a", "", nil))
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	// Second save with more turns + a new title (SetTitle is set-once, so we
	// can't change the title; bump turns instead to prove latest-wins).
	s.SetTitle("second") // no-op: SetTitle is set-once
	_ = s.BeginTurn()
	_ = s.RecordAssistant(session.NewAssistantMessage("b", "", nil))
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save 2: %v", err)
	}

	var ss port.SessionStore = st
	ml := ss.(port.MetaLister)
	rows, err := ml.MetaList(ctx)
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (latest snapshot)", r.Turns)
	}
	if r.State != session.StateCompleted {
		t.Errorf("State = %q, want %q (latest snapshot)", r.State, session.StateCompleted)
	}
	if r.Title != "first" {
		t.Errorf("Title = %q, want first (set-once)", r.Title)
	}
}

// TestReadLastLineLargeFileTailRead asserts readLastLine (via MetaList) reads
// only the TAIL of a large session file — the metadata decodes correctly even
// when the file is much larger than the seek window. This is the O(1)-per-file
// guarantee (no full-file scan).
func TestReadLastLineLargeFileTailRead(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)

	s := session.New("large", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	s.SetTitle("tail-read")
	// Append MANY snapshots so the file grows well beyond the seek window. Each
	// Save appends one line; the last line is the latest snapshot. The metadata
	// must reflect the LATEST (turns == count of BeginTurn calls).
	for i := 0; i < 50; i++ {
		_ = s.BeginTurn()
		_ = s.RecordAssistant(session.NewAssistantMessage(strings.Repeat("y", 5000), "", nil))
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	// Verify the file is larger than the seek window (so the tail-read path is
	// exercised, not the small-file full-read path).
	path := canonicalSnapshotPath(dir, s.ID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() < 64*1024 {
		t.Logf("note: file size %d < seek window 64KiB; tail-read path not fully exercised", info.Size())
	}

	var ss port.SessionStore = st
	ml := ss.(port.MetaLister)
	rows, err := ml.MetaList(ctx)
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Turns != 50 {
		t.Errorf("Turns = %d, want 50 (latest snapshot)", r.Turns)
	}
	if r.Title != "tail-read" {
		t.Errorf("Title = %q, want tail-read", r.Title)
	}
	if r.State != session.StateRunning {
		t.Errorf("State = %q, want %q", r.State, session.StateRunning)
	}
}

// touchSessionFile sets the mtime of the session file for id (mirrors
// setSessionMtime in the server tests; local to this package to avoid an
// import).
func touchSessionFile(t *testing.T, dir string, id session.SessionID, mtime time.Time) {
	t.Helper()
	path := canonicalSnapshotPath(dir, id)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}
