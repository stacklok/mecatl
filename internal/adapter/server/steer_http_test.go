package server_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type steerHTTPResponse struct {
	Outcome   string `json:"outcome"`
	MessageID string `json:"message_id"`
	Promoted  bool   `json:"promoted"`
	RunID     string `json:"run_id"`
	Code      string `json:"code"`
}

func postSteerHTTP(t *testing.T, srv *httptest.Server, id, route string, body io.Reader) (int, string, steerHTTPResponse) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/"+route, "application/json", body)
	if err != nil {
		t.Fatalf("POST %s: %v", route, err)
	}
	defer resp.Body.Close()
	var out steerHTTPResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s response: %v", route, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), out
}

func postSteerHTTPJSON(t *testing.T, srv *httptest.Server, id, route string, body any) (int, string, steerHTTPResponse) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", route, err)
	}
	return postSteerHTTP(t, srv, id, route, bytes.NewReader(data))
}

func waitForSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitForNoLiveRun(t *testing.T, svc *server.Service, id session.SessionID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := svc.LookupRun(id); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("promoted run %q did not finish", id)
}

func TestHTTPSteerAcceptedAppendedAndCorrelated(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(block.release)
		}
	}()
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newSteerService(t, llm, nil, block)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	promptResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(`{"text":"look"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer promptResp.Body.Close()
	waitForSignal(t, block.started, "blocking tool")
	live, ok := svc.LookupRun(session.SessionID(id))
	if !ok {
		t.Fatal("live run not registered")
	}

	status, contentType, first := postSteerHTTPJSON(t, srv, id, "steer", map[string]any{
		"expected_run_id": live.RunID(),
		"message_id":      "m-1",
		"parts": []map[string]any{{
			"data":      "cGl4ZWxz",
			"kind":      "image",
			"mime_type": "image/png",
		}},
		"text": "first",
	})
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") || first.Outcome != "accepted" || first.MessageID != "m-1" {
		t.Fatalf("first steer = status %d type %q body %+v", status, contentType, first)
	}
	status, _, second := postSteerHTTPJSON(t, srv, id, "steer", map[string]any{
		"expected_run_id": live.RunID(),
		"message_id":      "m-2",
		"text":            "second",
	})
	if status != http.StatusOK || second.Outcome != "appended" || second.MessageID != "m-2" {
		t.Fatalf("second steer = status %d body %+v", status, second)
	}

	close(block.release)
	released = true
	events := parseSSE(t, bufio.NewReader(promptResp.Body))
	var steered *mecatlv1.Event
	for _, ev := range events {
		if ev.GetType() == string(session.EvSteer) {
			steered = ev
			break
		}
	}
	if steered == nil {
		t.Fatalf("no steer event in %v", typesOf(events))
	}
	if got := steered.GetSteer(); got.GetText() != "first\n\nsecond" || got.GetMessageId() != "m-2" || len(got.GetParts()) != 1 || string(got.GetParts()[0].GetData()) != "pixels" {
		t.Fatalf("steer event = %+v", got)
	}
}

func TestHTTPSteerCancelHonorsExpectedRunAndBody(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(block.release)
		}
	}()
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	svc := newSteerService(t, llm, nil, block)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	promptResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(`{"text":"look"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer promptResp.Body.Close()
	waitForSignal(t, block.started, "blocking tool")
	live, ok := svc.LookupRun(session.SessionID(id))
	if !ok {
		t.Fatal("live run not registered")
	}
	status, _, accepted := postSteerHTTPJSON(t, srv, id, "steer", map[string]any{"text": "retract me", "message_id": "m-steer"})
	if status != http.StatusOK || accepted.Outcome != "accepted" {
		t.Fatalf("steer = status %d body %+v", status, accepted)
	}

	status, contentType, stale := postSteerHTTPJSON(t, srv, id, "cancel-steer", map[string]any{
		"expected_run_id": "stale-run",
		"message_id":      "m-stale",
	})
	if status != http.StatusConflict || !strings.HasPrefix(contentType, "application/problem+json") || stale.Code != "stale_run_control" {
		t.Fatalf("stale cancel-steer = status %d type %q body %+v", status, contentType, stale)
	}
	status, _, cancelled := postSteerHTTPJSON(t, srv, id, "cancel-steer", map[string]any{
		"expected_run_id": live.RunID(),
		"message_id":      "m-cancel",
	})
	if status != http.StatusOK || cancelled.Outcome != "retracted" || cancelled.MessageID != "m-cancel" {
		t.Fatalf("matching cancel-steer = status %d body %+v", status, cancelled)
	}

	close(block.release)
	released = true
	events := parseSSE(t, bufio.NewReader(promptResp.Body))
	if hasType(events, string(session.EvSteer)) {
		t.Fatalf("retracted steer drained: %v", typesOf(events))
	}
	status, _, ended := postSteerHTTPJSON(t, srv, id, "cancel-steer", map[string]any{"expected_run_id": live.RunID()})
	if status != http.StatusConflict || ended.Code != "stale_run_control" {
		t.Fatalf("qualified cancel-steer with no live run = status %d body %+v", status, ended)
	}
	status, _, none := postSteerHTTP(t, srv, id, "cancel-steer", strings.NewReader(""))
	if status != http.StatusOK || none.Outcome != "none_pending" {
		t.Fatalf("empty-body cancel-steer = status %d body %+v", status, none)
	}
}

func TestHTTPSteerPromotionIsUnaryAndBackgroundDrained(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("promoted"))
	svc := newSteerService(t, llm, nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	firstResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(`{"text":"first"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	firstEvents := parseSSE(t, bufio.NewReader(firstResp.Body))
	firstResp.Body.Close()
	var firstRunID string
	for _, ev := range firstEvents {
		if ev.GetType() == string(session.EvResult) {
			firstRunID = ev.GetRunId()
		}
	}
	if firstRunID == "" {
		t.Fatalf("first run emitted no run id: %v", typesOf(firstEvents))
	}

	status, _, strict := postSteerHTTPJSON(t, srv, id, "steer", map[string]any{
		"expected_run_id": firstRunID,
		"text":            "strict late",
	})
	if status != http.StatusConflict || strict.Code != "stale_run_control" {
		t.Fatalf("strict late steer = status %d body %+v", status, strict)
	}
	status, contentType, promoted := postSteerHTTPJSON(t, srv, id, "steer", map[string]any{
		"message_id": "m-promote",
		"text":       "unqualified late",
	})
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("promoted steer = status %d type %q body %+v", status, contentType, promoted)
	}
	if promoted.Outcome != "too_late" || !promoted.Promoted || promoted.RunID == "" || promoted.RunID == firstRunID || promoted.MessageID != "m-promote" {
		t.Fatalf("promoted steer body = %+v", promoted)
	}
	waitForNoLiveRun(t, svc, session.SessionID(id))
	if calls := llm.Calls(); calls != 2 {
		t.Fatalf("provider calls = %d, want 2", calls)
	}
	loaded, err := svc.GetSession(t.Context(), session.SessionID(id))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if loaded.State != session.StateCompleted {
		t.Fatalf("promoted session state = %q, want completed", loaded.State)
	}
}

func TestHTTPSteerPromotedBackgroundRunPersistsAwaiting(t *testing.T) {
	block := &blockingTextTool{started: make(chan struct{}), release: make(chan struct{})}
	llm := mockllm.New(
		mockllm.TextTurn("first"),
		mockllm.ToolCallTurn(call("ask-1", "Read", `{}`)),
	)
	svc := newSteerService(t, llm, []governance.Rule{{
		Scope:  governance.ScopeBuiltinDefault,
		Tool:   "Read",
		Effect: governance.Ask,
	}}, block)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	firstResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/prompt", "application/json", strings.NewReader(`{"text":"first"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	_ = parseSSE(t, bufio.NewReader(firstResp.Body))
	firstResp.Body.Close()

	status, _, promoted := postSteerHTTPJSON(t, srv, id, "steer", map[string]any{"text": "ask first"})
	if status != http.StatusOK || !promoted.Promoted || promoted.RunID == "" {
		t.Fatalf("promoted steer = status %d body %+v", status, promoted)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, loadErr := svc.GetSession(t.Context(), session.SessionID(id))
		if loadErr != nil {
			t.Fatalf("GetSession: %v", loadErr)
		}
		if stored.State == session.StateAwaiting {
			if _, ok := stored.PendingAsk(); !ok {
				t.Fatal("persisted awaiting session has no pending approval")
			}
			break
		}
		if time.Now().After(deadline) {
			_, live := svc.LookupRun(session.SessionID(id))
			t.Fatalf("promoted session state = %q, live = %t, provider calls = %d; want persisted awaiting", stored.State, live, llm.Calls())
		}
		time.Sleep(time.Millisecond)
	}

	cancelResp, err := http.Post(srv.URL+"/v1/sessions/"+id+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", cancelResp.StatusCode)
	}
	waitForNoLiveRun(t, svc, session.SessionID(id))
}

func TestHTTPSteerStrictValidationAndFeature(t *testing.T) {
	svc := newSteerService(t, mockllm.New(mockllm.TextTurn("unused")), nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	tests := []struct {
		name  string
		route string
		body  string
	}{
		{name: "empty steer", route: "steer", body: `{}`},
		{name: "unknown field", route: "steer", body: `{"text":"x","expectedRunId":"r"}`},
		{name: "trailing value", route: "steer", body: `{"text":"x"}{}`},
		{name: "malformed cancel", route: "cancel-steer", body: `{`},
		{name: "unknown cancel field", route: "cancel-steer", body: `{"messageId":"m"}`},
		{name: "long steer id", route: "steer", body: `{"text":"x","message_id":"` + strings.Repeat("é", 65) + `"}`},
		{name: "long cancel id", route: "cancel-steer", body: `{"message_id":"` + strings.Repeat("é", 65) + `"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, contentType, body := postSteerHTTP(t, srv, id, tc.route, strings.NewReader(tc.body))
			if status != http.StatusBadRequest || !strings.HasPrefix(contentType, "application/problem+json") || body.Code != "invalid_argument" {
				t.Fatalf("status %d type %q body %+v", status, contentType, body)
			}
		})
	}

	compat, err := http.Get(srv.URL + "/v1/compatibility")
	if err != nil {
		t.Fatalf("GET compatibility: %v", err)
	}
	defer compat.Body.Close()
	var advertised struct {
		Features []string `json:"features"`
	}
	if err := json.NewDecoder(compat.Body).Decode(&advertised); err != nil {
		t.Fatalf("decode compatibility: %v", err)
	}
	if !slices.Contains(advertised.Features, "http_steer") {
		t.Fatalf("features %v do not advertise http_steer", advertised.Features)
	}
}

func TestHTTPSteerOversizedBody(t *testing.T) {
	svc := newSteerService(t, mockllm.New(mockllm.TextTurn("unused")), nil)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)
	body := io.MultiReader(strings.NewReader(`{"text":"`), io.LimitReader(strings.NewReader(strings.Repeat("x", 33<<20)), 33<<20), strings.NewReader(`"}`))
	status, contentType, problem := postSteerHTTP(t, srv, id, "steer", body)
	if status != http.StatusRequestEntityTooLarge || !strings.HasPrefix(contentType, "application/problem+json") || problem.Code != "request_too_large" {
		t.Fatalf("oversized steer = status %d type %q body %+v", status, contentType, problem)
	}
}
