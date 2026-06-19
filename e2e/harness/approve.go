//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ApproveOverHTTP resolves a parked permission ask out-of-band over the daemon's
// HTTP surface: it POSTs /v1/sessions/{sessionID}/approve with the {ask_id,
// verdict} body and returns the response body for the caller to drain.
//
// This is the cloud-native Phase 2 REHYDRATE path: when the process that parked
// the ask has died and a SECOND mecated re-enters the loop AT the ask, the approve
// handler resumes the run and relays its events as Server-Sent Events on THIS
// response (the standard reconnect-and-relay shape — see internal/adapter/server/
// http.go approve()). The returned ReadCloser is that SSE stream; the caller MUST
// drain and Close it so the resumed run is not wedged behind an unread buffer (the
// server's drain-to-discard keeps the run honest, but the run's terminal state
// still has to be reached, which a drained body guarantees in-band).
//
// verdict is the three-way HTTP verdict string: "allow_once", "allow_always", or
// "deny" (see verdictFromHTTP server-side). A 2xx returns the body; any non-2xx
// returns an error with the body text for diagnosis.
func ApproveOverHTTP(ctx context.Context, httpAddr, sessionID, askID, verdict string) (io.ReadCloser, error) {
	body, err := json.Marshal(struct {
		AskID   string `json:"ask_id"`
		Verdict string `json:"verdict"`
	}{AskID: askID, Verdict: verdict})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("http://%s/v1/sessions/%s/approve", httpAddr, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, bytes.TrimSpace(msg))
	}
	return resp.Body, nil
}

// PromptOverHTTP starts a run on a session via POST /v1/sessions/{id}/prompt and
// returns the HTTP status code plus the response body. It is the cloud-native
// Phase 4 lease probe: a run-start refused by the cross-process lease comes back
// 409 Conflict (ErrSessionLeasedElsewhere → writeServiceError), which a streaming
// SSE client would otherwise hide. The caller inspects the status; on a 2xx it
// must drain+close the body itself (it is an SSE stream).
func PromptOverHTTP(ctx context.Context, httpAddr, sessionID, text string) (status int, body []byte, err error) {
	reqBody, merr := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if merr != nil {
		return 0, nil, merr
	}
	url := fmt.Sprintf("http://%s/v1/sessions/%s/prompt", httpAddr, sessionID)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if rerr != nil {
		return 0, nil, rerr
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, derr := http.DefaultClient.Do(req)
	if derr != nil {
		return 0, nil, fmt.Errorf("POST %s: %w", url, derr)
	}
	defer func() { _ = resp.Body.Close() }()
	// Read a bounded slice of the body for diagnosis / the refusal message. A 2xx
	// SSE stream would be unbounded, so cap the read — the caller asserting a 409
	// only needs the status and a short message.
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, body, nil
}

// DrainSSE reads an SSE stream to completion, returning the concatenated raw bytes
// (the caller can grep the frames). It always Closes the body. A 204 No Content
// (the same-process ack path) yields empty bytes — harmless for the rehydrate
// scenario, which expects the streaming relay path.
func DrainSSE(body io.ReadCloser) (out []byte, err error) {
	defer func() {
		if cerr := body.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return io.ReadAll(body)
}
