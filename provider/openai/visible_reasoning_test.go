package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestVisibleReasoning_Scenario3_SummaryRequestAndReplayFields(t *testing.T) {
	for _, route := range []string{"canonical", "openrouter", "custom"} {
		for _, effort := range []string{"", "high"} {
			t.Run(route+"/"+effort, func(t *testing.T) {
				var body map[string]json.RawMessage
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/responses" {
						t.Errorf("path = %q", r.URL.Path)
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode: %v", err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
				}))
				defer srv.Close()
				opts := []Option{WithAPIKey("test-key"), WithBaseURL(srv.URL + "/v1"), WithReasoningEffort(effort)}
				switch route {
				case "canonical":
					opts = append(opts, WithCacheDialect(CacheDialectOpenAI))
				case "openrouter":
					opts = append(opts, WithCacheDialect(CacheDialectOpenRouter))
				}
				p := New(opts...)
				seq, err := p.Stream(context.Background(), effortReq())
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				for _, err := range seq {
					if err != nil {
						t.Fatalf("stream: %v", err)
					}
				}
				var reasoning map[string]string
				if err := json.Unmarshal(body["reasoning"], &reasoning); err != nil {
					t.Fatalf("reasoning: %v", err)
				}
				if reasoning["summary"] != "auto" || reasoning["effort"] != effort {
					t.Errorf("reasoning = %v, want summary auto effort %q", reasoning, effort)
				}
				if string(body["store"]) != "false" || string(body["include"]) != `["reasoning.encrypted_content"]` {
					t.Errorf("stateless replay flags = store:%s include:%s", body["store"], body["include"])
				}
			})
		}
	}
}

func TestVisibleReasoning_Scenario3_UnsupportedSummaryCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, body, private string }{
		{"unsupported", `{"error":{"message":"Unsupported parameter: reasoning.summary","type":"invalid_request_error"}}`, "reasoning.summary"},
		{"unrelated", `{"error":{"message":"model is unavailable","type":"invalid_request_error"}}`, "model is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				var req struct {
					Reasoning struct {
						Summary string `json:"summary"`
					} `json:"reasoning"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("request: %v", err)
				}
				if req.Reasoning.Summary != "auto" {
					t.Errorf("summary = %q, want auto", req.Reasoning.Summary)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			p := New(WithAPIKey("test-key"), WithBaseURL(srv.URL+"/v1"))
			seq, err := p.Stream(context.Background(), port.LLMRequest{Model: "gpt-test", Messages: []session.Message{session.NewUserMessage("hello")}})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			chunks := 0
			var gotErr error
			for chunk, err := range seq {
				if err != nil {
					gotErr = err
					continue
				}
				chunks++
				t.Errorf("fabricated chunk: %+v", chunk)
			}
			if gotErr == nil || !strings.Contains(gotErr.Error(), "provider request failed (400 Bad Request)") || strings.Contains(gotErr.Error(), tc.private) {
				t.Errorf("error = %v, want safe 400 without provider body %q", gotErr, tc.private)
			}
			if chunks != 0 || count != 1 {
				t.Errorf("chunks = %d, requests = %d, want 0 and 1", chunks, count)
			}
		})
	}
}
