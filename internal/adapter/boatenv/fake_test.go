package boatenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Limits the live API enforces, measured against boat.dev (2026-09-23). The
// fake enforces them too, so an adapter that only works against a lenient
// fake fails here first.
const (
	fakeMaxCommandBytes = 128 << 10 // the whole command is one guest argv string
	fakeMaxStreamBytes  = 8 << 20   // stdout/stderr are truncated with a flag
	fakeMaxFileWrite    = 5 << 20   // files API per-call write limit
)

// fakeBoatAPI is an offline stand-in for the boat.dev v1 API. Commands run
// with the host's /bin/sh in a per-sandbox temp dir, so the real guest helper
// is exercised end to end.
type fakeBoatAPI struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	next   int

	sandboxes map[string]*fakeSandbox
	creates   []createSandboxRequest
	resumes   []resumeSandboxRequest
	ttls      []int
	timeouts  []int
	commands  int
	gets      int

	// startStates, when set, is the state a new sandbox reports to its first
	// GETs before becoming idle (to exercise readiness polling).
	startStates []string
}

type fakeSandbox struct {
	state   string
	root    string
	pending []string
}

func newFakeBoatAPI(t *testing.T) *fakeBoatAPI {
	t.Helper()
	f := &fakeBoatAPI{t: t, sandboxes: map[string]*fakeSandbox{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false, "type": "sandbox.error", "status": status, "code": code, "message": message,
		"error": map[string]any{"code": code, "message": message, "status": status},
	})
}

func (f *fakeBoatAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		apiError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
		return
	}
	if r.URL.Path == "/me" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "type": "user.info"})
		return
	}
	if r.URL.Path == "/sandboxes" && r.Method == http.MethodPost {
		var req createSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "invalid_json", "bad request")
			return
		}
		root := f.t.TempDir()
		f.mu.Lock()
		f.next++
		id := fmt.Sprintf("sbx-%d", f.next)
		f.sandboxes[id] = &fakeSandbox{state: "idle", root: root, pending: append([]string(nil), f.startStates...)}
		f.creates = append(f.creates, req)
		f.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "provisioning", "sandbox": sandboxState{ID: id, State: "provisioning"}})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "sandboxes" {
		apiError(w, http.StatusNotFound, "not_found", "no such route")
		return
	}
	id := parts[1]
	f.mu.Lock()
	sb := f.sandboxes[id]
	f.mu.Unlock()
	if sb == nil {
		apiError(w, http.StatusNotFound, "sandbox_not_found", "sandbox not found")
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		f.mu.Lock()
		f.gets++
		state := sb.state
		if len(sb.pending) > 0 {
			state, sb.pending = sb.pending[0], sb.pending[1:]
		}
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sandbox": sandboxState{ID: id, State: state}})
	case len(parts) == 2 && r.Method == http.MethodPatch:
		var req updateSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TTLSeconds < 1 {
			apiError(w, http.StatusBadRequest, "invalid_request", "ttlSeconds must be positive")
			return
		}
		f.mu.Lock()
		f.ttls = append(f.ttls, req.TTLSeconds)
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sandbox": sandboxState{ID: id, State: sb.state}})
	case len(parts) == 3 && parts[2] == "stop" && r.Method == http.MethodPost:
		f.setState(sb, "archived")
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "archiving"})
	case len(parts) == 3 && parts[2] == "resume" && r.Method == http.MethodPost:
		var req resumeSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.NoEnv {
			apiError(w, http.StatusBadRequest, "invalid_request", "resume must preserve noEnv")
			return
		}
		f.mu.Lock()
		f.resumes = append(f.resumes, req)
		sb.state = "idle"
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "resuming"})
	case len(parts) == 3 && parts[2] == "files" && r.Method == http.MethodPut:
		f.putFile(w, r, sb)
	case len(parts) == 3 && parts[2] == "commands" && r.Method == http.MethodPost:
		f.runCommand(w, r, sb)
	default:
		apiError(w, http.StatusNotFound, "not_found", "no such route")
	}
}

func (f *fakeBoatAPI) setState(sb *fakeSandbox, state string) {
	f.mu.Lock()
	sb.state = state
	f.mu.Unlock()
}

func (f *fakeBoatAPI) putFile(w http.ResponseWriter, r *http.Request, sb *fakeSandbox) {
	var req fileWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Encoding != "base64" {
		apiError(w, http.StatusBadRequest, "invalid_request", "bad file write")
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "bad base64")
		return
	}
	if len(data) > fakeMaxFileWrite {
		apiError(w, http.StatusBadRequest, "sandbox_direct_failed", fmt.Sprintf("File is too large for write_file (%d bytes > %d).", len(data), fakeMaxFileWrite))
		return
	}
	target := req.Path
	if !filepath.IsAbs(target) {
		target = filepath.Join(sb.root, target)
	} else if !strings.HasPrefix(target, "/tmp/") {
		apiError(w, http.StatusBadRequest, "invalid_path", "path must resolve under the work directory or /tmp")
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		apiError(w, http.StatusInternalServerError, "io", err.Error())
		return
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		apiError(w, http.StatusInternalServerError, "io", err.Error())
		return
	}
	f.t.Cleanup(func() { _ = os.Remove(target) })
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "type": "file.written", "path": req.Path, "size": len(data)})
}

func (f *fakeBoatAPI) runCommand(w http.ResponseWriter, r *http.Request, sb *fakeSandbox) {
	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "bad command")
		return
	}
	f.mu.Lock()
	state := sb.state
	f.commands++
	f.timeouts = append(f.timeouts, req.TimeoutSeconds)
	f.mu.Unlock()
	if state != "idle" {
		apiError(w, http.StatusConflict, "sandbox_not_running", "sandbox is "+state)
		return
	}
	if req.TimeoutSeconds < 1 || req.TimeoutSeconds > 600 {
		apiError(w, http.StatusBadRequest, "invalid_timeout", "timeoutSeconds must be an integer in 1-600")
		return
	}
	if len(req.Command) > fakeMaxCommandBytes {
		apiError(w, http.StatusBadRequest, "sandbox_direct_failed", "E2BIG: argument list too long, posix_spawn 'systemd-run'")
		return
	}
	cwd := sb.root
	if req.CWD != "" && req.CWD != "." {
		cwd = filepath.Join(sb.root, filepath.FromSlash(req.CWD))
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		apiError(w, http.StatusBadRequest, "sandbox_direct_failed", "cwd must be an existing directory.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	resp := commandResponse{Stdout: stdout.String(), Stderr: stderr.String()}
	if len(resp.Stdout) > fakeMaxStreamBytes {
		resp.Stdout, resp.StdoutTruncated = resp.Stdout[:fakeMaxStreamBytes], true
	}
	if len(resp.Stderr) > fakeMaxStreamBytes {
		resp.Stderr, resp.StderrTruncated = resp.Stderr[:fakeMaxStreamBytes], true
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		zero := 0
		resp.ExitCode, resp.Success = &zero, true
	case errors.As(runErr, &exitErr):
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			name := unix.SignalName(status.Signal())
			resp.Signal = &name
		} else {
			code := exitErr.ExitCode()
			resp.ExitCode = &code
		}
	default:
		code := 127
		resp.ExitCode = &code
	}
	resp.TimedOut = ctx.Err() == context.DeadlineExceeded
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (f *fakeBoatAPI) lastCreateNoEnv() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates) > 0 && f.creates[len(f.creates)-1].NoEnv
}

func (f *fakeBoatAPI) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func (f *fakeBoatAPI) resumeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resumes)
}

func (f *fakeBoatAPI) lastTTL() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ttls) == 0 {
		return 0
	}
	return f.ttls[len(f.ttls)-1]
}

func (f *fakeBoatAPI) lastTimeout() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.timeouts) == 0 {
		return 0
	}
	return f.timeouts[len(f.timeouts)-1]
}

func (f *fakeBoatAPI) state(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sb := f.sandboxes[id]; sb != nil {
		return sb.state
	}
	return ""
}

func (f *fakeBoatAPI) root(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sb := f.sandboxes[id]; sb != nil {
		return sb.root
	}
	return ""
}

// fakeProvider builds a provider over the fake with fast polling and the
// helper lock kept inside the test's temp dir.
func fakeProvider(t *testing.T, fake *fakeBoatAPI, mutate ...func(*Config)) *Provider {
	t.Helper()
	cfg := Config{
		APIKey: "test-key", BaseURL: fake.server.URL, HTTPClient: fake.server.Client(),
		Scope: "test", TTLSeconds: 60, ReadyTimeout: 5 * time.Second,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	provider, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	provider.client.poll = time.Millisecond
	provider.lockDir = t.TempDir()
	return provider
}
