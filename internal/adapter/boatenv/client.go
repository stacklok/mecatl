package boatenv

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

const defaultBaseURL = "https://boat.dev/api/v1"

var errSandboxNotReady = errors.New("boatenv: sandbox did not become ready")

type apiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	poll    time.Duration
}

type sandboxState struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type sandboxEnvelope struct {
	Sandbox sandboxState `json:"sandbox"`
}

type createSandboxRequest struct {
	Type       string `json:"type,omitempty"`
	TTLSeconds int    `json:"ttlSeconds,omitempty"`
	NoEnv      bool   `json:"noEnv"`
}

type resumeSandboxRequest struct {
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
	return fmt.Sprintf("boatenv: Boat API returned HTTP %d", e.status)
}

func newAPIClient(apiKey, baseURL string, httpClient *http.Client) (*apiClient, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("boatenv: API key is required")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("boatenv: invalid Boat API base URL")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("boatenv: non-loopback Boat API base URL must use HTTPS")
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

func (c *apiClient) createSandbox(ctx context.Context, machineType string, ttlSeconds int) (sandboxState, error) {
	var out sandboxEnvelope
	err := c.doJSON(ctx, http.MethodPost, "/sandboxes", createSandboxRequest{Type: machineType, TTLSeconds: ttlSeconds, NoEnv: true}, &out, http.StatusAccepted, http.StatusOK)
	if err != nil {
		return sandboxState{}, err
	}
	if out.Sandbox.ID == "" {
		return sandboxState{}, errors.New("boatenv: Boat API create response omitted sandbox id")
	}
	return out.Sandbox, nil
}

func (c *apiClient) getSandbox(ctx context.Context, id string) (sandboxState, error) {
	var out sandboxEnvelope
	if err := c.doJSON(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(id), nil, &out, http.StatusOK); err != nil {
		return sandboxState{}, err
	}
	if out.Sandbox.ID == "" {
		out.Sandbox.ID = id
	}
	return out.Sandbox, nil
}

func (c *apiClient) resumeSandbox(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/resume", resumeSandboxRequest{NoEnv: true}, nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
}

func (c *apiClient) stopSandbox(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/stop", nil, nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
}

// me is the cheapest authenticated call the API offers. It proves the
// credential and endpoint without allocating anything.
func (c *apiClient) me(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/me", nil, nil, http.StatusOK)
}

func (c *apiClient) ensureReady(ctx context.Context, id string) error {
	state, err := c.getSandbox(ctx, id)
	if err != nil {
		return err
	}
	resumed := false
	switch normalizeSandboxState(state.State) {
	case "idle", "ready", "running":
		return nil
	case "stopped", "archived":
		if err := c.resumeSandbox(ctx, id); err != nil {
			return err
		}
		resumed = true
	case "failed", "error", "deleted":
		return fmt.Errorf("%w: terminal state %q", errSandboxNotReady, state.State)
	}
	// Transitional states (provisioning, provisioned, resuming, archiving) are
	// polled rather than acted on: a stop still in flight must reach archived
	// before a resume is meaningful.
	return c.waitReady(ctx, id, resumed)
}

func (c *apiClient) waitReady(ctx context.Context, id string, resumed bool) error {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		state, err := c.getSandbox(ctx, id)
		if err != nil {
			return err
		}
		switch normalizeSandboxState(state.State) {
		case "idle", "ready", "running":
			return nil
		case "stopped", "archived":
			if !resumed {
				if err := c.resumeSandbox(ctx, id); err != nil {
					return err
				}
				resumed = true
			}
		case "failed", "error", "deleted":
			return fmt.Errorf("%w: terminal state %q", errSandboxNotReady, state.State)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", errSandboxNotReady, ctx.Err())
		case <-ticker.C:
		}
	}
}

func normalizeSandboxState(state string) string { return strings.ToLower(strings.TrimSpace(state)) }

func (c *apiClient) runCommand(ctx context.Context, sandboxID, cwd, command string) (commandResponse, error) {
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
	if err := c.doJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(sandboxID)+"/commands", commandRequest{Command: command, CWD: cwd, TimeoutSeconds: timeout}, &out, http.StatusOK); err != nil {
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
			return fmt.Errorf("boatenv: encode request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("boatenv: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("boatenv: Boat API request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
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
		return fmt.Errorf("boatenv: decode Boat API response: %w", err)
	}
	return nil
}
