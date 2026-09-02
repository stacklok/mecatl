package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// newDrainService is newService with the ENGINE sharing the service store
// (agent.Deps.Store), so the loop's terminate persists the terminal session
// state and the tests can assert — via Service.GetSession, which reads the
// store — that the relay returned only AFTER the run fully terminated (the
// happens-before chain: terminate → save → events close → relay drain ends).
func newDrainService(t *testing.T, llm *mockllm.Provider) *server.Service {
	t.Helper()
	store := memstore.New()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               store,
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// errClientGone is the sticky first-write failure both relay tests inject.
var errClientGone = errors.New("client gone")

// failingConverseStream is a hand-rolled mecatlv1.HarnessService_ConverseServer
// whose Send ALWAYS errors — the dead-client shape. Recv yields the prompt frame
// once, then io.EOF (which makes readControl exit promptly). The embedded
// grpc.ServerStream is nil: only Context/Send/Recv are exercised by Converse.
type failingConverseStream struct {
	grpc.ServerStream
	ctx    context.Context //nolint:containedctx // test fake mirroring grpc.ServerStream.Context
	prompt *mecatlv1.ConverseRequest
	recvs  atomic.Int64
	sends  atomic.Int64
}

func (s *failingConverseStream) Context() context.Context { return s.ctx }

func (s *failingConverseStream) Send(*mecatlv1.ConverseResponse) error {
	s.sends.Add(1)
	return errClientGone
}

func (s *failingConverseStream) Recv() (*mecatlv1.ConverseRequest, error) {
	if s.recvs.Add(1) == 1 {
		return s.prompt, nil
	}
	return nil, io.EOF
}

// TestRelaySendErrorDrainsBusyRun pins the gRPC relay's drain-to-discard
// contract: when Send errors mid-run while the run is still emitting (a blocked
// provider that keeps streaming until the relay's Cancel lands — only a handful
// of events actually flow before the loop terminates), the relay must cancel
// the run and KEEP RANGING over run.Events() until the channel closes — so the
// run can never wedge in its own emits behind the dead client — and only then
// return the recorded Send error. The drain is pinned by ORDERING, not volume:
// the session-cancelled assertion holds deterministically only because the
// relay returns AFTER the channel closed (terminate → store save →
// close(events) happens-before the return); a relay that bailed at the first
// Send error would race it. Exactly ONE Send happens (the error is sticky), and
// goroutine leaks are caught by the package goleak TestMain.
func TestRelaySendErrorDrainsBusyRun(t *testing.T) {
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := newDrainService(t, llm)
	hs := server.NewHarnessServer(svc)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := hs.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	stream := &failingConverseStream{
		ctx: ctx,
		prompt: &mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{
			Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"},
		}},
	}

	done := make(chan error, 1)
	go func() { done <- hs.Converse(stream) }()

	var convErr error
	select {
	case convErr = <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("Converse never returned: the relay wedged instead of draining the busy run")
	}
	if !errors.Is(convErr, errClientGone) {
		t.Fatalf("Converse error = %v, want the recorded send error %v", convErr, errClientGone)
	}
	if n := stream.sends.Load(); n != 1 {
		t.Fatalf("Send called %d times, want exactly 1 (no writes after the first error)", n)
	}
	// Converse returned only after run.Events() closed, so the run is fully
	// terminal: the session landed cancelled, identical to a healthy esc-cancel.
	sess, err := svc.GetSession(ctx, session.SessionID(cs.GetSessionId()))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.State != session.StateCancelled {
		t.Fatalf("session state = %q, want %q", sess.State, session.StateCancelled)
	}
}

// failingSSEWriter is an http.ResponseWriter+Flusher whose body writes ALWAYS
// fail — the dead-SSE-client shape.
type failingSSEWriter struct {
	header http.Header
	writes atomic.Int64
}

func (w *failingSSEWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (*failingSSEWriter) WriteHeader(int) {}
func (w *failingSSEWriter) Write([]byte) (int, error) {
	w.writes.Add(1)
	return 0, errClientGone
}
func (*failingSSEWriter) Flush() {}

// TestSSEWriteErrorDrainsBusyRun is TestRelaySendErrorDrainsBusyRun's HTTP/SSE
// twin: the first body write fails, the handler cancels the run, keeps draining
// run.Events() to discard until close (never wedging the run), performs no
// further writes, and returns with the session cancelled.
func TestSSEWriteErrorDrainsBusyRun(t *testing.T) {
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := newDrainService(t, llm)
	handler := server.NewHTTPHandler(svc)

	srv := httptest.NewServer(handler)
	defer srv.Close()
	id := createHTTPSession(t, srv)

	// Drive the prompt handler directly with the failing writer. The request ctx
	// must be cancellable so the handler's client-disconnect watcher goroutine
	// exits after the run (goleak); it is cancelled once the handler returns.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+id+"/prompt",
		strings.NewReader(`{"text":"go"}`)).WithContext(ctx)
	w := &failingSSEWriter{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("the SSE handler never returned: the relay wedged instead of draining the busy run")
	}
	if n := w.writes.Load(); n != 1 {
		t.Fatalf("Write called %d times, want exactly 1 (no writes after the first error)", n)
	}
	sess, err := svc.GetSession(ctx, session.SessionID(id))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.State != session.StateCancelled {
		t.Fatalf("session state = %q, want %q", sess.State, session.StateCancelled)
	}
}
