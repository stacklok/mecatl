package server_test

// The unary HTTP tier of steer-while-running (the deferred follow-up ADR 0232
// names): POST /v1/sessions/{id}/steer enqueues to the LIVE run's steer inbox
// via Service.Steer, promoting through the run-entry funnel (and relaying the
// promoted run as SSE) when no live run can take it, behind a too_late ack
// when the caller opts out of promotion — and POST
// /v1/sessions/{id}/cancel-steer retracts the pending (un-drained) steer via
// Service.CancelSteer. The EvSteer drain echo on the prompt SSE stream carries
// the client-minted message_id through the SAME shared Service.stampSteerEcho
// the gRPC Converse relay uses, and POST /v1/sessions advertises the steer bit
// on the capabilities echo like gRPC CreateSession does.

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
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// steerRespJSON decodes the steer / cancel-steer 200 body.
type steerRespJSON struct {
	Outcome   string `json:"outcome"`
	MessageID string `json:"message_id"`
}

// postSteer POSTs body to the session's steer (or cancel-steer) route and
// returns the status code plus the decoded outcome body (zero on a non-200).
func postSteer(t *testing.T, srv *httptest.Server, id, route, body string) (int, steerRespJSON) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/"+route, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", route, err)
	}
	defer resp.Body.Close()
	var out steerRespJSON
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s response: %v", route, err)
		}
	}
	return resp.StatusCode, out
}

// httpSessionState GETs the session snapshot and returns its state string.
func httpSessionState(t *testing.T, srv *httptest.Server, id string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v1/sessions/" + id)
	if err != nil {
		t.Fatalf("GET session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET session status = %d", resp.StatusCode)
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return out.State
}

// TestHTTPSteer_AcceptedEchoesMessageID: a steer POSTed against a genuinely
// LIVE run (a tool holds it mid-dispatch) is accepted — 200
// {"outcome":"accepted","message_id":<echoed>} — and the EvSteer drain echo on
// the prompt SSE stream carries the SAME client-minted message_id (the shared
// stampSteerEcho path), proving the HTTP relay correlates like the gRPC one.
func TestHTTPSteer_AcceptedEchoesMessageID(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("turn two done"),
	)
	svc := newSteerService(t, llm, nil, block)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)

	// Steer from a side goroutine once the tool genuinely holds the run, then
	// release the tool so the steer drains at the next turn boundary.
	type steerAck struct {
		status int
		body   steerRespJSON
	}
	acked := make(chan steerAck, 1)
	go func() {
		<-block.started
		status, body := postSteer(t, srv, id, "steer", `{"text":"also check b.go","message_id":"m-1"}`)
		acked <- steerAck{status: status, body: body}
		close(block.release)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"look"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	events := parseSSE(t, bufio.NewReader(resp.Body))

	ack := <-acked
	if ack.status != http.StatusOK {
		t.Fatalf("steer status = %d, want 200", ack.status)
	}
	if ack.body.Outcome != "accepted" {
		t.Fatalf("steer outcome = %q, want accepted", ack.body.Outcome)
	}
	if ack.body.MessageID != "m-1" {
		t.Fatalf("steer ack message_id = %q, want m-1 (the request's own id echoed)", ack.body.MessageID)
	}

	// The EvSteer drain echo rides the prompt SSE stream carrying the committed
	// text AND the client-minted message_id (stamped by the shared helper).
	var steerEv *mecatlv1.Event
	for _, ev := range events {
		if ev.GetType() == "steer" {
			steerEv = ev
			break
		}
	}
	if steerEv == nil {
		t.Fatalf("no steer echo event on the SSE stream: %v", typesOf(events))
	}
	if got := steerEv.GetSteer().GetText(); got != "also check b.go" {
		t.Fatalf("steer echo text = %q, want %q", got, "also check b.go")
	}
	if got := steerEv.GetSteer().GetMessageId(); got != "m-1" {
		t.Fatalf("steer echo message_id = %q, want m-1 (the HTTP relay must stamp the drain echo like the gRPC relay)", got)
	}
	if res := lastResult(t, events); res.GetStop() != "end_turn" || res.GetText() != "turn two done" {
		t.Fatalf("result = %+v", res)
	}
}

// TestHTTPSteer_TooLatePromotesAndRelays (ADR 0252): an UNQUALIFIED steer
// against a session with no live run is promoted to a follow-up run —
// Service.Steer's ADR 0232 contract — and the promoted run is relayed as SSE
// on this same response, terminal result included, leaving nothing registered.
func TestHTTPSteer_TooLatePromotesAndRelays(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("promoted answer"))
	svc := newSteerService(t, llm, nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json",
		strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	parseSSE(t, bufio.NewReader(resp.Body)) // drive the run to its terminal
	resp.Body.Close()
	if got := httpSessionState(t, srv, id); got != "completed" {
		t.Fatalf("precondition: state = %q, want completed", got)
	}

	promoteResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/steer", "application/json",
		strings.NewReader(`{"text":"late follow-up"}`))
	if err != nil {
		t.Fatalf("POST steer: %v", err)
	}
	defer promoteResp.Body.Close()
	if promoteResp.StatusCode != http.StatusOK {
		t.Fatalf("steer status = %d, want 200", promoteResp.StatusCode)
	}
	if ct := promoteResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("promoted steer Content-Type = %q, want an SSE relay", ct)
	}
	events := parseSSE(t, bufio.NewReader(promoteResp.Body))
	if res := lastResult(t, events); res.GetText() != "promoted answer" {
		t.Fatalf("promoted run result text = %q, want the second turn", res.GetText())
	}
	if _, live := svc.LookupRun(session.SessionID(id)); live {
		t.Fatalf("the promoted run must be deregistered after the relay")
	}
	if calls := llm.Calls(); calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (the promoted follow-up drives one)", calls)
	}
}

// TestHTTPSteer_StrictNeverPromotes (ADR 0249/0252): expected_run_id names a
// run that already ended — the steer is refused 409 (stale_run_control),
// nothing is promoted, the state and call count are untouched. The caller
// keeps the text.
func TestHTTPSteer_StrictNeverPromotes(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"))
	svc := newSteerService(t, llm, nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json",
		strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	parseSSE(t, bufio.NewReader(resp.Body))
	resp.Body.Close()

	strictResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/steer", "application/json",
		strings.NewReader(`{"text":"late follow-up","expected_run_id":"r-gone"}`))
	if err != nil {
		t.Fatalf("POST strict steer: %v", err)
	}
	defer strictResp.Body.Close()
	if strictResp.StatusCode != http.StatusConflict {
		t.Fatalf("strict steer status = %d, want 409", strictResp.StatusCode)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(strictResp.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem body: %v", err)
	}
	if problem.Code != "stale_run_control" {
		t.Fatalf("problem code = %q, want stale_run_control", problem.Code)
	}
	if _, live := svc.LookupRun(session.SessionID(id)); live {
		t.Fatalf("a strict too_late steer must NOT register a promoted run")
	}
	if calls := llm.Calls(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

// TestHTTPSteer_Rejections pins the request-validation edges: unknown session →
// 404 (absence-equivalent, mirroring cancel/approve), empty text → 400, and an
// oversized body → 413 (the same MaxBytesReader bound as /prompt).
func TestHTTPSteer_Rejections(t *testing.T) {
	svc := newSteerService(t, mockllm.New(mockllm.TextTurn("x")), nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	t.Run("unknown session", func(t *testing.T) {
		status, _ := postSteer(t, srv, "does-not-exist", "steer", `{"text":"hi"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
	})

	t.Run("empty text", func(t *testing.T) {
		status, _ := postSteer(t, srv, id, "steer", `{"text":""}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		// A body just over the wire cap (37 MiB > 32 MiB maxPromptBodyBytes).
		huge := bytes.Repeat([]byte("A"), 37<<20)
		body := append([]byte(`{"text":"`), huge...)
		body = append(body, []byte(`"}`)...)
		resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/steer", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST steer: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 for oversized body", resp.StatusCode)
		}
	})
}

// TestHTTPSteerCancel_RetractsPending: cancel-steer against a live run with a
// PENDING steer reports retracted, and the retracted text never drains (no
// EvSteer echo lands on the stream); with nothing pending it reports
// none_pending; an unknown session is 404.
func TestHTTPSteerCancel_RetractsPending(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newSteerService(t, llm, nil, block)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)

	// Steer then immediately retract, both while the tool holds the run.
	outcomes := make(chan [2]steerRespJSON, 1)
	go func() {
		<-block.started
		_, first := postSteer(t, srv, id, "steer", `{"text":"scratch that"}`)
		_, second := postSteer(t, srv, id, "cancel-steer", "")
		outcomes <- [2]steerRespJSON{first, second}
		close(block.release)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"look"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	events := parseSSE(t, bufio.NewReader(resp.Body))

	got := <-outcomes
	if got[0].Outcome != "accepted" {
		t.Fatalf("steer outcome = %q, want accepted", got[0].Outcome)
	}
	if got[1].Outcome != "retracted" {
		t.Fatalf("cancel-steer outcome = %q, want retracted", got[1].Outcome)
	}
	if hasType(events, "steer") {
		t.Fatalf("a retracted steer must never drain/echo: %v", typesOf(events))
	}

	// Nothing pending any more (the run is over): none_pending, never an error.
	status, none := postSteer(t, srv, id, "cancel-steer", "")
	if status != http.StatusOK || none.Outcome != "none_pending" {
		t.Fatalf("cancel-steer with nothing pending = (%d, %q), want (200, none_pending)", status, none.Outcome)
	}

	// Unknown session: absence-equivalent 404.
	if status, _ := postSteer(t, srv, "does-not-exist", "cancel-steer", ""); status != http.StatusNotFound {
		t.Fatalf("cancel-steer unknown session status = %d, want 404", status)
	}
}

// TestHTTPCreateSession_AdvertisesSteer: the POST /v1/sessions capabilities
// echo carries the steer bit — true when the engine arms Deps.EnableSteer,
// absent/false otherwise — so an HTTP client gates the steer affordance on the
// same composition-computed value gRPC clients read.
func TestHTTPCreateSession_AdvertisesSteer(t *testing.T) {
	decodeCaps := func(t *testing.T, srv *httptest.Server) bool {
		t.Helper()
		resp, err := http.Post(srv.URL+"/v1/sessions", "application/json",
			strings.NewReader(`{"workspace":"/ws"}`))
		if err != nil {
			t.Fatalf("POST /v1/sessions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create status = %d", resp.StatusCode)
		}
		var out struct {
			Capabilities struct {
				Steer bool `json:"steer"`
			} `json:"capabilities"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		return out.Capabilities.Steer
	}

	on := httptest.NewServer(server.NewHTTPHandler(newSteerService(t, mockllm.New(mockllm.TextTurn("x")), nil)))
	defer on.Close()
	if !decodeCaps(t, on) {
		t.Fatalf("capabilities steer = false, want true (EnableSteer armed)")
	}

	off := httptest.NewServer(server.NewHTTPHandler(newService(t, mockllm.New(mockllm.TextTurn("x")), allowRules())))
	defer off.Close()
	if decodeCaps(t, off) {
		t.Fatalf("capabilities steer = true, want false (EnableSteer off)")
	}
}
