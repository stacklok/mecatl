package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// readEventLog collects every event the EventLog recorded for id, failing on a
// per-item error (an infra fault the iterator surfaces).
func readEventLog(t *testing.T, log port.EventLog, id session.SessionID) []session.Event {
	t.Helper()
	var out []session.Event
	for ev, err := range log.Read(context.Background(), id) {
		if err != nil {
			t.Fatalf("event log read: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

// askingEventLogService builds a Service over a supplied EventLog whose engine
// emits ONE mutating Write call (gated as Ask under ModeDefault, no allow rule),
// so the relay records the tool.call -> permission.ask -> approval -> tool.result
// stream into the log. It returns the service and the session id.
func askingEventLogService(t *testing.T, log port.EventLog) (*server.Service, *mecatlv1.CreateSessionResponse) {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(&scriptTool{name: "Write", readOnly: false, content: "wrote"})
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Write", `{"path":"a.go"}`)),
		mockllm.TextTurn("done"),
	)
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, nil), // no rules: a mutating call asks
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               memstore.New(),
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cs, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	csResp := &mecatlv1.CreateSessionResponse{SessionId: string(cs.ID)}
	return svc, csResp
}

// TestEventLogRecordsApprovalVerdict is the cloud-native Phase 3a GATE: drive a
// session through tool.call -> permission.ask -> approve(allow_always) ->
// tool.result over the gRPC relay, then assert EventLog.Read yields the ordered
// stream INCLUDING exactly one EvApproval{Verdict:"allow_always", Tool:"Write"}
// positioned AFTER the EvPermissionAsk and BEFORE the tool.result.
//
// MUTATION-KILL: deleting the authorize emit in engine/agent/dispatch.go drops
// the EvApproval from the log; the "exactly one EvApproval" assertion then fails.
func TestEventLogRecordsApprovalVerdict(t *testing.T) {
	log := memstore.NewEventLog()
	svc, cs := askingEventLogService(t, log)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	// Drain to EOF, answering the single ask with ALLOW_ALWAYS. EvApproval is NOT
	// relayed to the wire in 3a, so we never see "approval" here — only in the log.
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		if ev.GetType() == "approval" {
			t.Fatalf("EvApproval must NOT be relayed to the client wire in 3a")
		}
		if ev.GetType() == "user_prompt" {
			t.Fatalf("EvUserPrompt must NOT be relayed to the client wire (log-only, ADR 0038)")
		}
		if ev.GetType() == "permission.ask" {
			if err := stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{
						AskId:   ev.GetAsk().GetAskId(),
						Allow:   true,
						Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS,
					},
				},
			}); err != nil {
				t.Fatalf("Send allow-always: %v", err)
			}
		}
		if ev.GetType() == "result" {
			_ = stream.CloseSend()
		}
	}

	logged := readEventLog(t, log, session.SessionID(cs.GetSessionId()))

	// Locate the indices of the relevant lifecycle events.
	askIdx, approvalIdx, resultIdx := -1, -1, -1
	approvals := 0
	var approval session.ApprovalPayload
	for i, ev := range logged {
		switch ev.Type {
		case session.EvPermissionAsk:
			if askIdx == -1 {
				askIdx = i
			}
		case session.EvApproval:
			approvals++
			approvalIdx = i
			if ev.Approval == nil {
				t.Fatalf("EvApproval at %d carries no ApprovalPayload", i)
			}
			approval = *ev.Approval
		case session.EvToolResult:
			if resultIdx == -1 {
				resultIdx = i
			}
		}
	}

	if approvals != 1 {
		t.Fatalf("expected exactly one EvApproval in the log, got %d (events: %v)", approvals, typeNames(logged))
	}
	if askIdx == -1 || resultIdx == -1 {
		t.Fatalf("log missing permission.ask (%d) or tool.result (%d): %v", askIdx, resultIdx, typeNames(logged))
	}
	if askIdx >= approvalIdx || approvalIdx >= resultIdx {
		t.Fatalf("ordering wrong: ask=%d approval=%d result=%d (want ask < approval < result)", askIdx, approvalIdx, resultIdx)
	}
	if approval.Verdict != session.VerdictStringAllowAlways {
		t.Fatalf("verdict = %q, want %q", approval.Verdict, session.VerdictStringAllowAlways)
	}
	if approval.Tool != "Write" {
		t.Fatalf("tool = %q, want Write", approval.Tool)
	}
	if !approval.AllowAlways {
		t.Fatalf("allow_always must set AllowAlways=true")
	}
	if approval.AskID == "" {
		t.Fatalf("EvApproval must carry the askID")
	}

	// EvUserPrompt is log-only (ADR 0038): the genuine prompt "go" must be DURABLY
	// recorded in the log (so the log shows what the user asked) and never relayed to
	// the wire (asserted in the drain loop above).
	var userPrompts int
	var firstPrompt string
	for _, ev := range logged {
		if ev.Type == session.EvUserPrompt {
			userPrompts++
			if ev.UserPrompt == nil {
				t.Fatalf("EvUserPrompt carries no UserPromptPayload")
			}
			if firstPrompt == "" {
				firstPrompt = ev.UserPrompt.Text
			}
		}
	}
	if userPrompts == 0 {
		t.Fatalf("log must record the user prompt (EvUserPrompt); got none: %v", typeNames(logged))
	}
	if firstPrompt != "go" {
		t.Fatalf("first logged user prompt = %q, want %q (the durable record of what the user asked)", firstPrompt, "go")
	}
}

// typeNames projects the logged events' types for a failure message.
func typeNames(evs []session.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = string(ev.Type)
	}
	return out
}

// childSecretSentinel is a secret-SHAPED stand-in for a child tool call's args
// (gauntlet #7): it is an innocuous literal that must NEVER surface in any logged
// event body — the redaction-leak detector asserts on its ABSENCE (per the repo
// rule against destructive strings in test literals, this is a harmless token,
// not a real secret).
const childSecretSentinel = "SENTINEL_child_arg_must_not_leak_9f3a"

// TestEventLogInheritsStreamRedaction is the gauntlet-#7 subtest: a Subagent
// delegation's child makes a tool call whose args carry a secret-shaped sentinel.
// The delegation events the relay records (subagent.*) are metadata-only, so the
// sentinel must NOT appear in ANY logged event's serialized body. The log inherits
// the stream's redaction; it adds none of its own.
//
// MUTATION-INTENT: if a future change forwarded raw child args on a delegation
// event, the serialized log would contain the sentinel and this fails.
func TestEventLogInheritsStreamRedaction(t *testing.T) {
	log := memstore.NewEventLog()

	// Child: a read-only tool call carrying the secret-shaped arg. Read-only so
	// the child auto-runs without parking on an ask.
	childCat := tool.NewCatalog()
	childCat.MustRegister(&scriptTool{name: "Read", readOnly: true, content: "child read body"})
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(call("k1", "Read", `{"path":"`+childSecretSentinel+`"}`)),
		mockllm.TextTurn("child done"),
	)
	childEngine := agent.NewEngine(agent.Deps{
		LLM:     childLLM,
		Catalog: childCat,
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "child-model",
	})
	task := agent.NewSubagentTool(childEngine)

	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(call("p1", "Subagent", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	engine := agent.NewEngine(agent.Deps{
		LLM:     parentLLM,
		Catalog: parentCat,
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               memstore.New(),
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: parentLLM.Capabilities(),
		EventLog:            log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cs, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(cs.ID), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()
	// Drain to EOF so every relayed event (incl. the delegation lifecycle) is logged.
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}

	logged := readEventLog(t, log, cs.ID)
	if len(logged) == 0 {
		t.Fatal("no events logged for the parent session")
	}
	// Serialize the WHOLE logged stream and assert the sentinel never appears: the
	// log stores already-redacted events, so a child's tool args never cross.
	sawSubagent := false
	for _, ev := range logged {
		if strings.HasPrefix(string(ev.Type), "subagent.") {
			sawSubagent = true
		}
		blob, merr := json.Marshal(ev)
		if merr != nil {
			t.Fatalf("marshal logged event: %v", merr)
		}
		if strings.Contains(string(blob), childSecretSentinel) {
			t.Fatalf("REDACTION LEAK: child arg sentinel surfaced in a logged %s event: %s", ev.Type, blob)
		}
	}
	if !sawSubagent {
		t.Fatalf("expected at least one subagent.* event in the log (delegation lifecycle): %v", typeNames(logged))
	}
}

// askingEventLogServiceOverStore builds a Service over the supplied store + EventLog
// whose engine emits the given turns and asks on a mutating Write (no allow rule). It
// is the resume-path twin of newAskingService (resume_awaiting_test.go) that also
// wires an EventLog, so a resume-from-awaiting run's EvApproval is recorded. ran
// counts Write executions; engineSaves controls whether the engine auto-saves.
func askingEventLogServiceOverStore(t *testing.T, store port.SessionStore, log port.EventLog, ps *permstore.Memory, ran *atomic.Int64, llm port.LLMProvider, engineSaves bool) *server.Service {
	t.Helper()
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: ran})
	var engineStore port.SessionStore
	if engineSaves {
		engineStore = store
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
		Store:   engineStore,
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		EventLog:   log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestEventLogRecordsResumePathVerdict is the resume-path GATE (the dead-process
// HTTP resumeFromAwaiting path): a session parks awaiting in svc1 (dies), then a
// fresh svc2 over the SAME store + SAME EventLog resumes it via POST /approve with
// allow_always. The gRPC ResumeApproval frame resolves the IN-FLIGHT run (no
// rehydrate, so it does not reach resolvePendingCall); only the HTTP rehydrate path
// drives resolvePendingCall, whose emit is otherwise UNGUARDED. The resumed run's
// SSE relay (relayRunSSE) Appends the EvApproval, so the log must hold exactly one
// EvApproval{allow_always, Write} on the resume path.
//
// MUTATION-KILL: deleting the resolvePendingCall emit in engine/agent/dispatch.go
// drops this EvApproval; the "exactly one resume-path EvApproval" assertion fails.
func TestEventLogRecordsResumePathVerdict(t *testing.T) {
	dir := t.TempDir()
	log := memstore.NewEventLog()

	// svc1: parks at the Write ask, then "dies". A durable awaiting snapshot remains.
	store1, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	var ran1 atomic.Int64
	svc1 := askingEventLogServiceOverStore(t, store1, log, permstore.New(), &ran1,
		mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"a.go"}`)))), false)
	sess, err := svc1.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	askID := driveServiceToAwaiting(t, svc1, sess.ID)

	// "Restart": fresh svc2 over the SAME store dir + SAME EventLog. Its engine has
	// the continuation turn (the tool call already happened pre-restart).
	store2, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore reopen: %v", err)
	}
	var ran2 atomic.Int64
	svc2 := askingEventLogServiceOverStore(t, store2, log, permstore.New(), &ran2,
		mockllm.New(mockllm.TextTurn("done after approval")), true)

	// POST /approve with allow_always over the HTTP surface → resumeFromAwaiting →
	// Engine.ResumeApproval → resolvePendingCall (the emit under test). httptest's
	// ResponseWriter is a Flusher, so this relays via relayRunSSE (which Appends).
	srv := httptest.NewServer(server.NewHTTPHandler(svc2))
	defer srv.Close()
	body, _ := json.Marshal(approveJSON{AskID: askID, Verdict: session.VerdictStringAllowAlways})
	resp, err := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/approve", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /approve: %v", err)
	}
	// Drain + close the SSE body so the resumed run completes and relayRunSSE finishes
	// appending (incl. the terminal EvResult).
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if ran2.Load() != 1 {
		t.Fatalf("pending Write executed %d time(s) on resume, want exactly 1", ran2.Load())
	}

	logged := readEventLog(t, log, sess.ID)
	approvals := 0
	var resumeApproval session.ApprovalPayload
	for _, ev := range logged {
		if ev.Type == session.EvApproval {
			approvals++
			if ev.Approval == nil {
				t.Fatalf("EvApproval carries no payload")
			}
			resumeApproval = *ev.Approval
		}
	}
	// Exactly one EvApproval total: the parking run (svc1) parked BEFORE any verdict
	// (it was cancelled at the ask), so the ONLY verdict in the whole log is the
	// resume-path one. That is precisely the resolvePendingCall emit under test.
	if approvals != 1 {
		t.Fatalf("expected exactly one (resume-path) EvApproval in the log, got %d: %v", approvals, typeNames(logged))
	}
	if resumeApproval.Verdict != session.VerdictStringAllowAlways {
		t.Fatalf("resume verdict = %q, want %q", resumeApproval.Verdict, session.VerdictStringAllowAlways)
	}
	if resumeApproval.Tool != "Write" {
		t.Fatalf("resume tool = %q, want Write", resumeApproval.Tool)
	}
	if !resumeApproval.AllowAlways {
		t.Fatalf("allow_always must set AllowAlways=true on the resume path")
	}
	if resumeApproval.AskID != askID {
		t.Fatalf("resume askID = %q, want %q", resumeApproval.AskID, askID)
	}
}

// TestEventLogSurvivesClientDisconnect is the durability-decoupling GATE (#2): a
// client disconnects mid-run (its send half / the server's Send to it FAILS), the run
// continues to its terminal, and the event log STILL contains the post-disconnect
// events INCLUDING the terminal EvResult. The log exists to survive the client, so
// the Append must be independent of client send success (it runs even on the
// drain-to-discard path after a dead client).
//
// It drives the gRPC relay because that surface gives a DETERMINISTIC dead-client
// signal: cancelling the client's Converse context makes the server's next
// stream.Send return an error, which sets sendErr and routes every subsequent event
// through the drain-to-discard branch — exactly the branch the decoupled Append must
// survive. (httptest buffers writes, so a closed HTTP body does not deterministically
// fail the server's Write; the gRPC stream does.)
//
// MUTATION-KILL: moving the Append to AFTER the drain-to-discard `continue` (healthy
// path only) drops every post-disconnect event — the terminal EvResult never lands —
// and this fails.
func TestEventLogSurvivesClientDisconnect(t *testing.T) {
	log := memstore.NewEventLog()

	// A read-only tool that BLOCKS in Execute until the test signals, so the run is
	// DETERMINISTICALLY mid-flight: the tool.call is relayed, the client cancels its
	// stream context, and only THEN does the tool unblock — so the tool.result and the
	// terminal EvResult are produced AFTER the client is gone (the server's Send fails
	// → drain-to-discard). With a healthy-path-only Append they would be dropped.
	gate := make(chan struct{})
	blocker := &gateTool{name: "Read", release: gate}
	llm := mockllm.New(
		mockllm.ToolCallTurn(call("c1", "Read", `{"path":"a.go"}`)),
		mockllm.TextTurn("all done"),
	)
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: catalogWith(blocker),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               memstore.New(),
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cs, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	streamCtx, cancelStream := context.WithCancel(context.Background())
	stream, err := client.Converse(streamCtx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(cs.ID), Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	// Read until the tool.call (the tool is now blocked in Execute), then CANCEL the
	// client stream — the server's subsequent Send fails (dead client).
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			t.Fatalf("Recv before disconnect: %v", rerr)
		}
		if resp.GetEvent().GetType() == "tool.call" {
			break
		}
	}
	cancelStream() // CLIENT DISCONNECT — the server's next Send will error
	close(gate)    // unblock the tool: tool.result + terminal are produced POST-disconnect

	// The run continues on the server goroutine (drain-to-discard). Poll the log until
	// the terminal EvResult lands — it must, because the Append is decoupled from the
	// (now-failed) client Send.
	var terminal *session.Event
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logged := readEventLog(t, log, cs.ID)
		for i := range logged {
			if logged[i].Type == session.EvResult {
				terminal = &logged[i]
			}
		}
		if terminal != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if terminal == nil {
		got := readEventLog(t, log, cs.ID)
		t.Fatalf("terminal EvResult never landed in the log after client disconnect; logged: %v", typeNames(got))
	}
	// The terminal EvResult is the post-disconnect tail: the client was already gone
	// (Send failing) when it was produced, so its presence proves the Append outlived
	// the client (the decoupling under test). The disconnect cancels the run, so the
	// terminal stop may be cancelled — the point is the log RECORDED the terminal
	// regardless of client liveness, not which terminal it is.
	logged := readEventLog(t, log, cs.ID)
	if logged[len(logged)-1].Type != session.EvResult {
		t.Fatalf("last logged event = %q, want the terminal result (the post-disconnect tail must be recorded)", logged[len(logged)-1].Type)
	}
}

// approveJSON mirrors the HTTP approve body (the server's struct is unexported);
// only the fields the resume-path test sets are present.
type approveJSON struct {
	AskID   string `json:"ask_id"`
	Verdict string `json:"verdict"`
}

// catalogWith builds a one-tool catalog (a local helper so a test can build an engine
// wired to a single tool).
func catalogWith(tl tool.Tool) *tool.Catalog {
	cat := tool.NewCatalog()
	cat.MustRegister(tl)
	return cat
}

// gateTool is a read-only tool that blocks in Execute until release is closed (or ctx
// is cancelled), letting a test hold a run mid-flight at a known point.
type gateTool struct {
	name    string
	release chan struct{}
}

func (g *gateTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: g.name, Description: g.name + ": gated test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*gateTool) ReadOnly() bool { return true }
func (g *gateTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return session.NewToolResult(in.ID, "gated body"), nil
}

var _ tool.Tool = (*gateTool)(nil)
