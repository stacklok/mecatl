package executionclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	mecatuiclient "github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	nativeBridgeOwnerToken   = "alice-test-token"
	nativeBridgeForeignToken = "bob-test-token"
)

type nativeBridgeRPCStats struct {
	ownerConverse atomic.Int32
	ownerResolve  atomic.Int32
	foreignCalls  atomic.Int32
	ownerResolved chan struct{}
	converseDone  chan struct{}
	resolveOnce   sync.Once
	converseOnce  sync.Once
	mu            sync.Mutex
	unmappedKinds []string
	terminalStops []string
	ownerControl  *mecatlv1.ResolveRunAskRequest
}

type nativeBridgePrincipalStream struct {
	grpc.ServerStream
	ctx   context.Context
	stats *nativeBridgeRPCStats
}

func (s nativeBridgePrincipalStream) SendMsg(message any) error {
	if frame, ok := message.(*mecatlv1.WatchSessionEventsResponse); ok {
		if event := frame.GetEvent(); event != nil {
			s.stats.mu.Lock()
			if mecatuiclient.EventToMsg(event) == nil {
				s.stats.unmappedKinds = append(s.stats.unmappedKinds, event.GetType())
			}
			if event.GetType() == "result" {
				s.stats.terminalStops = append(s.stats.terminalStops, event.GetResult().GetStop())
			}
			s.stats.mu.Unlock()
		}
	}
	return s.ServerStream.SendMsg(message)
}

func (s nativeBridgePrincipalStream) Context() context.Context { return s.ctx }

func (s *nativeBridgeRPCStats) principal(ctx context.Context) (*session.Principal, bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 {
		return nil, false
	}
	switch values[0] {
	case "Bearer " + nativeBridgeOwnerToken:
		return &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}, true
	case "Bearer " + nativeBridgeForeignToken:
		s.foreignCalls.Add(1)
		return &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser}, true
	default:
		return nil, false
	}
}

func (s *nativeBridgeRPCStats) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	principal, ok := s.principal(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "test principal required")
	}
	if strings.HasSuffix(info.FullMethod, "/ResolveRunAsk") && principal.Subject == "alice" {
		s.ownerResolve.Add(1)
		s.mu.Lock()
		s.ownerControl = req.(*mecatlv1.ResolveRunAskRequest)
		s.mu.Unlock()
		s.resolveOnce.Do(func() { close(s.ownerResolved) })
	}
	return handler(session.WithPrincipal(ctx, principal), req)
}

func (s *nativeBridgeRPCStats) stream(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	principal, ok := s.principal(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "test principal required")
	}
	if strings.HasSuffix(info.FullMethod, "/Converse") && principal.Subject == "alice" {
		s.ownerConverse.Add(1)
		if s.converseDone != nil {
			defer s.converseOnce.Do(func() { close(s.converseDone) })
		}
	}
	return handler(srv, nativeBridgePrincipalStream{ServerStream: stream, ctx: session.WithPrincipal(stream.Context(), principal), stats: s})
}

type nativeBridgePendingClient struct{ *mecatuiclient.Client }

func (c nativeBridgePendingClient) WatchPendingApprovalRun(ctx context.Context, approval mecatuiclient.PendingApproval) (ui.PendingApprovalWatch, error) {
	return c.Client.WatchPendingApprovalRun(ctx, approval)
}

// nativeBridgeModel observes only the public rendered view on Bubble Tea's
// goroutine, so barriers do not depend on the terminal renderer's flush timer.
type nativeBridgeModel struct {
	ui.Model
	ready, terminal chan string
	lastView        *atomic.Value
}

func (m *nativeBridgeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.Model.Update(msg)
	m.Model = next.(ui.Model)
	return m, cmd
}

func (m *nativeBridgeModel) View() tea.View {
	view := m.Model.View()
	text := ansi.Strip(view.Content)
	m.lastView.Store(text)
	if m.ready != nil && strings.Contains(text, "Permission required") && strings.Contains(text, "bridge-command") {
		m.ready <- text
		m.ready = nil
	}
	if m.terminal != nil && (strings.Contains(text, "connection failed") ||
		strings.Contains(text, "continuation complete") && strings.Contains(text, "done") && !strings.Contains(text, "enter queue")) {
		m.terminal <- text
		m.terminal = nil
	}
	return view
}

// This is a composition proof, not CLI flag resolution, OIDC login, or a live
// Kubernetes proof. Only authentication and the final executor are test-owned;
// authorization, public RPCs, durable history and native run access are real.
func TestNativePendingApprovalStartupRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		key        rune
		wantRun    bool
		wantOutput string
	}{
		{name: "allow-once", key: 'a', wantRun: true, wantOutput: "continuation complete"},
		{name: "deny", key: 'd', wantOutput: "continuation complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, t.TempDir())
			}

			profiles := explicitIDProfiles(t)
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
			release := make(chan struct{})
			releaseCommand := sync.OnceFunc(func() { close(release) })
			defer releaseCommand()
			executor := &recordingExecutor{executed: make(chan executionenv.ExecutorRequest, 1), release: release}
			controllerStore := executioncontroller.NewStore(dyn, "ns", profiles, executor)
			profile, err := controllerStore.ValidateProfile(ctx, "coding")
			if err != nil {
				t.Fatal(err)
			}
			owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
			ownerSum := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
			clientSum := sha256.Sum256([]byte("spiffe://example.test/mecak8s"))
			now := time.Now().UTC()
			sessionID := session.SessionID("native-ui-" + tc.name)
			ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env-" + tc.name, Revision: "rev-1"}
			envObj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": ref.ID, "namespace": "ns"},
				"spec":   map[string]any{"schemaVersion": int64(2), "revision": ref.Revision, "ownerHash": hex.EncodeToString(ownerSum[:]), "ownerIssuer": owner.Issuer, "ownerSubject": owner.Subject, "clientHash": hex.EncodeToString(clientSum[:]), "bindingID": string(sessionID), "profile": "coding", "profileDigest": profile.Digest, "desired": "Active"},
				"status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": "Healthy", "pod": map[string]any{"name": "executor-pod"}, "references": []any{map[string]any{"bindingID": string(sessionID), "state": "Published", "operationID": "seed", "createdAt": now.Format(time.RFC3339Nano)}}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
			}}
			if _, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Create(ctx, envObj, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			executionFixture := startFixture(t, controllerStore, nil)
			defer executionFixture.stop()
			executionClient, err := New(executionFixture.endpoint, executionFixture.clientTLS)
			if err != nil {
				t.Fatal(err)
			}
			defer executionClient.Close()
			provider, err := NewProvider(executionClient, "coding")
			if err != nil {
				t.Fatal(err)
			}

			storeDir, workspace := t.TempDir(), t.TempDir()
			store, err := jsonlstore.New(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			parked := session.New(sessionID, session.ModeDefault, ref, session.Limits{MaxTurns: 4}, now)
			if err := parked.RestoreLabels(owner, session.Authority{}); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, parked); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(settings, []byte("permissions:\n  ask:\n    - Shell\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			baseConfig := func(model port.LLMProvider) app.Config {
				return app.Config{
					UseMock: true, MockProvider: model, Workspace: workspace, StoreDir: storeDir,
					UserModelDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, Headless: true,
					RemoteExecution: true, TrustProject: true, OwnershipEnforced: true,
					PlacementProvider: provider, PlacementScope: "native-ui-test", PermissionConfigs: []string{settings},
				}
			}
			builtA, err := app.Build(ctx, baseConfig(mockllm.New(mockllm.ToolCallTurn(
				session.NewToolCall("native-shell", "Shell", json.RawMessage(`{"command":"printf bridge-command"}`)),
			))))
			if err != nil {
				t.Fatal(err)
			}
			closeA := sync.OnceFunc(func() { builtA.Close() })
			defer closeA()
			firstStats := &nativeBridgeRPCStats{converseDone: make(chan struct{})}
			firstListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				closeA()
				t.Fatal(err)
			}
			firstServer := grpc.NewServer(grpc.UnaryInterceptor(firstStats.unary), grpc.StreamInterceptor(firstStats.stream))
			mecatlv1.RegisterHarnessServiceServer(firstServer, server.NewHarnessServer(builtA.Service))
			go func() { _ = firstServer.Serve(firstListener) }()
			defer func() {
				firstServer.Stop()
				_ = firstListener.Close()
			}()
			firstClient, err := mecatuiclient.Dial(mecatuiclient.DialConfig{Server: firstListener.Addr().String(), AuthToken: nativeBridgeOwnerToken})
			if err != nil {
				firstServer.Stop()
				_ = firstListener.Close()
				closeA()
				t.Fatal(err)
			}
			defer func() { _ = firstClient.Close() }()
			runCtx, cancelRun := context.WithCancel(ctx)
			defer cancelRun()
			stream, err := firstClient.OpenConverseForSession(runCtx, string(sessionID))
			if err != nil {
				cancelRun()
				_ = firstClient.Close()
				firstServer.Stop()
				_ = firstListener.Close()
				closeA()
				t.Fatal(err)
			}
			messages := make(chan tea.Msg, 16)
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				stream.ReadLoop(runCtx, messages)
			}()
			defer func() {
				cancelRun()
				select {
				case <-readerDone:
				case <-time.After(5 * time.Second):
					t.Error("initial public stream reader did not exit")
				}
			}()
			if err := stream.SendPrompt(string(sessionID), "run native shell", nil); err != nil {
				cancelRun()
				t.Fatal(err)
			}
			var runID, askID string
			for askID == "" {
				select {
				case message, ok := <-messages:
					if !ok {
						t.Fatal("initial public stream closed before the ask")
					}
					if ask, ok := message.(mecatuiclient.PermissionAskMsg); ok {
						runID, askID = ask.RunID, ask.AskID
					}
				case <-ctx.Done():
					cancelRun()
					t.Fatalf("native Shell run did not reach its persisted ask: %v", ctx.Err())
				}
			}
			persisted, err := firstClient.DiscoverPendingApproval(ctx, string(sessionID))
			if err != nil || persisted.RunID != runID || persisted.AskID != askID || persisted.Cursor == "" {
				cancelRun()
				t.Fatalf("persist initial ask before restart: pending=%+v err=%v", persisted, err)
			}
			closeA()
			select {
			case <-firstStats.converseDone:
			case <-ctx.Done():
				cancelRun()
				t.Fatalf("parked public transport did not drain: %v", ctx.Err())
			}
			cancelRun()
			_ = firstClient.Close()
			firstServer.Stop()
			_ = firstListener.Close()

			var continuationRequests atomic.Int32
			builtB, err := app.Build(ctx, baseConfig(mockllm.NewWith([]mockllm.Option{
				mockllm.WithRequestObserver(func(port.LLMRequest) { continuationRequests.Add(1) }),
			}, mockllm.TextTurn("continuation complete"))))
			if err != nil {
				t.Fatal(err)
			}
			defer builtB.Close()
			stats := &nativeBridgeRPCStats{ownerResolved: make(chan struct{})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			grpcServer := grpc.NewServer(grpc.UnaryInterceptor(stats.unary), grpc.StreamInterceptor(stats.stream))
			mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(builtB.Service))
			go func() { _ = grpcServer.Serve(listener) }()
			defer func() {
				grpcServer.Stop()
				_ = listener.Close()
			}()

			ownerClient, err := mecatuiclient.Dial(mecatuiclient.DialConfig{Server: listener.Addr().String(), AuthToken: nativeBridgeOwnerToken})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ownerClient.Close() }()
			foreignClient, err := mecatuiclient.Dial(mecatuiclient.DialConfig{Server: listener.Addr().String(), AuthToken: nativeBridgeForeignToken})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = foreignClient.Close() }()

			snapshot, err := ownerClient.GetSession(ctx, string(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			transcript, err := ownerClient.GetSessionTranscript(ctx, string(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			pending, err := ownerClient.DiscoverPendingApproval(ctx, string(sessionID))
			if err != nil {
				stats.mu.Lock()
				stops := append([]string(nil), stats.terminalStops...)
				stats.mu.Unlock()
				t.Fatalf("discover after shutdown: snapshot=%q durable result.stop=%q: %v", snapshot.State, stops, err)
			}
			current, err := ownerClient.GetSession(ctx, string(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.State != "awaiting" || current.State != "awaiting" || pending.RunID != runID || pending.AskID != askID || pending.Cursor == "" {
				t.Fatalf("actual recovery payload: snapshot=%q current=%q pending=%+v", snapshot.State, current.State, pending)
			}
			if !transcript.Complete || transcript.SessionID != string(sessionID) || len(transcript.Messages) == 0 {
				t.Fatalf("actual transcript = %+v", transcript)
			}
			var foundOriginalCall bool
			for _, message := range transcript.Messages {
				for _, call := range message.ToolCalls {
					foundOriginalCall = foundOriginalCall || call.ID == "native-shell" && call.Name == "Shell" && strings.Contains(call.Args, "bridge-command")
				}
			}
			if !foundOriginalCall {
				t.Fatalf("original pending tool call absent from transcript: %+v", transcript.Messages)
			}

			if _, err := foreignClient.GetSession(ctx, string(sessionID)); !mecatuiclient.IsNotFound(err) {
				t.Fatalf("foreign session lookup = %v, want concealed not found", err)
			}
			if foreignPending, err := foreignClient.DiscoverPendingApproval(ctx, string(sessionID)); !mecatuiclient.IsPendingApprovalFailure(err, mecatuiclient.PendingApprovalUnavailable) || foreignPending != (mecatuiclient.PendingApproval{}) {
				t.Fatalf("foreign discovery leaked a handle or wrong refusal: %v", err)
			}
			if err := foreignClient.ResolvePendingApproval(ctx, pending, mecatuiclient.VerdictAllowOnce); !mecatuiclient.IsPendingApprovalFailure(err, mecatuiclient.PendingApprovalUnavailable) {
				t.Fatalf("foreign resolution = %v, want unavailable", err)
			}
			assertNoNativeActiveRun(t, dyn, ref.ID)
			executor.mu.Lock()
			operationsBeforeUI := len(executor.operations)
			executor.mu.Unlock()
			if operationsBeforeUI != 0 || continuationRequests.Load() != 0 {
				t.Fatalf("foreign checks executed work: operations=%d model-requests=%d", operationsBeforeUI, continuationRequests.Load())
			}

			observer, err := ownerClient.WatchPendingApprovalRun(ctx, pending)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			if boundary, err := observer.Recv(); err != nil || boundary.Kind != mecatuiclient.PendingApprovalEventBoundary {
				t.Fatalf("observer boundary: event=%+v err=%v", boundary, err)
			}
			selection := &mecatuiclient.ResumeSelection{
				Row:        mecatuiclient.SessionListItem{ID: string(sessionID), Title: snapshot.Title, Kind: transcript.Kind},
				Transcript: transcript, Snapshot: current, Pending: &pending,
			}
			model := ui.New(ui.Deps{
				Conv: ownerClient, PendingApprovals: nativeBridgePendingClient{ownerClient}, Resume: selection,
				Theme: theme.New("aztec", theme.AztecPalette()), Ctx: ctx, Workspace: workspace,
				InitialPrompt: "draft only", NoAltScreen: true, NoBanner: true,
			})
			ready, terminal := make(chan string, 1), make(chan string, 1)
			lastView := &atomic.Value{}
			tm := teatest.NewTestModel(t, &nativeBridgeModel{Model: model, ready: ready, terminal: terminal, lastView: lastView}, teatest.WithInitialTermSize(100, 40))
			defer func() {
				cancel()
				if err := tm.Quit(); err != nil {
					t.Errorf("quit TUI: %v", err)
				}
				tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
			}()
			select {
			case view := <-ready:
				if !strings.Contains(view, "Shell") || !strings.Contains(view, "draft only") {
					t.Fatalf("approval view lost original call or draft:\n%s", view)
				}
			case <-ctx.Done():
				t.Fatalf("TUI did not display approval: %v", ctx.Err())
			}
			if stats.ownerConverse.Load() != 0 || stats.ownerResolve.Load() != 0 || continuationRequests.Load() != 0 {
				t.Fatalf("startup entered work before keypress: converse=%d resolve=%d model=%d", stats.ownerConverse.Load(), stats.ownerResolve.Load(), continuationRequests.Load())
			}
			executor.mu.Lock()
			operationsBeforeKey := len(executor.operations)
			executor.mu.Unlock()
			if operationsBeforeKey != 0 {
				t.Fatalf("native operations before keypress = %d", operationsBeforeKey)
			}
			assertNoNativeActiveRun(t, dyn, ref.ID)

			tm.Send(tea.KeyPressMsg{Code: tc.key, Text: string(tc.key)})
			select {
			case <-stats.ownerResolved:
			case <-ctx.Done():
				t.Fatalf("owner verdict did not reach public gRPC: %v", ctx.Err())
			}
			stats.mu.Lock()
			control := stats.ownerControl
			stats.mu.Unlock()
			wantVerdict := mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY
			if tc.wantRun {
				wantVerdict = mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE
			}
			if control.GetSessionId() != string(sessionID) || control.GetExpectedRunId() != runID || control.GetAskId() != askID || control.GetVerdict() != wantVerdict {
				t.Fatal("keypress did not resolve the original session/run/ask with the selected verdict")
			}
			if tc.wantRun {
				select {
				case request := <-executor.executed:
					if request.Operation != executionenv.OpCommandStart || request.Command != "printf bridge-command" {
						t.Fatalf("native request operation=%v command=%q", request.Operation, request.Command)
					}
				case <-ctx.Done():
					t.Fatalf("allowed command did not reach native RPC: %v", ctx.Err())
				}
				stored, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, ref.ID, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				activeRun, found, err := unstructured.NestedMap(stored.Object, "status", "activeRun")
				if err != nil || !found || activeRun["runID"] != runID || activeRun["bindingID"] != string(sessionID) || activeRun["ownerHash"] != hex.EncodeToString(ownerSum[:]) {
					t.Fatalf("native claim before command return: found=%t run=%v err=%v", found, activeRun["runID"], err)
				}
				releaseCommand()
			}
			var (
				observedKinds []mecatuiclient.PendingApprovalEventKind
				observedNil   bool
				toolResults   int
			)
			for {
				event, err := observer.Recv()
				if err != nil {
					t.Fatalf("observe resumed run after %v: %v", observedKinds, err)
				}
				observedKinds = append(observedKinds, event.Kind)
				observedNil = observedNil || event.Kind == mecatuiclient.PendingApprovalEventOther && event.Message == nil
				if result, ok := event.Message.(mecatuiclient.ToolResultMsg); ok {
					if result.CallID != "native-shell" || result.IsError == tc.wantRun {
						t.Fatal("resumed tool result identity/outcome mismatch")
					}
					toolResults++
				}
				if event.Kind == mecatuiclient.PendingApprovalEventTerminal {
					result, ok := event.Message.(mecatuiclient.ResultMsg)
					if !ok || result.Stop != "end_turn" {
						t.Fatal("resumed run did not end normally")
					}
					break
				}
			}
			select {
			case <-terminal:
			case <-time.After(5 * time.Second):
				t.Fatalf("TUI did not render terminal continuation:\n%v", lastView.Load())
			}
			if err := tm.Quit(); err != nil {
				t.Fatalf("quit TUI: %v", err)
			}
			tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
			final, ok := tm.FinalModel(t).(*nativeBridgeModel)
			if !ok {
				t.Fatalf("final model = %T", tm.FinalModel(t))
			}
			stats.mu.Lock()
			unmappedKinds := append([]string(nil), stats.unmappedKinds...)
			stats.mu.Unlock()
			t.Logf("unmapped public event.type values: %q", unmappedKinds)
			view := ansi.Strip(final.View().Content)
			if strings.Contains(strings.ToLower(view), "connection failed") {
				t.Fatalf("actual TUI rejected the native continuation (unmapped ordinary event=%t; events=%v):\n%s", observedNil, observedKinds, view)
			}
			if final.ActiveSessionID() != string(sessionID) {
				t.Fatalf("active session = %q, want %q", final.ActiveSessionID(), sessionID)
			}
			if toolResults != 1 || continuationRequests.Load() != 1 || stats.ownerConverse.Load() != 0 || stats.ownerResolve.Load() != 1 || stats.foreignCalls.Load() != 3 {
				t.Fatalf("unexpected work counts: tool-results=%d model=%d converse=%d resolve=%d foreign=%d", toolResults, continuationRequests.Load(), stats.ownerConverse.Load(), stats.ownerResolve.Load(), stats.foreignCalls.Load())
			}
			if strings.Count(view, `command: "printf bridge-command"`) != 1 || strings.Count(view, "│ ✓ Shell")+strings.Count(view, "│ ✗ Shell") != 1 || !strings.Contains(view, tc.wantOutput) || !strings.Contains(view, "draft only") || strings.Contains(view, "enter queue") || strings.Contains(view, "… Shell") {
				t.Fatalf("terminal view lost draft, continuation, or single resolved tool card:\n%s", view)
			}
			executor.mu.Lock()
			operations := append([]executionenv.Operation(nil), executor.operations...)
			executor.mu.Unlock()
			if tc.wantRun {
				if len(operations) != 1 || operations[0] != executionenv.OpCommandStart || !strings.Contains(view, "out�") || !strings.Contains(view, "[exit code: 0]") {
					t.Fatalf("allow result projection: operations=%v\n%s", operations, view)
				}
			} else if len(operations) != 0 || !strings.Contains(view, "denied") {
				t.Fatalf("deny executed original command or lost refusal: operations=%v\n%s", operations, view)
			}
		})
	}
}

var _ ui.PendingApprovalController = nativeBridgePendingClient{}
