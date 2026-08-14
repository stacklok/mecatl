package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

func TestSessionIDHeader(t *testing.T) {
	var (
		mu       sync.Mutex
		captured = map[string]string{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		captured[body.Model] = r.Header.Get(sessionIDHeaderName)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedChatSSE)
	}))
	defer srv.Close()
	p := New(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"))

	tests := []struct {
		name string
		id   *session.SessionID
		want string
	}{
		{name: "absent"},
		{name: "empty omitted", id: ptrSessionID("")},
		{name: "exact", id: ptrSessionID("session/exact:42"), want: "session/exact:42"},
		{name: "leading space omitted", id: ptrSessionID(" session")},
		{name: "trailing space omitted", id: ptrSessionID("session ")},
		{name: "leading tab omitted", id: ptrSessionID("\tsession")},
		{name: "trailing tab omitted", id: ptrSessionID("session\t")},
		{name: "internal space and tab preserved", id: ptrSessionID("session id\tpart"), want: "session id\tpart"},
		{name: "carriage return omitted", id: ptrSessionID("bad\rid")},
		{name: "newline omitted", id: ptrSessionID("bad\nid")},
		{name: "control omitted", id: ptrSessionID("bad\x00id")},
		{name: "DEL omitted", id: ptrSessionID("bad\x7fid")},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := fmt.Sprintf("model-%d", i)
			ctx := context.Background()
			if tt.id != nil {
				ctx = port.WithSessionID(ctx, *tt.id)
			}
			drainSessionHeaderStream(ctx, t, p, model)
			mu.Lock()
			got := captured[model]
			mu.Unlock()
			if got != tt.want {
				t.Fatalf("X-Mecatl-Session-ID = %q, want %q", got, tt.want)
			}
		})
	}

	var wg sync.WaitGroup
	for i := range 12 {
		model := fmt.Sprintf("concurrent-%d", i)
		id := session.SessionID("session-" + model)
		wg.Add(1)
		go func() {
			defer wg.Done()
			drainSessionHeaderStream(port.WithSessionID(context.Background(), id), t, p, model)
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	for i := range 12 {
		model := fmt.Sprintf("concurrent-%d", i)
		want := "session-" + model
		if got := captured[model]; got != want {
			t.Errorf("%s header = %q, want %q", model, got, want)
		}
	}
}

func ptrSessionID(id session.SessionID) *session.SessionID { return &id }
