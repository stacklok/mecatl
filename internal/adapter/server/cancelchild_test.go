package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// newInteractiveSubagentService builds a Service whose engine is INTERACTIVE and
// carries a Subagent tool with a Bash-bearing child, so a child substitution ask
// surfaces on the Converse stream (the parked state the CancelChild e2e cancels
// out of). It returns the service and the recording Bash so the test can assert
// the command never executed.
func newInteractiveSubagentService(t *testing.T) (*server.Service, *scriptTool) {
	t.Helper()
	bash := &scriptTool{name: "Bash", readOnly: false, content: "ran"}
	childCat := tool.NewCatalog()
	childCat.MustRegister(bash)
	childLLM := mockllm.New(
		// A substitution with a non-read-only inner stand-in: NOT auto-approvable,
		// so it parks the child on a surfaced ask.
		mockllm.ToolCallTurn(call("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child: never reached"),
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
		mockllm.ToolCallTurn(call("p1", "Subagent", `{"prompt":"run it"}`)),
		mockllm.TextTurn("parent: done"),
	)
	engine := agent.NewEngine(agent.Deps{
		LLM:         parentLLM,
		Catalog:     parentCat,
		Policy:      permpolicy.NewPolicy(allowRules(), nil),
		Model:       "test-model",
		Interactive: true, // the child ask SURFACES instead of auto-denying
	})
	svc, err := server.NewService(server.Config{
		Engine:              engine,
		Store:               memstore.New(),
		Workspaces:          func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: parentLLM.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, bash
}

// TestGRPCConverseCancelChild is the wire e2e for the per-child cancel: a Subagent
// child parks on a surfaced permission.ask; the client answers with a CancelChild
// frame instead of an approval. The server must emit a permission.retract for the
// surfaced ask_id, fold the child back as a SUCCESS-with-note
// "[subagent cancelled by user]" tool result, never run the command, and complete
// the parent run cleanly.
func TestGRPCConverseCancelChild(t *testing.T) {
	svc, bash := newInteractiveSubagentService(t)
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
		t.Fatalf("send prompt: %v", err)
	}

	var (
		evs        []*mecatlv1.Event
		childID    string
		askID      string
		retractIDs []string
		sentCancel bool
	)
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			break // EOF (or ctx timeout — the assertions below fail loudly then)
		}
		ev := resp.GetEvent()
		evs = append(evs, ev)
		switch ev.GetType() {
		case "subagent.start":
			childID = ev.GetSubagent().GetChildId()
		case "permission.ask":
			askID = ev.GetAsk().GetAskId()
			if !sentCancel {
				sentCancel = true
				// Answer the parked ask with a per-child CANCEL, not a verdict.
				if serr := stream.Send(&mecatlv1.ConverseRequest{
					Kind: &mecatlv1.ConverseRequest_CancelChild{CancelChild: &mecatlv1.CancelChild{ChildId: childID}},
				}); serr != nil {
					t.Fatalf("send CancelChild: %v", serr)
				}
			}
		case "permission.retract":
			retractIDs = append(retractIDs, ev.GetAsk().GetAskId())
		}
	}

	if !sentCancel || childID == "" || askID == "" {
		t.Fatalf("setup did not surface the child ask (childID=%q askID=%q); events: %v", childID, askID, typesOf(evs))
	}
	if len(retractIDs) != 1 || retractIDs[0] != askID {
		t.Fatalf("expected one permission.retract for ask %q, got %v", askID, retractIDs)
	}
	var taskResult *mecatlv1.ToolResult
	for _, ev := range evs {
		if ev.GetType() == "tool.result" {
			taskResult = ev.GetToolResult()
		}
	}
	if taskResult == nil || taskResult.GetIsError() {
		t.Fatalf("expected a non-error Subagent result, got %+v", taskResult)
	}
	if !strings.Contains(taskResult.GetContent(), "[subagent cancelled by user]") {
		t.Fatalf("result must carry the cancelled-by-user note, got %q", taskResult.GetContent())
	}
	if !strings.Contains(taskResult.GetContent(), "agentId: "+childID) {
		t.Fatalf("result must keep the resumable agentId trailer, got %q", taskResult.GetContent())
	}
	if bash.ran() {
		t.Fatalf("the cancelled child's command must never execute")
	}
	if res := lastResult(t, evs); res.GetStop() == "error" || res.GetStop() == "cancelled" {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", res.GetStop())
	}
}

// TestGRPCConverseApproveSurfacedChildAsk is the wire e2e proving the INTERACTIVE
// embedded posture works end to end (the mecatui-embedded fix, issue #31): a
// Subagent child parks on a surfaced permission.ask; the client answers with a
// ResumeApproval(askID=<the surfaced child askID>, AllowOnce) — exactly what the
// mecatui modal sends — and the verdict must route back to the child
// (Run.Approve → childAskRouter), so the child's command RUNS and the parent
// completes cleanly. This is the load-bearing path mecatui's embedded
// Interactive=true relies on: a child ask is NOT auto-denied; it reaches the
// human's modal and the human's approval reaches the child.
func TestGRPCConverseApproveSurfacedChildAsk(t *testing.T) {
	svc, bash := newInteractiveSubagentService(t)
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
		t.Fatalf("send prompt: %v", err)
	}

	var (
		evs         []*mecatlv1.Event
		askID       string
		sentVerdict bool
	)
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			break
		}
		ev := resp.GetEvent()
		evs = append(evs, ev)
		if ev.GetType() == "permission.ask" && !sentVerdict {
			sentVerdict = true
			askID = ev.GetAsk().GetAskId()
			// Approve the SURFACED CHILD ask exactly as the mecatui modal would:
			// ResumeApproval carrying the surfaced askID. The server routes it to the
			// owning child via childAskRouter.
			if serr := stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{
					AskId:   askID,
					Allow:   true,
					Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE,
				}},
			}); serr != nil {
				t.Fatalf("send ResumeApproval: %v", serr)
			}
		}
	}

	if !sentVerdict || askID == "" {
		t.Fatalf("the child ask must SURFACE to the client (not auto-deny); events: %v", typesOf(evs))
	}
	if !bash.ran() {
		t.Fatalf("approving the surfaced child ask must run the child command (verdict routed to the child)")
	}
	if res := lastResult(t, evs); res.GetStop() == "error" || res.GetStop() == "cancelled" {
		t.Fatalf("the PARENT run must complete cleanly after the child approval, got stop %q", res.GetStop())
	}
}

// parkTool is a read-only member tool that signals when it starts executing and
// parks until its ctx is cancelled — the deterministic "member is mid-drive" anchor
// the team-member cancel e2e sequences on.
type parkTool struct {
	started chan struct{}
	once    sync.Once
}

func newParkTool() *parkTool { return &parkTool{started: make(chan struct{})} }

func (*parkTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Park", Description: "parks until cancelled", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*parkTool) ReadOnly() bool { return true }
func (p *parkTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return session.NewToolResult(in.ID, "interrupted"), nil
}

// newTeamConverseService builds a Service whose parent engine carries a Team tool
// with a lead (benign text turns) and a worker that PARKS mid-tool, so the
// team-member cancel e2e can kill the worker through the real server stream. It
// returns the service and the worker's parking tool (the mid-drive anchor).
func newTeamConverseService(t *testing.T) (*server.Service, *parkTool) {
	t.Helper()
	park := newParkTool()
	leadProv := mockllm.New(
		mockllm.TextTurn("lead: briefed, waiting"),
		mockllm.TextTurn("CONSOLIDATED: worker stopped; partials noted."),
	)
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(call("w1", "Park", `{}`)),
		mockllm.TextTurn("worker: never reached"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}
	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(park)
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:     prov,
			Catalog: cat,
			Policy:  permpolicy.NewPolicy(allowRules(), nil),
			Model:   "member-model",
		})}
	}
	teamTool := agent.NewTeamTool(factory)

	parentCat := tool.NewCatalog()
	parentCat.MustRegister(teamTool)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(call("p1", "Team",
			`{"goal":"fix the bug","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent: got the report"),
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
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, park
}

// TestGRPCConverseCancelTeamMember is the wire e2e for the per-MEMBER cancel: the
// Team tool runs on the Converse path; the worker parks mid-tool; the client reads
// the worker's member_session_id off a team.member event (the D16 field — never a
// derived id) and answers with a CancelChild frame. The member must stop (team.end
// dispositions: stopped/cancelled), the team must still complete with a non-error
// deliverable, and the parent run must end cleanly.
func TestGRPCConverseCancelTeamMember(t *testing.T) {
	svc, park := newTeamConverseService(t)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
		t.Fatalf("send prompt: %v", err)
	}

	// The cancel frame goes out from a separate goroutine ONLY once the worker is
	// genuinely parked (the tool started), with the member id learned off the stream.
	// Both waits select on the test ctx: if the member-id event never arrives (the
	// D16 field went missing) the goroutine exits at the ctx deadline and the
	// gotID=="" assertion below fails LOUD, instead of sendDone.Wait deadlocking the
	// test to the go-test timeout.
	memberID := make(chan string, 1)
	var sendErr error
	var sendDone sync.WaitGroup
	sendDone.Add(1)
	go func() {
		defer sendDone.Done()
		var id string
		select {
		case id = <-memberID:
		case <-ctx.Done():
			return
		}
		select {
		case <-park.started:
		case <-ctx.Done():
			return
		}
		sendErr = stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_CancelChild{CancelChild: &mecatlv1.CancelChild{ChildId: id}},
		})
	}()

	var (
		evs     []*mecatlv1.Event
		gotID   string
		teamEnd *mecatlv1.Team
	)
	for {
		resp, rerr := stream.Recv()
		if rerr != nil {
			break // EOF (or ctx timeout — the assertions below fail loudly then)
		}
		ev := resp.GetEvent()
		evs = append(evs, ev)
		switch ev.GetType() {
		case "team.member":
			if tm := ev.GetTeam(); tm.GetMember() == "worker" && tm.GetMemberSessionId() != "" {
				if gotID == "" {
					gotID = tm.GetMemberSessionId()
					memberID <- gotID
				}
			}
		case "team.end":
			teamEnd = ev.GetTeam()
		}
	}
	sendDone.Wait()

	if gotID == "" {
		t.Fatalf("no team.member event carried the worker's member_session_id; events: %v", typesOf(evs))
	}
	if sendErr != nil {
		t.Fatalf("send CancelChild: %v", sendErr)
	}
	// The published team id is namespaced under the parent session id (review
	// finding 2, issue #368): the server-generated session id + the Team call
	// id "p1".
	if want := "team-" + cs.GetSessionId() + "-p1-worker"; gotID != want {
		t.Fatalf("member_session_id = %q, want %q (MemberSessionID of the published team id)", gotID, want)
	}
	if teamEnd == nil {
		t.Fatalf("no team.end event; events: %v", typesOf(evs))
	}
	workerStopped := false
	for _, d := range teamEnd.GetDispositions() {
		if d.GetName() == "worker" {
			workerStopped = d.GetStopped() &&
				d.GetReason() == mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED
		}
	}
	if !workerStopped {
		t.Fatalf("team.end must dispose the worker stopped/cancelled, got %+v", teamEnd.GetDispositions())
	}
	var teamResult *mecatlv1.ToolResult
	for _, ev := range evs {
		if ev.GetType() == "tool.result" {
			teamResult = ev.GetToolResult()
		}
	}
	if teamResult == nil || teamResult.GetIsError() {
		t.Fatalf("the Team tool must still fold back a non-error deliverable, got %+v", teamResult)
	}
	if res := lastResult(t, evs); res.GetStop() == "error" || res.GetStop() == "cancelled" {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", res.GetStop())
	}
}

// TestServiceCancelChildFallbacks pins the Approve-mirror error contract: an
// unknown session → ErrNotFound; a known session with no live run → ErrNoActiveRun.
func TestServiceCancelChildFallbacks(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), allowRules())
	if err := svc.CancelChild(context.Background(), "nope", "subagent-x"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("unknown session: got %v, want ErrNotFound", err)
	}
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := svc.CancelChild(context.Background(), sess.ID, "subagent-x"); !errors.Is(err, server.ErrNoActiveRun) {
		t.Fatalf("runless session: got %v, want ErrNoActiveRun", err)
	}
}

// TestHTTPCancelChild pins the REST mirror: bad body → 400, missing child_id → 400,
// unknown session → 404, known-but-runless session → 409 (the same surface shape as
// /approve; the live-run path is covered end-to-end by the gRPC test).
func TestHTTPCancelChild(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("ok")), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	id := createHTTPSession(t, srv)

	post := func(path, body string) int {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("/v1/sessions/"+id+"/cancel-child", "{not json"); got != http.StatusBadRequest {
		t.Fatalf("bad body: status = %d, want 400", got)
	}
	if got := post("/v1/sessions/"+id+"/cancel-child", `{}`); got != http.StatusBadRequest {
		t.Fatalf("missing child_id: status = %d, want 400", got)
	}
	if got := post("/v1/sessions/unknown/cancel-child", `{"child_id":"subagent-x"}`); got != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, want 404", got)
	}
	if got := post("/v1/sessions/"+id+"/cancel-child", `{"child_id":"subagent-x"}`); got != http.StatusConflict {
		t.Fatalf("runless session: status = %d, want 409", got)
	}
}
