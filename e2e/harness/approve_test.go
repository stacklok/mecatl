//go:build e2e

package harness

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveAskOverHTTP(t *testing.T) {
	t.Run("exact route body and acknowledgement", func(t *testing.T) {
		var gotPath, gotAccept string
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAccept = r.Header.Get("Accept")
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"run_id":"run-1","ask_id":"ask-1"}`)
		}))
		defer srv.Close()

		addr := strings.TrimPrefix(srv.URL, "http://")
		ack, err := ResolveAskOverHTTP(context.Background(), addr, "session-1", "run-1", "ask-1", "allow_once")
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != "/v1/sessions/session-1/controls/resolve-ask" {
			t.Fatalf("path = %q", gotPath)
		}
		if gotAccept != "application/json" {
			t.Fatalf("Accept = %q", gotAccept)
		}
		if string(gotBody) != `{"expected_run_id":"run-1","ask_id":"ask-1","verdict":"allow_once"}` {
			t.Fatalf("body = %s", gotBody)
		}
		if ack.RunID != "run-1" || ack.AskID != "ask-1" {
			t.Fatalf("ack = %+v", ack)
		}
	})

	t.Run("rejects mismatched acknowledgement", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"run_id":"other","ask_id":"ask-1"}`)
		}))
		defer srv.Close()
		_, err := ResolveAskOverHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "session-1", "run-1", "ask-1", "deny")
		if err == nil || !strings.Contains(err.Error(), "acknowledgement correlation") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("parses RFC 9457 conflict codes", func(t *testing.T) {
		for _, code := range []string{"session_leased_elsewhere", "stale_run_control"} {
			t.Run(code, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, `{"type":"urn:mecatl:error:`+code+`","title":"Conflict","status":409,"detail":"conflict detail","code":"`+code+`","error":"conflict detail"}`)
				}))
				defer srv.Close()
				_, err := ResolveAskOverHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "session-1", "run-1", "ask-1", "deny")
				var statusErr *HTTPStatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("err = %T %v", err, err)
				}
				if statusErr.StatusCode != http.StatusConflict || statusErr.Code != code {
					t.Fatalf("status error = %+v", statusErr)
				}
			})
		}
	})

	t.Run("preserves non-problem error without a machine code", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"code":"not-a-problem-code","error":"proxy failure"}`)
		}))
		defer srv.Close()
		_, err := ResolveAskOverHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "session-1", "run-1", "ask-1", "deny")
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("err = %T %v", err, err)
		}
		if statusErr.StatusCode != http.StatusBadGateway || statusErr.Code != "" || !strings.Contains(statusErr.Body, "proxy failure") {
			t.Fatalf("status error = %+v", statusErr)
		}
	})
}

func TestReplaySessionEventsOverHTTPRejectsOversize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("x", 32*1024)
		for written := 0; written <= maxEventReplayBytes; written += len(chunk) {
			_, _ = io.WriteString(w, chunk)
		}
	}))
	defer srv.Close()

	_, err := ReplaySessionEventsOverHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "session-1")
	if err == nil || !strings.Contains(err.Error(), "event replay exceeds") {
		t.Fatalf("err = %v", err)
	}
}
