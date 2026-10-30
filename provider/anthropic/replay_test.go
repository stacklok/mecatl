package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func signedRedactedTurnSSE(turn int) string {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_test","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thought\n雪"}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG-%d=="}}`, turn),
		`{"type":"content_block_stop","index":0}`,
		fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"REDACTED-%d=="}}`, turn),
		`{"type":"content_block_stop","index":1}`,
		fmt.Sprintf(`{"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":"","signature":"EMPTY-%d=="}}`, turn),
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Checking."}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"toolu_reused","name":"Read","input":{}}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"\"x.go\"}"}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`,
	}
	var s strings.Builder
	for _, event := range events {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &header); err != nil {
			panic(err) // Fixed test fixture JSON.
		}
		fmt.Fprintf(&s, "event: %s\ndata: %s\n\n", header.Type, event)
	}
	return s.String()
}

func streamReplayTurn(t *testing.T, p *Provider, messages []port.LLMMessage) port.LLMMessage {
	t.Helper()
	seq, err := p.Stream(context.Background(), port.LLMRequest{Model: "claude-sonnet-4-6", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	m := assistantMessage("", nil, nil)
	var display string
	replayCount, doneCount := 0, 0
	for c, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		if doneCount != 0 {
			t.Fatal("chunk after terminal ChunkDone")
		}
		switch c.Kind {
		case port.ChunkText:
			m.Text += c.Text
		case port.ChunkReasoning:
			display += c.Text
		case port.ChunkToolCall:
			m.ToolCalls = append(m.ToolCalls, *c.ToolCall)
		case port.ChunkReplay:
			replayCount++
			if c.Text != "" || !c.ReplayIsOutput {
				t.Fatal("replay is not opaque output")
			}
			m.Replay = c.Replay
		case port.ChunkDone:
			doneCount++
			if c.Stop != session.StopEndTurn {
				t.Fatal("wrong stop reason")
			}
		}
	}
	if replayCount != 1 || doneCount != 1 || display != "thought\n雪" || m.Text != "Checking." {
		t.Fatal("stream changed replay/display/terminal behavior")
	}
	if !reflect.DeepEqual(m.ToolCalls, []port.LLMToolCall{toolCall("toolu_reused", "Read", json.RawMessage(`{"path":"x.go"}`))}) {
		t.Fatal("provider-facing tool call changed")
	}
	return m
}

func assertReplayWirePair(t *testing.T, assistant, result json.RawMessage, turn int) {
	t.Helper()
	wantAssistant := fmt.Sprintf(`{"role":"assistant","content":[{"type":"thinking","thinking":"thought\n雪","signature":"SIG-%d=="},{"type":"redacted_thinking","data":"REDACTED-%d=="},{"type":"thinking","thinking":"","signature":"EMPTY-%d=="},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_reused","name":"Read","input":{"path":"x.go"}}]}`, turn, turn, turn)
	wantResult := fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_reused","is_error":false,"content":[{"type":"text","text":"result-%d"}]}]}`, turn)
	for i, pair := range [][2][]byte{{assistant, []byte(wantAssistant)}, {result, []byte(wantResult)}} {
		var got, want any
		if err := json.Unmarshal(pair[0], &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pair[1], &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("turn %d message %d changed ordered blocks or exact field values: got %s, want %s", turn, i, pair[0], pair[1])
		}
	}
}

func TestStreamRequestReplayTwoTurns(t *testing.T) {
	requests := make(chan []byte, 2)
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		turn++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, signedRedactedTurnSSE(turn))
	}))
	defer srv.Close()
	p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL), WithMaxTokens(16000), WithConversationCaching(false))
	messages := []port.LLMMessage{userMessage("read twice")}
	for i := 1; i <= 2; i++ {
		m := streamReplayTurn(t, p, messages)
		wantReplay := fmt.Sprintf(`{"v":1,"blocks":[{"t":"thinking","x":"thought\n雪","s":"SIG-%d=="},{"t":"redacted","d":"REDACTED-%d=="},{"t":"thinking","s":"EMPTY-%d=="}]}`, i, i, i)
		if m.Replay.Format != "anthropic.messages" || m.Replay.Version != 1 || !bytes.Equal(m.Replay.Data, []byte(wantReplay)) {
			t.Fatal("stream envelope bytes changed")
		}
		var request struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(<-requests, &request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != len(messages) {
			t.Fatal("stateless request omitted history")
		}
		if i == 2 {
			assertReplayWirePair(t, request.Messages[1], request.Messages[2], 1)
		}
		messages = append(messages, m, toolMessage(toolResult(m.ToolCalls[0].ID, fmt.Sprintf("result-%d", i))))
	}
	// Both turns, including sequential reuse of the same provider pairing token,
	// remain independently replayable in the next stateless request.
	_, raw := messagesJSON(t, p, messages)
	var wire []json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	assertReplayWirePair(t, wire[1], wire[2], 1)
	assertReplayWirePair(t, wire[3], wire[4], 2)
}

func TestReplayFreeCanonicalHistoryUsesSyntheticPairing(t *testing.T) {
	history := []session.Message{session.NewUserMessage("read twice")}
	for _, id := range []session.ToolCallID{"canonical_1", "canonical_2"} {
		history = append(history,
			session.NewAssistantMessage("Checking.", []session.ToolCall{session.NewToolCall(id, "Read", json.RawMessage(`{"path":"x.go"}`))}),
			session.NewToolMessage(session.NewToolResult(id, "result")))
	}
	messages, err := port.ProjectMessages(history)
	if err != nil {
		t.Fatal(err)
	}
	p := New(WithAPIKey("synthetic"), WithMaxTokens(16000), WithConversationCaching(false))
	params, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 1; i < len(params.Messages); i += 2 {
		blocks := messageContent(params.Messages[i])
		if len(blocks) != 2 || blocks[0].OfText == nil || blocks[1].OfToolUse == nil || messages[i].Replay != nil {
			t.Fatal("replay-free history fabricated thinking or lost text/tools")
		}
		id := blocks[1].OfToolUse.ID
		if id == "" || id == string(history[i].ToolCalls[0].ID) || seen[id] || messageContent(params.Messages[i+1])[0].OfToolResult.ToolUseID != id {
			t.Fatal("synthetic provider pairing is missing, reused or unmatched")
		}
		seen[id] = true
	}
}
