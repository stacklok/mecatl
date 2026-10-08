package guestexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

var scenarioBinding = control.Binding{
	Owner:         "caller:alice",
	SessionID:     "session-1",
	EnvironmentID: "environment-1",
	Ref:           "microvm:environment-1",
	Generation:    7,
}

var capabilityKey = []byte("0123456789abcdef0123456789abcdef")

var testWorkloadIdentity = WorkloadIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}

func TestGuestCommandEnvUsesBoundRepositoryPaths(t *testing.T) {
	const gitDir = "/run/mecatl/repositories/logical/metadata"
	const root = "/run/mecatl/repositories/logical/worktree"
	env := guestCommandEnv(DefaultRuntimeContract(), gitDir, root)
	if !slices.Contains(env, "GIT_DIR="+gitDir) || !slices.Contains(env, "GIT_WORK_TREE="+root) {
		t.Fatalf("guest command environment does not use repository binding: %v", env)
	}
}

func TestManagedTemporaryScopeIsGuestOwnedAndCleaned(t *testing.T) {
	workspaceRoot := t.TempDir()
	server, err := NewGuestServer(ServerConfig{
		Binding: scenarioBinding, WorkspaceRoot: workspaceRoot, WorkloadIdentity: testWorkloadIdentity,
	})
	if err != nil {
		t.Fatalf("NewGuestServer: %v", err)
	}
	verifier, err := control.NewCapabilityVerifier(capabilityKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := control.NewCapabilityIssuer(capabilityKey)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := issuer.Issue(scenarioBinding)
	if err != nil {
		t.Fatal(err)
	}
	client, closeServer := openExecClient(t, server, verifier, scenarioBinding, capability, defaultMaxFrameBytes)
	defer closeServer()
	runner := mustRunner(t, RunnerConfig{Binding: scenarioBinding, Client: client})

	result, err := runner.RunWithTemporaryScope(context.Background(), `printf '%s\n' "$TMPDIR"; printf guest-only > "$TMPDIR/marker"; cat "$TMPDIR/marker"`, tool.TemporaryScopeManaged)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("managed command: result=%+v err=%v", result, err)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "/tmp/mecatl-managed-") || lines[1] != "guest-only" {
		t.Fatalf("managed temporary result = %q", result.Stdout)
	}
	if _, err := os.Stat(lines[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed temporary directory survived command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed temporary file escaped into workspace: %v", err)
	}
}

func TestManagedTemporaryScopeCleansUpAfterCancellation(t *testing.T) {
	runner, closeServer := scenarioRunner(t, scenarioBinding, Limits{CancelGrace: 100 * time.Millisecond})
	defer closeServer()
	ctx, cancel := context.WithCancel(context.Background())
	var output temporaryPathBuffer
	output.notify = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunStreamingWithTemporaryScope(ctx, `printf '%s\n' "$TMPDIR"; sleep 60`, tool.TemporaryScopeManaged, &output)
		done <- err
	}()
	select {
	case <-output.notify:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("managed command did not publish its temporary path")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("managed cancellation = %v", err)
	}
	tempDir := strings.TrimSpace(output.String())
	if !strings.HasPrefix(tempDir, "/tmp/mecatl-managed-") {
		t.Fatalf("managed temporary path = %q", tempDir)
	}
	if _, err := os.Stat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed temporary directory survived cancellation: %v", err)
	}
}

func TestGuestExecPinsReconstructedGitMetadata(t *testing.T) {
	t.Setenv("GIT_DIR", "/host/source/.git/worktrees/session")
	t.Setenv("GIT_WORK_TREE", "/host/source")
	runner, closeServer := scenarioRunner(t, scenarioBinding, Limits{})
	defer closeServer()

	result, err := runner.Run(context.Background(), `printf '%s\n%s\n' "$GIT_DIR" "$GIT_WORK_TREE"`)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Run: result=%+v err=%v", result, err)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 2 || lines[0] != worktree.GuestMetadata || !filepath.IsAbs(lines[1]) || lines[1] == "/host/source" {
		t.Fatalf("guest Git binding = %q, want fixed metadata plus the bound workspace root", result.Stdout)
	}
}

func TestMicroVMEnvironments_Scenario4_ExecStreamingPreservesChannelsAndExit(t *testing.T) {
	t.Parallel()
	runner, closeServer := scenarioRunner(t, scenarioBinding, Limits{})
	defer closeServer()

	result, err := runner.Run(context.Background(), "printf 'out-1\\nout-2\\n'; printf 'err-1\\nerr-2\\n' >&2; exit 7")
	if err != nil {
		t.Fatalf("Run nonzero guest command: %v", err)
	}
	if result.Stdout != "out-1\nout-2\n" || result.Stderr != "err-1\nerr-2\n" || result.ExitCode != 7 {
		t.Fatalf("result = %#v, want distinct stdout/stderr and exit 7", result)
	}

	var streamed bytes.Buffer
	exit, err := runner.RunStreaming(context.Background(), "printf 'out-1\\n'; sleep 0.05; printf 'err-1\\n' >&2; sleep 0.05; printf 'out-2\\n'; exit 9", &streamed)
	if err != nil {
		t.Fatalf("RunStreaming nonzero guest command: %v", err)
	}
	if exit != 9 || streamed.String() != "out-1\nerr-1\nout-2\n" {
		t.Fatalf("stream = %q exit=%d, want ordered merged output and exit 9", streamed.String(), exit)
	}
}

func TestMicroVMEnvironments_Scenario4_CancelKillsGuestProcessGroup(t *testing.T) {
	runner, closeServer := scenarioRunner(t, scenarioBinding, Limits{CancelGrace: 500 * time.Millisecond})
	defer closeServer()

	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	resultCh := make(chan struct {
		result CommandResult
		err    error
	}, 1)
	go func() {
		result, err := runner.Run(ctx, "printf 'partial\\n'; (trap '' TERM; while :; do :; done) & child=$!; printf '%s\\n' \"$child\"; wait")
		resultCh <- struct {
			result CommandResult
			err    error
		}{result, err}
	}()

	var outcome struct {
		result CommandResult
		err    error
	}
	select {
	case outcome = <-resultCh:
		t.Fatalf("command ended before cancellation: result=%#v err=%v", outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
		cancel()
	}
	select {
	case outcome = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled guest process group did not terminate within bound")
	}
	if !errors.Is(outcome.err, context.Canceled) || errors.Is(outcome.err, ErrTransport) {
		t.Fatalf("cancel error = %v, want context.Canceled distinct from transport fault", outcome.err)
	}
	if !strings.Contains(outcome.result.Stdout, "partial\n") {
		t.Fatalf("partial stdout lost on cancellation: %q", outcome.result.Stdout)
	}
	lines := strings.Fields(outcome.result.Stdout)
	if len(lines) < 2 {
		t.Fatalf("guest child pid missing from partial output: %q", outcome.result.Stdout)
	}
	childPID, err := strconv.Atoi(lines[1])
	if err != nil {
		t.Fatalf("parse guest child pid %q: %v", lines[1], err)
	}
	childGoneDeadline := time.Now().Add(500 * time.Millisecond)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(childGoneDeadline) {
			t.Fatalf("guest process-group child %d survived cancellation: %v", childPID, err)
		}
		time.Sleep(time.Millisecond)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("process-group cancellation exceeded bound: %v", time.Since(started))
	}

	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer deadlineCancel()
	deadlineResult, deadlineErr := runner.Run(deadlineCtx, "printf 'before-deadline\\n'; while :; do :; done")
	if !errors.Is(deadlineErr, context.DeadlineExceeded) || errors.Is(deadlineErr, ErrTransport) {
		t.Fatalf("deadline error = %v, want context.DeadlineExceeded distinct from transport fault", deadlineErr)
	}
	if deadlineResult.Stdout != "before-deadline\n" {
		t.Fatalf("partial deadline output = %q, want produced bytes", deadlineResult.Stdout)
	}
}

func TestMicroVMEnvironments_Scenario4_GuestProtocolBoundaryIsBounded(t *testing.T) {
	limits := Limits{MaxFrameBytes: 256, MaxOutputBytes: 64, MaxConcurrent: 1, CancelGrace: 200 * time.Millisecond}
	var reached atomic.Int32
	server, err := newGuestServerForTest(ServerConfig{Binding: scenarioBinding, Limits: limits, WorkspaceRoot: t.TempDir()}, func(_ string) {
		reached.Add(1)
	})
	if err != nil {
		t.Fatalf("NewGuestServer: %v", err)
	}
	verifier, err := control.NewCapabilityVerifier(capabilityKey)
	if err != nil {
		t.Fatalf("NewCapabilityVerifier: %v", err)
	}
	issuer, err := control.NewCapabilityIssuer(capabilityKey)
	if err != nil {
		t.Fatalf("NewCapabilityIssuer: %v", err)
	}
	capability, err := issuer.Issue(scenarioBinding)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	client, closeConnection := openExecClient(t, server, verifier, scenarioBinding, capability, limits.MaxFrameBytes)
	defer closeConnection()

	for name, mutate := range map[string]func(*control.Binding){
		"owner":      func(b *control.Binding) { b.Owner = "caller:mallory" },
		"session":    func(b *control.Binding) { b.SessionID = "session-2" },
		"ref":        func(b *control.Binding) { b.Ref = "microvm:stale" },
		"generation": func(b *control.Binding) { b.Generation++ },
	} {
		t.Run("wrong "+name, func(t *testing.T) {
			claim := scenarioBinding
			mutate(&claim)
			runner := mustRunner(t, RunnerConfig{Binding: claim, Client: client, Limits: limits})
			if _, err := runner.Run(context.Background(), "echo forbidden"); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("wrong %s: got %v, want ErrUnauthenticated", name, err)
			}
		})
	}
	if reached.Load() != 0 {
		t.Fatalf("invalid bindings reached guest execution %d times", reached.Load())
	}

	if _, replayCloseErr := openExecClientError(server, verifier, scenarioBinding, capability, limits.MaxFrameBytes); !errors.Is(replayCloseErr, control.ErrUnauthenticatedCapability) {
		t.Fatalf("replayed endpoint capability: got %v, want ErrUnauthenticatedCapability", replayCloseErr)
	}

	runner := mustRunner(t, RunnerConfig{Binding: scenarioBinding, Client: client, Limits: limits})
	if _, err := runner.Run(context.Background(), strings.Repeat("x", 1024)); !errors.Is(err, control.ErrFrameTooLarge) {
		t.Fatalf("oversized request: got %v, want ErrFrameTooLarge", err)
	}
	if _, err := runner.Run(context.Background(), "printf '%0100d' 1"); !errors.Is(err, ErrOutputOverflow) {
		t.Fatalf("output overflow: got %v, want ErrOutputOverflow", err)
	}

	blockCtx, blockCancel := context.WithCancel(context.Background())
	defer blockCancel()
	firstDone := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(blockCtx, "while :; do :; done")
		firstDone <- runErr
	}()
	deadline := time.Now().Add(time.Second)
	for reached.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := runner.Run(context.Background(), "echo over-limit"); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("excessive concurrent exec: got %v, want ErrConcurrencyLimit", err)
	}
	blockCancel()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("bounded concurrent command did not cancel")
	}
}

func newGuestServerForTest(cfg ServerConfig, onStart func(string)) (*GuestServer, error) {
	if cfg.WorkloadIdentity == (WorkloadIdentity{}) {
		cfg.WorkloadIdentity = testWorkloadIdentity
	}
	server, err := NewGuestServer(cfg)
	if err == nil {
		server.onStart = onStart
	}
	return server, err
}

func scenarioRunner(t *testing.T, binding control.Binding, limits Limits) (*Runner, func()) {
	t.Helper()
	server, err := NewGuestServer(ServerConfig{
		Binding: scenarioBinding, Limits: limits, WorkspaceRoot: t.TempDir(), WorkloadIdentity: testWorkloadIdentity,
	})
	if err != nil {
		t.Fatalf("NewGuestServer: %v", err)
	}
	verifier, err := control.NewCapabilityVerifier(capabilityKey)
	if err != nil {
		t.Fatalf("NewCapabilityVerifier: %v", err)
	}
	issuer, err := control.NewCapabilityIssuer(capabilityKey)
	if err != nil {
		t.Fatalf("NewCapabilityIssuer: %v", err)
	}
	capability, err := issuer.Issue(binding)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	client, closeServer := openExecClient(t, server, verifier, binding, capability, limits.normalized().MaxFrameBytes)
	return mustRunner(t, RunnerConfig{Binding: binding, Client: client, Limits: limits}), closeServer
}

func openExecClient(t *testing.T, server *GuestServer, verifier *control.CapabilityVerifier, binding control.Binding, capability string, maxFrameBytes uint32) (*control.Client, func()) {
	t.Helper()
	host, guest := net.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- control.ServeMultiplex(context.Background(), guest, scenarioBinding, verifier, map[control.ServiceName]control.Handler{control.ServiceExec: server.Handler()}, maxFrameBytes)
	}()
	client, err := control.OpenClient(context.Background(), host, binding, capability, []control.ServiceName{control.ServiceExec}, maxFrameBytes)
	if err != nil {
		_ = host.Close()
		_ = guest.Close()
		<-serveDone
		t.Fatalf("OpenClient: %v", err)
	}
	return client, func() {
		_ = client.Close()
		_ = guest.Close()
		if err := <-serveDone; err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("ServeMultiplex: %v", err)
		}
	}
}

func openExecClientError(server *GuestServer, verifier *control.CapabilityVerifier, binding control.Binding, capability string, maxFrameBytes uint32) (*control.Client, error) {
	host, guest := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- control.ServeMultiplex(context.Background(), guest, scenarioBinding, verifier, map[control.ServiceName]control.Handler{control.ServiceExec: server.Handler()}, maxFrameBytes)
	}()
	client, err := control.OpenClient(context.Background(), host, binding, capability, []control.ServiceName{control.ServiceExec}, maxFrameBytes)
	_ = host.Close()
	_ = guest.Close()
	<-done
	return client, err
}

func TestTransportFaultIsNotGuestExit(t *testing.T) {
	issuer, err := control.NewCapabilityIssuer(capabilityKey)
	if err != nil {
		t.Fatalf("NewCapabilityIssuer: %v", err)
	}
	verifier, err := control.NewCapabilityVerifier(capabilityKey)
	if err != nil {
		t.Fatalf("NewCapabilityVerifier: %v", err)
	}
	capability, err := issuer.Issue(scenarioBinding)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	host, guest := net.Pipe()
	serveDone := make(chan error, 1)
	handler := func(context.Context, string, json.RawMessage, func(any) error) (any, string, error) {
		_ = guest.Close()
		return nil, "transport", io.ErrClosedPipe
	}
	go func() {
		serveDone <- control.ServeMultiplex(context.Background(), guest, scenarioBinding, verifier, map[control.ServiceName]control.Handler{
			control.ServiceExec: handler,
		}, control.DefaultMaxMessageBytes)
	}()
	client, err := control.OpenClient(context.Background(), host, scenarioBinding, capability, []control.ServiceName{control.ServiceExec}, control.DefaultMaxMessageBytes)
	if err != nil {
		t.Fatalf("OpenClient: %v", err)
	}
	defer func() {
		_ = client.Close()
		<-serveDone
	}()
	runner := mustRunner(t, RunnerConfig{Binding: scenarioBinding, Client: client})
	marker := t.TempDir() + "/must-not-exist"
	if _, err := runner.Run(context.Background(), "touch "+marker); !errors.Is(err, ErrTransport) {
		t.Fatalf("transport close = %v, want ErrTransport", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transport fault fell back to host command: %v", err)
	}
}

type temporaryPathBuffer struct {
	bytes.Buffer
	notify chan struct{}
}

func (b *temporaryPathBuffer) Write(p []byte) (int, error) {
	n, err := b.Buffer.Write(p)
	select {
	case b.notify <- struct{}{}:
	default:
	}
	return n, err
}

func mustRunner(t *testing.T, cfg RunnerConfig) *Runner {
	t.Helper()
	runner, err := NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}
