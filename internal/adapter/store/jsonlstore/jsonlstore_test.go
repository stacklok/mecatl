package jsonlstore_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func newStore(t *testing.T) (*jsonlstore.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st, dir
}

// TestNewCreatesDirAt0700 pins the privacy posture (issue #79): the store holds
// raw conversation transcripts in plaintext, so New creates a not-yet-existing
// store dir owner-only (mode 0700).
// canonicalFamilyPath re-derives the on-disk family stem INDEPENDENTLY of the
// production encoder. It is a deliberate second implementation: calling
// jsonlstore's own encodeSessionToken would make every layout assertion in this
// file vacuous (the test would agree with the code by construction, however
// wrong both were). Keep it hand-written, and if it ever disagrees with the
// package, decide which one is right rather than deleting this.
//
// The scheme: "sid-v1-" + up to 40 sanitized chars of the id (anything outside
// [A-Za-z0-9-_] becomes '_', "id" when nothing survives) + "-" + 32 hex chars
// of SHA-256 over the whole id.
func canonicalFamilyPath(dir string, id session.SessionID, suffix string) string {
	var b strings.Builder
	for _, r := range string(id) {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	prefix := b.String()
	if prefix == "" {
		prefix = "id"
	}
	sum := sha256.Sum256([]byte(id))
	token := "sid-v1-" + prefix + "-" + hex.EncodeToString(sum[:16])
	return filepath.Join(dir, "sid-v1", token+suffix)
}

func canonicalSnapshotPath(dir string, id session.SessionID) string {
	return canonicalFamilyPath(dir, id, ".session.json")
}

func TestCreateCollisionLeavesCurrentFamilyUntouched(t *testing.T) {
	ctx := context.Background()
	first, dir := newStore(t)
	second, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("New(second): %v", err)
	}
	winner := session.New("create-collision", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/winner", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0).UTC())
	if err := first.Save(ctx, winner); err != nil {
		t.Fatalf("Save(winner): %v", err)
	}
	if err := first.Append(ctx, winner.ID, session.Event{Type: session.EvResult, Seq: 7}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	first.ToolCall(winner.ID, session.NewToolCall("call-1", "Read", json.RawMessage(`{"path":"a"}`)), session.NewToolResult("call-1", "ok"), 0, 0)

	paths := []string{
		canonicalSnapshotPath(dir, winner.ID),
		canonicalFamilyPath(dir, winner.ID, ".events.jsonl"),
		canonicalFamilyPath(dir, winner.ID, ".tools.jsonl"),
	}
	before := make([][]byte, len(paths))
	beforeInfo := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		before[i], err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", filepath.Base(path), err)
		}
		beforeInfo[i], err = os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s): %v", filepath.Base(path), err)
		}
	}

	loser := session.New(winner.ID, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/loser", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 9}, time.Unix(2, 0).UTC())
	if err := second.Create(ctx, loser); !errors.Is(err, port.ErrSessionAlreadyExists) {
		t.Fatalf("Create(collision) = %v, want ErrSessionAlreadyExists", err)
	}
	for i, path := range paths {
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile after collision (%s): %v", filepath.Base(path), readErr)
		}
		if !reflect.DeepEqual(after, before[i]) {
			t.Errorf("%s changed on collision", filepath.Base(path))
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat after collision (%s): %v", filepath.Base(path), statErr)
		}
		if !info.ModTime().Equal(beforeInfo[i].ModTime()) || info.Size() != beforeInfo[i].Size() {
			t.Errorf("%s metadata changed on collision", filepath.Base(path))
		}
	}
}

func TestNewCreatesDirAt0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store") // a fresh path New must create
	if _, err := jsonlstore.New(dir); err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir mode = %o, want 0700 (owner-only; the store holds plaintext transcripts)", perm)
	}
	canonicalInfo, err := os.Stat(filepath.Join(dir, "sid-v1"))
	if err != nil {
		t.Fatalf("Stat canonical dir: %v", err)
	}
	if perm := canonicalInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("canonical dir mode = %o, want 0700", perm)
	}
}

func driven(t *testing.T) *session.Session {
	t.Helper()
	s := session.New("sess-1", session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{
		MaxTurns: 7, MaxToolCalls: 11, MaxConsecutiveFailures: 4,
	}, time.Unix(1700000000, 0).UTC())
	_ = s.BeginTurn()
	_ = s.RecordAssistant(session.NewAssistantMessage("plan", "rsn", []session.ToolCall{
		session.NewToolCall("c1", "Grep", json.RawMessage(`{"q":"foo"}`)),
	}))
	_ = s.RecordToolResults([]session.ToolResult{session.NewToolResult("c1", "hit")})
	return s
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	want := driven(t)
	if err := st.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != want.State || got.Mode != want.Mode ||
		got.Limits != want.Limits || got.Counters != want.Counters {
		t.Fatalf("scalar mismatch: got %+v want %+v", got, want)
	}
	if !reflect.DeepEqual(got.Conversation, want.Conversation) {
		t.Fatalf("conversation mismatch:\n got %+v\nwant %+v", got.Conversation, want.Conversation)
	}
}

func TestCurrentSnapshotLatestWins(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	s := driven(t)
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save#1: %v", err)
	}
	// Advance the session and save again.
	_ = s.PauseForApproval(session.PendingAsk{AskID: "a1", Tool: "Shell", Reason: "approve"})
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save#2: %v", err)
	}

	// Repeated saves replace one current v2 snapshot.
	path := canonicalSnapshotPath(dir, s.ID)
	if n := countLines(t, path); n != 1 {
		t.Fatalf("session file has %d lines, want one current snapshot", n)
	}

	// Load returns the latest snapshot (awaiting, with pending ask).
	got, err := st.Load(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != session.StateAwaiting {
		t.Fatalf("loaded state = %q, want awaiting (latest)", got.State)
	}
	ask, ok := got.PendingAsk()
	if !ok || ask.AskID != "a1" {
		t.Fatalf("pending ask = %+v,%v; want a1", ask, ok)
	}
}

func TestResumePausedAwaitingSession(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	s := driven(t)
	ask := session.PendingAsk{AskID: "ask-9", Tool: "Edit", Args: json.RawMessage(`{"p":"f"}`), Reason: "write"}
	if err := s.PauseForApproval(ask); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := st.Load(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotAsk, ok := got.PendingAsk()
	if !ok {
		t.Fatalf("reloaded session has no pending ask; cannot resume")
	}
	if gotAsk.AskID != ask.AskID || gotAsk.Tool != ask.Tool ||
		gotAsk.Reason != ask.Reason || string(gotAsk.Args) != string(ask.Args) {
		t.Fatalf("pending ask = %+v, want %+v", gotAsk, ask)
	}
}

func TestLoadNotFound(t *testing.T) {
	st, _ := newStore(t)
	_, err := st.Load(context.Background(), "ghost")
	if !errors.Is(err, jsonlstore.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestToolCallLogParseable(t *testing.T) {
	st, dir := newStore(t)
	call := session.NewToolCall("call-7", "Shell", json.RawMessage(`{"cmd":"ls"}`))
	res := session.NewToolResult("call-7", "file.txt")
	st.ToolCall("sess-1", call, res, 800*time.Microsecond, 1500*time.Microsecond)
	st.ToolCall("sess-1", session.NewToolCall("call-8", "Read", nil),
		session.NewToolError("call-8", "nope"), 0, 42*time.Microsecond)

	path := canonicalFamilyPath(dir, "sess-1", ".tools.jsonl")
	if n := countLines(t, path); n != 2 {
		t.Fatalf("tools file has %d lines, want 2", n)
	}

	lines := readLines(t, path)
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("first record not parseable: %v", err)
	}
	if rec["type"] != "tool_call" {
		t.Errorf("type = %v, want tool_call", rec["type"])
	}
	if rec["tool"] != "Shell" {
		t.Errorf("tool = %v, want Shell", rec["tool"])
	}
	if rec["call_id"] != "call-7" {
		t.Errorf("call_id = %v, want call-7", rec["call_id"])
	}
	if rec["result"] != "file.txt" {
		t.Errorf("result = %v, want file.txt", rec["result"])
	}
	if rec["took_micros"].(float64) != 1500 {
		t.Errorf("took_micros = %v, want 1500", rec["took_micros"])
	}
	if rec["queued_micros"].(float64) != 800 {
		t.Errorf("queued_micros = %v, want 800", rec["queued_micros"])
	}

	var rec2 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &rec2); err != nil {
		t.Fatalf("second record not parseable: %v", err)
	}
	if rec2["is_error"] != true {
		t.Errorf("is_error = %v, want true", rec2["is_error"])
	}
}

func TestSessionIDSanitizedToSafeFilename(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	s := session.New("../escape/../x", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/w", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0).UTC())
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// File must live directly under dir, not escape it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		// The sanitized name must stay a single path element (no traversal).
		if filepath.Base(e.Name()) != e.Name() {
			t.Errorf("store entry %q escaped the store dir", e.Name())
		}
	}
	// Round-trip still works via the original id.
	if _, err := st.Load(ctx, "../escape/../x"); err != nil {
		t.Fatalf("Load after sanitize: %v", err)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	return len(readLines(t, path))
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return lines
}

// TestListDecodesRealIDAndMtime pins that List returns the logical id from the
// snapshot and the authoritative snapshot file's mtime.
func TestListDecodesRealIDAndMtime(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	const id = session.SessionID("team-abc/lead") // sanitized on disk, real in the snapshot
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List returned %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].ID != id {
		t.Errorf("List id = %q, want the REAL snapshot id %q (filename-derived ids are mangled)", entries[0].ID, id)
	}
	path := canonicalSnapshotPath(dir, id)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !entries[0].ModifiedAt.Equal(info.ModTime()) {
		t.Errorf("ModifiedAt = %v, want the file mtime %v", entries[0].ModifiedAt, info.ModTime())
	}
}

// TestListSkipsUndecodableFiles pins the best-effort posture: a corrupt or
// empty .session.jsonl (whose Load would fail identically) is skipped, not a
// List error.
func TestListSkipsUndecodableFiles(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	if err := st.Save(ctx, driven(t)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.session.jsonl"), []byte("{not json\n"), 0o644); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.session.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	entries, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "sess-1" {
		t.Errorf("List = %+v, want exactly the one decodable session", entries)
	}
}

// TestDeleteRemovesBothFilesIdempotently pins that Delete removes the session
// snapshot, the tool-call log, AND the event log, and that a second Delete (or a
// Delete of a never-saved id) succeeds.
func TestDeleteRemovesBothFilesIdempotently(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	s := driven(t)
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st.ToolCall(s.ID, session.NewToolCall("c9", "Read", json.RawMessage(`{"p":"x"}`)),
		session.NewToolResult("c9", "ok"), time.Millisecond, time.Millisecond)
	if err := st.Append(ctx, s.ID, session.Event{Type: session.EvResult}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	paths := []string{
		canonicalSnapshotPath(dir, s.ID),
		canonicalFamilyPath(dir, s.ID, ".tools.jsonl"),
		canonicalFamilyPath(dir, s.ID, ".events.jsonl"),
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("precondition: %s missing: %v", filepath.Base(path), err)
		}
	}
	if err := st.Delete(ctx, s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still present after Delete (stat err %v)", filepath.Base(path), err)
		}
	}
	if _, err := st.Load(ctx, s.ID); !errors.Is(err, jsonlstore.ErrNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
	if err := st.Delete(ctx, s.ID); err != nil {
		t.Errorf("second Delete = %v, want nil (idempotent)", err)
	}
	if err := st.Delete(ctx, "never-saved"); err != nil {
		t.Errorf("Delete(never-saved) = %v, want nil (idempotent)", err)
	}
}

// TestDeletePartialFailureLeavesSessionVisible pins Delete's removal ORDER:
// tools sidecar FIRST, session file LAST. When removing the tools file fails,
// the session file must SURVIVE — it is what List enumerates, so the pair
// stays visible and the next retention sweep retries the whole Delete. (The
// reverse order would permanently leak an invisible orphaned .tools.jsonl.)
// The unremovable tools file is simulated portably by replacing it with a
// NON-EMPTY directory, which os.Remove refuses on every platform.
func TestDeletePartialFailureLeavesSessionVisible(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	s := driven(t)
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sessionFile := canonicalSnapshotPath(dir, s.ID)
	toolsPath := canonicalFamilyPath(dir, s.ID, ".tools.jsonl")

	// Make the tools path unremovable: a non-empty directory under the sidecar's name.
	if err := os.MkdirAll(filepath.Join(toolsPath, "block"), 0o755); err != nil {
		t.Fatalf("mkdir blocking tools path: %v", err)
	}

	if err := st.Delete(ctx, s.ID); err == nil {
		t.Fatal("Delete with an unremovable tools file = nil error, want failure")
	}
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("session file did not survive the partial Delete failure (stat: %v) — the List entry is gone and the orphan can never be retried", err)
	}
	entries, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List after partial failure: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != s.ID {
		t.Fatalf("List after partial failure = %+v, want the surviving session entry (the retry handle)", entries)
	}

	// Unblock and retry: the sweep's next Delete must complete the pair.
	if err := os.RemoveAll(toolsPath); err != nil {
		t.Fatalf("unblock tools path: %v", err)
	}
	if err := st.Delete(ctx, s.ID); err != nil {
		t.Fatalf("retry Delete after unblocking = %v, want nil", err)
	}
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Errorf("session file still present after the retry (stat err %v)", err)
	}
}

// TestEventLogAppendReadCumulative pins the jsonlstore EventLog: Append records
// each event as a format-tagged line and Read yields ALL of them in append order
// (cumulative — not latest-line-wins like the snapshot read).
func TestEventLogAppendReadCumulative(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	want := []session.Event{
		{Type: session.EvToolCall, Seq: 1, ToolCall: &session.ToolCall{ID: "c1", Name: "Read"}},
		{Type: session.EvPermissionAsk, Seq: 2, Ask: &session.PendingAsk{AskID: "a1", Tool: "Shell"}},
		{Type: session.EvApproval, Seq: 3, Approval: &session.ApprovalPayload{AskID: "a1", Verdict: session.VerdictStringAllowAlways, Tool: "Shell", AllowAlways: true}},
		{Type: session.EvResult, Seq: 4, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	for _, ev := range want {
		if err := st.Append(ctx, "sess-1", ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// The on-disk line carries the format tag.
	raw, err := os.ReadFile(canonicalFamilyPath(dir, "sess-1", ".events.jsonl"))
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	if !strings.Contains(string(raw), `"v":"eventlog-json/1"`) {
		t.Fatalf("events file missing the format tag: %s", raw)
	}

	var got []session.Event
	for ev, err := range st.Read(ctx, "sess-1") {
		if err != nil {
			t.Fatalf("Read item error: %v", err)
		}
		got = append(got, ev)
	}
	if len(got) != len(want) {
		t.Fatalf("Read returned %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type || got[i].Seq != want[i].Seq {
			t.Errorf("event %d = (%s,%d), want (%s,%d)", i, got[i].Type, got[i].Seq, want[i].Type, want[i].Seq)
		}
	}
	if got[2].Approval == nil || got[2].Approval.Verdict != session.VerdictStringAllowAlways || !got[2].Approval.AllowAlways {
		t.Errorf("approval payload not round-tripped: %+v", got[2].Approval)
	}
}

// TestEventLogReadMissIsEmpty pins that Read of a session with no event log
// yields an EMPTY sequence (absence is data, not an error).
func TestEventLogReadMissIsEmpty(t *testing.T) {
	st, _ := newStore(t)
	n := 0
	for _, err := range st.Read(context.Background(), "never-appended") {
		if err != nil {
			t.Fatalf("miss should not error: %v", err)
		}
		n++
	}
	if n != 0 {
		t.Fatalf("Read of a missing log yielded %d events, want 0", n)
	}
}

// TestEventLogReadRejectsUnknownFormat pins that an unknown format tag is yielded
// as an infra error (a forward-incompatible log must fail loud, not skip).
func TestEventLogReadRejectsUnknownFormat(t *testing.T) {
	st, dir := newStore(t)
	// One good line, then a line with a future format tag.
	if err := st.Append(context.Background(), "sess-1", session.Event{Type: session.EvResult}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	f, err := os.OpenFile(canonicalFamilyPath(dir, "sess-1", ".events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events file: %v", err)
	}
	if _, err := f.WriteString(`{"v":"eventlog-json/999","ev":{"Type":"result"}}` + "\n"); err != nil {
		t.Fatalf("write bad line: %v", err)
	}
	_ = f.Close()

	var sawErr bool
	var n int
	for _, err := range st.Read(context.Background(), "sess-1") {
		if err != nil {
			sawErr = true
			break
		}
		n++
	}
	if n != 1 {
		t.Fatalf("expected one good event before the bad line, got %d", n)
	}
	if !sawErr {
		t.Fatalf("unknown format tag must surface as an error")
	}
}

// TestEventLogReadEarlyBreakReleasesHandle pins the iter.Seq2 contract: a Read
// that breaks after the first event returns cleanly (the deferred Close fires when
// the range body returns false), and a subsequent Append + full Read still works —
// proving no file handle lingered to wedge the next open.
func TestEventLogReadEarlyBreakReleasesHandle(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	for i := 0; i < 5; i++ {
		if err := st.Append(ctx, "sess-1", session.Event{Type: session.EvMessageDelta, Seq: int64(i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Break after the FIRST event.
	got := 0
	for range st.Read(ctx, "sess-1") {
		got++
		break
	}
	if got != 1 {
		t.Fatalf("early-break yielded %d events, want 1", got)
	}
	// The store still works: append once more and read ALL six cumulatively.
	if err := st.Append(ctx, "sess-1", session.Event{Type: session.EvResult, Seq: 5}); err != nil {
		t.Fatalf("Append after early-break: %v", err)
	}
	total := 0
	for _, err := range st.Read(ctx, "sess-1") {
		if err != nil {
			t.Fatalf("Read after early-break: %v", err)
		}
		total++
	}
	if total != 6 {
		t.Fatalf("Read after early-break returned %d events, want 6 (no lingering handle / truncation)", total)
	}
}

// TestEventLogReadMalformedRecordPaths pins the two undecodable-record error paths:
// a malformed ENVELOPE line (not JSON at all) and a malformed INNER event (valid
// envelope, junk "ev"). Both yield (zero, err) and stop after the preceding good
// events.
func TestEventLogReadMalformedRecordPaths(t *testing.T) {
	cases := []struct {
		name    string
		badLine string
	}{
		{"malformed envelope", `{not json`},
		{"malformed inner event", `{"v":"eventlog-json/1","ev":not-json}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, dir := newStore(t)
			if err := st.Append(context.Background(), "sess-1", session.Event{Type: session.EvToolCall}); err != nil {
				t.Fatalf("Append: %v", err)
			}
			f, err := os.OpenFile(canonicalFamilyPath(dir, "sess-1", ".events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatalf("open events file: %v", err)
			}
			if _, err := f.WriteString(tc.badLine + "\n"); err != nil {
				t.Fatalf("write bad line: %v", err)
			}
			_ = f.Close()

			good, sawErr := 0, false
			for _, err := range st.Read(context.Background(), "sess-1") {
				if err != nil {
					sawErr = true
					break
				}
				good++
			}
			if good != 1 {
				t.Fatalf("expected 1 good event before the bad line, got %d", good)
			}
			if !sawErr {
				t.Fatalf("a %s must surface as an error", tc.name)
			}
		})
	}
}

// TestEventLogConcurrentAppend pins that the family flock serializes concurrent
// Appends across goroutines (run under -race): N goroutines append M events each;
// a final Read sees exactly N*M well-formed records (no interleaved/torn line).
func TestEventLogConcurrentAppend(t *testing.T) {
	ctx := context.Background()
	st, _ := newStore(t)
	const goroutines, perG = 8, 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if err := st.Append(ctx, "sess-1", session.Event{Type: session.EvMessageDelta, Seq: int64(g*perG + i)}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	count := 0
	for _, err := range st.Read(ctx, "sess-1") {
		if err != nil {
			t.Fatalf("Read after concurrent append: %v (a torn line means the mutex did not serialize)", err)
		}
		count++
	}
	if count != goroutines*perG {
		t.Fatalf("Read returned %d events, want %d (lost or torn appends)", count, goroutines*perG)
	}
}
