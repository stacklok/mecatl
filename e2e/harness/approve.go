//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// ResolveAskAcknowledgement is the exact run/ask correlation acknowledged by
// the resolve-ask control.
type ResolveAskAcknowledgement struct {
	RunID string `json:"run_id"`
	AskID string `json:"ask_id"`
}

// HTTPStatusError preserves a non-success response for retry classification and
// diagnostics.
type HTTPStatusError struct {
	Method     string
	URL        string
	StatusCode int
	Code       string
	Body       string
}

type problemDocument struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code"`
	Error    string `json:"error,omitempty"`
}

const maxEventReplayBytes = 32 << 20

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s %s: status %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

func readHTTPStatusError(resp *http.Response, method, url string) *HTTPStatusError {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := bytes.TrimSpace(msg)
	var problem problemDocument
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "application/problem+json" {
		_ = json.Unmarshal(body, &problem)
	}
	return &HTTPStatusError{
		Method: method, URL: url, StatusCode: resp.StatusCode, Code: problem.Code, Body: string(body),
	}
}

// ResolveAskOverHTTP resolves an ordinary permission ask on one exact run. The
// endpoint returns an acknowledgement only; observe completion through the
// durable session event replay.
func ResolveAskOverHTTP(ctx context.Context, httpAddr, sessionID, runID, askID, verdict string) (ResolveAskAcknowledgement, error) {
	body, err := json.Marshal(struct {
		ExpectedRunID string `json:"expected_run_id"`
		AskID         string `json:"ask_id"`
		Verdict       string `json:"verdict"`
	}{ExpectedRunID: runID, AskID: askID, Verdict: verdict})
	if err != nil {
		return ResolveAskAcknowledgement{}, err
	}
	url := fmt.Sprintf("http://%s/v1/sessions/%s/controls/resolve-ask", httpAddr, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ResolveAskAcknowledgement{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ResolveAskAcknowledgement{}, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ResolveAskAcknowledgement{}, readHTTPStatusError(resp, http.MethodPost, url)
	}
	var ack ResolveAskAcknowledgement
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ack); err != nil {
		return ResolveAskAcknowledgement{}, fmt.Errorf("POST %s: decode acknowledgement: %w", url, err)
	}
	if ack.RunID != runID || ack.AskID != askID {
		return ResolveAskAcknowledgement{}, fmt.Errorf("POST %s: acknowledgement correlation = run_id %q ask_id %q, want %q and %q", url, ack.RunID, ack.AskID, runID, askID)
	}
	return ack, nil
}

// ReplaySessionEventsOverHTTP reads the finite durable event replay for a
// session. The response uses SSE framing but ends at the current log tail.
func ReplaySessionEventsOverHTTP(ctx context.Context, httpAddr, sessionID string) ([]byte, error) {
	url := fmt.Sprintf("http://%s/v1/sessions/%s/events", httpAddr, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, readHTTPStatusError(resp, http.MethodGet, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEventReplayBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: read event replay: %w", url, err)
	}
	if len(body) > maxEventReplayBytes {
		return nil, fmt.Errorf("GET %s: event replay exceeds %d bytes", url, maxEventReplayBytes)
	}
	return body, nil
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
