package llmresilience

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	openai "github.com/stacklok/mecatl/provider/openai"
)

func TestADR_0291_ProviderSessionHeaderRetryWrapper(t *testing.T) {
	const sessionID = "retry-wrapper-session"
	var (
		mu      sync.Mutex
		headers []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get(sessionaffinity.HeaderName))
		attempt := len(headers)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"retry"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer srv.Close()

	provider := Wrap(openai.New(openai.WithAPIKey("test"), openai.WithBaseURL(srv.URL+"/v1"), openai.WithMaxRetries(0)), Config{MaxAttempts: 2})
	ctx := port.WithSessionID(context.Background(), sessionID)
	seq, err := provider.Stream(ctx, port.LLMRequest{Model: "model", Messages: []session.Message{session.NewUserMessage("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 2 || headers[0] != sessionID || headers[1] != sessionID {
		t.Fatalf("retry wrapper provider headers = %#v, want exact session id on both attempts", headers)
	}
}
