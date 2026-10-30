package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/app"
)

type startupCompatibilityServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	major      int32
	err        error
	stall      bool
	mu         sync.Mutex
	calls      []string
	entered    func()
	closed     chan struct{}
	liveOpened chan struct{}
}

func (*startupCompatibilityServer) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}
func (*startupCompatibilityServer) HandleRPC(context.Context, stats.RPCStats) {}
func (*startupCompatibilityServer) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (s *startupCompatibilityServer) HandleConn(_ context.Context, event stats.ConnStats) {
	if _, ok := event.(*stats.ConnEnd); ok && s.closed != nil {
		s.closed <- struct{}{}
	}
}

func (s *startupCompatibilityServer) GetCompatibilityInfo(ctx context.Context, _ *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	if s.entered != nil {
		s.entered()
	}
	if s.stall {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: s.major, Features: []string{"remote-private-detail"}}, s.err
}

func (*startupCompatibilityServer) ListModels(context.Context, *mecatlv1.ListModelsRequest) (*mecatlv1.ListModelsResponse, error) {
	return &mecatlv1.ListModelsResponse{}, nil
}

func (*startupCompatibilityServer) CreateSession(context.Context, *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	return &mecatlv1.CreateSessionResponse{SessionId: "fresh-session"}, nil
}

func (*startupCompatibilityServer) GetSession(_ context.Context, req *mecatlv1.GetSessionRequest) (*mecatlv1.GetSessionResponse, error) {
	if req.GetSessionId() != "completed-session" {
		return nil, status.Error(codes.NotFound, "unknown fixture session")
	}
	return &mecatlv1.GetSessionResponse{Session: &mecatlv1.Session{SessionId: "completed-session", State: "completed"}}, nil
}

func (*startupCompatibilityServer) GetSessionTranscript(_ context.Context, req *mecatlv1.GetSessionTranscriptRequest) (*mecatlv1.GetSessionTranscriptResponse, error) {
	if req.GetSessionId() != "completed-session" {
		return nil, status.Error(codes.NotFound, "unknown fixture session")
	}
	return &mecatlv1.GetSessionTranscriptResponse{
		SessionId: "completed-session", Kind: "main", Complete: true,
		Messages: []*mecatlv1.ConversationMessage{{Role: "assistant", Text: "Loaded completed conversation"}},
	}, nil
}

func (*startupCompatibilityServer) ListSessions(context.Context, *mecatlv1.ListSessionsRequest) (*mecatlv1.ListSessionsResponse, error) {
	return &mecatlv1.ListSessionsResponse{Sessions: []*mecatlv1.SessionSummary{{
		SessionId: "completed-session", Kind: "main", State: "completed", ModifiedAtUnix: 100,
		Capabilities: &mecatlv1.SessionInventoryCapabilities{PublicChat: true, Inspect: true, AuthoritativeTranscript: true},
	}}}, nil
}

func (s *startupCompatibilityServer) StreamSessionLive(req *mecatlv1.StreamSessionLiveRequest, stream grpc.ServerStreamingServer[mecatlv1.Event]) error {
	if req.GetSessionId() != "completed-session" {
		return status.Error(codes.NotFound, "unknown fixture session")
	}
	if s.liveOpened != nil {
		close(s.liveOpened)
	}
	<-stream.Context().Done()
	return nil
}

func (s *startupCompatibilityServer) record(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method[strings.LastIndex(method, "/")+1:])
}

func (s *startupCompatibilityServer) observedCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func startStartupCompatibilityServer(t *testing.T, service *startupCompatibilityServer, options ...grpc.ServerOption) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	options = append(options,
		grpc.StatsHandler(service),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			service.record(info.FullMethod)
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			service.record(info.FullMethod)
			return handler(srv, stream)
		}),
	)
	server := grpc.NewServer(options...)
	mecatlv1.RegisterHarnessServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func TestStartupCompatibilityFreshAccepted(t *testing.T) {
	isolatedEmbeddedTestStorage(t)
	service := &startupCompatibilityServer{major: 2}
	target := startStartupCompatibilityServer(t, service)
	ran := false
	err := runWithOptions([]string{"mecatui", "connect", target, "--anonymous"}, runOptions{
		runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
			ran = true
			if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo"}) {
				t.Fatalf("RPCs before normal UI = %v", calls)
			}
			// Run the real Init catalog command, then its real CreateSession
			// continuation. Do not execute the status-line wait or recurring ticks.
			batch, ok := model.Init()().(tea.BatchMsg)
			if !ok || len(batch) < 2 {
				t.Fatalf("Init = %T, want startup batch", batch)
			}
			catalog := batch[1]()
			if _, ok := catalog.(client.ModelsMsg); !ok {
				t.Fatalf("catalog command = %T", catalog)
			}
			updated, create := model.Update(catalog)
			if create == nil {
				t.Fatal("catalog did not continue to CreateSession")
			}
			ready, ok := create().(client.SessionReadyMsg)
			if !ok || ready.SessionID != "fresh-session" {
				t.Fatalf("create result = %#v", ready)
			}
			return updated, nil
		},
	})
	if err != nil || !ran {
		t.Fatalf("startup error = %v, normal UI ran = %v", err, ran)
	}
	if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo", "ListModels", "GetCompatibilityInfo", "CreateSession", "GetSession"}) {
		t.Fatalf("fresh startup RPCs = %v", calls)
	}
}

func TestStartupCompatibilityResumeAccepted(t *testing.T) {
	for _, candidate := range []string{"explicit", "automatic", "auth-recovery"} {
		t.Run(candidate, func(t *testing.T) {
			isolatedEmbeddedTestStorage(t)
			service := &startupCompatibilityServer{major: 2, liveOpened: make(chan struct{})}
			target := startStartupCompatibilityServer(t, service)
			argv := []string{"mecatui", "connect", target, "--anonymous", "--theme=aztec"}
			wantCalls := []string{"GetCompatibilityInfo", "GetSession", "GetCompatibilityInfo", "GetSessionTranscript"}
			ran := false
			options := runOptions{runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
				ran = true
				if calls := service.observedCalls(); !reflect.DeepEqual(calls, wantCalls) {
					t.Fatalf("resume RPCs before UI = %v, want %v", calls, wantCalls)
				}
				if model.ActiveSessionID() != "completed-session" {
					t.Fatalf("adopted session = %q", model.ActiveSessionID())
				}
				batch := model.Init()().(tea.BatchMsg)
				updated, _ := model.Update(batch[1]()) // Real startup-resume-ready reducer, not CreateSession.
				updated, _ = updated.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				if view := ansi.Strip(updated.View().Content); !strings.Contains(view, "Loaded completed conversation") {
					t.Fatalf("resumed transcript not visible: %s", view)
				}
				select {
				case <-service.liveOpened:
				case <-time.After(5 * time.Second):
					t.Fatal("resumed UI did not open its read-only live feed")
				}
				return updated, nil
			}}
			switch candidate {
			case "explicit":
				argv = append(argv, "--resume=completed-session")
			case "automatic":
				argv = append(argv, "--resume-latest")
				wantCalls = []string{"GetCompatibilityInfo", "ListSessions", "GetCompatibilityInfo", "GetSessionTranscript", "GetSession", "GetCompatibilityInfo"}
			case "auth-recovery":
				options.connectResumeSessionID = "completed-session"
			}
			if err := runWithOptions(argv, options); err != nil || !ran {
				t.Fatalf("resume startup = %v, UI ran = %v", err, ran)
			}
			wantCalls = append(wantCalls, "StreamSessionLive")
			if calls := service.observedCalls(); !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("resume reached unexpected RPCs (including fresh create): %v", calls)
			}
		})
	}
}

func TestStartupCompatibilityRejectedBeforeFreshOrExplicitResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "fresh"
		if resume {
			name = "explicit-resume"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				major   int32
				err     error
				message string
				auth    bool
			}{
				{name: "newer", major: 3, message: "update mecatui"},
				{name: "older", major: 1, message: "update server"},
				{name: "invalid-zero", message: "invalid compatibility information"},
				{name: "invalid-negative", major: -1, message: "invalid compatibility information"},
				{name: "missing", err: status.Error(codes.Unimplemented, "remote-private-detail"), message: "update server"},
				{name: "auth", err: status.Error(codes.Unauthenticated, "remote-private-detail"), auth: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					isolatedEmbeddedTestStorage(t)
					service := &startupCompatibilityServer{major: tc.major, err: tc.err}
					target := startStartupCompatibilityServer(t, service)
					argv := []string{"mecatui", "connect", target, "--anonymous"}
					if resume {
						argv = append(argv, "--resume=exact-session")
					}
					var source customization.Source
					recoveryRan := false
					err := runWithOptions(argv, runOptions{
						statusSourceStarted: func(started customization.Source) { source = started },
						runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
							assertStartupSourceStopped(t, source)
							view := model.View().Content
							if !tc.auth || !strings.Contains(view, "Authentication needs attention.") || strings.Contains(view, "remote-private-detail") {
								t.Fatalf("unexpected normal/recovery UI: %s", view)
							}
							recoveryRan = true
							return model, nil
						}})
					assertStartupSourceStopped(t, source)
					if tc.auth {
						if err != nil || !recoveryRan {
							t.Fatalf("auth recovery: error = %v, ran = %v", err, recoveryRan)
						}
					} else {
						if err == nil || !strings.Contains(err.Error(), tc.message) || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "remote-private-detail") {
							t.Fatalf("compatibility rejection = %v, want %q, not cancellation or remote text", err, tc.message)
						}
						var compatibilityErr *client.CompatibilityError
						if !errors.As(err, &compatibilityErr) {
							t.Fatalf("lost typed compatibility error: %v", err)
						}
					}
					// Interceptors count every unary and streaming RPC, including
					// unimplemented reads, pending approvals, and run entry.
					if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo"}) {
						t.Fatalf("rejected startup reached downstream RPCs: %v", calls)
					}
				})
			}
		})
	}
}

func assertStartupSourceStopped(t *testing.T, source customization.Source) {
	t.Helper()
	if source == nil {
		t.Fatal("startup did not create a source")
	}
	for {
		select {
		case _, open := <-source.Changed():
			if !open {
				return
			}
		default:
			t.Fatal("startup source worker is still running")
		}
	}
}

func TestStartupCompatibilityTransportFailureClosesSource(t *testing.T) {
	storage := isolatedEmbeddedTestStorage(t)
	writeIsolatedExecutionSettings(t, app.PlacementHostLocal)
	stop := errors.New("startup transport failed")
	var source customization.Source
	err := runWithOptions([]string{"mecatui", "--mock", "--quiet", "--product-metrics=false", "--workspace=" + t.TempDir(),
		"--memory-dir=" + storage.memoryDir, "--store-dir=" + storage.storeDir, "--user-model-dir=" + storage.userModelDir}, runOptions{
		statusSourceStarted: func(started customization.Source) { source = started },
		beforeEmbeddedStart: func(app.Config) error { return stop },
	})
	if !errors.Is(err, stop) || errors.Is(err, context.Canceled) {
		t.Fatalf("transport failure = %v", err)
	}
	assertStartupSourceStopped(t, source)
}

func TestStartupCompatibilityFailedReconnectClosesSourceBeforeRecovery(t *testing.T) {
	isolatedEmbeddedTestStorage(t)
	// Use a test-owned certificate with the real restart's TLS/CA parameters.
	certificateServer := httptest.NewTLSServer(nil)
	defer certificateServer.Close()
	serverCA := filepath.Join(t.TempDir(), "server-ca.pem")
	if err := os.WriteFile(serverCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateServer.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &startupCompatibilityServer{major: 3}
	target := startStartupCompatibilityServer(t, service, grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: certificateServer.TLS.Certificates, MinVersion: tls.VersionTLS12,
	})))
	var source customization.Source
	runs, views := 0, 0
	err := restartFromConnectIntentWith([]string{"mecatui"}, ui.ConnectRestartIntent{Target: target, Action: ui.RetryAfterCleanup, ResumeSessionID: "completed-session"}, restartTransport{Target: target, TLSCAFile: serverCA}, connectRestartOps{
		run: func(argv []string, options runOptions) error {
			runs++
			if options.recoveryOnly {
				assertStartupSourceStopped(t, source)
				if options.connectReason != "" || options.connectTarget != target || options.connectResumeSessionID != "completed-session" || options.connectTransport != (restartTransport{Target: target, TLSCAFile: serverCA}) {
					t.Fatalf("recovery lost transport/candidate or retained stale auth: %#v", options)
				}
			} else {
				want := []string{"mecatui", "connect", target, "--tls", "--tls-ca", serverCA}
				if !reflect.DeepEqual(argv, want) || options.connectResumeSessionID != "completed-session" {
					t.Fatalf("restart invocation = %v, options = %#v", argv, options)
				}
				// The fixture has no saved OIDC identity; keep the actual restart
				// target and TLS parameters, but explicitly connect anonymously.
				argv = append(argv, "--anonymous")
			}
			options.statusSourceStarted = func(started customization.Source) { source = started }
			options.runProgram = func(_ context.Context, model ui.Model) (tea.Model, error) {
				if !options.recoveryOnly {
					t.Fatal("mismatched reconnect entered normal UI")
				}
				assertStartupSourceStopped(t, source)
				updated, _ := model.Update(model.Init()()) // Real disconnected saved-target loading.
				updated, _ = updated.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				view := ansi.Strip(updated.View().Content)
				if !strings.Contains(view, "update mecatui") || strings.Contains(view, "Authentication needs attention.") || strings.Contains(view, "Sign in again") || strings.Contains(view, "remote-private-detail") {
					t.Fatalf("disconnected recovery view = %s", view)
				}
				views++
				return updated, nil
			}
			return runWithOptions(argv, options)
		},
	})
	if err != nil || runs != 2 || views != 1 {
		t.Fatalf("failed reconnect = %v, runs = %d, recovery views = %d", err, runs, views)
	}
	assertStartupSourceStopped(t, source)
	if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo"}) {
		t.Fatalf("mismatched reconnect reached downstream RPCs: %v", calls)
	}
}

func TestStartupCompatibilityTimeoutIsNotRetirementCancellation(t *testing.T) {
	isolatedEmbeddedTestStorage(t)
	service := &startupCompatibilityServer{stall: true}
	target := startStartupCompatibilityServer(t, service)
	err := runWithOptions([]string{"mecatui", "connect", target, "--anonymous"}, runOptions{runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
		t.Fatal("timeout entered normal or auth-recovery UI")
		return model, nil
	}})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("probe timeout = %v, want deadline, not cancellation", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout display = %v", err)
	}
	if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo"}) {
		t.Fatalf("timed-out startup RPCs = %v", calls)
	}
}

func TestStartupCompatibilityImmediateRemoteDeadlineIsSafeTimeout(t *testing.T) {
	isolatedEmbeddedTestStorage(t)
	service := &startupCompatibilityServer{err: status.Error(codes.DeadlineExceeded, "remote-private-detail")}
	target := startStartupCompatibilityServer(t, service)
	err := runWithOptions([]string{"mecatui", "connect", target, "--anonymous"}, runOptions{runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
		t.Fatal("remote deadline entered normal or auth-recovery UI")
		return model, nil
	}})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("remote deadline = %v, want deadline, not cancellation", err)
	}
	if diagnosis, ok := startupCompatibilityDiagnosis(err); !ok || diagnosis != "server compatibility check timed out; retry the connection" {
		t.Fatalf("startupCompatibilityDiagnosis(%v) = %q, %v", err, diagnosis, ok)
	}
	if strings.Contains(err.Error(), "remote-private-detail") {
		t.Fatalf("remote detail exposed in startup error: %q", err)
	}
	if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo"}) {
		t.Fatalf("remote-deadline startup RPCs = %v", calls)
	}
}

func TestStartupCompatibilityCleanupRetiresWithoutInterruption(t *testing.T) {
	ctx, handler := setupSignalHandler()
	interrupted := runCleanup(handler, func() {
		if ctx.Err() != nil {
			t.Error("handler retired before cleanup")
		}
		select {
		case <-handler.done:
			t.Error("handler unregistered before cleanup")
		default:
		}
	})
	if interrupted || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("retirement: interrupted = %v, ctx error = %v", interrupted, ctx.Err())
	}
	select {
	case <-handler.done:
	default:
		t.Fatal("cleanup returned before handler unregistered")
	}
}

func TestStartupCompatibilityRejectedBeforeAutomaticOrAuthRecoveryResume(t *testing.T) {
	for _, candidate := range []string{"automatic", "auth-recovery"} {
		t.Run(candidate, func(t *testing.T) {
			isolatedEmbeddedTestStorage(t)
			service := &startupCompatibilityServer{major: 3}
			target := startStartupCompatibilityServer(t, service)
			argv := []string{"mecatui", "connect", target, "--anonymous"}
			options := runOptions{runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
				t.Errorf("incompatible candidate entered normal or disconnected UI; RPCs = %v", service.observedCalls())
				return model, nil
			}}
			if candidate == "automatic" {
				argv = append(argv, "--resume-latest")
			} else {
				options.connectResumeSessionID = "auth-candidate"
			}
			err := runWithOptions(argv, options)
			var mismatch *client.CompatibilityError
			if !errors.As(err, &mismatch) || errors.Is(err, context.Canceled) {
				t.Fatalf("candidate rejection = %v", err)
			}
			if calls := service.observedCalls(); !reflect.DeepEqual(calls, []string{"GetCompatibilityInfo"}) {
				t.Fatalf("candidate reached downstream RPCs: %v", calls)
			}
		})
	}
}

func shortEmbeddedRuntimeDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		t.Chdir(t.TempDir())
		t.Setenv("XDG_RUNTIME_DIR", "/proc/self/cwd")
		return
	}

	dir, err := os.MkdirTemp("", "mecatui-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
}

func TestStartupCompatibilityEmbeddedRejected(t *testing.T) {
	storage := isolatedEmbeddedTestStorage(t)
	shortEmbeddedRuntimeDir(t)
	writeIsolatedExecutionSettings(t, app.PlacementHostLocal)
	stop := errors.New("controlled embedded compatibility rejection")
	var source customization.Source
	var socket string
	checked, cleaned := false, false
	err := runWithOptions([]string{"mecatui", "--mock", "--quiet", "--toolhive-llm=false", "--product-metrics=false", "--workspace=" + t.TempDir(),
		"--memory-dir=" + storage.memoryDir, "--store-dir=" + storage.storeDir, "--user-model-dir=" + storage.userModelDir}, runOptions{
		statusSourceStarted: func(started customization.Source) { source = started },
		afterCompatibilityCheck: func() error {
			// This hook runs only AFTER the production client.CheckCompatibility
			// succeeds over the actual embedded server's socket. The server's API
			// major is a constant, so inject failure here, not a fake checker/server.
			checked = true
			storage.assertBuilt(t)
			sockets, err := filepath.Glob(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "mecatui-*", "mecated.sock"))
			if err != nil || len(sockets) != 1 {
				t.Fatalf("live embedded sockets = %v, %v", sockets, err)
			}
			socket = sockets[0]
			conn, err := net.DialTimeout("unix", socket, time.Second)
			if err != nil {
				t.Fatalf("embedded listener was not live: %v", err)
			}
			_ = conn.Close()
			return stop
		},
		wrapTransportCleanup: func(cleanup func()) func() {
			return func() {
				cleanup() // Never substitute fake cleanup for the owned embedded resources.
				cleaned = true
				if _, err := os.Stat(filepath.Dir(socket)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("embedded socket directory survived cleanup: %v", err)
				}
				if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
					_ = conn.Close()
					t.Error("embedded listener survived cleanup")
				}
			}
		},
		runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
			t.Error("embedded compatibility rejection entered UI")
			return model, nil
		},
	})
	if !errors.Is(err, stop) || errors.Is(err, context.Canceled) || !checked || !cleaned {
		t.Fatalf("embedded rejection = %v, real check succeeded = %v, cleanup ran = %v", err, checked, cleaned)
	}
	var preflight *startupCompatibilityError
	if !errors.As(err, &preflight) {
		t.Fatalf("embedded rejection did not use startup preflight gate: %v", err)
	}
	assertStartupSourceStopped(t, source)
}

func TestStartupCompatibilityEmbeddedAccepted(t *testing.T) {
	storage := isolatedEmbeddedTestStorage(t)
	shortEmbeddedRuntimeDir(t)
	settings := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl")
	if err := os.MkdirAll(settings, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings, "settings.yaml"), []byte("execution: {default_placement: "+app.PlacementHostLocal+"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	ran := false
	err := runWithOptions([]string{"mecatui", "--mock", "--quiet", "--toolhive-llm=false", "--product-metrics=false", "--workspace=" + workspace,
		"--memory-dir=" + storage.memoryDir, "--store-dir=" + storage.storeDir, "--user-model-dir=" + storage.userModelDir}, runOptions{
		runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
			ran = true
			storage.assertBuilt(t)
			// Embedded Init wraps the normal startup batch with a progress wait.
			batch := model.Init()().(tea.BatchMsg)
			startup := batch[0]().(tea.BatchMsg)
			catalog := startup[1]()
			if _, ok := catalog.(client.ModelsMsg); !ok {
				t.Fatalf("embedded catalog = %T", catalog)
			}
			updated, create := model.Update(catalog)
			if create == nil {
				t.Fatal("embedded catalog did not continue to create")
			}
			ready, ok := create().(client.SessionReadyMsg)
			if !ok || ready.SessionID == "" {
				t.Fatalf("embedded create = %#v", ready)
			}
			return updated, nil
		},
	})
	if err != nil || !ran {
		t.Fatalf("embedded startup = %v, UI ran = %v", err, ran)
	}
	sockets, err := filepath.Glob(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "mecatui-*", "mecated.sock"))
	if err != nil || len(sockets) != 0 {
		t.Fatalf("embedded transport leaked sockets: %v, %v", sockets, err)
	}
}

// The child uses the real startup and shutdown paths. Readiness is emitted only
// at the blocked RPC/cleanup/UI boundary; stdin releases cleanup after the parent
// has observed the signal handler's acknowledgement. No polling or sleeps.
func TestStartupCompatibilitySignalLifecycle(t *testing.T) {
	if mode := os.Getenv("MECATUI_TEST_STARTUP_LIFECYCLE"); mode != "" {
		runStartupCompatibilitySignalChild(t, mode)
		return
	}
	for _, mode := range []string{"preflight", "cleanup-first", "cleanup-second", "dial-cleanup", "keymap-cleanup", "normal-cleanup", "failure-success", "reconnect"} {
		t.Run(mode, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestStartupCompatibilitySignalLifecycle$")
			cmd.Env = append(os.Environ(), "MECATUI_TEST_STARTUP_LIFECYCLE="+mode)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pr.Close()
			cmd.Stdout, cmd.Stderr = pw, pw
			if err := cmd.Start(); err != nil {
				_ = pw.Close()
				t.Fatal(err)
			}
			_ = pw.Close()
			defer func() { _ = cmd.Process.Kill() }()
			out := &syncBuffer{}
			scanner := bufio.NewScanner(pr)
			first, second := false, false
			for scanner.Scan() {
				line := scanner.Text()
				_, _ = fmt.Fprintln(out, line)
				if line == "startup-lifecycle-ready" {
					if first {
						t.Fatal("duplicate readiness")
					}
					first = true
					if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
						t.Fatal(err)
					}
				}
				if strings.Contains(line, "shutting down gracefully") && mode != "preflight" {
					switch mode {
					case "cleanup-second":
						second = true
						if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
							t.Fatal(err)
						}
					case "cleanup-first", "dial-cleanup", "keymap-cleanup", "normal-cleanup":
						if _, err := io.WriteString(input, "release\n"); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			err = cmd.Wait()
			if scanner.Err() != nil {
				t.Fatal(scanner.Err())
			}
			if mode == "cleanup-second" {
				var exitErr *exec.ExitError
				if !second || !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
					t.Fatalf("second signal = %v; %s", err, out.String())
				}
			} else if err != nil || !strings.Contains(out.String(), "startup-lifecycle-complete") {
				t.Fatalf("lifecycle = %v; %s", err, out.String())
			}
			if !first {
				t.Fatalf("no synchronized signal: %s", out.String())
			}
			if count := strings.Count(out.String(), "shutting down gracefully"); count != 1 {
				t.Fatalf("signal had %d owners, want one: %s", count, out.String())
			}
		})
	}
}

func runStartupCompatibilitySignalChild(t *testing.T, mode string) {
	isolatedEmbeddedTestStorage(t)
	service := &startupCompatibilityServer{major: 3, closed: make(chan struct{}, 4)}
	if mode == "preflight" {
		service.stall = true
		service.entered = func() { fmt.Println("startup-lifecycle-ready") }
	}
	if mode == "reconnect" || mode == "keymap-cleanup" || mode == "normal-cleanup" {
		service.major = 2
	}
	target := startStartupCompatibilityServer(t, service)
	argv := []string{"mecatui", "connect", target, "--anonymous"}
	switch mode {
	case "dial-cleanup":
		argv = append(argv, "--tls-ca="+filepath.Join(t.TempDir(), "missing-ca.pem"))
	case "keymap-cleanup":
		argv = append(argv, "--keymap=NotAnAction=ctrl+f9")
	}
	var source customization.Source
	cleaned := false
	options := runOptions{
		statusSourceStarted: func(started customization.Source) { source = started },
		wrapTransportCleanup: func(cleanup func()) func() {
			return func() {
				if mode != "dial-cleanup" {
					select {
					case <-service.closed:
					case <-time.After(5 * time.Second):
						t.Error("startup client not closed before transport cleanup")
					}
				}
				if strings.HasPrefix(mode, "cleanup-") || strings.HasSuffix(mode, "-cleanup") {
					fmt.Println("startup-lifecycle-ready")
					if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
						t.Error(err)
					}
				}
				cleanup()
				cleaned = true
			}
		},
		runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
			if mode == "normal-cleanup" {
				return connectRestartModel{intent: ui.ConnectRestartIntent{Target: target, Action: ui.ConnectSaved}}, nil
			}
			if mode != "reconnect" {
				t.Error("rejected startup entered normal or disconnected UI")
			}
			return model, nil
		},
	}
	runCalls := 0
	runStartup := func(_ []string, next runOptions) error {
		runCalls++
		if next.recoveryOnly {
			t.Fatal("operator interruption reopened disconnected retry")
		}
		return runWithOptions(argv, options)
	}
	var err error
	if mode == "preflight" || strings.HasSuffix(mode, "-cleanup") {
		err = restartFromConnectIntentWith(argv, ui.ConnectRestartIntent{Target: target, Action: ui.ConnectSaved}, restartTransport{}, connectRestartOps{run: runStartup})
		if runCalls != 1 {
			t.Fatalf("interrupted reconnect run count = %d", runCalls)
		}
	} else {
		err = runStartup(argv, runOptions{})
	}
	assertStartupSourceStopped(t, source)
	if !cleaned {
		t.Fatal("startup transport cleanup did not run")
	}
	if mode == "preflight" || strings.HasPrefix(mode, "cleanup-") || strings.HasSuffix(mode, "-cleanup") {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("operator interruption = %v, want cancellation", err)
		}
		wantCalls := []string{"GetCompatibilityInfo"}
		if mode == "dial-cleanup" {
			wantCalls = nil
		}
		if calls := service.observedCalls(); !reflect.DeepEqual(calls, wantCalls) {
			t.Fatalf("interrupted startup RPCs = %v", calls)
		}
	} else {
		if mode == "failure-success" {
			var mismatch *client.CompatibilityError
			if !errors.As(err, &mismatch) {
				t.Fatalf("initial mismatch = %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		// Use a separate server so no fixture state changes beneath an old RPC.
		success := &startupCompatibilityServer{major: 2}
		successTarget := startStartupCompatibilityServer(t, success)
		runSuccess := func(_ []string, _ runOptions) error {
			return runWithOptions([]string{"mecatui", "connect", successTarget, "--anonymous"}, runOptions{
				runProgram: func(ctx context.Context, model ui.Model) (tea.Model, error) {
					fmt.Println("startup-lifecycle-ready")
					<-ctx.Done()
					return model, nil
				},
			})
		}
		if mode == "reconnect" {
			err = restartFromConnectIntentWith(argv, ui.ConnectRestartIntent{Target: successTarget, Action: ui.ConnectSaved}, restartTransport{}, connectRestartOps{run: runSuccess})
		} else {
			err = runSuccess(nil, runOptions{})
		}
		if err != nil {
			t.Fatalf("successor = %v", err)
		}
	}
	fmt.Println("startup-lifecycle-complete")
}
