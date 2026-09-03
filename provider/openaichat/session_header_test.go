package openaichat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const completedChatSSE = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"test","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"test","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n" +
	"data: [DONE]\n\n"

func drainSessionHeaderStream(ctx context.Context, t *testing.T, p *Provider, model string) {
	t.Helper()
	seq, err := p.Stream(ctx, port.LLMRequest{Model: model, Messages: []session.Message{session.NewUserMessage("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
	}
}

type sessionHeaderVector struct {
	Name          string `json:"name"`
	Value         string `json:"value"`
	Legal         bool   `json:"legal"`
	ProviderLegal *bool  `json:"provider_legal"`
}

func sessionHeaderVectors(t *testing.T) []sessionHeaderVector {
	t.Helper()
	data, err := os.ReadFile("../../testdata/session_header_values.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []sessionHeaderVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func TestADR_0290_SessionHeaderLegalValueParity(t *testing.T) {
	if sessionIDHeaderName != "X-Mecatl-Session-ID" {
		t.Fatalf("sessionIDHeaderName = %q", sessionIDHeaderName)
	}
	for _, vector := range sessionHeaderVectors(t) {
		t.Run(vector.Name, func(t *testing.T) {
			expected := vector.Legal
			if vector.ProviderLegal != nil {
				expected = *vector.ProviderLegal
			}
			if got := validHTTPHeaderValue(vector.Value); got != expected {
				t.Errorf("validHTTPHeaderValue(%q) = %v, want retained provider decision %v", vector.Value, got, expected)
			}
		})
	}
}

func TestADR_0290_ProviderSessionHeaderExact(t *testing.T) {
	const id = "session/exact:42?node=a&b=c"
	headers := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get(sessionIDHeaderName)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedChatSSE)
	}))
	defer srv.Close()
	p := New(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"))
	ctx := port.WithSessionID(context.Background(), id)
	for range 2 {
		drainSessionHeaderStream(ctx, t, p, "model")
	}
	close(headers)
	for got := range headers {
		if got != id {
			t.Errorf("session header = %q, want exact %q", got, id)
		}
	}
}

func TestADR_0290_ProviderSessionHeaderOptional(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "absent", ctx: context.Background()},
		{name: "empty", ctx: port.WithSessionID(context.Background(), "")},
		{name: "illegal", ctx: port.WithSessionID(context.Background(), "bad\nid")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header <- r.Header.Get(sessionIDHeaderName)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, completedChatSSE)
			}))
			defer srv.Close()
			drainSessionHeaderStream(tc.ctx, t, New(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1")), "model")
			if got := <-header; got != "" {
				t.Errorf("optional session header = %q, want omitted", got)
			}
		})
	}
}

type sessionHeaderPairBarrier struct {
	mu      sync.Mutex
	waiting int
	gate    chan struct{}
}

func newSessionHeaderPairBarrier() *sessionHeaderPairBarrier {
	return &sessionHeaderPairBarrier{gate: make(chan struct{})}
}

func (b *sessionHeaderPairBarrier) wait(ctx context.Context) error {
	b.mu.Lock()
	gate := b.gate
	b.waiting++
	if b.waiting == 2 {
		b.waiting = 0
		b.gate = make(chan struct{})
		close(gate)
	}
	b.mu.Unlock()
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestADR_0290_ProviderSessionHeaderConcurrentIsolationRace(t *testing.T) {
	var (
		mu       sync.Mutex
		captured = map[string]int{}
		barrier  = newSessionHeaderPairBarrier()
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := barrier.wait(r.Context()); err != nil {
			return
		}
		mu.Lock()
		captured[r.Header.Get(sessionIDHeaderName)]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedChatSSE)
	}))
	defer srv.Close()
	p := New(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, id := range []session.SessionID{"session-one", "session-two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				drainSessionHeaderStream(port.WithSessionID(ctx, id), t, p, string(id))
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if captured["session-one"] != 2 || captured["session-two"] != 2 || len(captured) != 2 {
		t.Fatalf("captured session headers = %v, want two attempts for each originating session only", captured)
	}
}
