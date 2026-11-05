package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestResponsesOpaqueReplayCodec(t *testing.T) {
	want := responsesReplay{Reasoning: []reasoningItem{
		{ID: "rs_a", Blob: "encrypted-a"},
		{ID: "rs_b", Blob: "encrypted-b", After: 1},
	}, Phase: "future_phase"}
	replay := packReplay(want)
	if replay.Format != "openai.responses" || replay.Version != 1 || string(replay.Data) != `{"reasoning":[{"i":"rs_a","e":"encrypted-a"},{"i":"rs_b","e":"encrypted-b","n":1}],"phase":"future_phase"}` {
		t.Fatal("opaque replay format/schema changed")
	}
	got, err := unpackReplay(replay)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("codec round trip failed: %v", err)
	}
	if packReplay(responsesReplay{}) != nil {
		t.Fatal("empty replay should remain a no-op")
	}
}

func TestResponsesReplayCompatibilityAndMalformedBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		replay    *session.ProviderReplay
		wantError bool
	}{
		{"absent", nil, false},
		{"foreign namespace", &session.ProviderReplay{Format: "anthropic.messages", Version: 1, Data: []byte("private-canary not JSON")}, false},
		{"future version", &session.ProviderReplay{Format: responsesReplayFormat, Version: 2, Data: []byte("private-canary not JSON")}, false},
		{"invalid JSON", &session.ProviderReplay{Format: responsesReplayFormat, Version: 1, Data: []byte("private-canary not JSON")}, true},
		{"null", &session.ProviderReplay{Format: responsesReplayFormat, Version: 1, Data: []byte("null")}, true},
		{"wrong shape", &session.ProviderReplay{Format: responsesReplayFormat, Version: 1, Data: []byte(`{"reasoning":"private-canary"}`)}, true},
		{"partial items", packReplay(responsesReplay{Reasoning: []reasoningItem{{ID: "rs_ok", Blob: "private-canary"}, {ID: "rs_bad"}}}), true},
		{"missing ID", packReplay(responsesReplay{Reasoning: []reasoningItem{{Blob: "private-canary"}}}), true},
		{"negative placement", packReplay(responsesReplay{Reasoning: []reasoningItem{{ID: "rs_a", Blob: "private-canary", After: -1}}}), true},
		{"unordered placement", packReplay(responsesReplay{Reasoning: []reasoningItem{{ID: "rs_a", Blob: "private-canary", After: 1}, {ID: "rs_b", Blob: "private-canary"}}}), true},
		{"invalid UTF8", &session.ProviderReplay{Format: responsesReplayFormat, Version: 1, Data: []byte("{\"phase\":\"\xffprivate-canary\"}")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := port.LLMMessage{Role: session.RoleAssistant, Text: "visible", ProviderPhase: "display-only", Replay: tc.replay}
			req := port.LLMRequest{Model: "test", Messages: []port.LLMMessage{m}}
			params, err := buildParams(req)
			if (err != nil) != tc.wantError {
				t.Fatalf("error presence = %v, want %v", err != nil, tc.wantError)
			}
			if err != nil {
				if strings.Contains(err.Error(), "private-canary") {
					t.Fatal("private replay leaked through error")
				}
				return
			}
			wire, err := json.Marshal(params.Input.OfInputItemList)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(wire), "private-canary") || strings.Contains(string(wire), "phase") || strings.Contains(string(wire), "reasoning") {
				t.Fatal("incompatible replay or display phase reached wire input")
			}
			if _, removed := withoutEncryptedReasoning(req); removed {
				t.Fatal("incompatible replay unlocked encrypted-reasoning recovery")
			}
		})
	}
}

func TestResponsesMalformedReplayFailsBeforeHTTP(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = io.WriteString(w, completedResponseSSE)
	}))
	defer srv.Close()
	err := collectStreamError(t, New(WithAPIKey("synthetic"), WithBaseURL(srv.URL)), port.LLMRequest{Model: "test", Messages: []port.LLMMessage{{
		Role: session.RoleAssistant, Replay: &session.ProviderReplay{Format: responsesReplayFormat, Version: 1, Data: []byte("private-canary")},
	}}})
	if err == nil || requests != 0 || strings.Contains(err.Error(), "private-canary") {
		t.Fatalf("malformed replay admission failed: requests=%d, err=%v", requests, err)
	}
}

func TestResponsesReplayTerminalTiming(t *testing.T) {
	for _, tc := range []struct {
		fixture  string
		isOutput bool
		phase    string
	}{
		{"reasoning_turn.sse", true, ""},
		{"phase_turn.sse", false, "final_answer"},
		{"interleaved_multipart_turn.sse", true, "future_phase"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			chunks := decodeFixture(t, tc.fixture)
			count := 0
			for i, chunk := range chunks {
				if chunk.Kind != port.ChunkReplay {
					continue
				}
				count++
				if i != len(chunks)-3 || chunks[i+1].Kind != port.ChunkUsage || chunks[i+2].Kind != port.ChunkDone || chunk.ReplayIsOutput != tc.isOutput || chunk.Text != "" {
					t.Fatal("replay is not one complete terminal envelope with correct output timing")
				}
				env, err := unpackReplay(chunk.Replay)
				if err != nil || env.Phase != tc.phase {
					t.Fatalf("phase replay mismatch: %v", err)
				}
			}
			if count != 1 {
				t.Fatalf("replay count = %d, want 1", count)
			}
		})
	}
	prefix := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_a\",\"encrypted_content\":\"private\"}}\n\n"
	for _, terminal := range []string{"", "data: {\"type\":\"response.failed\",\"response\":{}}\n\n", "data: {\"type\":\"response.incomplete\",\"response\":{}}\n\n"} {
		chunks, _ := decodeSSE(strings.NewReader(prefix + terminal))
		for _, chunk := range chunks {
			if chunk.Kind == port.ChunkReplay {
				t.Fatal("unsuccessful stream leaked tentative replay")
			}
		}
	}
}

func TestResponsesTwoTurnRequestStreamReplay(t *testing.T) {
	first, err := os.ReadFile("testdata/multi_reasoning_item_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile("testdata/interleaved_multipart_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	var bodies []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body.Input)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			_, _ = w.Write(first)
		} else {
			_, _ = w.Write(second)
			// The offline fixture ends after one newline; live SSE dispatch requires
			// the terminating blank line for its final completed event.
			_, _ = io.WriteString(w, "\n")
		}
	}))
	defer srv.Close()
	p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL))
	req := port.LLMRequest{Model: "test", Messages: []port.LLMMessage{userMessage("inspect")}}
	for turn := range 2 {
		seq, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var chunks []port.Chunk
		for chunk, err := range seq {
			if err != nil {
				t.Fatalf("turn %d: %v", turn, err)
			}
			chunks = append(chunks, chunk)
		}
		msg := aggregateAssistantMessage(chunks)
		req.Messages = append(req.Messages, msg)
		for _, call := range msg.ToolCalls {
			req.Messages = append(req.Messages, toolMessage(toolResult(call.ID, "result:"+string(call.ID))))
		}
		if turn == 0 {
			req.Messages = append(req.Messages, userMessage("continue"))
		}
	}
	const wantSecond = `[{"content":"inspect","role":"user"},{"encrypted_content":"ENCRYPTED_AAA","id":"rs_aaa","summary":[],"type":"reasoning"},{"arguments":"{\"path\":\"a.go\"}","call_id":"call_one","id":"fc_1","name":"Read","type":"function_call"},{"encrypted_content":"ENCRYPTED_BBB","id":"rs_bbb","summary":[],"type":"reasoning"},{"arguments":"{\"task\":\"Enforce client map bound\"}","call_id":"call_two","id":"fc_2","name":"Subagent","type":"function_call"},{"encrypted_content":"ENCRYPTED_CCC","id":"rs_ccc","summary":[],"type":"reasoning"},{"call_id":"call_one","output":"result:call_one","type":"function_call_output"},{"call_id":"call_two","output":"result:call_two","type":"function_call_output"},{"content":"continue","role":"user"}]`
	assertWireJSON(t, bodies[1], []byte(wantSecond))
	items := inputItems(t, req)
	last := items[len(items)-4:]
	wantLast := []byte(`[{"encrypted_content":"OPAQUE_REASONING","id":"rs_1","summary":[],"type":"reasoning"},{"arguments":"{\"path\":\"a.go\"}","call_id":"call_1","id":"fc_1","name":"Read","type":"function_call"},{"content":"firstsecond","phase":"future_phase","role":"assistant"},{"call_id":"call_1","output":"result:call_1","type":"function_call_output"}]`)
	wire, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	assertWireJSON(t, wire, wantLast)
	// Earlier private replay remains exact after accepting another streamed turn.
	wire, err = json.Marshal(items[:len(items)-4])
	if err != nil {
		t.Fatal(err)
	}
	assertWireJSON(t, wire, []byte(wantSecond))
}

func assertWireJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("wire mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestResponsesReplayFreeAndForeignItemIDs(t *testing.T) {
	for _, replay := range []*session.ProviderReplay{nil, {Format: "anthropic.messages", Version: 1, Data: []byte("private-canary")}, {Format: responsesReplayFormat, Version: 2, Data: []byte("private-canary")}} {
		m := port.LLMMessage{Role: session.RoleAssistant, ToolCalls: []port.LLMToolCall{{ID: "provider-pair", Name: "Read", Args: json.RawMessage(`{}`)}}, Replay: replay}
		if replay != nil {
			m.ToolCalls[0].ItemID = "foreign-item"
		}
		req := port.LLMRequest{Messages: []port.LLMMessage{m, toolMessage(toolResult("provider-pair", "ok"))}}
		before, _ := json.Marshal(req)
		first := inputItems(t, req)
		second := inputItems(t, req)
		id, _ := first[0]["id"].(string)
		if !strings.HasPrefix(id, "fc_mecatl_") || len(id) != 50 || first[0]["call_id"] != "provider-pair" || first[1]["call_id"] != "provider-pair" || !reflect.DeepEqual(first, second) {
			t.Fatal("target item synthesis/pairing is not valid and deterministic")
		}
		after, _ := json.Marshal(req)
		if !bytes.Equal(before, after) {
			t.Fatal("request item synthesis mutated caller DTOs")
		}
	}
}

func TestResponsesFlattenedTextUsesLastNonemptyReplayPhase(t *testing.T) {
	const sse = `data: {"type":"response.output_text.delta","delta":"first"}

data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_a","phase":"commentary"}}

data: {"type":"response.output_text.delta","delta":"second"}

data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_b","phase":"future_phase"}}

data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_c"}}

data: {"type":"response.completed","response":{"status":"completed"}}

`
	chunks, err := decodeSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	msg := aggregateAssistantMessage(chunks)
	if msg.Text != "firstsecond" || msg.ProviderPhase != "future_phase" {
		t.Fatal("flattened text or last nonempty display phase changed")
	}
	msg.ProviderPhase = "display-does-not-control-replay"
	items := inputItems(t, port.LLMRequest{Messages: []port.LLMMessage{msg}})
	if len(items) != 1 || items[0]["content"] != "firstsecond" || items[0]["phase"] != "future_phase" {
		t.Fatal("private replay expanded text items or used display phase")
	}
}

func FuzzResponsesReplay(f *testing.F) {
	f.Add([]byte(`{"reasoning":[{"i":"rs_a","e":"opaque","n":1}],"phase":"future_phase"}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"reasoning":[{"i":"rs_a"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := unpackReplay(&session.ProviderReplay{Format: responsesReplayFormat, Version: 1, Data: data})
		if err != nil {
			return
		}
		previous := 0
		for _, item := range env.Reasoning {
			if item.ID == "" || item.Blob == "" || item.After < previous {
				t.Fatal("accepted invalid reasoning item")
			}
			previous = item.After
		}
	})
}
