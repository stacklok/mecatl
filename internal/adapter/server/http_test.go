package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// createHTTPSession POSTs /v1/sessions and returns the new session id.
func createHTTPSession(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	body := strings.NewReader(`{"workspace":"/ws"}`)
	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("POST /v1/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if out.SessionID == "" {
		t.Fatalf("empty session id")
	}
	return out.SessionID
}

// parseSSE reads an SSE body and returns the decoded proto Events from each
// `data:` line until the stream ends.
func parseSSE(t *testing.T, r *bufio.Reader) []*mecatlv1.Event {
	t.Helper()
	var out []*mecatlv1.Event
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if data, ok := strings.CutPrefix(trimmed, "data: "); ok {
				var ev mecatlv1.Event
				if jerr := json.Unmarshal([]byte(data), &ev); jerr != nil {
					t.Fatalf("decode SSE data %q: %v", data, jerr)
				}
				out = append(out, &ev)
			}
		}
		if err != nil {
			return out
		}
	}
}

// TestHTTPPromptSSE drives /v1/sessions then /v1/sessions/{id}/prompt and
// asserts the SSE stream carries the event taxonomy and a terminal result.
func TestHTTPPromptSSE(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "body"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a"}`)),
		mockllm.TextTurn("all done"),
	)
	svc := newService(t, llm, allowRules(), read)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)

	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json",
		strings.NewReader(`{"text":"look"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	events := parseSSE(t, bufio.NewReader(resp.Body))
	if !hasType(events, "tool.call") || !hasType(events, "tool.result") {
		t.Fatalf("missing tool events: %v", typesOf(events))
	}
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" || res.GetText() != "all done" {
		t.Fatalf("result = %+v", res)
	}
}

// TestHTTPApprove mirrors the gRPC headline test over HTTP: a prompt pauses on
// a permission.ask; a concurrent POST /approve resolves it; the run completes.
func TestHTTPApprove(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newService(t, llm, nil, write) // nil rules => Ask
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()

	// Read the SSE stream incrementally; when the ask arrives, POST /approve.
	r := bufio.NewReader(resp.Body)
	var events []*mecatlv1.Event
	var approved bool
	for {
		line, rerr := r.ReadString('\n')
		if data, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: "); ok {
			var ev mecatlv1.Event
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("decode: %v", err)
			}
			events = append(events, &ev)
			if ev.GetType() == "permission.ask" && !approved {
				approved = true
				body, _ := json.Marshal(map[string]any{"ask_id": ev.GetAsk().GetAskId(), "allow": true})
				ar, aerr := http.Post(srv.URL+"/v1/sessions/"+id+"/approve",
					"application/json", bytes.NewReader(body))
				if aerr != nil {
					t.Fatalf("POST approve: %v", aerr)
				}
				ar.Body.Close()
				if ar.StatusCode != http.StatusNoContent {
					t.Fatalf("approve status = %d", ar.StatusCode)
				}
			}
			if ev.GetType() == "result" {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	if !approved {
		t.Fatalf("no permission.ask seen: %v", typesOf(events))
	}
	if !write.ran() {
		t.Fatalf("approved tool did not run")
	}
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" {
		t.Fatalf("stop = %q, want end_turn", res.GetStop())
	}
}

// TestHTTPGetSession round-trips a created session through GET.
func TestHTTPGetSession(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)
	resp, err := http.Get(srv.URL + "/v1/sessions/" + id)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
		State     string `json:"state"`
		Workspace string `json:"workspace"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SessionID != id || out.State != "idle" || out.Workspace != "/ws" {
		t.Fatalf("snapshot = %+v", out)
	}
}

func TestHTTPSetMode(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/mode", "application/json", strings.NewReader(`{"mode":"accept-edits"}`))
	if err != nil {
		t.Fatalf("POST /mode: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
		Mode      string `json:"mode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SessionID != id || out.Mode != "acceptEdits" {
		t.Fatalf("set mode response = %+v", out)
	}
}

// resolvedModelBody is the JSON shape of the resolved_model field on the HTTP
// create + getSession responses (a local decode mirror — the handler owns the
// encode side).
type resolvedModelBody struct {
	ProviderID    string `json:"provider_id"`
	ModelID       string `json:"model_id"`
	ContextWindow int64  `json:"context_window"`
}

// TestHTTPCreateSessionEchoesResolvedModel: the HTTP POST /v1/sessions JSON
// response carries resolved_model from the composition single source
// (Service.ResolvedModel → DefaultResolvedModel for a default session), NOT a raw
// read-back of the (empty) request model. Sibling of the gRPC echo test.
func TestHTTPCreateSessionEchoesResolvedModel(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}
	svc := newResolvedModelService(t, dflt, nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{"workspace":"/ws"}`))
	if err != nil {
		t.Fatalf("POST /v1/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var out struct {
		SessionID     string             `json:"session_id"`
		ResolvedModel *resolvedModelBody `json:"resolved_model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if out.ResolvedModel == nil {
		t.Fatalf("create response missing resolved_model")
	}
	if got := *out.ResolvedModel; got != (resolvedModelBody{ProviderID: "openai", ModelID: "gpt-default", ContextWindow: 128000}) {
		t.Fatalf("resolved_model = %+v, want the composition default openai/gpt-default/128000", got)
	}
}

// TestHTTPGetSessionEchoesResolvedModel: the HTTP GET /v1/sessions/{id} snapshot
// carries resolved_model too (wire parity with gRPC GetSession), read from
// Service.ResolvedModel.
func TestHTTPGetSessionEchoesResolvedModel(t *testing.T) {
	dflt := server.ResolvedModel{ProviderID: "anthropic", ModelID: "claude-x", ContextWindow: 200000}
	svc := newResolvedModelService(t, dflt, nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)
	resp, err := http.Get(srv.URL + "/v1/sessions/" + id)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		SessionID     string             `json:"session_id"`
		ResolvedModel *resolvedModelBody `json:"resolved_model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ResolvedModel == nil {
		t.Fatalf("getSession response missing resolved_model (wire parity with gRPC GetSession)")
	}
	if got := *out.ResolvedModel; got != (resolvedModelBody{ProviderID: "anthropic", ModelID: "claude-x", ContextWindow: 200000}) {
		t.Fatalf("resolved_model = %+v, want anthropic/claude-x/200000", got)
	}
}

// TestHTTPGetSessionNotFound returns 404 for an unknown id.
func TestHTTPGetSessionNotFound(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/sessions/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHTTPCloseSession asserts DELETE /v1/sessions/{id}: a created session closes
// with 204, a second DELETE is idempotent (still 204, since close != delete-snapshot),
// and a never-created id returns 404.
func TestHTTPCloseSession(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)

	del := func(path string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := del("/v1/sessions/" + id); code != http.StatusNoContent {
		t.Fatalf("first DELETE status = %d, want 204", code)
	}
	// Idempotent: the snapshot still persists, so a second DELETE is also 204.
	if code := del("/v1/sessions/" + id); code != http.StatusNoContent {
		t.Fatalf("second DELETE status = %d, want 204", code)
	}
	// Unknown id -> 404.
	if code := del("/v1/sessions/never-created"); code != http.StatusNotFound {
		t.Fatalf("unknown id DELETE status = %d, want 404", code)
	}
}

// --- team HTTP/SSE parity ----------------------------------------------------

// parseTeamSSE reads an SSE body and returns the decoded proto TeamEvents from
// each `data:` line until the stream ends — the team analogue of parseSSE.
func parseTeamSSE(t *testing.T, r *bufio.Reader) []*mecatlv1.TeamEvent {
	t.Helper()
	var out []*mecatlv1.TeamEvent
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			if data, ok := strings.CutPrefix(trimmed, "data: "); ok {
				var te mecatlv1.TeamEvent
				if jerr := json.Unmarshal([]byte(data), &te); jerr != nil {
					t.Fatalf("decode SSE data %q: %v", data, jerr)
				}
				out = append(out, &te)
			}
		}
		if err != nil {
			return out
		}
	}
}

// TestHTTPTeamLifecycle drives the full team REST surface: create with a roster
// (lead + read-only worker) → 201 with the enrolled members; GET lists them;
// POST /run streams TeamEvents and closes; POST /messages → 204; DELETE → 204.
func TestHTTPTeamLifecycle(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := teamService(t, llm)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	// POST /v1/teams with an initial roster → 201, body has team_id + members.
	createBody := `{"workspace":"/ws","name":"test","members":[` +
		`{"name":"lead","lead":true,"initial_prompt":"go"},` +
		`{"name":"worker"}]}`
	resp, err := http.Post(srv.URL+"/v1/teams", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /v1/teams: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created mecatlv1.CreateTeamResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	teamID := created.GetTeamId()
	if teamID == "" {
		t.Fatal("empty team id")
	}
	if got := created.GetMembers(); len(got) != 2 || got[0].GetName() != "lead" || got[1].GetName() != "worker" {
		t.Fatalf("create members = %v, want [lead worker]", created.GetMembers())
	}

	// GET /v1/teams/{id} → 200 lists both members.
	gresp, err := http.Get(srv.URL + "/v1/teams/" + teamID)
	if err != nil {
		t.Fatalf("GET /v1/teams/{id}: %v", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", gresp.StatusCode)
	}
	var listed mecatlv1.ListTeamResponse
	if err := json.NewDecoder(gresp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.GetMembers()) != 2 {
		t.Fatalf("list members = %d, want 2", len(listed.GetMembers()))
	}

	// POST /v1/teams/{id}/messages → 204 (operator → lead).
	msgBody := `{"to":"lead","body":"ping"}`
	mresp, err := http.Post(srv.URL+"/v1/teams/"+teamID+"/messages", "application/json", strings.NewReader(msgBody))
	if err != nil {
		t.Fatalf("POST messages: %v", err)
	}
	mresp.Body.Close()
	if mresp.StatusCode != http.StatusNoContent {
		t.Fatalf("messages status = %d, want 204", mresp.StatusCode)
	}

	// POST /v1/teams/{id}/run → 200 text/event-stream; at least the lead's events
	// arrive (a result frame) and the stream closes.
	rresp, err := http.Post(srv.URL+"/v1/teams/"+teamID+"/run", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, want 200", rresp.StatusCode)
	}
	if ct := rresp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("run content-type = %q", ct)
	}
	events := parseTeamSSE(t, bufio.NewReader(rresp.Body))
	var sawLeadResult bool
	for _, te := range events {
		if te.GetMember() == "lead" && te.GetEvent().GetType() == "result" {
			sawLeadResult = true
		}
	}
	if !sawLeadResult {
		t.Fatalf("no lead result frame in stream of %d events", len(events))
	}

	// DELETE /v1/teams/{id} → 204.
	dreq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/teams/"+teamID, nil)
	dresp, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", dresp.StatusCode)
	}
}

// TestHTTPTeamsDisabled asserts POST /v1/teams against a Service with no
// MemberEngine → 412 Precondition Failed (ErrTeamsDisabled).
func TestHTTPTeamsDisabled(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules()) // no MemberEngine wired
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/teams", "application/json", strings.NewReader(`{"workspace":"/ws"}`))
	if err != nil {
		t.Fatalf("POST /v1/teams: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
}

// TestHTTPTeamNotFound asserts GET /v1/teams/{id} on an unknown id → 404.
func TestHTTPTeamNotFound(t *testing.T) {
	svc := teamService(t, mockllm.New(mockllm.TextTurn("x")))
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/teams/team-nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHTTPTeamTooMany asserts CreateTeam past MaxTeams → 429 Too Many Requests
// (ErrTooManyTeams).
func TestHTTPTeamTooMany(t *testing.T) {
	svc := teamServiceMaxTeams(t, mockllm.New(mockllm.TextTurn("x")), 1)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	// First create fills the only slot.
	r1, err := http.Post(srv.URL+"/v1/teams", "application/json", strings.NewReader(`{"workspace":"/ws"}`))
	if err != nil {
		t.Fatalf("POST #1: %v", err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("create #1 status = %d, want 201", r1.StatusCode)
	}

	// Second create is over the cap → 429.
	r2, err := http.Post(srv.URL+"/v1/teams", "application/json", strings.NewReader(`{"workspace":"/ws"}`))
	if err != nil {
		t.Fatalf("POST #2: %v", err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("create #2 status = %d, want 429", r2.StatusCode)
	}
}

// TestHTTPPromptRejectsSSRFURL asserts a prompt part carrying a disallowed media
// URL (cloud metadata IP) is rejected at the HTTP choke point with 400, never
// reaching the run.
func TestHTTPPromptRejectsSSRFURL(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	body := `{"text":"look","parts":[{"kind":"image","mime_type":"image/png","url":"https://169.254.169.254/latest/meta-data/"}]}`
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for SSRF url", resp.StatusCode)
	}
}

// TestHTTPPromptRejectsOversizedBody asserts an over-limit request body is
// rejected with 413 (Request Entity Too Large), not buffered into memory / OOM.
func TestHTTPPromptRejectsOversizedBody(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	// A body just over the wire cap (37 MiB > 32 MiB maxPromptBodyBytes).
	huge := bytes.Repeat([]byte("A"), 37<<20)
	body := append([]byte(`{"text":"`), huge...)
	body = append(body, []byte(`"}`)...)
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for oversized body", resp.StatusCode)
	}
}

// TestHTTPPromptRejectsOversizedPart asserts a part exceeding the per-part inline
// cap (but within the body limit) is rejected with 400 at the choke point.
func TestHTTPPromptRejectsOversizedPart(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	// 11 MiB of raw bytes > MaxMediaBytes (10 MiB); base64 (~14.7 MiB) stays under
	// the 32 MiB body cap so the per-part size check (not the body reader) trips.
	raw := bytes.Repeat([]byte{0xAB}, 11<<20)
	part := promptPartJSON{Kind: "image", MimeType: "image/png", Data: raw}
	reqBody, err := json.Marshal(map[string]any{"text": "look", "parts": []promptPartJSON{part}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversized part", resp.StatusCode)
	}
}

// promptPartJSON mirrors the server's HTTP prompt part JSON shape for test
// request construction ([]byte marshals as base64, matching the handler decode).
type promptPartJSON struct {
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type,omitempty"`
	Data     []byte `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// TestHTTPRunTeamEmitsOutcomeFrame pins the terminal outcome frame on the HTTP
// SSE RunTeam stream (issue #36) — wire parity with the gRPC handler — and the
// budget knob end-to-end: createTeamBody.max_team_tokens (500, tightening an
// unconfigured server budget) trips at the round boundary, and the FINAL SSE data
// frame carries TeamEvent.outcome with budget_exhausted and the "budget" stop;
// no earlier frame carries an outcome.
func TestHTTPRunTeamEmitsOutcomeFrame(t *testing.T) {
	// budgetTripProviders' round-0 spend (700) crosses the 500 budget while the
	// worker's self-ping keeps the team NON-quiescent — the "budget" stop shape.
	svc := teamServicePerMember(t, budgetTripProviders(), 0)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	createBody := `{"workspace":"/ws","name":"test","goal":"do one round of work",` +
		`"max_team_tokens":500,` +
		`"members":[{"name":"lead","lead":true,"initial_prompt":"delegate then synthesise"},` +
		`{"name":"worker","initial_prompt":"do the work"}]}`
	resp, err := http.Post(srv.URL+"/v1/teams", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /v1/teams: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created mecatlv1.CreateTeamResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	rresp, err := http.Post(srv.URL+"/v1/teams/"+created.GetTeamId()+"/run", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, want 200", rresp.StatusCode)
	}
	events := parseTeamSSE(t, bufio.NewReader(rresp.Body))
	if len(events) < 2 {
		t.Fatalf("stream carried %d frames, want member events + a terminal outcome frame", len(events))
	}

	last := events[len(events)-1]
	out := last.GetOutcome()
	if out == nil {
		t.Fatalf("last SSE frame has no outcome: %+v", last)
	}
	if last.GetMember() != "" || last.GetEvent() != nil {
		t.Errorf("terminal frame must carry ONLY the outcome (member=%q event=%v)", last.GetMember(), last.GetEvent())
	}
	for i, te := range events[:len(events)-1] {
		if te.GetOutcome() != nil {
			t.Errorf("frame %d carries an outcome; only the terminal frame may", i)
		}
	}
	if !out.GetBudgetExhausted() {
		t.Error("outcome.budget_exhausted = false, want true (request budget 500, round-0 spent 700)")
	}
	if out.GetStop() != "budget" {
		t.Errorf("outcome.stop = %q, want %q", out.GetStop(), "budget")
	}
	if out.GetRounds() < 1 {
		t.Errorf("outcome.rounds = %d, want >= 1", out.GetRounds())
	}
}

// TestHTTPRunTeamEmptyTeamOutcomeOnly pins the lazy-header path of the SSE
// RunTeam handler (issue #36): CreateTeam permits an empty roster (the client may
// SpawnTeammate before RunTeam, see Service.CreateTeam), and running the empty
// team streams ZERO member events — so the terminal outcome frame is the FIRST
// write and must itself produce the 200 + text/event-stream headers ("an empty
// team still delivers its outcome"). The run is trivially quiescent: 0 rounds,
// stop "end_turn", exactly one frame on the stream.
func TestHTTPRunTeamEmptyTeamOutcomeOnly(t *testing.T) {
	svc := teamService(t, mockllm.New()) // LLM never consulted: no members run
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/teams", "application/json",
		strings.NewReader(`{"workspace":"/ws","name":"empty"}`))
	if err != nil {
		t.Fatalf("POST /v1/teams: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (empty rosters are permitted)", resp.StatusCode)
	}
	var created mecatlv1.CreateTeamResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if len(created.GetMembers()) != 0 {
		t.Fatalf("create members = %v, want none", created.GetMembers())
	}

	rresp, err := http.Post(srv.URL+"/v1/teams/"+created.GetTeamId()+"/run", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, want 200", rresp.StatusCode)
	}
	if ct := rresp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("run content-type = %q, want text/event-stream (lazy headers written by the outcome frame)", ct)
	}
	events := parseTeamSSE(t, bufio.NewReader(rresp.Body))
	if len(events) != 1 {
		t.Fatalf("stream carried %d frames, want exactly the terminal outcome frame", len(events))
	}
	out := events[0].GetOutcome()
	if out == nil {
		t.Fatalf("sole frame has no outcome: %+v", events[0])
	}
	if events[0].GetMember() != "" || events[0].GetEvent() != nil {
		t.Errorf("terminal frame must carry ONLY the outcome (member=%q event=%v)", events[0].GetMember(), events[0].GetEvent())
	}
	if !out.GetQuiescent() {
		t.Error("outcome.quiescent = false, want true (an empty team is trivially quiescent)")
	}
	if out.GetStop() != "end_turn" {
		t.Errorf("outcome.stop = %q, want %q", out.GetStop(), "end_turn")
	}
	if out.GetRounds() != 0 {
		t.Errorf("outcome.rounds = %d, want 0", out.GetRounds())
	}
}

// TestHTTPScheduleLifecycle (S7): the schedule REST surface over
// httptest.NewServer(NewHTTPHandler(svc)) — create+get+fire+pause over a
// jsonlstore-backed Service, plus the no-store→501 path on a memstore-backed
// Service. Mirrors the repo's http_test.go convention.
func TestHTTPScheduleLifecycle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	// POST /v1/schedules → 201.
	createBody := `{"name":"http-cron","prompt":"hello","workspace":"/ws",` +
		`"mode":2,"trigger":{"cron":"@every 1m"}}`
	resp, err := http.Post(srv.URL+"/v1/schedules", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /v1/schedules: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created mecatlv1.CreateScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.GetSchedule().GetSpec().GetName() != "http-cron" {
		t.Errorf("name = %q, want http-cron", created.GetSchedule().GetSpec().GetName())
	}
	if !created.GetSchedule().GetState().GetEnabled() {
		t.Error("Enabled = false, want true")
	}
	// Singleton defaulted to true (S3: the create-seam applies the default).
	if !created.GetSchedule().GetSpec().GetSingleton() {
		t.Error("Singleton = false, want true (the create-seam default)")
	}

	// GET /v1/schedules/http-cron → 200.
	gresp, err := http.Get(srv.URL + "/v1/schedules/http-cron")
	if err != nil {
		t.Fatalf("GET /v1/schedules/http-cron: %v", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", gresp.StatusCode)
	}
	var got mecatlv1.GetScheduleResponse
	if err := json.NewDecoder(gresp.Body).Decode(&got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.GetSchedule().GetSpec().GetName() != "http-cron" {
		t.Errorf("get name = %q, want http-cron", got.GetSchedule().GetSpec().GetName())
	}

	// POST /v1/schedules/http-cron/pause → 204.
	presp, err := http.Post(srv.URL+"/v1/schedules/http-cron/pause", "application/json", nil)
	if err != nil {
		t.Fatalf("POST pause: %v", err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusNoContent {
		t.Fatalf("pause status = %d, want 204", presp.StatusCode)
	}
	// Verify Enabled=false after pause.
	if paused, _ := schedStore.Load(context.Background(), "http-cron"); paused.State.Enabled {
		t.Errorf("after pause: Enabled = true, want false")
	}

	// POST /v1/schedules/http-cron/resume → 204.
	rresp, err := http.Post(srv.URL+"/v1/schedules/http-cron/resume", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume: %v", err)
	}
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusNoContent {
		t.Fatalf("resume status = %d, want 204", rresp.StatusCode)
	}

	// POST /v1/schedules/http-cron/fire → 412 (no scheduler wired here, but the
	// jsonlstore DOES expose a ScheduleStore — Create/Get/Pause/Resume above all
	// worked — so this is the store-present/scheduler-not-running case: the HTTP
	// handler delegates to svc.FireNow, which returns ErrSchedulerNotRunning,
	// distinct from the true no-store ErrNoScheduleStore/501 case exercised by
	// TestHTTPScheduleNoStore501 below).
	fresp, err := http.Post(srv.URL+"/v1/schedules/http-cron/fire", "application/json", nil)
	if err != nil {
		t.Fatalf("POST fire: %v", err)
	}
	defer fresp.Body.Close()
	if fresp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("fire status = %d, want 412 (scheduler not running, but the store is present)", fresp.StatusCode)
	}
}

// TestHTTPScheduleNoStore501 (S7): a Service with no ScheduleStore (memstore)
// reports schedule RPCs as 501.
func TestHTTPScheduleNoStore501(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	// POST /v1/schedules → 501.
	resp, err := http.Post(srv.URL+"/v1/schedules", "application/json",
		strings.NewReader(`{"name":"x","prompt":"y","workspace":"/ws","mode":2,"trigger":{"cron":"@every 1m"}}`))
	if err != nil {
		t.Fatalf("POST /v1/schedules: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("create status = %d, want 501", resp.StatusCode)
	}

	// GET /v1/schedules → 501.
	gresp, err := http.Get(srv.URL + "/v1/schedules")
	if err != nil {
		t.Fatalf("GET /v1/schedules: %v", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("list status = %d, want 501", gresp.StatusCode)
	}
}
