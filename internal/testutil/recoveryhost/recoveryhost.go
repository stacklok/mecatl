// Package recoveryhost contains offline fixtures for command-root recovery proofs.
package recoveryhost

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Fixture serves one actual failed inference followed by one clean stream.
// Model discovery is also confined to this HTTP server.
type Fixture struct {
	Server *httptest.Server
	Calls  atomic.Int32
}

type ShutdownFixture struct {
	Server  *httptest.Server
	Calls   atomic.Int32
	Probe   chan struct{}
	Stopped chan struct{}
}

func newShutdownFixture(t *testing.T) *ShutdownFixture {
	t.Helper()
	f := &ShutdownFixture{Probe: make(chan struct{}), Stopped: make(chan struct{})}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5","object":"model"}]}`)
			return
		}
		switch f.Calls.Add(1) {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"fixture unavailable"}}`)
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(f.Probe)
			<-r.Context().Done()
			close(f.Stopped)
		default:
			t.Error("provider request continued after host shutdown")
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// New starts a loopback-only inference fixture owned by t.
func New(t *testing.T) *Fixture {
	t.Helper()
	f := &Fixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-5","object":"model"}]}`)
			return
		}
		if f.Calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"fixture unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"host recovered\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// Flags selects the offline endpoint and a two-call recovery policy.
func (f *Fixture) Flags() []string {
	return []string{"--model=gpt-5", "--openai-base-url=" + f.Server.URL + "/v1", "--toolhive-llm=false", "--product-metrics=false", "--llm-max-attempts=2", "--llm-recovery-budget=5s"}
}

// Command uses only explicit fixture environment, never inherited provider auth.
func Command(t *testing.T, root, mode string, args []string) *exec.Cmd {
	t.Helper()
	argv := append([]string{"-test.run=^TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy$", "--", "recovery-host", mode}, args...)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], argv...) //nolint:gosec // own test binary, fixed test entry point
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_RUNTIME_DIR=" + root, "XDG_CONFIG_DIRS=" + root, "XDG_DATA_DIRS=" + root,
		"OPENAI_API_KEY=test", "MECATL_PRODUCT_METRICS=false", "DO_NOT_TRACK=1"}
	return cmd
}

// ChildArgs lets the SAME test enter its real command root in the subprocess.
// A read-only stderr file deterministically fails writes (portable EBADF).
func ChildArgs(t *testing.T) ([]string, bool) {
	t.Helper()
	args := flag.Args()
	if len(args) < 2 || args[0] != "recovery-host" {
		return nil, false
	}
	if args[1] == "failed writer" {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = f
		t.Cleanup(func() { os.Stderr = old; _ = f.Close() })
	}
	return args[2:], true
}

// AssertLog requires the wrapper's real successful-recovery record.
func AssertLog(t *testing.T, logs string) {
	t.Helper()
	if !strings.Contains(logs, "llm provider recovery") || !strings.Contains(logs, "decision=recovered") || !strings.Contains(logs, "attempt=2") {
		t.Fatalf("actual recovery decision missing from selected host sink: %s", logs)
	}
}

func createDaemonSession(ctx context.Context, t *testing.T, client *http.Client, addr string) string {
	t.Helper()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/sessions", strings.NewReader(`{"mode":"default"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			var created struct {
				SessionID string `json:"session_id"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&created)
			_ = resp.Body.Close()
			if decodeErr != nil || resp.StatusCode >= 300 {
				t.Fatalf("create session: status=%d error=%v", resp.StatusCode, decodeErr)
			}
			if created.SessionID == "" {
				t.Fatal("created session has no ID")
			}
			return created.SessionID
		}
		select {
		case <-ctx.Done():
			t.Fatal("daemon did not become ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Daemon drives the actual command run/serve path in a child process. No logger
// is injected; the command chooses stderr and its operational logger itself.
func Daemon(t *testing.T, extra []string) {
	t.Helper()
	for _, mode := range []string{"operational sink", "failed writer"} {
		t.Run(mode, func(t *testing.T) {
			f := New(t)
			root := t.TempDir()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			_ = listener.Close()
			args := append(f.Flags(), "--grpc-addr=127.0.0.1:0", "--http-addr="+addr, "--workspace="+root,
				"--no-soul", "--no-user-model", "--user-model-dir="+filepath.Join(root, "usermodel"),
				"--permissions-conventional=false", "--agents-conventional=false", "--posture=strict", "--no-shell")
			args = append(args, extra...)
			cmd := Command(t, root, mode, args)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			joined := false
			defer func() {
				if !joined {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			client := &http.Client{Timeout: 3 * time.Second}
			id := createDaemonSession(ctx, t, client, addr)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, id), strings.NewReader(`{"text":"recover"}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || !strings.Contains(string(body), "host recovered") || !strings.Contains(string(body), "end_turn") {
				t.Fatalf("recovery failed: %s %v", body, err)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			joined = true
			if err != nil {
				t.Fatalf("daemon exit: %v stderr=%s stdout=%s", err, &stderr, &stdout)
			}
			if f.Calls.Load() != 2 {
				t.Fatalf("actual provider calls=%d want 2", f.Calls.Load())
			}
			if mode == "operational sink" {
				AssertLog(t, stderr.String())
			}
			if strings.Contains(stdout.String(), "llm provider recovery") {
				t.Fatal("operational recovery logs leaked to stdout")
			}
		})
	}
}

// DaemonShutdown proves the actual command process cancels an established
// recovery probe and that a fresh process sharing its store stays idle.
func DaemonShutdown(t *testing.T, extra []string) {
	t.Helper()
	f := newShutdownFixture(t)
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	start := func(addr string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
		args := []string{"--model=gpt-5", "--openai-base-url=" + f.Server.URL + "/v1", "--toolhive-llm=false", "--product-metrics=false",
			"--llm-max-attempts=3", "--llm-recovery-budget=5s", "--llm-breaker-threshold=1", "--llm-breaker-cooldown=20ms",
			"--grpc-addr=127.0.0.1:0", "--http-addr=" + addr, "--workspace=" + root, "--store-dir=" + storeDir,
			"--no-soul", "--no-user-model", "--user-model-dir=" + filepath.Join(root, "usermodel"),
			"--permissions-conventional=false", "--agents-conventional=false", "--posture=strict", "--no-shell"}
		args = append(args, extra...)
		cmd := Command(t, root, "shutdown", args)
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd, stdout, stderr
	}
	freeAddr := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		_ = listener.Close()
		return addr
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}

	addr := freeAddr()
	first, firstOut, firstErr := start(addr)
	joined := false
	defer func() {
		if !joined {
			_ = first.Process.Kill()
			_ = first.Wait()
		}
	}()
	id := createDaemonSession(ctx, t, client, addr)
	requestDone := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, id), strings.NewReader(`{"text":"recover"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			err = resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-f.Probe:
	case <-ctx.Done():
		t.Fatal("provider probe was not established")
	}
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.Stopped:
	case <-ctx.Done():
		_ = first.Process.Kill()
		_ = first.Wait()
		joined = true
		t.Fatalf("host signal did not cancel provider request: stderr=%s stdout=%s", firstErr, firstOut)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first daemon exit: %v stderr=%s stdout=%s", err, firstErr, firstOut)
	}
	joined = true
	select {
	case <-requestDone:
	case <-ctx.Done():
		t.Fatal("client request did not settle after host shutdown")
	}
	if f.Calls.Load() != 2 {
		t.Fatalf("provider calls at shutdown=%d want 2", f.Calls.Load())
	}

	restartAddr := freeAddr()
	second, secondOut, secondErr := start(restartAddr)
	secondJoined := false
	defer func() {
		if !secondJoined {
			_ = second.Process.Kill()
			_ = second.Wait()
		}
	}()
	_ = createDaemonSession(ctx, t, client, restartAddr)
	time.Sleep(100 * time.Millisecond)
	if f.Calls.Load() != 2 {
		t.Fatalf("restarted process resumed inference without explicit start: %d", f.Calls.Load())
	}
	if err := second.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second daemon exit: %v stderr=%s stdout=%s", err, secondErr, secondOut)
	}
	secondJoined = true
}
