package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// listSessionsService builds a Service over a jsonlstore. DefaultResolvedModel is
// set to "test-model" so the tests can assert ListSessions does NOT fall back to
// it for a session with its own persisted ModelID (M1: a non-live row must report
// sess.ModelID, never the default engine's model).
func listSessionsService(t *testing.T, store port.SessionStore) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("hi"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		DefaultResolvedModel: server.ResolvedModel{
			ProviderID: "openai",
			ModelID:    "test-model",
		},
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// setSessionMtime locates the physical snapshot by its embedded logical id; the
// filename codec is private to jsonlstore.
func setSessionMtime(t *testing.T, dir string, id session.SessionID, mtime time.Time) {
	t.Helper()
	for _, scanDir := range []string{dir, filepath.Join(dir, "sid-v1")} {
		entries, err := os.ReadDir(scanDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".session.jsonl") {
				continue
			}
			path := filepath.Join(scanDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			if len(lines) == 0 {
				continue
			}
			var head struct {
				ID session.SessionID `json:"id"`
			}
			if json.Unmarshal(lines[len(lines)-1], &head) != nil || head.ID != id {
				continue
			}
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				t.Fatalf("Chtimes %s: %v", path, err)
			}
			return
		}
	}
	t.Fatalf("snapshot for %q not found", id)
}

// TestListSessionsOverJsonlstore seeds 3 fixture sessions (varying mtimes),
// calls Service.ListSessions, and asserts the rows are sorted most-recent-first
// and each carries SessionID/ModifiedAtUnix/State/Turns/CreatedAtUnix/ModelID.
// A corrupt snapshot file still returns a row with zeroed snapshot fields + a
// valid id/modified_at.
func TestListSessionsOverJsonlstore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}

	// Three sessions with distinct creation times, turn counts, and PERSISTED
	// model ids (each different from the service's DefaultResolvedModel
	// "test-model" and from each other) — a non-live ListSessions row must report
	// the session's OWN model, never the default engine's (the M1 regression this
	// test guards).
	mkSession := func(id session.SessionID, created time.Time, turns int, modelID string) *session.Session {
		s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, created)
		s.ModelID = modelID
		// Bump the turn counter by recording assistant turns.
		for i := 0; i < turns; i++ {
			s.BeginTurn()
			s.RecordAssistant(session.NewAssistantMessage("ok", "", nil))
		}
		if err := s.Complete(); err != nil {
			t.Fatalf("Complete %s: %v", id, err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
		return s
	}
	sessA := mkSession("sess-a", time.Unix(1700000000, 0).UTC(), 1, "model-a") // oldest creation
	sessB := mkSession("sess-b", time.Unix(1700000100, 0).UTC(), 3, "model-b")
	sessC := mkSession("sess-c", time.Unix(1700000200, 0).UTC(), 2, "model-c")

	// Vary mtimes so the sort order is unambiguous: B newest, then C, then A.
	// (jsonlstore derives ModifiedAt from the session file's mtime.)
	setSessionMtime(t, dir, sessA.ID, time.Unix(1800000000, 0).UTC())
	setSessionMtime(t, dir, sessB.ID, time.Unix(1900000000, 0).UTC())
	setSessionMtime(t, dir, sessC.ID, time.Unix(1850000000, 0).UTC())

	// Add a CORRUPT snapshot file: its last line decodes a valid id for List, but
	// its full snapshot Load fails (an invalid state that RestoreState rejects).
	// ListSessions must still surface it with zeroed snapshot fields + a valid
	// id/modified_at.
	corruptPath := filepath.Join(dir, "corrupt.session.jsonl")
	// Last line carries a decodable id but a bogus state Restore() rejects.
	if err := os.WriteFile(corruptPath, []byte(`{"id":"corrupt","state":"bogus-state"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if err := os.Chtimes(corruptPath, time.Unix(1750000000, 0).UTC(), time.Unix(1750000000, 0).UTC()); err != nil {
		t.Fatalf("Chtimes corrupt: %v", err)
	}

	svc := listSessionsService(t, st)
	rows, err := svc.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("ListSessions returned %d rows, want 4: %+v", len(rows), rows)
	}

	// Sorted most-recent-first by mtime: B (1900...), C (1850...), A (1800...),
	// corrupt (1750...).
	wantOrder := []string{string(sessB.ID), string(sessC.ID), string(sessA.ID), "corrupt"}
	for i, w := range wantOrder {
		if rows[i].SessionID != w {
			t.Fatalf("row[%d].SessionID = %q, want %q (full order: %+v)", i, rows[i].SessionID, w, rows)
		}
	}
	// modified_at descending.
	for i := 1; i < len(rows); i++ {
		if rows[i].ModifiedAtUnix > rows[i-1].ModifiedAtUnix {
			t.Fatalf("rows not sorted descending by modified_at: row[%d]=%d > row[%d]=%d", i, rows[i].ModifiedAtUnix, i-1, rows[i-1].ModifiedAtUnix)
		}
	}

	// Spot-check the populated rows carry the snapshot fields.
	byID := make(map[string]server.SessionSummary, len(rows))
	for _, r := range rows {
		byID[r.SessionID] = r
	}
	for _, c := range []struct {
		id       string
		state    string
		turns    int
		created  int64
		modelID  string
		modified int64
	}{
		{string(sessA.ID), string(session.StateCompleted), 1, 1700000000, "model-a", 1800000000},
		{string(sessB.ID), string(session.StateCompleted), 3, 1700000100, "model-b", 1900000000},
		{string(sessC.ID), string(session.StateCompleted), 2, 1700000200, "model-c", 1850000000},
	} {
		row := byID[c.id]
		if row.State != c.state {
			t.Errorf("row %s: State = %q, want %q", c.id, row.State, c.state)
		}
		if row.Turns != c.turns {
			t.Errorf("row %s: Turns = %d, want %d", c.id, row.Turns, c.turns)
		}
		if row.CreatedAtUnix != c.created {
			t.Errorf("row %s: CreatedAtUnix = %d, want %d", c.id, row.CreatedAtUnix, c.created)
		}
		if row.ModelID != c.modelID {
			t.Errorf("row %s: ModelID = %q, want %q", c.id, row.ModelID, c.modelID)
		}
		if row.ModifiedAtUnix != c.modified {
			t.Errorf("row %s: ModifiedAtUnix = %d, want %d", c.id, row.ModifiedAtUnix, c.modified)
		}
	}

	// The corrupt row: valid id + modified_at, zeroed snapshot fields.
	corrupt := byID["corrupt"]
	if corrupt.ModifiedAtUnix != 1750000000 {
		t.Errorf("corrupt row ModifiedAtUnix = %d, want 1750000000", corrupt.ModifiedAtUnix)
	}
	if corrupt.State != "" || corrupt.Turns != 0 || corrupt.CreatedAtUnix != 0 || corrupt.ModelID != "" {
		t.Errorf("corrupt row must have zeroed snapshot fields, got %+v", corrupt)
	}
}

// TestListSessionsPruneUnsupportedEmpty asserts a store that does NOT implement
// PrunableStore degrades to an empty slice + nil error (never an error).
func TestListSessionsPruneUnsupportedEmpty(t *testing.T) {
	// memstore does not implement port.PrunableStore.
	svc := listSessionsService(t, memstore.New())
	rows, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions on non-PrunableStore: %v (want nil err)", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListSessions on non-PrunableStore returned %d rows, want 0", len(rows))
	}
}

// --- gRPC + HTTP e2e for both new RPCs --------------------------------------

// TestStreamSessionEventsGRPC drives a fixture session to completion over the
// gRPC relay, then replays it via the generated gRPC client and asserts the
// round-trip through toProto — including the log-only events a live Converse
// skips.
func TestStreamSessionEventsGRPC(t *testing.T) {
	log := memstore.NewEventLog()
	svc, cs := askingEventLogService(t, log)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	driveAskingSessionToCompletion(t, client, cs.GetSessionId())

	stream, err := client.StreamSessionEvents(context.Background(), &mecatlv1.StreamSessionEventsRequest{SessionId: cs.GetSessionId()})
	if err != nil {
		t.Fatalf("StreamSessionEvents: %v", err)
	}
	var replayed []string
	sawApproval, sawUserPrompt := false, false
	var approvalVerdict, approvalTool, approvalAskID string
	var firstPrompt string
	for {
		ev, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("Recv: %v", rerr)
		}
		replayed = append(replayed, ev.GetType())
		if ev.GetType() == "approval" {
			sawApproval = true
			if a := ev.GetApproval(); a != nil {
				approvalVerdict = a.GetVerdict()
				approvalTool = a.GetTool()
				approvalAskID = a.GetAskId()
			}
		}
		if ev.GetType() == "user_prompt" {
			sawUserPrompt = true
			if up := ev.GetUserPrompt(); up != nil && firstPrompt == "" {
				firstPrompt = up.GetText()
			}
		}
	}
	if len(replayed) == 0 {
		t.Fatal("gRPC StreamSessionEvents returned no events")
	}
	if !sawApproval {
		t.Fatalf("gRPC replay MUST include EvApproval (log-only); got: %v", replayed)
	}
	// Strengthened (QA flag): assert the replayed approval carries the PAYLOAD, not
	// just type=="approval" — toProto MUST have projected Verdict/Tool/AskID.
	if approvalVerdict != session.VerdictStringAllowAlways {
		t.Errorf("gRPC replay approval.Verdict = %q, want %q", approvalVerdict, session.VerdictStringAllowAlways)
	}
	if approvalTool != "Write" {
		t.Errorf("gRPC replay approval.Tool = %q, want Write", approvalTool)
	}
	if approvalAskID == "" {
		t.Errorf("gRPC replay approval.AskId must be populated (not stripped to bare type/seq/turn)")
	}
	if !sawUserPrompt {
		t.Fatalf("gRPC replay MUST include EvUserPrompt (log-only); got: %v", replayed)
	}
	if firstPrompt != "go" {
		t.Errorf("gRPC replay user_prompt.Text = %q, want %q (payload must survive toProto)", firstPrompt, "go")
	}
}

// TestStreamSessionEventsHTTP_SSE replays a fixture session over the HTTP SSE
// surface and asserts the data: frames decode.
func TestStreamSessionEventsHTTP_SSE(t *testing.T) {
	log := memstore.NewEventLog()
	svc, cs := askingEventLogService(t, log)
	gclient, gcleanup := dialGRPC(t, svc)
	driveAskingSessionToCompletion(t, gclient, cs.GetSessionId())
	gcleanup()

	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/sessions/" + cs.GetSessionId() + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	frames := decodeSSEFrames(t, body)
	if len(frames) == 0 {
		t.Fatal("no SSE data frames decoded")
	}
	sawApproval, sawUserPrompt := false, false
	var approvalVerdict, approvalTool, approvalAskID string
	var firstPrompt string
	for _, f := range frames {
		var ev mecatlv1.Event
		if err := json.Unmarshal(f, &ev); err != nil {
			t.Fatalf("decode SSE frame: %v (frame=%s)", err, f)
		}
		if ev.GetType() == "approval" {
			sawApproval = true
			if a := ev.GetApproval(); a != nil {
				approvalVerdict = a.GetVerdict()
				approvalTool = a.GetTool()
				approvalAskID = a.GetAskId()
			}
		}
		if ev.GetType() == "user_prompt" {
			sawUserPrompt = true
			if up := ev.GetUserPrompt(); up != nil && firstPrompt == "" {
				firstPrompt = up.GetText()
			}
		}
	}
	if !sawApproval {
		t.Fatalf("HTTP SSE replay MUST include EvApproval (log-only); got frames: %d", len(frames))
	}
	if !sawUserPrompt {
		t.Fatalf("HTTP SSE replay MUST include EvUserPrompt (log-only); got frames: %d", len(frames))
	}
	// Strengthened (QA flag): assert the replayed payloads survive toProto, not just
	// the type field — the wire event MUST carry Verdict/Tool/AskID and Text.
	if approvalVerdict != session.VerdictStringAllowAlways {
		t.Errorf("HTTP SSE replay approval.Verdict = %q, want %q", approvalVerdict, session.VerdictStringAllowAlways)
	}
	if approvalTool != "Write" {
		t.Errorf("HTTP SSE replay approval.Tool = %q, want Write", approvalTool)
	}
	if approvalAskID == "" {
		t.Errorf("HTTP SSE replay approval.AskId must be populated (not stripped to bare type/seq/turn)")
	}
	if firstPrompt != "go" {
		t.Errorf("HTTP SSE replay user_prompt.Text = %q, want %q (payload must survive toProto)", firstPrompt, "go")
	}
}

// decodeSSEFrames extracts the JSON payload of each `data: {...}\n\n` frame.
func decodeSSEFrames(t *testing.T, body []byte) [][]byte {
	t.Helper()
	var frames [][]byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		frames = append(frames, payload)
	}
	return frames
}

// TestListSessionsGRPC asserts the unary ListSessions surface over gRPC.
func TestListSessionsGRPC(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	sA := session.New("ls-a", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := st.Save(ctx, sA); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sB := session.New("ls-b", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000100, 0).UTC())
	// sB used a non-default model at create time; its OWN persisted ModelID must
	// surface here, never the service's DefaultResolvedModel ("test-model") — the
	// M1 regression this test guards (a non-live row previously reported the
	// default engine's model for every session, regardless of what it actually ran on).
	sB.ModelID = "model-b"
	if err := st.Save(ctx, sB); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setSessionMtime(t, dir, sA.ID, time.Unix(1800000000, 0).UTC())
	setSessionMtime(t, dir, sB.ID, time.Unix(1900000000, 0).UTC())

	svc := listSessionsService(t, st)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListSessions(ctx, &mecatlv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.GetSessions()) != 2 {
		t.Fatalf("sessions = %d, want 2", len(resp.GetSessions()))
	}
	// Most-recent-first: B, then A.
	if resp.GetSessions()[0].GetSessionId() != "ls-b" || resp.GetSessions()[1].GetSessionId() != "ls-a" {
		t.Fatalf("order = %s, %s; want ls-b, ls-a", resp.GetSessions()[0].GetSessionId(), resp.GetSessions()[1].GetSessionId())
	}
	if resp.GetSessions()[0].GetModelId() != "model-b" {
		t.Errorf("model_id = %q, want model-b (sB's own persisted model, not the default)", resp.GetSessions()[0].GetModelId())
	}
}

// TestListSessionsHTTP asserts the unary ListSessions surface over HTTP.
func TestListSessionsHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	s := session.New("ls-http", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	svc := listSessionsService(t, st)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.ListSessionsResponse
	if code := httpGet(t, srv, "/v1/sessions", &resp); code != 200 {
		t.Fatalf("GET /v1/sessions status = %d", code)
	}
	if len(resp.GetSessions()) != 1 {
		t.Fatalf("sessions = %d, want 1", len(resp.GetSessions()))
	}
	if resp.GetSessions()[0].GetSessionId() != "ls-http" {
		t.Errorf("session_id = %q, want ls-http", resp.GetSessions()[0].GetSessionId())
	}
}

// failingListStore wraps a SessionStore, implementing port.PrunableStore with an
// injectable List error. Save/Load/Delete delegate to the inner store so the
// service's per-row Load path still works when List succeeds. It is the fixture
// for the ListSessions error-propagation tests: a List returning a NON-
// ErrPruneUnsupported error must surface as ErrInternal (never a bare error).
type failingListStore struct {
	port.SessionStore
	listErr error
}

func (f *failingListStore) List(_ context.Context) ([]port.StoredSession, error) {
	return nil, f.listErr
}

func (f *failingListStore) PageSessionMetadata(context.Context, port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	return port.SessionMetadataPage{}, f.listErr
}

func (f *failingListStore) Delete(ctx context.Context, id session.SessionID) error {
	if ps, ok := f.SessionStore.(port.PrunableStore); ok {
		return ps.Delete(ctx, id)
	}
	return nil
}

// listSessionsServiceOverStore builds a Service over the supplied store (a peer
// of listSessionsService that takes a store argument instead of constructing a
// jsonlstore, so the failing-PrunableStore wrapper can be wired in).
func listSessionsServiceOverStore(t *testing.T, store port.SessionStore) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("hi"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		DefaultResolvedModel: server.ResolvedModel{
			ProviderID: "openai",
			ModelID:    "test-model",
		},
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestListSessionsMapsListErrorToErrInternal asserts a PrunableStore whose List
// returns a NON-ErrPruneUnsupported error surfaces as ErrInternal at the Service
// layer (and is NOT swallowed as an empty slice the way ErrPruneUnsupported is).
func TestListSessionsMapsListErrorToErrInternal(t *testing.T) {
	// memstore implements PrunableStore; wrap it so List returns an infra error.
	inner := memstore.New()
	svc := listSessionsServiceOverStore(t, &failingListStore{
		SessionStore: inner,
		listErr:      errors.New("simulated infra failure: disk unreachable"),
	})
	rows, err := svc.ListSessions(context.Background())
	if !errors.Is(err, server.ErrInternal) {
		t.Fatalf("err = %v, want ErrInternal (a non-ErrPruneUnsupported list error)", err)
	}
	if rows != nil {
		t.Fatalf("ListSessions rows = %v, want nil on error", rows)
	}
}

// TestListSessionsErrPruneUnsupportedStillEmpty asserts the ErrPruneUnsupported
// branch still degrades to an empty slice (the error-propagation guard must not
// regress the sticky-disable behaviour).
func TestListSessionsErrPruneUnsupportedStillEmpty(t *testing.T) {
	inner := memstore.New()
	svc := listSessionsServiceOverStore(t, &failingListStore{
		SessionStore: inner,
		listErr:      port.ErrPruneUnsupported,
	})
	rows, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ErrPruneUnsupported must degrade to nil err, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ErrPruneUnsupported must yield 0 rows, got %d", len(rows))
	}
}

// TestListSessionsGRPCMapsListErrorToInternal asserts the gRPC wire maps a List
// error to codes.Internal (the wire-level mapping of ErrInternal).
func TestListSessionsGRPCMapsListErrorToInternal(t *testing.T) {
	inner := memstore.New()
	svc := listSessionsServiceOverStore(t, &failingListStore{
		SessionStore: inner,
		listErr:      errors.New("simulated infra failure"),
	})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	_, err := client.ListSessions(context.Background(), &mecatlv1.ListSessionsRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status = %v, want codes.Internal (ErrInternal wire mapping)", status.Code(err))
	}
}

// TestListSessionsHTTPMapsListErrorTo500 asserts the HTTP wire maps a List error
// to 500 (the wire-level mapping of ErrInternal).
func TestListSessionsHTTPMapsListErrorTo500(t *testing.T) {
	inner := memstore.New()
	svc := listSessionsServiceOverStore(t, &failingListStore{
		SessionStore: inner,
		listErr:      errors.New("simulated infra failure"),
	})
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	if code := httpGet(t, srv, "/v1/sessions", nil); code != http.StatusInternalServerError {
		t.Fatalf("GET /v1/sessions status = %d, want 500 (ErrInternal → Internal Server Error)", code)
	}
}

type corruptSessionIDStore struct {
	port.SessionStore
	sess *session.Session
}

func (s *corruptSessionIDStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return s.sess, nil
}

// TestGetSessionRejectsInvalidUTF8IDBeforeProtoMapping pins opaque identity:
// malformed persisted bytes must not be repaired into a different clipboard handle.
func TestGetSessionRejectsInvalidUTF8IDBeforeProtoMapping(t *testing.T) {
	inner := memstore.New()
	corrupt := session.New("bad\xffid", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0))
	svc := listSessionsServiceOverStore(t, &corruptSessionIDStore{SessionStore: inner, sess: corrupt})

	if _, err := svc.GetSession(context.Background(), "lookup-id"); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("GetSession invalid UTF-8 id error = %v, want ErrInternal", err)
	}
}
