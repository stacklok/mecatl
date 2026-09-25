package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Disconnect closes only the CLIENT connection. Stopping the gRPC server here
// would mask a missing disconnect-to-run cancellation path.
func recoveryHarnessClient(t *testing.T, svc *server.Service) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(svc))
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///provider-recovery", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	disconnect := func() { _ = conn.Close() }
	t.Cleanup(disconnect)
	return mecatlv1.NewHarnessServiceClient(conn), disconnect
}

// Receives real wrapper decisions; buffered delivery never controls retry policy.
type recoverySignals struct {
	port.NopDiagnostics
	wait chan struct{}
}

func (d recoverySignals) With(...any) port.Diagnostics { return d }
func (d recoverySignals) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	if msg != "llm provider recovery" {
		return
	}
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "decision" && args[i+1] == "wait" {
			select {
			case d.wait <- struct{}{}:
			default:
			}
		}
	}
}

func awaitRecovery(t *testing.T, ctx context.Context, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", what, ctx.Err())
	}
}

func awaitRecoveryRunEnd(t *testing.T, ctx context.Context, svc *server.Service, id session.SessionID) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, ok := svc.LookupRun(id); !ok {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("active recovery run was not joined")
		}
	}
}

func TestServerProviderRecovery_Scenario6_DisconnectAndShutdownCancelRecovery(t *testing.T) {
	t.Run("direct-write child parent cancellation", testRecoveryChildParentCancellation)
	for _, transport := range []string{"grpc", "sse", "parent cancel", "host shutdown"} {
		for _, phase := range []string{"breaker wait", "half-open probe"} {
			t.Run(transport+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				var calls atomic.Int32
				probe, stopped := make(chan struct{}), make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						writeRecoveryFailure(w)
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					close(probe)
					select {
					case <-r.Context().Done():
						close(stopped)
					case <-ctx.Done():
					}
				}))
				defer func() { cancel(); srv.Close() }()
				cfg := recoveryAppConfig(t, srv.URL)
				cfg.LLMBreakerCooldown = 2 * time.Second
				signals := recoverySignals{wait: make(chan struct{}, 4)}
				cfg.Diagnostics = signals
				built, err := buildIsolated(t, ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer built.Close()
				sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				runCtx, stopRun := context.WithCancel(ctx)
				defer stopRun()
				var disconnect func()
				switch transport {
				case "grpc":
					client, closeClient := recoveryHarnessClient(t, built.Service)
					defer closeClient()
					stream, err := client.Converse(runCtx)
					if err != nil {
						t.Fatal(err)
					}
					if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "recover"}}}); err != nil {
						t.Fatal(err)
					}
					disconnect = closeClient
				case "sse":
					h := httptest.NewServer(server.NewHTTPHandler(built.Service))
					defer h.Close()
					req, err := http.NewRequestWithContext(runCtx, http.MethodPost, h.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", strings.NewReader(`{"text":"recover"}`))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/json")
					resp, err := h.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("SSE status=%d", resp.StatusCode)
					}
					disconnect = func() { _ = resp.Body.Close() }
				default:
					run, err := built.Service.StartRun(runCtx, sess.ID, "recover")
					if err != nil {
						t.Fatal(err)
					}
					go func() {
						defer built.Service.FinishRun(sess.ID, run)
						for range run.Events() {
						}
					}()
					disconnect = stopRun
					if transport == "host shutdown" {
						disconnect = built.Close
					}
				}
				awaitRecovery(t, ctx, signals.wait, "breaker cooldown")
				if phase == "half-open probe" {
					awaitRecovery(t, ctx, probe, "real half-open HTTP call")
				}
				if _, ok := built.Service.LookupRun(sess.ID); !ok {
					t.Fatal("run ended before disconnection")
				}
				disconnect()
				if phase == "half-open probe" {
					awaitRecovery(t, ctx, stopped, "provider request cancellation")
				}
				awaitRecoveryRunEnd(t, ctx, built.Service, sess.ID)
				want := int32(1)
				if phase == "half-open probe" {
					want = 2
				}
				// Wait beyond eligibility: a hidden sleeper would issue another call.
				select {
				case <-time.After(cfg.LLMBreakerCooldown + 50*time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if calls.Load() != want {
					t.Fatalf("provider calls after disconnect=%d want %d", calls.Load(), want)
				}
			})
		}
	}
}

func testRecoveryChildParentCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	probe, stopped := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			writeRecoveryToolTurn(w, "delegate", "Subagent", `{"prompt":"write beta","mode":"read-write"}`)
		case 2:
			writeRecoveryToolTurn(w, "write", "Write", `{"path":"beta.txt","content":"prior write"}`)
		case 3:
			writeRecoveryFailure(w)
		case 4:
			if !strings.HasPrefix(r.Header.Get("X-Mecatl-Session-ID"), "subagent-") {
				t.Error("probe did not belong to delegated child")
			}
			close(probe)
			select {
			case <-r.Context().Done():
				close(stopped)
			case <-ctx.Done():
			}
		default:
			t.Error("provider continued after parent cancellation")
		}
	}))
	defer func() { cancel(); srv.Close() }()
	cfg := recoveryAppConfig(t, srv.URL)
	recorder := &recoveryToolRecorder{}
	cfg.MetricsRoleScoper = func(string) (port.EventSink, port.ToolCallRecorder) { return nil, recorder }
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, stopParent := context.WithCancel(ctx)
	defer stopParent()
	run, err := built.Service.StartRun(parentCtx, sess.ID, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan session.StopReason, 1)
	go func() {
		defer built.Service.FinishRun(sess.ID, run)
		var stop session.StopReason
		for ev := range run.Events() {
			if ev.Result != nil {
				stop = ev.Result.Stop
			}
		}
		done <- stop
	}()
	awaitRecovery(t, ctx, probe, "child recovery after prior write")
	stopParent()
	awaitRecovery(t, ctx, stopped, "child probe joined after parent cancellation")
	select {
	case stop := <-done:
		if stop != session.StopCancelled {
			t.Fatalf("parent terminal=%s", stop)
		}
	case <-ctx.Done():
		t.Fatal("parent failed to join child")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.writes != 1 || calls.Load() != 4 {
		t.Fatalf("writes=%d provider calls=%d", recorder.writes, calls.Load())
	}
}

func TestServerProviderRecovery_Scenario6_NoDetachedOrRestartContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeRecoveryFailure(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeRecoveryTextTurn(w, "explicit continuation")
	}))
	defer func() { cancel(); srv.Close() }()
	cfg := recoveryAppConfig(t, srv.URL)
	cfg.LLMBreakerCooldown = 2 * time.Second
	signals := recoverySignals{wait: make(chan struct{}, 4)}
	cfg.Diagnostics = signals
	first, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	sess, err := first.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	client, disconnect := recoveryHarnessClient(t, first.Service)
	defer disconnect()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "original prompt"}}}); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, ctx, signals.wait, "live recovery before detach")
	disconnect()
	awaitRecoveryRunEnd(t, ctx, first.Service, sess.ID)
	before, err := first.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != session.StateCancelled || len(before.Conversation.Messages) == 0 || before.Conversation.Messages[0].Role != session.RoleUser || before.Conversation.Messages[0].Text != "original prompt" {
		t.Fatalf("disconnect did not preserve a cancelled session with its prompt: state=%s history=%+v", before.State, before.Conversation.Messages)
	}
	first.Close()
	second, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Fetching a persisted session in the new process is not run resumption.
	after, err := second.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State || !reflect.DeepEqual(after.Conversation.Messages, before.Conversation.Messages) {
		t.Fatal("restart rewrote persisted history")
	}
	select {
	case <-time.After(cfg.LLMBreakerCooldown + 50*time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if calls.Load() != 1 {
		t.Fatalf("detached/restarted process continued inference: %d", calls.Load())
	}
	if _, ok := second.Service.LookupRun(sess.ID); ok {
		t.Fatal("restart created a hidden run")
	}
	// Positive control: only an explicit user action re-enters the engine.
	run, err := second.Service.StartRun(ctx, sess.ID, "continue explicitly")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Service.FinishRun(sess.ID, run)
	if text := drainRun(run); text != "explicit continuation" {
		t.Fatalf("explicit continuation=%q", text)
	}
	if calls.Load() != 2 {
		t.Fatalf("explicit continuation calls=%d", calls.Load())
	}
}
