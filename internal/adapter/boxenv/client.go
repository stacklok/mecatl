package boxenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://ascii.dev/api/box/v1"

var errBoxNotReady = errors.New("boxenv: box did not become ready")

type apiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	poll    time.Duration
}

type boxState struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type boxEnvelope struct {
	Box boxState `json:"box"`
}

type createBoxRequest struct {
	Type       string `json:"type,omitempty"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
	NoEnv      bool   `json:"noEnv"`
}

type resumeBoxRequest struct {
	NoEnv bool `json:"noEnv"`
}

type commandRequest struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type commandResponse struct {
	Success  bool   `json:"success"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut"`
}

type apiStatusError struct{ status int }

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("boxenv: Box API returned HTTP %d", e.status)
}

func newAPIClient(apiKey, baseURL string, httpClient *http.Client) (*apiClient, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("boxenv: API key is required")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("boxenv: invalid Box API base URL")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("boxenv: non-loopback Box API base URL must use HTTPS")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 70 * time.Second}
	}
	return &apiClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient, poll: 500 * time.Millisecond}, nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func (c *apiClient) createBox(ctx context.Context, boxType string, ttlSeconds int) (boxState, error) {
	var out boxEnvelope
	err := c.doJSON(ctx, http.MethodPost, "/boxes", createBoxRequest{Type: boxType, TTLSeconds: ttlSeconds, NoEnv: true}, &out, http.StatusAccepted, http.StatusOK)
	if err != nil {
		return boxState{}, err
	}
	if out.Box.ID == "" {
		return boxState{}, errors.New("boxenv: Box API create response omitted box id")
	}
	return out.Box, nil
}

func (c *apiClient) getBox(ctx context.Context, id string) (boxState, error) {
	var out boxEnvelope
	if err := c.doJSON(ctx, http.MethodGet, "/boxes/"+url.PathEscape(id), nil, &out, http.StatusOK); err != nil {
		return boxState{}, err
	}
	if out.Box.ID == "" {
		out.Box.ID = id
	}
	return out.Box, nil
}

func (c *apiClient) resumeBox(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/boxes/"+url.PathEscape(id)+"/resume", resumeBoxRequest{NoEnv: true}, nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
}

func (c *apiClient) stopBox(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/boxes/"+url.PathEscape(id)+"/stop", nil, nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
}

func (c *apiClient) ensureReady(ctx context.Context, id string) error {
	state, err := c.getBox(ctx, id)
	if err != nil {
		return err
	}
	switch normalizeBoxState(state.State) {
	case "idle", "ready", "running":
		return nil
	case "stopped", "archived":
		if err := c.resumeBox(ctx, id); err != nil {
			return err
		}
	case "failed", "error", "deleted":
		return fmt.Errorf("%w: terminal state %q", errBoxNotReady, state.State)
	}
	return c.waitReady(ctx, id)
}

func (c *apiClient) waitReady(ctx context.Context, id string) error {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	resumed := false
	for {
		state, err := c.getBox(ctx, id)
		if err != nil {
			return err
		}
		switch normalizeBoxState(state.State) {
		case "idle", "ready", "running":
			return nil
		case "stopped", "archived":
			if !resumed {
				if err := c.resumeBox(ctx, id); err != nil {
					return err
				}
				resumed = true
			}
		case "failed", "error", "deleted":
			return fmt.Errorf("%w: terminal state %q", errBoxNotReady, state.State)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", errBoxNotReady, ctx.Err())
		case <-ticker.C:
		}
	}
}

func normalizeBoxState(state string) string { return strings.ToLower(strings.TrimSpace(state)) }

func (c *apiClient) runCommand(ctx context.Context, boxID, cwd, command string) (commandResponse, error) {
	timeout := 60
	if deadline, ok := ctx.Deadline(); ok {
		seconds := int(time.Until(deadline).Seconds())
		if seconds < 1 {
			seconds = 1
		}
		if seconds < timeout {
			timeout = seconds
		}
	}
	var out commandResponse
	if err := c.doJSON(ctx, http.MethodPost, "/boxes/"+url.PathEscape(boxID)+"/commands", commandRequest{Command: command, CWD: cwd, TimeoutSeconds: timeout}, &out, http.StatusOK); err != nil {
		return commandResponse{}, err
	}
	if out.TimedOut {
		return out, context.DeadlineExceeded
	}
	return out, nil
}

func (c *apiClient) doJSON(ctx context.Context, method, path string, in, out any, accepted ...int) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("boxenv: encode request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("boxenv: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("boxenv: Box API request: %w", err)
	}
	defer resp.Body.Close()
	ok := false
	for _, status := range accepted {
		if resp.StatusCode == status {
			ok = true
			break
		}
	}
	if !ok {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &apiStatusError{status: resp.StatusCode}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("boxenv: decode Box API response: %w", err)
	}
	return nil
}
