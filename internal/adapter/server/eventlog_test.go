package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
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

func TestGRPCRelayStreamsOriginalChunksAndDurablyCoalesces(t *testing.T) {
	log := memstore.NewEventLog()
	llm := mockllm.New(mockllm.ChunksTurn(
		mockllm.TextChunk("alpha"), mockllm.TextChunk("-"), mockllm.TextChunk("雪"),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	engine := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: memstore.New(), EventLog: log,

		Now: func() time.Time { return time.Unix(0, 0) }, DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	stream, err := client.Converse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{
		Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"},
	}}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	var wire []string
	for {
		resp, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if ev := resp.GetEvent(); ev.GetType() == string(session.EvMessageDelta) {
			wire = append(wire, ev.GetText())
		}
	}
	if got := strings.Join(wire, "|"); got != "alpha|-|雪" {
		t.Fatalf("wire chunks = %q, want original chunk boundaries", got)
	}

	logged := readEventLog(t, log, sess.ID)
	var durable []string
	for _, ev := range logged {
		if ev.Type == session.EvMessageDelta {
			durable = append(durable, ev.Text)
		}
	}
	if len(durable) != 1 || durable[0] != "alpha-雪" {
		t.Fatalf("durable delta chunks = %q, want one bounded coalesced chunk", durable)
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: sess.ID, Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, CreatedAt: time.Unix(0, 0),
	}, log.Read(context.Background(), sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(folded.Conversation.Messages) != 2 || folded.Conversation.Messages[1].Text != "alpha-雪" {
		t.Fatalf("folded messages = %+v, want exact assistant text", folded.Conversation.Messages)
	}
}

func TestLiveRelaysPersistButOmitObservedNetworkAttempt(t *testing.T) {
	for _, transport := range []string{"grpc", "http"} {
		t.Run(transport, func(t *testing.T) {
			log := memstore.NewEventLog()
			digest, ok := session.NetworkCorrelationDigest("request", "Bearer-SECRET")
			if !ok {
				t.Fatal("digest rejected test correlation")
			}
			llm := &observedAttemptProvider{observations: []session.NetworkAttemptPayload{
				{Attempt: 1, MaxAttempts: 2, RetryDisposition: "sk-live-SECRET", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"},
				{Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect", CorrelationKind: "request", CorrelationDigest: "Bearer-SECRET"},
				{
					Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit",
					Decision: "retry", BackoffMs: 7, FailureClass: "connect",
					CorrelationKind: "request", CorrelationDigest: digest,
				},
			}}
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model", EnableDurableEvidence: true})
			svc, err := newPlacementTestService(server.Config{
				Engine: engine, Store: memstore.New(), EventLog: log,

				Now: func() time.Time { return time.Unix(0, 0) }, DefaultCapabilities: llm.Capabilities(),
			})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}

			var wireTypes []string
			switch transport {
			case "grpc":
				client, cleanup := dialGRPC(t, svc)
				defer cleanup()
				stream, err := client.Converse(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"}}}); err != nil {
					t.Fatal(err)
				}
				_ = stream.CloseSend()
				for {
					resp, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					wireTypes = append(wireTypes, resp.GetEvent().GetType())
				}
			case "http":
				srv := httptest.NewServer(server.NewHTTPHandler(svc))
				defer srv.Close()
				resp, err := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", "application/json", strings.NewReader(`{"text":"go"}`))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				for _, ev := range parseSSE(t, bufio.NewReader(resp.Body)) {
					wireTypes = append(wireTypes, ev.GetType())
				}
			}
			for _, typ := range wireTypes {
				if typ == string(session.EvNetworkAttempt) || typ == string(session.EvRequestManifest) {
					t.Fatalf("live %s stream exposed debugger-only event %q: %v", transport, typ, wireTypes)
				}
			}
			logged := readEventLog(t, log, sess.ID)
			var attempts []session.NetworkAttemptPayload
			var manifests []session.RequestManifestPayload
			for _, ev := range logged {
				if ev.Type == session.EvRequestManifest {
					if ev.RequestManifest == nil {
						t.Fatal("durable request.manifest has nil payload")
					}
					manifests = append(manifests, *ev.RequestManifest)
				}
				if ev.Type == session.EvNetworkAttempt {
					if ev.NetworkAttempt == nil {
						t.Fatal("durable network.attempt has nil payload")
					}
					attempts = append(attempts, *ev.NetworkAttempt)
				}
			}
			if len(attempts) != 1 || attempts[0].SessionID != sess.ID || attempts[0].RunSerial < 1 || attempts[0].Turn != 0 || attempts[0].CorrelationDigest != digest {
				t.Fatalf("durable attempts = %+v", attempts)
			}
			if len(manifests) != 1 || manifests[0].Model != "test-model" || manifests[0].MessageCount != 1 || manifests[0].MessageBytes <= 0 {
				t.Fatalf("durable request manifests = %+v", manifests)
			}
			encoded, err := json.Marshal(logged)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"sk-live-SECRET", "Bearer-SECRET"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("durable log leaked producer token %q: %s", secret, encoded)
				}
			}
		})
	}
}

type observedAttemptProvider struct {
	observations []session.NetworkAttemptPayload
}

func (*observedAttemptProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *observedAttemptProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	for _, observation := range p.observations {
		port.ObserveAttempt(ctx, observation)
	}
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil)
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

type durableEventSink struct {
	log port.EventLog
	id  session.SessionID
}

func (s durableEventSink) Emit(ctx context.Context, ev session.Event) {
	_ = s.log.Append(context.WithoutCancel(ctx), s.id, ev)
}

func observedAttemptTeamService(t *testing.T, log port.EventLog, logID session.SessionID) *server.Service {
	t.Helper()
	llm := &observedAttemptProvider{observations: []session.NetworkAttemptPayload{{
		Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit",
		Decision: "retry", FailureClass: "connect",
	}}}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock", EnableDurableEvidence: true,
			Sink: durableEventSink{log: log, id: logID},
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("unused")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine, Store: memstore.New(), EventLog: log,

		Now: func() time.Time { return time.Unix(0, 0) }, MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestDirectTeamTransportsOmitNetworkAttemptWithoutAffectingDurableObservation(t *testing.T) {
	for _, transport := range []string{"grpc", "http"} {
		t.Run(transport, func(t *testing.T) {
			log := memstore.NewEventLog()
			logID := session.SessionID("team-member-observed")
			svc := observedAttemptTeamService(t, log, logID)
			var wireTypes []string

			switch transport {
			case "grpc":
				client, cleanup := dialGRPC(t, svc)
				defer cleanup()
				created, err := client.CreateTeam(context.Background(), &mecatlv1.CreateTeamRequest{
					SessionId: "source", Name: "test",
					Members: []*mecatlv1.TeammateSpec{{Name: "lead", Lead: true, InitialPrompt: "go"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				stream, err := client.RunTeam(context.Background(), &mecatlv1.RunTeamRequest{TeamId: created.GetTeamId()})
				if err != nil {
					t.Fatal(err)
				}
				for _, te := range recvAllTeamEvents(t, stream) {
					if te.GetEvent() != nil {
						wireTypes = append(wireTypes, te.GetEvent().GetType())
					}
				}
			case "http":
				srv := httptest.NewServer(server.NewHTTPHandler(svc))
				defer srv.Close()
				create, err := http.Post(srv.URL+"/v1/teams", "application/json", strings.NewReader(`{"session_id":"source","name":"test","members":[{"name":"lead","lead":true,"initial_prompt":"go"}]}`))
				if err != nil {
					t.Fatal(err)
				}
				defer create.Body.Close()
				var created mecatlv1.CreateTeamResponse
				if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
					t.Fatal(err)
				}
				run, err := http.Post(srv.URL+"/v1/teams/"+created.GetTeamId()+"/run", "application/json", strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				defer run.Body.Close()
				for _, te := range parseTeamSSE(t, bufio.NewReader(run.Body)) {
					if te.GetEvent() != nil {
						wireTypes = append(wireTypes, te.GetEvent().GetType())
					}
				}
			}

			var ordinary bool
			for _, typ := range wireTypes {
				if typ == string(session.EvNetworkAttempt) || typ == string(session.EvRequestManifest) {
					t.Fatalf("direct Team %s stream exposed debugger-only event %q: %v", transport, typ, wireTypes)
				}
				ordinary = ordinary || typ == string(session.EvTurnStart) || typ == string(session.EvMessageDelta)
			}
			if !ordinary {
				t.Fatalf("direct Team %s stream omitted ordinary member events: %v", transport, wireTypes)
			}
			var durableAttempt, durableManifest bool
			for _, ev := range readEventLog(t, log, logID) {
				durableAttempt = durableAttempt || ev.Type == session.EvNetworkAttempt
				durableManifest = durableManifest || ev.Type == session.EvRequestManifest
			}
			if !durableAttempt || !durableManifest {
				t.Fatalf("transport filtering removed debugger-only durable observation: attempt=%t manifest=%t", durableAttempt, durableManifest)
			}
		})
	}
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
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cs, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
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
// (gauntlet #7): it is an innocuous literal that must NEVER surface VERBATIM in any
// logged event body — the redaction-leak detector asserts on the ABSENCE of its full
// form (ADR 0079: the delegation projection now forwards a clampPreview-BOUNDED,
// control-byte-scrubbed preview, so only a clamped head of the sentinel may cross,
// never the whole thing). Per the repo rule against destructive strings in test
// literals, this is a harmless token, not a real secret.
//
// The sentinel is longer than the clampPreview cap (200 runes) so verbatim carriage
// is impossible by construction: if the projection ever forwarded the raw args, the
// FULL sentinel (head + tail) would appear; a clamped preview drops the tail.
var childSecretSentinel = "SENTINEL_child_arg_must_not_leak_9f3a" + strings.Repeat("_pad", 120) + "_TAIL"

// childSecretSentinelTail is the part of the sentinel that clamping MUST remove.
const childSecretSentinelTail = "_TAIL"

// TestEventLogInheritsStreamRedaction is the gauntlet-#7 subtest: a Subagent
// delegation's child makes a tool call whose args carry a secret-shaped sentinel
// longer than the preview cap. The delegation events the relay records (subagent.*)
// are BOUNDED previews (ADR 0079), so the sentinel's TAIL must NOT appear in ANY
// logged event's serialized body — the log inherits the stream's redaction; it adds
// none of its own.
//
// MUTATION-INTENT: if a future change forwarded raw child args on a delegation
// event, the serialized log would contain the full sentinel (tail included) and this
// fails.
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
	task := newServerTestSubagent(childEngine)

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
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: parentLLM.Capabilities(),
		EventLog:            log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cs, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
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
	// Serialize the WHOLE logged stream and assert the sentinel never appears
	// VERBATIM: the log stores already-redacted events, so a child's tool args cross
	// only as a clamped, scrubbed preview (ADR 0079) — the tail clamping removes
	// must never surface.
	sawSubagent := false
	sawClampedPreview := false
	for _, ev := range logged {
		if strings.HasPrefix(string(ev.Type), "subagent.") {
			sawSubagent = true
		}
		blob, merr := json.Marshal(ev)
		if merr != nil {
			t.Fatalf("marshal logged event: %v", merr)
		}
		if strings.Contains(string(blob), childSecretSentinelTail) {
			t.Fatalf("REDACTION LEAK: child arg sentinel surfaced UNBOUNDED in a logged %s event: %s", ev.Type, blob)
		}
		if strings.HasPrefix(string(ev.Type), "subagent.") && strings.Contains(string(blob), "SENTINEL_child_arg_must_not_leak") {
			sawClampedPreview = true
		}
	}
	if !sawSubagent {
		t.Fatalf("expected at least one subagent.* event in the log (delegation lifecycle): %v", typeNames(logged))
	}
	if !sawClampedPreview {
		t.Fatal("expected a logged subagent.* event carrying a clamped arg preview (ADR 0079); the redaction guard did not exercise")
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
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:      func() time.Time { return time.Unix(0, 0) },
		EventLog: log,
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
	sess, err := svc1.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
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

	// Resume the durable awaiting snapshot directly, then pass every resumed event
	// through the same recorder the HTTP/gRPC relays use for durable append.
	resumed, err := svc2.ApproveRun(context.Background(), sess.ID, askID, session.VerdictAllowAlways, "")
	if err != nil {
		t.Fatalf("ApproveRun: %v", err)
	}
	recorder := server.NewRunEventRecorder(context.Background(), svc2, sess.ID)
	for ev := range resumed.Events() {
		recorder.Observe(ev)
	}
	recorder.Close()

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
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		EventLog:            log,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cs, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
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
	// Prompt-ingress title metadata is persisted after the terminal loop event, so a
	// post-save session.title notification may follow the terminal result. The result
	// itself remains the required post-disconnect durable evidence.
	logged := readEventLog(t, log, cs.ID)
	if last := logged[len(logged)-1].Type; last != session.EvResult && last != session.EvSessionTitle {
		t.Fatalf("last logged event = %q, want the terminal result or its post-save title notification", last)
	}
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
func (g *gateTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return session.NewToolResult(in.ID, "gated body"), nil
}

var _ tool.Tool = (*gateTool)(nil)

// --- StreamSessionEvents (issue #245 Phase 1) --------------------------------

// driveAskingSessionToCompletion drives the askingEventLogService session through
// its full Converse cycle (tool.call -> permission.ask -> approve(allow_always) ->
// tool.result -> result) over the gRPC relay, answering the single ask. It is the
// shared setup for the StreamSessionEvents replay tests: by the time it returns,
// the durable EventLog holds the full timeline including the log-only EvApproval
// and EvUserPrompt the live relay skipped.
func driveAskingSessionToCompletion(t *testing.T, client mecatlv1.HarnessServiceClient, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: sessionID, Text: "go"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		ev := resp.GetEvent()
		if ev.GetType() == "approval" || ev.GetType() == "user_prompt" {
			t.Fatalf("live relay must skip %s (log-only), got it on the wire", ev.GetType())
		}
		if ev.GetType() == "permission.ask" {
			if err := stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{
						AskId:   ev.GetAsk().GetAskId(),
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
}

// TestStreamSessionEventsRoundTrip drives a fixture session through Converse,
// then replays it via Service.StreamSessionEvents and asserts the replayed
// events equal the recorded (type + seq), AND that the log-only events the live
// relay skipped (EvApproval, EvUserPrompt) ARE present on the replay — the
// replay-vs-live correctness call (D1).
func TestStreamSessionEventsRoundTrip(t *testing.T) {
	log := memstore.NewEventLog()
	svc, cs := askingEventLogService(t, log)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	driveAskingSessionToCompletion(t, client, cs.GetSessionId())

	// The recorded timeline (directly from the EventLog) is the source of truth.
	recorded := readEventLog(t, log, session.SessionID(cs.GetSessionId()))

	// Replay over the Service method (proto-free core).
	replayed, err := svc.StreamSessionEvents(context.Background(), session.SessionID(cs.GetSessionId()))
	if err != nil {
		t.Fatalf("StreamSessionEvents: %v", err)
	}
	var got []session.Event
	for ev, iterErr := range replayed {
		if iterErr != nil {
			t.Fatalf("replay iterator: %v", iterErr)
		}
		got = append(got, ev)
	}
	if len(got) != len(recorded) {
		t.Fatalf("replay length = %d, want %d (recorded)", len(got), len(recorded))
	}
	for i, ev := range got {
		if ev.Type != recorded[i].Type {
			t.Fatalf("replay[%d].Type = %q, want %q", i, ev.Type, recorded[i].Type)
		}
		if ev.Seq != recorded[i].Seq {
			t.Fatalf("replay[%d].Seq = %d, want %d", i, ev.Seq, recorded[i].Seq)
		}
	}

	// The log-only kinds MUST be present on the replay (the live relay skips them;
	// StreamSessionEvents does NOT).
	sawApproval, sawUserPrompt := false, false
	var approvalTool string
	var approvalVerdict string
	var firstPrompt string
	for _, ev := range got {
		switch ev.Type {
		case session.EvApproval:
			sawApproval = true
			if ev.Approval != nil {
				approvalTool = ev.Approval.Tool
				approvalVerdict = ev.Approval.Verdict
			}
		case session.EvUserPrompt:
			sawUserPrompt = true
			if ev.UserPrompt != nil && firstPrompt == "" {
				firstPrompt = ev.UserPrompt.Text
			}
		}
	}
	if !sawApproval {
		t.Fatalf("replay MUST include EvApproval (the live relay skips it); got: %v", typeNames(got))
	}
	if approvalTool != "Write" {
		t.Fatalf("replay EvApproval.Tool = %q, want Write", approvalTool)
	}
	if approvalVerdict != session.VerdictStringAllowAlways {
		t.Fatalf("replay EvApproval.Verdict = %q, want %q", approvalVerdict, session.VerdictStringAllowAlways)
	}
	if !sawUserPrompt {
		t.Fatalf("replay MUST include EvUserPrompt (the live relay skips it); got: %v", typeNames(got))
	}
	if firstPrompt != "go" {
		t.Fatalf("replay EvUserPrompt.Text = %q, want %q", firstPrompt, "go")
	}
}

// TestStreamSessionEventsUnknownIDIsEmpty asserts an unknown/pruned session id
// yields an EMPTY stream (absence is data), not an error.
func TestStreamSessionEventsUnknownIDIsEmpty(t *testing.T) {
	log := memstore.NewEventLog()
	svc, _ := askingEventLogService(t, log) // engine wired; we never drive this session

	events, err := svc.StreamSessionEvents(context.Background(), session.SessionID("never-existed"))
	if err != nil {
		t.Fatalf("StreamSessionEvents unknown id: %v (want nil err + empty stream)", err)
	}
	count := 0
	for _, iterErr := range events {
		if iterErr != nil {
			t.Fatalf("replay iterator error on unknown id: %v", iterErr)
		}
		count++
	}
	if count != 0 {
		t.Fatalf("unknown id replayed %d events, want 0 (absence is data)", count)
	}
}

// TestLiveConverseRelaySkipsLogOnlyKinds is the CRITICAL regression guard for the
// replay-vs-live filter split. The three log-only kinds (EvApproval /
// EvCompactionArchive / EvUserPrompt) are persisted to the durable EventLog (so
// StreamSessionEvents can replay them) but MUST NOT appear on the LIVE Converse
// client wire — the driving client already holds its own prompt/verdict; these are
// audit records. This test drives a session through the LIVE gRPC Converse relay,
// collects every client-side event, and asserts NONE of the three log-only kinds
// arrive on the live wire (while they ARE in the durable log, proving the split is
// a relay FILTER, not a toProto gap).
//
// MUTATION-KILL: if a future change moved the skip out of the relay loop (e.g.
// making toProto drop them — which would also break the replay), or accidentally
// relayed the log-only kinds live, this fails.
func TestLiveConverseRelaySkipsLogOnlyKinds(t *testing.T) {
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
	var liveTypes []string
	for {
		resp, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("Recv: %v", rerr)
		}
		ev := resp.GetEvent()
		liveTypes = append(liveTypes, ev.GetType())
		switch ev.GetType() {
		case "approval", "user_prompt", "compaction.archive":
			t.Fatalf("LIVE relay must SKIP log-only %s (it is audit history, not a client event); live types: %v", ev.GetType(), liveTypes)
		case "permission.ask":
			if err := stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{
						AskId:   ev.GetAsk().GetAskId(),
						Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS,
					},
				},
			}); err != nil {
				t.Fatalf("Send allow-always: %v", err)
			}
		case "result":
			_ = stream.CloseSend()
		}
	}

	// The durable log MUST contain the log-only kinds the live wire skipped — this
	// is the other half of the split (they are persisted, just not relayed live). If
	// the log is also missing them, the skip became a drop (the bug this guards
	// against): toProto is now total, but the relay FILTER decides what to SEND live.
	logged := readEventLog(t, log, session.SessionID(cs.GetSessionId()))
	hasApproval, hasUserPrompt := false, false
	for _, ev := range logged {
		if ev.Type == session.EvApproval {
			hasApproval = true
		}
		if ev.Type == session.EvUserPrompt {
			hasUserPrompt = true
		}
	}
	if !hasApproval {
		t.Fatalf("durable log MUST contain EvApproval (live skipped it, log kept it); logged: %v", typeNames(logged))
	}
	if !hasUserPrompt {
		t.Fatalf("durable log MUST contain EvUserPrompt (live skipped it, log kept it); logged: %v", typeNames(logged))
	}
}

// nilEventLogService builds a Service with NO durable EventLog wired — the
// deployment shape where the StreamSessionEvents read-back surface is absent.
// It is the shared setup for the ErrNoEventLog wire tests (the gRPC + HTTP
// handlers must map it to UNIMPLEMENTED / 501, not a bare 500).
func nilEventLogService(t *testing.T) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("hi"))
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		// EventLog intentionally nil.
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestStreamSessionEventsNilEventLogUnimplemented asserts a Service with NO
// durable EventLog wired returns ErrNoEventLog (the wire adapters map to
// UNIMPLEMENTED / HTTP 501).
func TestStreamSessionEventsNilEventLogUnimplemented(t *testing.T) {
	svc := nilEventLogService(t)
	_, err := svc.StreamSessionEvents(context.Background(), session.SessionID("any"))
	if !errors.Is(err, server.ErrNoEventLog) {
		t.Fatalf("err = %v, want ErrNoEventLog", err)
	}
}

// TestStreamSessionEventsNilEventLogHTTP501 asserts the HTTP handler maps a
// nil-EventLog Service to 501 Not Implemented (the wire-level mapping the QA
// reviewer flagged: the Service-layer test above does not exercise the handler).
func TestStreamSessionEventsNilEventLogHTTP501(t *testing.T) {
	svc := nilEventLogService(t)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/sessions/any/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (ErrNoEventLog → Not Implemented)", resp.StatusCode)
	}
}

// TestStreamSessionEventsNilEventLogGRPCUnimplemented asserts the gRPC handler
// maps a nil-EventLog Service to codes.Unimplemented (the wire-level mapping the
// QA reviewer flagged: the Service-layer test only asserts errors.Is(ErrNoEventLog)).
func TestStreamSessionEventsNilEventLogGRPCUnimplemented(t *testing.T) {
	svc := nilEventLogService(t)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	stream, err := client.StreamSessionEvents(context.Background(), &mecatlv1.StreamSessionEventsRequest{SessionId: "any"})
	if err == nil {
		// The error may arrive on Recv rather than the initial call; drain.
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("status = %v, want codes.Unimplemented (ErrNoEventLog)", status.Code(err))
	}
}

// TestStreamSessionEventsGRPC_EmptySessionID asserts the gRPC handler rejects an
// empty session_id with InvalidArgument (the wire-level gate no test exercised).
func TestStreamSessionEventsGRPC_EmptySessionID(t *testing.T) {
	log := memstore.NewEventLog()
	svc, _ := askingEventLogService(t, log) // any service with a wired EventLog
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	stream, err := client.StreamSessionEvents(context.Background(), &mecatlv1.StreamSessionEventsRequest{SessionId: ""})
	if err == nil {
		// The error may arrive on Recv rather than the initial call; drain.
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status = %v, want codes.InvalidArgument (empty session_id)", status.Code(err))
	}
}

func TestSyntheticUserPromptReplay_Scenario2_ReplayAndLiveRelayCompatibility(t *testing.T) {
	log := memstore.NewEventLog()
	svc, cs := askingEventLogService(t, log)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	// This helper fails if ordinary Converse exposes a non-delivery user_prompt.
	driveAskingSessionToCompletion(t, client, cs.GetSessionId())
	if err := log.Append(context.Background(), session.SessionID(cs.GetSessionId()), session.Event{
		Type:       session.EvUserPrompt,
		UserPrompt: &session.UserPromptPayload{Text: "synthetic replay marker", Synthetic: true},
	}); err != nil {
		t.Fatal(err)
	}

	grpcReplay, err := client.StreamSessionEvents(context.Background(), &mecatlv1.StreamSessionEventsRequest{SessionId: cs.GetSessionId()})
	if err != nil {
		t.Fatalf("gRPC replay: %v", err)
	}
	grpcSynthetic := false
	for {
		ev, recvErr := grpcReplay.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("gRPC replay receive: %v", recvErr)
		}
		if ev.GetUserPrompt().GetText() == "synthetic replay marker" {
			grpcSynthetic = ev.GetUserPrompt().GetSynthetic()
		}
	}
	if !grpcSynthetic {
		t.Fatal("gRPC replay did not expose synthetic=true")
	}

	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()
	resp, err := http.Get(httpServer.URL + "/v1/sessions/" + cs.GetSessionId() + "/events")
	if err != nil {
		t.Fatalf("HTTP replay: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	httpSynthetic := false
	for _, frame := range decodeSSEFrames(t, body) {
		var ev mecatlv1.Event
		if err := json.Unmarshal(frame, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.GetUserPrompt().GetText() == "synthetic replay marker" {
			httpSynthetic = ev.GetUserPrompt().GetSynthetic()
		}
	}
	if !httpSynthetic {
		t.Fatal("HTTP replay did not expose synthetic=true")
	}

	// The scheduled-delivery live exception remains the sole user_prompt exception.
	TestFireDelivery_Scenario6_LiveSubscriptionRelaysDeliveryNote(t)
}
