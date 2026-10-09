package boatenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const defaultBaseURL = "https://boat.dev/api/v1"

const (
	// defaultCommandSeconds is the runner's own timeout, applied only when the
	// caller's context carries no deadline (the CommandRunner contract).
	defaultCommandSeconds = 60
	// maxCommandSeconds is the largest timeoutSeconds the Boat API accepts.
	maxCommandSeconds = 600
	// commandGrace lets a command the guest already timed out report back
	// before the HTTP request itself is abandoned.
	commandGrace = 30 * time.Second
	// defaultRequestTimeout bounds a non-command call whose context carries
	// no deadline. There is deliberately no client-wide timeout: a long
	// command must not be cut off by an unrelated HTTP limit.
	defaultRequestTimeout = 60 * time.Second
	// maxResponseBytes bounds any decoded API response. Boat caps command
	// output at 8 MiB per stream; JSON escaping can grow that several-fold.
	maxResponseBytes = 64 << 20
	// maxFileWriteBytes is the files API's per-call write limit (measured
	// live: larger writes are rejected with "File is too large").
	maxFileWriteBytes   = 5 << 20
	maxErrorDetailRunes = 240
)

var (
	errSandboxNotReady = errors.New("boatenv: sandbox did not become ready")
	// errSandboxNotRunning marks a command the API refused because the
	// sandbox is not running (HTTP 409). The command did not start, so it is
	// safe to make the sandbox ready and retry.
	errSandboxNotRunning = errors.New("boatenv: sandbox is not running")
	errResponseTooLarge  = errors.New("boatenv: Boat API response exceeds the size limit")
)

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
	NoEnv      bool `json:"noEnv"`
	TTLSeconds int  `json:"ttlSeconds,omitempty"`
}

type updateSandboxRequest struct {
	TTLSeconds int `json:"ttlSeconds"`
}

type commandRequest struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// commandResponse mirrors the API's command result. exitCode is null when
// the process was killed by a signal (the signal is then named), so it is
// decoded as a pointer rather than silently becoming 0.
type commandResponse struct {
	Success         bool    `json:"success"`
	ExitCode        *int    `json:"exitCode"`
	Signal          *string `json:"signal"`
	Stdout          string  `json:"stdout"`
	Stderr          string  `json:"stderr"`
	StdoutTruncated bool    `json:"stdoutTruncated"`
	StderrTruncated bool    `json:"stderrTruncated"`
	TimedOut        bool    `json:"timedOut"`
}

type fileWriteRequest struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// commandResult is a finished command with its exit status resolved.
type commandResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	StdoutTruncated bool
	StderrTruncated bool
}

// apiStatusError is a non-success API response. code and message come from
// the API's error envelope; they never contain request credentials.
type apiStatusError struct {
	status  int
	code    string
	message string
}

func (e *apiStatusError) Error() string {
	detail := e.code
	if e.message != "" && e.message != e.code {
		if detail != "" {
			detail += ": "
		}
		detail += e.message
	}
	if detail == "" {
		return fmt.Sprintf("boatenv: Boat API returned HTTP %d", e.status)
	}
	return fmt.Sprintf("boatenv: Boat API returned HTTP %d (%s)", e.status, detail)
}

// commandTimeoutError is a command the guest killed at its time limit. It
// matches context.DeadlineExceeded so callers keep their timeout handling,
// and it names the limit that was actually enforced.
type commandTimeoutError struct {
	seconds int
	capped  bool
}

func (e *commandTimeoutError) Error() string {
	if e.capped {
		return fmt.Sprintf("boatenv: command exceeded Boat's %ds maximum command duration", e.seconds)
	}
	return fmt.Sprintf("boatenv: command timed out after %ds", e.seconds)
}

func (*commandTimeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

func newAPIClient(apiKey, baseURL string, httpClient *http.Client) (*apiClient, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("boatenv: API key is required")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("boatenv: invalid Boat API base URL")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("boatenv: non-loopback Boat API base URL must use HTTPS")
	}
	client := &http.Client{}
	if httpClient != nil {
		copied := *httpClient
		client = &copied
	}
	// The API never redirects. Refusing redirects keeps the bearer credential
	// from being replayed to another scheme or host.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &apiClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: client, poll: 500 * time.Millisecond}, nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// me is the cheapest authenticated call the API offers. It proves the
// credential and endpoint without allocating anything.
func (c *apiClient) me(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/me", nil, nil, http.StatusOK)
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

// resumeSandbox restores an archived sandbox. It always keeps noEnv and
// re-arms the archival TTL, so a resumed sandbox that is later left idle is
// archived again instead of running indefinitely.
func (c *apiClient) resumeSandbox(ctx context.Context, id string, ttlSeconds int) error {
	return c.doJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/resume", resumeSandboxRequest{NoEnv: true, TTLSeconds: ttlSeconds}, nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
}

func (c *apiClient) stopSandbox(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/stop", nil, nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
}

// setTTL re-arms automatic archival to ttlSeconds from now (measured live:
// PATCH sets archiveAfter relative to the call, and the sandbox keeps
// running).
func (c *apiClient) setTTL(ctx context.Context, id string, ttlSeconds int) error {
	return c.doJSON(ctx, http.MethodPatch, "/sandboxes/"+url.PathEscape(id), updateSandboxRequest{TTLSeconds: ttlSeconds}, nil, http.StatusOK, http.StatusNoContent)
}

// putFile writes data to path inside the sandbox through the files API. It
// is the out-of-band channel for payloads too large for a command line.
func (c *apiClient) putFile(ctx context.Context, id, path string, data []byte) error {
	if len(data) > maxFileWriteBytes {
		return fmt.Errorf("boatenv: staged chunk of %d bytes exceeds the %d byte files API limit", len(data), maxFileWriteBytes)
	}
	req := fileWriteRequest{Path: path, Content: base64.StdEncoding.EncodeToString(data), Encoding: "base64"}
	return c.doJSON(ctx, http.MethodPut, "/sandboxes/"+url.PathEscape(id)+"/files", req, nil, http.StatusOK, http.StatusCreated, http.StatusNoContent)
}

func (c *apiClient) ensureReady(ctx context.Context, id string, ttlSeconds int) error {
	state, err := c.getSandbox(ctx, id)
	if err != nil {
		return err
	}
	resumed := false
	switch normalizeSandboxState(state.State) {
	case "idle", "ready", "running":
		// Already up (for example, still inside a closed binding's grace):
		// restore the full archival window for the work about to start.
		return c.setTTL(ctx, id, ttlSeconds)
	case "stopped", "archived":
		if err := c.resume(ctx, id, ttlSeconds); err != nil {
			return err
		}
		resumed = true
	case "failed", "error", "deleted":
		return fmt.Errorf("%w: terminal state %q", errSandboxNotReady, state.State)
	}
	// Transitional states (provisioning, provisioned, resuming, archiving) are
	// polled rather than acted on: a stop still in flight must reach archived
	// before a resume is meaningful.
	return c.waitReady(ctx, id, ttlSeconds, resumed)
}

func (c *apiClient) waitReady(ctx context.Context, id string, ttlSeconds int, resumed bool) error {
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
				if err := c.resume(ctx, id, ttlSeconds); err != nil {
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

// resume starts a resume, treating a conflict as another caller's resume
// already in flight: readiness polling then waits for it like any other.
func (c *apiClient) resume(ctx context.Context, id string, ttlSeconds int) error {
	err := c.resumeSandbox(ctx, id, ttlSeconds)
	var statusErr *apiStatusError
	if errors.As(err, &statusErr) && statusErr.status == http.StatusConflict {
		return nil
	}
	return err
}

func normalizeSandboxState(state string) string { return strings.ToLower(strings.TrimSpace(state)) }

// commandSeconds derives the guest time limit from the caller's deadline:
// the runner default applies only when there is no deadline, and the API's
// 600s ceiling is reported so a timeout names the limit really enforced.
func commandSeconds(ctx context.Context, now time.Time) (seconds int, capped bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultCommandSeconds, false
	}
	remaining := deadline.Sub(now)
	seconds = int((remaining + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	if seconds > maxCommandSeconds {
		return maxCommandSeconds, true
	}
	return seconds, false
}

func (c *apiClient) runCommand(ctx context.Context, sandboxID, cwd, command string) (commandResult, error) {
	seconds, capped := commandSeconds(ctx, time.Now())
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second+commandGrace)
	defer cancel()
	var out commandResponse
	err := c.doJSON(reqCtx, http.MethodPost, "/sandboxes/"+url.PathEscape(sandboxID)+"/commands", commandRequest{Command: command, CWD: cwd, TimeoutSeconds: seconds}, &out, http.StatusOK)
	if err != nil {
		var statusErr *apiStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusConflict {
			return commandResult{}, fmt.Errorf("%w: %w", errSandboxNotRunning, err)
		}
		return commandResult{}, err
	}
	result := resolveCommand(out)
	if out.TimedOut {
		return result, &commandTimeoutError{seconds: seconds, capped: capped}
	}
	return result, nil
}

// resolveCommand turns the API's result into a process exit status. A
// signal-killed process has no exit code; like a local runner it reports -1,
// and the signal is named on stderr so the failure is never read as success.
func resolveCommand(out commandResponse) commandResult {
	result := commandResult{Stdout: out.Stdout, Stderr: out.Stderr, StdoutTruncated: out.StdoutTruncated, StderrTruncated: out.StderrTruncated}
	switch {
	case out.ExitCode != nil && (*out.ExitCode != 0 || out.Success):
		result.ExitCode = *out.ExitCode
	default:
		result.ExitCode = -1
		note := "[boatenv: command failed without an exit status]"
		if out.Signal != nil && *out.Signal != "" {
			note = "[boatenv: command terminated by " + sanitizeDetail(*out.Signal) + "]"
		}
		if out.TimedOut {
			break
		}
		if result.Stderr != "" && !strings.HasSuffix(result.Stderr, "\n") {
			result.Stderr += "\n"
		}
		result.Stderr += note
	}
	return result
}

type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func statusError(status int, body []byte) *apiStatusError {
	out := &apiStatusError{status: status}
	var env errorEnvelope
	if json.Unmarshal(body, &env) == nil {
		out.code, out.message = env.Code, env.Message
		if env.Error != nil {
			if env.Error.Code != "" {
				out.code = env.Error.Code
			}
			if env.Error.Message != "" {
				out.message = env.Error.Message
			}
		}
	}
	out.code, out.message = sanitizeDetail(out.code), sanitizeDetail(out.message)
	return out
}

// sanitizeDetail keeps server-provided error text printable and short
// before it reaches diagnostics or a tool result.
func sanitizeDetail(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > maxErrorDetailRunes {
		s = string(runes[:maxErrorDetailRunes]) + "…"
	}
	return s
}

func (c *apiClient) doJSON(ctx context.Context, method, path string, in, out any, accepted ...int) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
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
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return statusError(resp.StatusCode, detail)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("boatenv: read Boat API response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return errResponseTooLarge
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("boatenv: decode Boat API response: %w", err)
	}
	return nil
}
