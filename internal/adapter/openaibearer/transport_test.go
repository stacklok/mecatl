package openaibearer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdkopenai "github.com/openai/openai-go/v3"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	openai "github.com/stacklok/mecatl/provider/openai"
)

const completedResponseSSE = "event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"completed"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func writeToken(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBearerTokenFileTransportRotatesTrimsAndOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	var got []string
	base := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = append(got, req.Header.Get("Authorization"))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	client, err := NewHTTPClient(path, "https://api.example/v1", base)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"  first-token\n", "second-token\t"} {
		writeToken(t, path, token)
		req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer sdk-value")
		if _, err := client.Do(req); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Bearer sdk-value" {
			t.Fatal("transport mutated the caller's request")
		}
	}
	want := []string{"Bearer first-token", "Bearer second-token"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Authorization headers = %#v, want %#v", got, want)
	}
}

func TestBearerTokenFileTransportLoadFailuresDoNotCallBaseOrLeak(t *testing.T) {
	const secret = "token-material-must-not-leak"
	var calls atomic.Int32
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected call")
	})}
	for _, tc := range []struct {
		name    string
		prepare func(string)
		generic bool
	}{
		{name: "missing", generic: true},
		{name: "unreadable", generic: true, prepare: func(path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "empty", prepare: func(path string) { writeToken(t, path, " \n\t") }},
		{name: "spaces", prepare: func(path string) { writeToken(t, path, secret+" invalid") }},
		{name: "control", prepare: func(path string) { writeToken(t, path, secret+"\x00") }},
		{name: "unicode", prepare: func(path string) { writeToken(t, path, secret+"é") }},
		{name: "oversized", prepare: func(path string) { writeToken(t, path, secret+strings.Repeat("x", maxBearerTokenFileBytes)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if tc.prepare != nil {
				tc.prepare(path)
			}
			client, err := NewHTTPClient(path, "https://api.example/v1", base)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodPost, "https://api.example/v1/responses", nil)
			_, err = client.Do(req)
			if err == nil {
				t.Fatal("request succeeded")
			}
			if !errors.Is(err, llmresilience.ErrCredentials) {
				t.Fatalf("error classification = %v, want ErrCredentials", err)
			}
			if tc.generic && !strings.Contains(err.Error(), bearerTokenUnavailable) {
				t.Fatalf("generic load error = %q, want operator guidance %q", err, bearerTokenUnavailable)
			}
			for _, leaked := range []string{secret, path, "Authorization"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error leaked credential material: %v", err)
				}
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("base transport called %d times", calls.Load())
	}
}

func TestBearerTokenFileTransportOriginTLSAndRedirectPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeToken(t, path, "safe-token")
	for _, raw := range []string{
		"http://api.example/v1",
		"ftp://api.example/v1",
		"https://user@api.example/v1",
		"https://api.example/v1?credential=must-not-reach-diagnostics",
		"https://api.example/v1#credential-must-not-reach-diagnostics",
	} {
		if _, err := NewHTTPClient(path, raw, nil); err == nil {
			t.Errorf("accepted unsafe base URL %q", raw)
		} else if strings.Contains(err.Error(), "must-not-reach-diagnostics") {
			t.Errorf("base URL diagnostic leaked query or fragment: %v", err)
		}
	}
	if _, err := NewHTTPClient(path, "http://127.0.0.1:8080/v1", nil); err != nil {
		t.Fatalf("loopback HTTP rejected: %v", err)
	}

	var calls atomic.Int32
	client, err := NewHTTPClient(path, "https://api.example/v1", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected call")
	})})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://other.example/v1/responses", nil)
	if _, err := client.Do(req); err == nil || calls.Load() != 0 {
		t.Fatalf("cross-origin request err=%v calls=%d", err, calls.Load())
	}

	var redirectTargetCalled atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetCalled.Store(true) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	redirectClient, err := NewHTTPClient(path, origin.URL+"/v1", origin.Client())
	if err != nil {
		t.Fatal(err)
	}
	redirectReq, _ := http.NewRequest(http.MethodPost, origin.URL+"/v1/responses", strings.NewReader("body"))
	resp, err := redirectClient.Do(redirectReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || redirectTargetCalled.Load() {
		t.Fatalf("redirect status=%d targetCalled=%v", resp.StatusCode, redirectTargetCalled.Load())
	}
}

func TestBearerTokenFileTransportConcurrentRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeToken(t, path, "concurrent-token")
	var calls atomic.Int32
	client, err := NewHTTPClient(path, "https://api.example/v1", &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer concurrent-token" {
			return nil, errors.New("wrong authorization")
		}
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://api.example/v1/responses", nil)
			if _, err := client.Do(req); err != nil {
				t.Errorf("request: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 32 {
		t.Fatalf("calls = %d, want 32", calls.Load())
	}
}

func TestBearerTokenFileResponsesAPIDoesNotRetryAuthenticationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeToken(t, path, "rejected-token")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"unauthorized"}}`)
	}))
	defer srv.Close()
	client, err := NewHTTPClient(path, srv.URL+"/v1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider := openai.New(openai.WithBaseURL(srv.URL+"/v1"), openai.WithHTTPClient(client), openai.WithMaxRetries(0))
	seq, err := provider.Stream(context.Background(), port.LLMRequest{Model: "model", Messages: []session.Message{session.NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var terminalErr error
	for _, streamErr := range seq {
		if streamErr != nil {
			terminalErr = streamErr
		}
	}
	if terminalErr == nil {
		t.Fatal("authentication failure did not yield a terminal error")
	}
	var apiErr *sdkopenai.Error
	if !errors.As(terminalErr, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("terminal error = %v, want HTTP 401", terminalErr)
	}
	for _, leaked := range []string{"rejected-token", path} {
		if strings.Contains(terminalErr.Error(), leaked) {
			t.Fatalf("terminal error leaked credential material: %v", terminalErr)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("authentication failure request count = %d, want 1", calls.Load())
	}
}

func TestBearerTokenFileResponsesAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeToken(t, path, "responses-token")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		if req.URL.Path != "/v1/responses" || req.Header.Get("Authorization") != "Bearer responses-token" {
			t.Errorf("request path=%q authorization=%q", req.URL.Path, req.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedResponseSSE)
	}))
	defer srv.Close()
	client, err := NewHTTPClient(path, srv.URL+"/v1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	provider := openai.New(openai.WithBaseURL(srv.URL+"/v1"), openai.WithHTTPClient(client), openai.WithMaxRetries(0))
	seq, err := provider.Stream(context.Background(), port.LLMRequest{Model: "model", Messages: []session.Message{session.NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var completed bool
	for chunk, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Kind == port.ChunkText {
			text += chunk.Text
		}
		if chunk.Kind == port.ChunkDone {
			completed = true
		}
	}
	if text != "completed" || !completed || hits.Load() != 1 {
		t.Fatalf("Responses result text=%q completed=%v hits=%d", text, completed, hits.Load())
	}
}
