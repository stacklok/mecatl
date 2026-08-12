package agent_test

// Per-member cancel for agent teams (BACKGROUND-SUBAGENTS I2). Two layers:
//
//   - Supervisor.CancelMember — the seam both cancel paths share: mid-drive (the
//     drive ctx derives from the member ctx → the EXISTING StopCancelled
//     classification in runTurn de-schedules the member and releases its tasks)
//     and idle-between-rounds (planRound's up-front member-ctx check).
//   - Run.CancelChild(MemberSessionID) — the Converse-path registry route: the
//     supervisor registered each member's cancel in the PARENT run's child-run
//     registry (via withParentCaps), so the same wire frame that cancels a
//     subagent cancels a team member.

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// promptRecordingProvider wraps a scripted provider and records the LAST user text
// of every request, so a test can assert on the SYNTHESIS prompt the lead saw (the
// stopped-member status digest) without touching supervisor internals.
type promptRecordingProvider struct {
	inner port.LLMProvider
	mu    sync.Mutex
	seen  []string
}

func (p *promptRecordingProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

func (p *promptRecordingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	p.seen = append(p.seen, lastUserText(req))
	p.mu.Unlock()
	return p.inner.Stream(ctx, req)
}

func (p *promptRecordingProvider) lastPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 {
		return ""
	}
	return p.seen[len(p.seen)-1]
}

// cancelMemberFactory mirrors memberFactory but registers extra tools into every
// member catalog and accepts arbitrary port.LLMProvider values (so the lead can be
// wrapped in a promptRecordingProvider).
func cancelMemberFactory(t *testing.T, tm *team.Team, providers map[string]port.LLMProvider, extra ...tool.Tool) agent.MemberEngine {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("cancelMemberFactory: no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		for _, tl := range extra {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:     prov,
			Catalog: cat,
			Policy:  allow,
			Hooks:   noopHooks{},
			Model:   "mock",
		})}
	}
}

// claimTaskFor seeds the shared task list with one task claimed by member, so a
// cancel test can assert ReleaseTasks fired (the task returns to pending).
func claimTaskFor(t *testing.T, tm *team.Team, member string) team.TaskID {
	t.Helper()
	id, err := tm.CreateTask("investigate the bug")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, ok, err := tm.ClaimNext(member); !ok || err != nil {
		t.Fatalf("ClaimNext(%s): ok=%v err=%v", member, ok, err)
	}
	return id
}

// assertTaskReleased asserts the single seeded task went back to PENDING with no
// assignee (the stopped member's claim was released).
func assertTaskReleased(t *testing.T, tm *team.Team) {
	t.Helper()
	tasks := tm.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one task, got %d", len(tasks))
	}
	if tasks[0].State != team.TaskPending || tasks[0].Assignee != "" {
		t.Fatalf("cancelled member's task must be released to pending, got state=%q assignee=%q",
			tasks[0].State, tasks[0].Assignee)
	}
}

// memberOutcome returns the named member's outcome.
func memberOutcome(t *testing.T, out agent.TeamOutcome, name string) agent.MemberOutcome {
	t.Helper()
	for _, m := range out.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no outcome for member %q: %+v", name, out.Members)
	return agent.MemberOutcome{}
}

// TestCancelMemberMidDrive cancels a worker parked mid-tool: the drive unwinds via
// the EXISTING StopCancelled classification — the worker is de-scheduled with the
// cancelled disposition, its claimed task is released, the lead still synthesises,
// and the synthesis prompt's TRUSTED status digest names the stopped member.
func TestCancelMemberMidDrive(t *testing.T) {
	tm := team.New("demo")
	park := newNamedParkTool("Park")

	lead := &promptRecordingProvider{inner: mockllm.New(
		mockllm.TextTurn("Lead briefed; waiting."),
		mockllm.TextTurn("CONSOLIDATED: worker was stopped; partial findings noted."),
	)}
	worker := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Park", `{}`)),
		mockllm.TextTurn("worker: never reached"),
	)
	providers := map[string]port.LLMProvider{"lead": lead, "worker": worker}
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		cancelMemberFactory(t, tm, providers, park),
		agent.WithMaxRounds(6), agent.WithTeamGoal("fix the bug"))

	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "coordinate"}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker", InitialPrompt: "investigate"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}
	claimTaskFor(t, tm, "worker")

	// Run under a cancellable ctx so the watchdog below can UNWEDGE a failed cancel:
	// if CancelMember did not actually reach the worker's in-flight drive (the
	// wrong-member mutation), the park would hold the round forever — the watchdog
	// kills the whole run instead and the test fails by the driveCancelled assertion,
	// loud, never by hanging to the go-test timeout.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var cancelOK, driveCancelled bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		<-park.started // the worker is genuinely mid-drive
		cancelOK = sup.CancelMember("worker")
		// The observable proxy for "the member ctx is cancelled": the parked tool's
		// OWN ctx (derived from the member ctx via the AfterFunc merge) must die.
		select {
		case <-park.cancelled:
			driveCancelled = true
		case <-time.After(10 * time.Second):
			cancelRun()
		}
	}()
	out := sup.Run(runCtx, nil)
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelMember must return true for an enrolled member")
	}
	if !driveCancelled {
		t.Fatalf("CancelMember must cancel the worker's in-flight drive (the parked tool's ctx never died)")
	}
	w := memberOutcome(t, out, "worker")
	if !w.Stopped || w.Disposition != agent.DispositionStopped || w.Reason != agent.StopReasonCancelled {
		t.Fatalf("worker disposition = %+v, want stopped/cancelled", w)
	}
	assertTaskReleased(t, tm)
	if !strings.Contains(out.Report, "CONSOLIDATED") {
		t.Fatalf("the lead must still synthesise after a member cancel, got report %q", out.Report)
	}
	// The synthesis prompt's TRUSTED "Team status:" digest flags the cancelled member.
	synth := lead.lastPrompt()
	if !strings.Contains(synth, "Team status:") || !strings.Contains(synth, "worker (cancelled)") {
		t.Fatalf("synthesis prompt must carry the stopped-member digest, got:\n%s", synth)
	}
}

// TestCancelMemberIdleBetweenRounds cancels a member while it is IDLE (before the
// round that would schedule it): planRound's up-front member-ctx check de-schedules
// it — stopped + cancelled + task released — and its provider is NEVER driven.
func TestCancelMemberIdleBetweenRounds(t *testing.T) {
	tm := team.New("demo")
	lead := mockllm.New(
		mockllm.TextTurn("Lead ran round 0."),
		mockllm.TextTurn("CONSOLIDATED: report."),
	)
	worker := mockllm.New(mockllm.TextTurn("worker: never reached"))
	providers := map[string]port.LLMProvider{"lead": lead, "worker": worker}
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		cancelMemberFactory(t, tm, providers),
		agent.WithMaxRounds(6), agent.WithTeamGoal("fix the bug"))

	ctx := context.Background()
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "coordinate"}); err != nil {
		t.Fatalf("AddMember(lead): %v", err)
	}
	// The worker has NO initial prompt: round 0 would schedule it only via its
	// claimed task — but the cancel (fired while it idles, before Run) must win.
	if err := sup.AddMember(ctx, agent.MemberSpec{Name: "worker"}); err != nil {
		t.Fatalf("AddMember(worker): %v", err)
	}
	claimTaskFor(t, tm, "worker")

	if !sup.CancelMember("worker") {
		t.Fatalf("CancelMember must return true for an enrolled member")
	}
	out := sup.Run(ctx, nil)

	w := memberOutcome(t, out, "worker")
	if !w.Stopped || w.Reason != agent.StopReasonCancelled {
		t.Fatalf("worker disposition = %+v, want stopped/cancelled", w)
	}
	assertTaskReleased(t, tm)
	if got := worker.Calls(); got != 0 {
		t.Fatalf("a member cancelled while idle must never be driven, consumed %d turns", got)
	}
	if !strings.Contains(out.Report, "CONSOLIDATED") {
		t.Fatalf("the lead must still synthesise, got report %q", out.Report)
	}
}

// TestCancelMemberUnknownFalse pins the unknown-name contract.
func TestCancelMemberUnknownFalse(t *testing.T) {
	tm := team.New("demo")
	sup := agent.NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		cancelMemberFactory(t, tm, map[string]port.LLMProvider{}))
	if sup.CancelMember("nobody") {
		t.Fatalf("CancelMember(unknown) must return false")
	}
}

// TestCancelChildReachesTeamMember is the Converse-path registry route, in-process:
// the parent model runs the Team tool; the worker parks mid-tool; the parent run's
// CancelChild — addressed by the member SESSION id carried on the team.member
// events (D16: MemberSessionID, never a derived id) — stops the member; the team
// completes with the member disposed stopped/cancelled and the parent run ends
// cleanly.
func TestCancelChildReachesTeamMember(t *testing.T) {
	park := newNamedParkTool("Park")
	leadProv := mockllm.New(
		mockllm.TextTurn("Lead briefed; waiting."),
		mockllm.TextTurn("CONSOLIDATED: worker stopped; reporting partials."),
	)
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(toolCall("w1", "Park", `{}`)),
		mockllm.TextTurn("worker: never reached"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		prov, ok := providers[spec.Name]
		if !ok {
			t.Fatalf("no provider for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(park)
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHooks{}, Model: "mock",
		})}
	}

	teamTool := agent.NewTeamTool(factory)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Team",
			`{"goal":"fix the bug","members":[{"name":"lead","role":"coordinate"},{"name":"worker","role":"investigate"}]}`)),
		mockllm.TextTurn("parent: got the report"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, teamTool)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	gotMember := make(chan string, 1)
	var cancelOK bool
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		memberID := <-gotMember
		<-park.started // the worker is genuinely mid-drive
		cancelOK = r.CancelChild(memberID)
	}()
	evs := drainObserving(t, r, func(ev session.Event) {
		if ev.Type == session.EvTeamMember && ev.Team != nil &&
			ev.Team.Member == "worker" && ev.Team.MemberSessionID != "" {
			select {
			case gotMember <- ev.Team.MemberSessionID:
			default:
			}
		}
	})
	cancelDone.Wait()

	if !cancelOK {
		t.Fatalf("CancelChild must return true for a live team member")
	}
	// The id the events carried IS the canonical MemberSessionID (team id = the
	// parent Team call id) — the single-handle convention, no derivation drift.
	for _, ev := range evs {
		if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.Member == "worker" {
			if want := string(agent.MemberSessionID("p1", "worker")); ev.Team.MemberSessionID != want {
				t.Fatalf("worker MemberSessionID = %q, want %q", ev.Team.MemberSessionID, want)
			}
			break
		}
	}
	// team.end disposes the worker stopped/cancelled.
	var end *session.TeamPayload
	for _, ev := range evs {
		if ev.Type == session.EvTeamEnd && ev.Team != nil {
			end = ev.Team
		}
	}
	if end == nil {
		t.Fatalf("no team.end event: %v", typesOf(evs))
	}
	found := false
	for _, d := range end.Dispositions {
		if d.Name == "worker" {
			found = true
			if d.Disposition != "stopped" || d.Reason != "cancelled" {
				t.Fatalf("worker disposition = %+v, want stopped/cancelled", d)
			}
		}
	}
	if !found {
		t.Fatalf("team.end missing the worker disposition: %+v", end.Dispositions)
	}
	// The Team tool still folds back a usable deliverable and the parent completes.
	res := subagentResultOf(t, evs)
	if res.IsError {
		t.Fatalf("the Team result must not be an error after a member cancel: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Team id: p1") {
		t.Fatalf("the Team result must keep its id header: %q", res.Content)
	}
	if got := lastResult(t, evs); got.Stop == session.StopError || got.Stop == session.StopCancelled {
		t.Fatalf("the PARENT run must complete cleanly, got stop %q", got.Stop)
	}
}
