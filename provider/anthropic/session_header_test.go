package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const completedMessageSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

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
	Name  string `json:"name"`
	Value string `json:"value"`
	Legal bool   `json:"legal"`
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
			if got := validHTTPHeaderValue(vector.Value); got != vector.Legal {
				t.Errorf("validHTTPHeaderValue(%q) = %v, want %v", vector.Value, got, vector.Legal)
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
		_, _ = io.WriteString(w, completedMessageSSE)
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
				_, _ = io.WriteString(w, completedMessageSSE)
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
	mu         sync.Mutex
	cond       *sync.Cond
	waiting    int
	generation int
}

func newSessionHeaderPairBarrier() *sessionHeaderPairBarrier {
	barrier := &sessionHeaderPairBarrier{}
	barrier.cond = sync.NewCond(&barrier.mu)
	return barrier
}

func (b *sessionHeaderPairBarrier) wait() {
	b.mu.Lock()
	generation := b.generation
	b.waiting++
	if b.waiting == 2 {
		b.waiting = 0
		b.generation++
		b.cond.Broadcast()
		b.mu.Unlock()
		return
	}
	for generation == b.generation {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

func TestADR_0290_ProviderSessionHeaderConcurrentIsolationRace(t *testing.T) {
	var (
		mu       sync.Mutex
		captured = map[string]int{}
		barrier  = newSessionHeaderPairBarrier()
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		barrier.wait()
		mu.Lock()
		captured[r.Header.Get(sessionIDHeaderName)]++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedMessageSSE)
	}))
	defer srv.Close()
	p := New(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"))
	var wg sync.WaitGroup
	for _, id := range []session.SessionID{"session-one", "session-two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				drainSessionHeaderStream(port.WithSessionID(context.Background(), id), t, p, string(id))
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
