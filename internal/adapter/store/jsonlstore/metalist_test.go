package jsonlstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
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
	if err := s.RestoreSessionMetadata(session.SessionKindSubagent, session.SessionRelationship{
		ParentSessionID: "parent-1",
		CallID:          "call-1",
	}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
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

	// MetaList is the legacy compatibility seam and intentionally keeps its old
	// projection. The discovery pager carries the additive taxonomy and workspace.
	pager := ss.(port.SessionMetadataPager)
	page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("PageSessionMetadata: %v", err)
	}
	if len(page.Sessions) != 1 {
		t.Fatalf("PageSessionMetadata returned %d rows, want 1: %+v", len(page.Sessions), page.Sessions)
	}
	discovery := page.Sessions[0]
	if discovery.Workspace != "/ws" || discovery.Kind != session.SessionKindSubagent {
		t.Errorf("discovery metadata = workspace %q kind %q", discovery.Workspace, discovery.Kind)
	}
	if discovery.Relationship.ParentSessionID != "parent-1" || discovery.Relationship.CallID != "call-1" {
		t.Errorf("discovery relationship = %+v", discovery.Relationship)
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
	// Seed a historical v1 file with MANY snapshots so the bounded tail reader
	// is exercised independently of the v2 current-snapshot writer.
	var history bytes.Buffer
	for i := 0; i < 50; i++ {
		_ = s.BeginTurn()
		_ = s.RecordAssistant(session.NewAssistantMessage(strings.Repeat("y", 5000), "", nil))
		line, err := sessnap.Marshal(s)
		if err != nil {
			t.Fatalf("Marshal %d: %v", i, err)
		}
		history.Write(line)
		history.WriteByte('\n')
	}
	path := canonicalFamilyPath(dir, s.ID, ".session.jsonl")
	if err := os.WriteFile(path, history.Bytes(), 0o600); err != nil {
		t.Fatalf("write v1 history: %v", err)
	}
	// Verify the file is larger than the seek window (so the tail-read path is
	// exercised, not the small-file full-read path).
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
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatalf("Unmarshal %s: %v", path, err)
	}
	current["modified_at"], err = json.Marshal(mtime)
	if err != nil {
		t.Fatalf("Marshal modified_at: %v", err)
	}
	data, err = json.Marshal(current)
	if err != nil {
		t.Fatalf("Marshal current snapshot: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}
