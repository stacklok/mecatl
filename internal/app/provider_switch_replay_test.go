package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/provider/anthropic"
	"github.com/stacklok/mecatl/provider/openai"
)

func TestFactoryProviderSwitchReplayAndCascade(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("read fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sourceRequests, targetRequests [][]byte
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		sourceRequests = append(sourceRequests, raw)
		n := len(sourceRequests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{`{"type":"message_start","message":{"id":"msg","role":"assistant","usage":{"input_tokens":7}}}`}
		if n <= 2 {
			frames = append(frames,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"SOURCE-PRIVATE-THOUGHT","signature":"SOURCE-PRIVATE-SIGNATURE"}}`,
				`{"type":"content_block_stop","index":0}`,
				fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"SOURCE-PRIVATE-PAIR-%d","name":"Read","input":{}}}`, n),
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"fixture.txt\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`)
		} else {
			frames = append(frames, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, strings.Repeat("settled source analysis ", 300)), `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		}
		frames = append(frames, `{"type":"message_stop"}`)
		for _, frame := range frames {
			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(frame), &event); err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, frame)
		}
	}))
	defer source.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		targetRequests = append(targetRequests, raw)
		n := len(targetRequests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp\",\"status\":\"in_progress\"}}\n\n")
		if n <= 6 && n%3 != 0 {
			_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"TARGET-ITEM-%d\",\"call_id\":\"TARGET-PAIR-%d\",\"name\":\"Read\",\"arguments\":\"{\\\"path\\\":\\\"fixture.txt\\\"}\"}}\n\n", n, n)
		} else {
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"compact done\"}\n\n")
		}
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"status\":\"completed\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n")
	}))
	defer target.Close()
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: workspace, UserModelDir: t.TempDir(), NoSoul: true, Compaction: "cascade", Tokenizer: "heuristic",
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "fixture", "ANTHROPIC_API_KEY": "fixture"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerAnthropic {
				return anthropic.New(anthropic.WithAPIKey("fixture"), anthropic.WithBaseURL(source.URL), anthropic.WithConversationCaching(false))
			}
			return openai.New(openai.WithAPIKey("fixture"), openai.WithBaseURL(target.URL))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	run := func(id session.SessionID, text string) {
		t.Helper()
		r, err := built.Service.StartRun(t.Context(), id, text)
		if err != nil {
			t.Fatal(err)
		}
		ended := false
		for ev := range r.Events() {
			if ev.Ask != nil {
				r.Cancel()
				t.Fatal("unexpected permission ask")
			}
			if ev.Result != nil {
				ended = ev.Result.Stop == session.StopEndTurn && ev.Result.Error == ""
				if !ended {
					t.Errorf("run result: %+v", ev.Result)
				}
			}
		}
		built.Service.FinishRun(id, r)
		if !ended {
			t.Fatal("run failed")
		}
	}
	src, err := built.Service.CreateSessionWithProvider(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: providerAnthropic, ModelID: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatal(err)
	}
	// This retained goal exceeds the manual cascade budget but not either model's
	// automatic threshold, so tier 4 must be reached without replacing its factory.
	run(src.ID, strings.Repeat("retained goal context ", 30000))
	src, err = built.Service.GetSession(t.Context(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := boundCallID(t, src, "SOURCE-PRIVATE-PAIR-1")
	hasReplay := false
	for _, m := range src.Conversation.Messages {
		if m.Replay != nil && m.Replay.Provider != nil {
			hasReplay = true
		}
	}
	if !hasReplay {
		t.Fatal("source adapter did not accept private replay")
	}
	dst, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: providerOpenAI, ModelID: "gpt-5"}, server.ProfileDefault, server.WithSourceSession(src.ID))
	if err != nil {
		t.Fatal(err)
	}
	run(dst.ID, "first target turn")
	first, err := built.Service.GetSession(t.Context(), dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := factoryHistoryIDs(first)
	run(dst.ID, "second target turn")
	second, err := built.Service.GetSession(t.Context(), dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondIDs := factoryHistoryIDs(second)
	if len(secondIDs) != 6 || len(firstIDs) != 4 || secondIDs[0] != sourceID || secondIDs[1] != boundCallID(t, src, "SOURCE-PRIVATE-PAIR-2") {
		t.Fatalf("historical identities lost: %v / %v", firstIDs, secondIDs)
	}
	for i, id := range firstIDs {
		if secondIDs[i] != id {
			t.Fatal("later target turn changed historical canonical IDs")
		}
	}
	result, err := built.Service.CompactSession(t.Context(), dst.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sourceRequests) != 3 || len(targetRequests) != 7 {
		t.Fatalf("factory routes: source=%d target=%d", len(sourceRequests), len(targetRequests))
	}
	if !strings.Contains(string(sourceRequests[1]), "SOURCE-PRIVATE-SIGNATURE") || !strings.Contains(string(sourceRequests[1]), "SOURCE-PRIVATE-PAIR") {
		t.Fatal("source replay was not used before switch")
	}
	for i, raw := range targetRequests {
		if strings.Contains(string(raw), "SOURCE-PRIVATE-") {
			t.Fatalf("target request %d leaked source private replay", i)
		}
		var req struct {
			Model string           `json:"model"`
			Input []map[string]any `json:"input"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "gpt-5" {
			t.Fatalf("target/auxiliary model = %q", req.Model)
		}
		calls, results := map[string]string{}, map[string]bool{}
		for _, item := range req.Input {
			switch item["type"] {
			case "function_call":
				id, _ := item["call_id"].(string)
				itemID, _ := item["id"].(string)
				calls[id] = itemID
			case "function_call_output":
				id, _ := item["call_id"].(string)
				results[id] = true
			}
		}
		for id, item := range calls {
			if id == "" || !results[id] {
				t.Fatalf("request %d unpaired call", i)
			}
			if strings.HasPrefix(id, "TARGET-PAIR-") && item != strings.Replace(id, "PAIR", "ITEM", 1) {
				t.Fatalf("request %d lost target item identity", i)
			}
		}
		if i < 6 {
			if !results["engine_"+string(sourceID)] {
				t.Fatal("carryover did not retain reconstructed source pairing at target boundary")
			}
			for _, n := range []int{1, 2, 4, 5} {
				if n <= i {
					id := fmt.Sprintf("TARGET-PAIR-%d", n)
					if calls[id] != fmt.Sprintf("TARGET-ITEM-%d", n) || !results[id] {
						t.Fatalf("request %d lost exact target pairing/item %d", i, n)
					}
				}
			}
		}
		if i == 5 && len(calls) != 6 {
			t.Fatal("second target continuation lost prior occurrences")
		}
		if i == 6 && (len(calls) == 0 || !strings.Contains(string(raw), "context-compaction summariser")) {
			t.Fatalf("factory-built auxiliary cascade: calls=%d summarizer=%t", len(calls), strings.Contains(string(raw), "context-compaction summariser"))
		}
	}
	if !result.Changed {
		t.Fatal("factory cascade did not compact")
	}
}

func factoryHistoryIDs(s *session.Session) []session.ToolCallID {
	var ids []session.ToolCallID
	for _, m := range s.Conversation.Messages {
		for _, c := range m.ToolCalls {
			ids = append(ids, c.ID)
		}
	}
	return ids
}
