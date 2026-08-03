package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/openai"
)

// terminalSSEHandler answers ANY request with a minimal terminal SSE
// response, so a client that DOES follow a redirect into it gets a
// well-formed streaming response its iterator can drain without hanging.
func terminalSSEHandler(hit *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if hit != nil {
			hit.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","sequence_number":0,"response":{"status":"completed"}}` + "\n\n"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}
}

// driveStream issues one Stream call against p and fully drains the
// iterator, returning the number of chunks yielded and EITHER the outer
// construction error or the first mid-stream iterator error (nil if the
// stream completed cleanly).
func driveStream(p port.LLMProvider) (chunks int, err error) {
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model:    "m",
		Messages: []session.Message{session.NewUserMessage("hi")},
	})
	if err != nil {
		return 0, err
	}
	var streamErr error
	for _, e := range seq {
		chunks++
		if e != nil {
			streamErr = e
		}
	}
	return chunks, streamErr
}

// TestGatewayInferenceRefusesRedirects is the F3 (issue #262 review finding
// 3) falsifiable e2e: a hostile/misconfigured listener squatting the
// ToolHive gateway's loopback port answers the INFERENCE request (not just
// the listing probe) with a redirect to an off-loopback "attacker" host. The
// gateway registry entry's provider must REFUSE to follow it (CWE-918) —
// the conversation body + Authorization header must never leave loopback.
//
// The primary assertion is the SECURITY property (attacker hit count == 0).
// Empirically, openai-go v3.37.0's request layer only
// treats status >= 400 as an API error (requestconfig.go), so a 3xx response
// CheckRedirect leaves as the "final" response is NOT surfaced as an error —
// the SSE decoder finds no recognized event lines in the redirect page's body.
// The Responses adapter's terminal-event guard then fails the zero-event EOF
// closed as a truncation. That is the correct, secure outcome: no data left
// loopback, no attacker-controlled content was parsed as a completion, and the
// non-response is no longer silently accepted as a clean turn.
//
// Anti-vacuous control (mutation-test-the-drift-guards discipline, in the
// SAME test): driving the identical redirect through a BARE
// openai.New(WithBaseURL(proxy)) — no WithHTTPClient option — DOES follow
// the redirect, hits the attacker, and yields the attacker's real completion
// chunks — proving this harness would actually catch a regression that
// dropped the option from newGatewayEntry.
func TestGatewayInferenceRefusesRedirects(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(terminalSSEHandler(&attackerHits))
	defer attacker.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusTemporaryRedirect)
	}))
	defer proxy.Close()

	// The gateway registry entry, built exactly as production does (the
	// explicit --toolhive-llm-base-url path; loopback-only is validated
	// separately in TestToolhiveExplicitBaseURL_LoopbackOnly — this test's
	// job is the redirect-refusal wiring, not the loopback gate).
	reg, err := buildProviderRegistry(Config{
		ToolhiveLLMBaseURL: proxy.URL,
		LLMMaxAttempts:     1, // no retries: keep the hit-count assertion deterministic
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	entry, ok := reg.Lookup(providerToolhive)
	if !ok {
		t.Fatal("toolhive entry missing")
	}

	chunks, streamErr := driveStream(entry.provider)
	if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
		t.Fatalf("gateway inference stream error = %v, want truncation after the refused redirect", streamErr)
	}
	if chunks != 0 {
		t.Errorf("gateway inference stream yielded %d chunks from a redirect response, want 0", chunks)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("attacker hit count = %d, want 0 (the gateway inference client must refuse to follow the redirect)", got)
	}

	// --- Anti-vacuous control: the SAME redirect through a bare adapter with
	// no WithHTTPClient option DOES follow it, proving the guard is real. ---
	attackerHits.Store(0)
	bare := openai.New(openai.WithBaseURL(proxy.URL))
	chunks, streamErr = driveStream(bare)
	if streamErr != nil {
		t.Fatalf("control: bare adapter (no redirect-refusing client) unexpectedly errored: %v", streamErr)
	}
	if chunks == 0 {
		t.Error("control: bare adapter yielded 0 chunks — expected the attacker's completion to come through")
	}
	if got := attackerHits.Load(); got < 1 {
		t.Fatalf("control: bare adapter did NOT follow the redirect (attacker hits = %d) — this test would not catch a regression", got)
	}
}
