package server_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// dialGRPC stands up an in-memory gRPC server backed by svc and returns a
// connected client plus a cleanup func.
func dialGRPC(t *testing.T, svc *server.Service) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
}

// newService builds a server.Service over a real *agent.Engine wired with
// mockllm + memfs + permpolicy + the given tools and policy rules.
func newService(t *testing.T, llm *mockllm.Provider, rules []governance.Rule, tools ...tool.Tool) *server.Service {
	return newServiceWithImplementation(t, llm, rules, "", tools...)
}

func newServiceWithImplementation(t *testing.T, llm *mockllm.Provider, rules []governance.Rule, implementation string, tools ...tool.Tool) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range tools {
		cat.MustRegister(tl)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(rules, nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:               engine,
		BuildID:              "test-build",
		ServerImplementation: implementation,
		ProviderEndpoint: func(providerID string) string {
			if providerID != "test-provider" {
				return ""
			}
			return "https://user:secret@provider.example:8443/api/../v1?token=secret#fragment"
		},
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		// The server reads DefaultCapabilities (composition-computed), not the engine.
		// In these tests there is no catalog/selector, so the intersection is the bare
		// adapter caps — mirror that by sourcing them from the wired provider.
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestGRPCGetServerInfoReturnsSafeDiagnosticsSnapshot(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	info, err := client.GetServerInfo(context.Background(), &mecatlv1.GetServerInfoRequest{ProviderId: "test-provider"})
	if err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	if got := info.GetBuildId(); got != "test-build" {
		t.Fatalf("build_id = %q, want test-build", got)
	}
	if got := info.GetServerImplementation(); got != "unknown" {
		t.Fatalf("server_implementation = %q, want unknown", got)
	}
	if got := info.GetLlmProviderDisplayEndpoint(); got != "https://provider.example:8443/v1" {
		t.Fatalf("llm_provider_display_endpoint = %q, want sanitized origin and path", got)
	}
	if strings.Contains(info.GetLlmProviderDisplayEndpoint(), "secret") || strings.Contains(info.GetLlmProviderDisplayEndpoint(), "token") || strings.Contains(info.GetLlmProviderDisplayEndpoint(), "#") {
		t.Fatalf("unsafe endpoint projection: %q", info.GetLlmProviderDisplayEndpoint())
	}
	if _, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: "never-created"}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetServerInfo must not create or inspect session state; GetSession code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPCGetServerInfoNormalizesImplementation(t *testing.T) {
	for _, implementation := range []string{"mecated", "mecak8s", "mecatui"} {
		t.Run(implementation, func(t *testing.T) {
			svc := newServiceWithImplementation(t, mockllm.New(), allowRules(), implementation)
			client, cleanup := dialGRPC(t, svc)
			defer cleanup()
			info, err := client.GetServerInfo(context.Background(), &mecatlv1.GetServerInfoRequest{ProviderId: "test-provider"})
			if err != nil || info.GetServerImplementation() != implementation {
				t.Fatalf("GetServerInfo = %#v, %v", info, err)
			}
		})
	}
	for _, implementation := range []string{"grpc://127.0.0.1", "mecated\nforged", "token=secret-value", "Mecated", strings.Repeat("a", 65)} {
		t.Run("invalid", func(t *testing.T) {
			svc := newServiceWithImplementation(t, mockllm.New(), allowRules(), implementation)
			client, cleanup := dialGRPC(t, svc)
			defer cleanup()
			info, err := client.GetServerInfo(context.Background(), &mecatlv1.GetServerInfoRequest{ProviderId: "test-provider"})
			if err != nil || info.GetServerImplementation() != "unknown" {
				t.Fatalf("GetServerInfo = %#v, %v; want unknown", info, err)
			}
		})
	}
}

// TestGRPCConverseFullCycle drives CreateSession then a Converse stream that
// runs to a terminal result, asserting the event taxonomy crosses the wire.
func TestGRPCConverseFullCycle(t *testing.T) {
	read := &scriptTool{name: "Read", readOnly: true, content: "file body"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("all done"),
	)
	svc := newService(t, llm, allowRules(), read)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "look"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()

	events := recvAll(t, stream)
	if !hasType(events, "tool.call") || !hasType(events, "tool.result") {
		t.Fatalf("missing tool events: %v", typesOf(events))
	}
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" || res.GetText() != "all done" {
		t.Fatalf("result = %+v", res)
	}
}

// TestGRPCConverseInvalidUTF8ToolResult is the issue-#402 regression: a tool
// whose result carries invalid UTF-8 (the orphaned-lead-byte sequence BSD
// `cat -t` produces from an em dash) must NOT terminate the Converse stream
// with codes.Internal. Before the fix, toProtoToolResult copied the Go string
// into a protobuf string field verbatim and proto.Marshal rejected it, killing
// the stream mid-run. recvAll t.Fatalf's on any non-EOF Recv error, so reaching
// a terminal result here proves the stream survived; we additionally assert the
// result text was repaired to U+FFFD.
func TestGRPCConverseInvalidUTF8ToolResult(t *testing.T) {
	bad := &scriptTool{name: "Bash", readOnly: true, content: "out \xe2M-^@M-^T end"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Bash", `{"command":"sed -n 1p x | cat -t"}`)),
		mockllm.TextTurn("all done"),
	)
	svc := newService(t, llm, allowRules(), bad)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "run"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()

	// recvAll fails the test on any non-EOF error (codes.Internal from a failed
	// marshal), so draining to EOF here IS the regression assertion.
	events := recvAll(t, stream)

	// The tool.result event crossed the wire with its text repaired.
	var tr *mecatlv1.ToolResult
	for _, e := range events {
		if e.GetType() == "tool.result" && e.GetToolResult().GetCallId() == "c1" {
			tr = e.GetToolResult()
			break
		}
	}
	if tr == nil {
		t.Fatalf("no tool.result event for c1 in %v", typesOf(events))
	}
	if !strings.ContainsRune(tr.GetContent(), '�') {
		t.Fatalf("tool.result content not repaired to U+FFFD: %q", tr.GetContent())
	}

	// The run reached its terminal result, not a stream-kill.
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" || res.GetText() != "all done" {
		t.Fatalf("result = %+v", res)
	}
}

// TestGRPCCloseSession asserts the session-end RPC: a created session closes ok,
// a second close is idempotent (still ok, since close != delete-snapshot), and a
// never-created id surfaces as codes.NotFound.
func TestGRPCCloseSession(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("close-session-reply")), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	transcript, err := client.GetSessionTranscript(ctx, &mecatlv1.GetSessionTranscriptRequest{SessionId: cs.GetSessionId()})
	if err != nil {
		t.Fatalf("GetSessionTranscript after create: %v", err)
	}
	if len(transcript.GetMessages()) != 0 {
		t.Fatalf("fresh session transcript has %d messages, want empty", len(transcript.GetMessages()))
	}

	// The established server-test convention drives the real Engine through the
	// Service lifecycle and persists its terminal snapshot before CloseSession.
	if got := driveCompletedTurn(t, svc, session.SessionID(cs.GetSessionId()), "persist this conversation"); got != "close-session-reply" {
		t.Fatalf("completed turn reply = %q, want close-session-reply", got)
	}

	if _, err := client.CloseSession(ctx, &mecatlv1.CloseSessionRequest{SessionId: cs.GetSessionId()}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	// EndSession releases runtime resources but keeps the persisted snapshot.
	transcript, err = client.GetSessionTranscript(ctx, &mecatlv1.GetSessionTranscriptRequest{SessionId: cs.GetSessionId()})
	if err != nil {
		t.Fatalf("GetSessionTranscript after close: %v", err)
	}
	var sawUser, sawAssistant bool
	for _, message := range transcript.GetMessages() {
		if message.GetText() == "persist this conversation" {
			sawUser = true
		}
		if message.GetText() == "close-session-reply" {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("closed session transcript lost conversation: user=%v assistant=%v messages=%+v", sawUser, sawAssistant, transcript.GetMessages())
	}
	// Idempotent: the snapshot still persists, so a second close succeeds.
	if _, err := client.CloseSession(ctx, &mecatlv1.CloseSessionRequest{SessionId: cs.GetSessionId()}); err != nil {
		t.Fatalf("second CloseSession: %v", err)
	}
	// Unknown id -> NotFound.
	_, err = client.CloseSession(ctx, &mecatlv1.CloseSessionRequest{SessionId: "never-created"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("CloseSession unknown id code = %v, want NotFound", status.Code(err))
	}
}

func TestGRPCSetMode(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	resp, err := client.SetMode(ctx, &mecatlv1.SetModeRequest{
		SessionId: cs.GetSessionId(),
		Mode:      mecatlv1.PermissionMode_PERMISSION_MODE_PLAN,
	})
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got := resp.GetSession().GetMode(); got != mecatlv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatalf("mode = %v, want PLAN", got)
	}

	got, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: cs.GetSessionId()})
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.GetSession().GetMode() != mecatlv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatalf("persisted mode = %v, want PLAN", got.GetSession().GetMode())
	}
}

// TestGRPCConversePermissionApprove is the headline test: the model proposes a
// tool that requires approval; the loop pauses with a permission.ask; the
// client replies with ResumeApproval{allow:true} on the SAME stream; the loop
// resumes, the tool runs, and the run completes.
func TestGRPCConversePermissionApprove(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
	)
	// nil rules => default decision is Ask.
	svc := newService(t, llm, nil, write)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	var events []*mecatlv1.Event
	var sawAsk bool
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		events = append(events, ev)
		if ev.GetType() == "permission.ask" {
			sawAsk = true
			if err := stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{AskId: ev.GetAsk().GetAskId(), Allow: true},
				},
			}); err != nil {
				t.Fatalf("Send approve: %v", err)
			}
		}
		if ev.GetType() == "result" {
			break
		}
	}
	if !sawAsk {
		t.Fatalf("no permission.ask received: %v", typesOf(events))
	}
	if !write.ran() {
		t.Fatalf("approved tool did not run")
	}
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" {
		t.Fatalf("stop = %q, want end_turn", res.GetStop())
	}
}

// newLearningService mirrors newService but wires a REAL learn store
// (permstore.Memory) into the policy, so an ALLOW_ALWAYS verdict actually learns a
// session-keyed rule (newService's nil store makes Learn a no-op — fine for the
// allow-once tests, useless here). It also wires the SHARED session store into the
// engine Deps (like the resume tests): the ENGINE persists the terminal session
// state at run end, so a second Converse on the same session loads the completed
// snapshot rather than the stale "awaiting" one saved at the ask pause.
func newLearningService(t *testing.T, llm *mockllm.Provider, rules []governance.Rule, tools ...tool.Tool) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	for _, tl := range tools {
		cat.MustRegister(tl)
	}
	store := memstore.New()
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(rules, permstore.New()),
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

// TestGRPCConverseAllowAlwaysLearns drives the new verdict enum through the REAL
// RPC path end to end: the first run pauses on a permission.ask and the client
// resolves it with ResumeApproval{Verdict: ALLOW_ALWAYS} (the enum, not just the
// legacy bool); a SECOND run on the SAME session issues the identical tool call and
// must complete WITHOUT a second permission.ask — the learned session-keyed rule
// suppressed the re-ask (the gRPC mirror of agent's TestAllowAlwaysLearnsThenNoAsk).
func TestGRPCConverseAllowAlwaysLearns(t *testing.T) {
	write := &scriptTool{name: "Write", readOnly: false, content: "wrote"}
	// One shared turn script across BOTH runs (the mock advances its cursor per
	// Stream call): each run proposes the IDENTICAL Write call, then finishes.
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done"),
		mockllm.ToolCallTurn(call("c2", "Write", `{"path":"a"}`)),
		mockllm.TextTurn("done again"),
	)
	// nil rules => Write asks by default; the learn store is what ALLOW_ALWAYS writes to.
	svc := newLearningService(t, llm, nil, write)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Run 1: approve the ask with the ALLOW_ALWAYS verdict enum.
	stream1, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse 1: %v", err)
	}
	if err := stream1.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt 1: %v", err)
	}
	// Drain run 1 to EOF (not just to the result event): the server saves the
	// completed session snapshot before it closes the stream, so EOF is the
	// barrier guaranteeing the second Converse loads the terminal snapshot, not
	// the mid-run "awaiting" one persisted at the ask pause.
	asks1 := 0
	for {
		resp, err := stream1.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv 1: %v", err)
		}
		ev := resp.GetEvent()
		if ev.GetType() == "permission.ask" {
			asks1++
			if err := stream1.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{
						AskId:   ev.GetAsk().GetAskId(),
						Allow:   true,
						Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS,
					},
				},
			}); err != nil {
				t.Fatalf("Send always-allow: %v", err)
			}
		}
		if ev.GetType() == "result" {
			_ = stream1.CloseSend()
		}
	}
	if asks1 != 1 {
		t.Fatalf("run 1: expected exactly 1 permission.ask, got %d", asks1)
	}
	if write.runs() != 1 {
		t.Fatalf("run 1: approved tool should have run once, runs=%d", write.runs())
	}

	// Run 2 (same session, identical command): the learned rule must suppress the
	// re-ask — the run streams straight to its terminal result with NO permission.ask.
	stream2, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse 2: %v", err)
	}
	if err := stream2.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "again"}},
	}); err != nil {
		t.Fatalf("Send prompt 2: %v", err)
	}
	_ = stream2.CloseSend()
	events2 := recvAll(t, stream2)
	if hasType(events2, "permission.ask") {
		t.Fatalf("run 2: the learned allow-always rule should suppress the re-ask, got events %v", typesOf(events2))
	}
	if write.runs() != 2 {
		t.Fatalf("run 2: the identical call should still execute (learned allow), runs=%d", write.runs())
	}
	res := lastResult(t, events2)
	if res.GetStop() != "end_turn" {
		t.Fatalf("run 2 stop = %q, want end_turn", res.GetStop())
	}
}

// TestGRPCConverseCancel sends a Cancel frame on a blocked run and asserts a
// terminal cancelled result.
func TestGRPCConverseCancel(t *testing.T) {
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Wait for the first streamed delta, then cancel.
	var events []*mecatlv1.Event
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv before cancel: %v", err)
		}
		events = append(events, resp.GetEvent())
		if resp.GetEvent().GetType() == "message.delta" {
			break
		}
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}},
	}); err != nil {
		t.Fatalf("Send cancel: %v", err)
	}

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv after cancel: %v", err)
		}
		events = append(events, resp.GetEvent())
		if resp.GetEvent().GetType() == "result" {
			break
		}
	}
	res := lastResult(t, events)
	if res.GetStop() != "cancelled" {
		t.Fatalf("stop = %q, want cancelled", res.GetStop())
	}
}

// TestGRPCConverseFirstFrameMustBePrompt rejects a non-prompt first frame.
func TestGRPCConverseFirstFrameMustBePrompt(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_ = stream.CloseSend()
	_, err = stream.Recv()
	if err == nil {
		t.Fatalf("expected error for non-prompt first frame")
	}
}

// TestGRPCCreateSessionWithCarryover drives source_session_id THROUGH the gRPC
// CreateSession handler: a source session with a small valid conversation,
// carried over via CreateSessionRequest.SourceSessionId, yields a NEW session
// whose persisted history is seeded verbatim. MUTATION-VERIFY: deleting the
// `if src := req.GetSourceSessionId()` wiring line in grpc.go leaves the new
// session with an EMPTY history — the len/content assertions below go red (the
// handler itself still succeeds, so an error-path check alone would not catch it).
func TestGRPCCreateSessionWithCarryover(t *testing.T) {
	// Two text turns: the source's and the post-carryover Converse (the shared
	// engine drives both, so the mockllm carries both replies).
	svc := newService(t, mockllm.New(
		mockllm.TextTurn("GRPC-CARRY-SRC-REPLY"),
		mockllm.TextTurn("GRPC-CARRY-NEW-REPLY"),
	), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build the source over gRPC CreateSession, then drive its turn through the
	// SERVICE-level API (StartRun→drain→Persist→FinishRun, the driveCompletedTurn
	// pattern) so its history is durably persisted for loadAndReopen on carryover.
	src, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession source: %v", err)
	}
	srcID := session.SessionID(src.GetSessionId())
	if got := driveCompletedTurn(t, svc, srcID, "carry this context"); got != "GRPC-CARRY-SRC-REPLY" {
		t.Fatalf("source turn reply = %q", got)
	}

	// The wire field: SourceSessionId must cross the handler into a seeded history.
	cs, err := client.ForkSession(ctx, &mecatlv1.ForkSessionRequest{
		SourceSessionId: src.GetSessionId(),
	})
	if err != nil {
		t.Fatalf("CreateSession with carryover: %v", err)
	}
	if cs.GetSessionId() == "" || cs.GetSessionId() == src.GetSessionId() {
		t.Fatalf("carryover session id = %q, want a new distinct id", cs.GetSessionId())
	}

	newSess, err := svc.GetSession(ctx, session.SessionID(cs.GetSessionId()))
	if err != nil {
		t.Fatalf("GetSession new: %v", err)
	}
	if len(newSess.Conversation.Messages) == 0 {
		t.Fatalf("carryover seeded NO history — the source_session_id wiring did not reach the service")
	}
	var sawUser, sawAssistant bool
	for _, m := range newSess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "carry this context") {
			sawUser = true
		}
		if m.Role == session.RoleAssistant && strings.Contains(m.Text, "GRPC-CARRY-SRC-REPLY") {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("seeded history missing the source's user/assistant text (user=%v assistant=%v)", sawUser, sawAssistant)
	}

	// The seeded session runs a turn to completion over the WIRE: the carried
	// history replays with no provider 400 (the ForkSnapshot+SeedHistory pairing).
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "continue"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()
	if res := lastResult(t, recvAll(t, stream)); res.GetStop() != "end_turn" {
		t.Fatalf("carryover-session Converse result = %+v, want end_turn", res)
	}
}

// TestGRPCCreateSessionCrossProviderCarryover drives a CROSS-PROVIDER carryover
// THROUGH the gRPC CreateSession handler: a blob-carrying source persisted on
// provider A (Reasoning/ProviderPhase + a tool call's ItemID), carried over via
// CreateSessionRequest.SourceSessionId AND an explicit selector on provider B,
// SUCCEEDS (NOT InvalidArgument) and the seeded session's blobs are CLEARED
// while text/tool-call/tool-result content is preserved. MUTATION-VERIFY: if
// someone re-adds a same-provider gate in the HANDLER (rejecting a cross-provider
// source_session_id with InvalidArgument), this goes red at the CreateSession
// call — the existing TestGRPCCreateSessionWithCarryover is same-provider (default
// selector) and would NOT catch it. Mirrors TestCarryoverCrossProviderStripsBlobs
// at the service level, but pins the WIRE handler path. Reuses persistBlobsSource
// for the blob-carrying source shape.
func TestGRPCCreateSessionCrossProviderCarryover(t *testing.T) {
	const newReply = "GRPC-CROSS-NEW-REPLY"
	var seen atomic.Value
	// Build the service over a SessionEngine factory (a cross-provider selector
	// requires a per-session engine), NOT the shared-engine newService helper.
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn(newReply), mockllm.TextTurn(newReply)),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := server.NewService(server.Config{
		Engine:        shared,
		Store:         store,
		Workspaces:    func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultLimits: session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:           func() time.Time { return time.Unix(0, 0) },
		SessionEngine: carryoverFactory(newReply, &seen),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Persist a blob-carrying, tool-pairing-valid source directly on provider A.
	const srcProvider = "openrouter"
	persistBlobsSource(t, store, "grpc-src-cross", srcProvider)
	srcSel := server.ProviderSelector{ProviderID: srcProvider, ModelID: "anthropic/claude-3.5-sonnet"}

	// The WIRE call: source_session_id + an explicit CROSS-provider selector.
	newSel := server.ProviderSelector{ProviderID: "openai", ModelID: "gpt-4o"}
	seen.Store(server.ProviderSelector{})
	cs, err := client.ForkSession(ctx, &mecatlv1.ForkSessionRequest{
		SourceSessionId: "grpc-src-cross",
		ProviderId:      newSel.ProviderID,
		ModelId:         newSel.ModelID,
	})
	if err != nil {
		t.Fatalf("cross-provider CreateSession with carryover: err = %v, want nil (carryover is always allowed across providers)", err)
	}
	if cs.GetSessionId() == "" || cs.GetSessionId() == "grpc-src-cross" {
		t.Fatalf("carryover session id = %q, want a new distinct id", cs.GetSessionId())
	}

	// The new session rehydrated a per-session engine for the NEW provider.
	if got := seen.Load().(server.ProviderSelector); got != newSel {
		t.Fatalf("new session rehydration selector = %+v, want %+v", got, newSel)
	}

	// Assert the seeded history: blobs CLEARED, text/tool-call/tool-result
	// content PRESERVED. OpenAI destination → ToolCall ItemIDs are SYNTHESISED
	// (carryover_item_ prefix, NOT fc_). Load both directly from the store.
	srcSnap, err := store.Load(ctx, "grpc-src-cross")
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}
	newSnap, err := store.Load(ctx, session.SessionID(cs.GetSessionId()))
	if err != nil {
		t.Fatalf("Load new: %v", err)
	}
	if got, want := len(newSnap.Conversation.Messages), len(srcSnap.Conversation.Messages); got != want {
		t.Fatalf("seeded history len = %d, want source's %d", got, want)
	}
	var sawAssistantText, sawToolCall, sawToolResult, sawReasoning, sawPhase bool
	seenIDs := make(map[string]int) // id → msg index
	for i, sm := range srcSnap.Conversation.Messages {
		nm := newSnap.Conversation.Messages[i]
		if nm.Role != sm.Role {
			t.Fatalf("msg %d: role = %q, want source's %q", i, nm.Role, sm.Role)
		}
		if nm.Text != sm.Text {
			t.Fatalf("msg %d: text = %q, want source's %q", i, nm.Text, sm.Text)
		}
		// Tool-result content is provider-neutral: it survives the strip.
		if sm.Role == session.RoleTool {
			if sm.ToolResult == nil || nm.ToolResult == nil {
				t.Fatalf("msg %d: tool message lost its ToolResult (src=%v new=%v)", i, sm.ToolResult, nm.ToolResult)
			}
			if nm.ToolResult.CallID != sm.ToolResult.CallID || nm.ToolResult.Content != sm.ToolResult.Content || nm.ToolResult.IsError != sm.ToolResult.IsError {
				t.Fatalf("msg %d: ToolResult drifted (callID %q/%q content %q/%q isErr %v/%v)", i,
					nm.ToolResult.CallID, sm.ToolResult.CallID, nm.ToolResult.Content, sm.ToolResult.Content, nm.ToolResult.IsError, sm.ToolResult.IsError)
			}
			sawToolResult = true
		}
		if nm.Reasoning != "" {
			sawReasoning = true
		}
		if nm.ProviderPhase != "" {
			sawPhase = true
		}
		for j, c := range nm.ToolCalls {
			// OpenAI destination: ItemIDs are synthesised.
			if c.ItemID == "" {
				t.Fatalf("msg %d call %d: ItemID is empty — want a synthesised id (cross-provider to openai must synthesise)", i, j)
			}
			if !strings.HasPrefix(c.ItemID, "carryover_item_") {
				t.Fatalf("msg %d call %d: ItemID = %q, want carryover_item_ prefix", i, j, c.ItemID)
			}
			if strings.HasPrefix(c.ItemID, "fc_") {
				t.Fatalf("msg %d call %d: ItemID = %q, must NOT use fc_ prefix (collision risk)", i, j, c.ItemID)
			}
			if prevIdx, dup := seenIDs[c.ItemID]; dup {
				t.Fatalf("msg %d call %d: duplicate ItemID %q (first seen at msg %d)", i, j, c.ItemID, prevIdx)
			}
			seenIDs[c.ItemID] = i
			// Preserve caller identity.
			if c.ID != session.ToolCallID("c1") || c.Name != "Read" || string(c.Args) != `{"path":"f.go"}` {
				t.Fatalf("msg %d call %d: tool-call identity/args drifted (ID=%q Name=%q Args=%s)", i, j, c.ID, c.Name, c.Args)
			}
		}
	}
	if len(seenIDs) == 0 {
		t.Fatalf("no tool calls to synthesise ItemIDs for — source fixture missing tool calls")
	}
	// Pin the provider-neutral content survived and the provider-private blobs stripped.
	for _, m := range newSnap.Conversation.Messages {
		if m.Role == session.RoleAssistant {
			if m.Text == "reading f.go" {
				sawAssistantText = true
			}
			if len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == "c1" {
				sawToolCall = true
			}
		}
	}
	if !sawAssistantText || !sawToolCall || !sawToolResult {
		t.Fatalf("stripped history lost provider-neutral content (assistantText=%v toolCall=%v toolResult=%v)", sawAssistantText, sawToolCall, sawToolResult)
	}
	if sawReasoning || sawPhase {
		t.Fatalf("cross-provider carryover kept a provider-private blob (reasoning=%v phase=%v)", sawReasoning, sawPhase)
	}
	if err := session.ValidateToolPairing(newSnap.Conversation.Messages); err != nil {
		t.Fatalf("seeded history not tool-pairing-valid after strip: %v", err)
	}

	// The seeded session runs a turn to completion over the WIRE: the stripped,
	// provider-neutral history replays cleanly to the new provider (no 400).
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "continue"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()
	if res := lastResult(t, recvAll(t, stream)); res.GetStop() != "end_turn" || res.GetText() != newReply {
		t.Fatalf("cross-provider carryover Converse result = stop %q text %q, want end_turn/%q", res.GetStop(), res.GetText(), newReply)
	}
	_ = srcSel // srcSel documents the source provider; the source is persisted, not created via a selector
}

// allowRules returns a policy rule set that allows every tool call: the
// canonical allow-all FLOOR (permpolicy.AllowAllFloorRules — same ruleset as
// production childRules(), so a floor-scoped allow never registers as a
// CONFIGURED allow and a child's substitution ask still surfaces).
func allowRules() []governance.Rule {
	return permpolicy.AllowAllFloorRules()
}

// recvAllTeamEvents drains a RunTeam stream to EOF and returns every frame —
// the TeamEvent analogue of recvAll.
func recvAllTeamEvents(t *testing.T, stream mecatlv1.HarnessService_RunTeamClient) []*mecatlv1.TeamEvent {
	t.Helper()
	var out []*mecatlv1.TeamEvent
	for {
		te, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		out = append(out, te)
	}
}

// TestGRPCRunTeamEmitsOutcomeFrame pins the terminal outcome frame on the gRPC
// RunTeam stream (issue #36): the stream's LAST frame carries TeamEvent.outcome
// (member empty, event nil, fields populated), and no earlier frame does.
func TestGRPCRunTeamEmitsOutcomeFrame(t *testing.T) {
	svc := teamService(t, mockllm.New(
		mockllm.TextTurn("delegating"), mockllm.TextTurn("report"),
	))
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	created, err := client.CreateTeam(ctx, &mecatlv1.CreateTeamRequest{
		SessionId: "source", Name: "test",
		Members: []*mecatlv1.TeammateSpec{{Name: "lead", Lead: true, InitialPrompt: "go"}},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	stream, err := client.RunTeam(ctx, &mecatlv1.RunTeamRequest{TeamId: created.GetTeamId()})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	events := recvAllTeamEvents(t, stream)
	if len(events) < 2 {
		t.Fatalf("stream carried %d frames, want member events + a terminal outcome frame", len(events))
	}

	last := events[len(events)-1]
	if last.GetOutcome() == nil {
		t.Fatalf("last frame has no outcome: %+v", last)
	}
	if last.GetMember() != "" || last.GetEvent() != nil {
		t.Errorf("terminal frame must carry ONLY the outcome (member=%q event=%v)", last.GetMember(), last.GetEvent())
	}
	for i, te := range events[:len(events)-1] {
		if te.GetOutcome() != nil {
			t.Errorf("frame %d carries an outcome; only the terminal frame may", i)
		}
	}

	out := last.GetOutcome()
	if out.GetRounds() < 1 {
		t.Errorf("outcome.rounds = %d, want >= 1", out.GetRounds())
	}
	if !out.GetQuiescent() || out.GetStop() != "end_turn" {
		t.Errorf("outcome quiescent=%v stop=%q, want quiescent end_turn", out.GetQuiescent(), out.GetStop())
	}
	if out.GetBudgetExhausted() {
		t.Error("outcome.budget_exhausted = true, want false (no budget set)")
	}
	ds := out.GetDispositions()
	if len(ds) != 1 || ds[0].GetName() != "lead" || ds[0].GetStopped() {
		t.Errorf("outcome.dispositions = %+v, want one done lead", ds)
	}
}

// TestGRPCRunTeamSurfacesBudgetExhausted pins the budget knob END-TO-END over the
// wire (issue #36): CreateTeamRequest.max_team_tokens (against a server with no
// configured budget — tighten from "unlimited") trips at the round boundary, and
// the terminal outcome frame reports budget_exhausted with the "budget" stop
// string. Usage pattern mirrors TestRunTeamBudgetExhaustedOutcome (team_test.go).
func TestGRPCRunTeamSurfacesBudgetExhausted(t *testing.T) {
	// budgetTripProviders' round-0 spend (700) crosses the 500 budget while the
	// worker's self-ping keeps the team NON-quiescent — the "budget" stop shape.
	svc := teamServicePerMember(t, budgetTripProviders(), 0) // no server budget; the request's 500 tightens "unlimited"
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	created, err := client.CreateTeam(ctx, &mecatlv1.CreateTeamRequest{
		SessionId: "source", Name: "test", Goal: "do one round of work",
		MaxTeamTokens: 500,
		Members: []*mecatlv1.TeammateSpec{
			{Name: "lead", Lead: true, InitialPrompt: "delegate then synthesise"},
			{Name: "worker", InitialPrompt: "do the work"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	stream, err := client.RunTeam(ctx, &mecatlv1.RunTeamRequest{TeamId: created.GetTeamId()})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	events := recvAllTeamEvents(t, stream)
	if len(events) == 0 {
		t.Fatal("empty RunTeam stream")
	}
	out := events[len(events)-1].GetOutcome()
	if out == nil {
		t.Fatalf("last frame has no outcome: %+v", events[len(events)-1])
	}
	if !out.GetBudgetExhausted() {
		t.Error("outcome.budget_exhausted = false, want true (request budget 500, round-0 spent 700)")
	}
	if out.GetStop() != "budget" {
		t.Errorf("outcome.stop = %q, want %q", out.GetStop(), "budget")
	}
	if total := out.GetUsage().GetInputTokens() + out.GetUsage().GetOutputTokens(); total < 500 {
		t.Errorf("outcome.usage total = %d, want >= the 500 budget", total)
	}
}
