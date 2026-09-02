package server_test

// CancelTeammate — the RunTeam-path per-member cancel unary (issue #29,
// BACKGROUND-SUBAGENTS D4). The engine seam (Supervisor.CancelMember) is already
// pinned by TestCancelMemberMidDrive / TestCancelMemberIdleBetweenRounds /
// TestCancelMemberUnknownFalse (engine/agent/teamcancel_test.go); these tests pin
// the server wiring: the Service method (phase gate + sentinel mapping), the gRPC
// handler, and the HTTP mirror, plus the mid-round e2e through the real
// supervisor + mockllm members.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// cancelTeammateService builds a team-enabled Service with PER-MEMBER providers
// (round-0 members run concurrently, so they must not race over one shared turn
// queue), registering the extra tools into every member catalog. It also returns
// an accessor for the team aggregate the factory bound to, so a test can seed a
// claimed task and assert its release — the Service creates the aggregate
// internally and exposes no other handle on it.
func cancelTeammateService(t *testing.T, providers map[string]*mockllm.Provider, extra ...tool.Tool) (*server.Service, func() *team.Team) {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	var (
		mu       sync.Mutex
		captured *team.Team
	)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		mu.Lock()
		captured = tm
		mu.Unlock()
		llm := providers[spec.Name]
		if llm == nil {
			t.Fatalf("no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		for _, tl := range extra {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := server.NewService(server.Config{
		Engine:       engine,
		Store:        memstore.New(),
		Workspaces:   func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, func() *team.Team {
		mu.Lock()
		defer mu.Unlock()
		return captured
	}
}

// parkedWorkerProviders scripts a lead (benign text turns, then a synthesis
// report) and a worker that PARKS mid-tool — the deterministic "member is
// mid-round" anchor the cancel tests sequence on.
func parkedWorkerProviders() map[string]*mockllm.Provider {
	return map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.TextTurn("lead: briefed, waiting"),
			mockllm.TextTurn("CONSOLIDATED: worker stopped; partials noted."),
		),
		"worker": mockllm.New(
			mockllm.ToolCallTurn(call("w1", "Park", `{}`)),
			mockllm.TextTurn("worker: never reached"),
		),
	}
}

// parkedRoster is the lead + parking-worker roster parkedWorkerProviders scripts.
func parkedRoster() []agent.MemberSpec {
	return []agent.MemberSpec{
		{Name: "lead", Lead: true, InitialPrompt: "coordinate"},
		{Name: "worker", InitialPrompt: "investigate"},
	}
}

// TestCancelTeammateMidRound is the service-level e2e: a worker parked mid-round
// is cancelled via Service.CancelTeammate. The worker de-schedules with the
// cancelled stop reason, its claimed task releases back to pending, and the team
// still delivers the lead's consolidated report. A live-team cancel for an
// UNKNOWN member name is also probed here (the live run is the natural anchor):
// it must return ErrChildNotFound — the deliberately family-neutral sentinel,
// not a new one.
func TestCancelTeammateMidRound(t *testing.T) {
	park := newParkTool()
	svc, teamOf := cancelTeammateService(t, parkedWorkerProviders(), park)
	ctx := context.Background()

	id, _, err := svc.CreateTeam(ctx, "/ws", "test", "fix the bug", 0, parkedRoster())
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	// Seed the shared task list with one task claimed by the worker, so the cancel
	// can prove ReleaseTasks fired.
	tm := teamOf()
	if tm == nil {
		t.Fatal("the member factory never bound a team aggregate")
	}
	if _, err := tm.CreateTask("investigate the bug"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, ok, cerr := tm.ClaimNext("worker"); !ok || cerr != nil {
		t.Fatalf("ClaimNext(worker): ok=%v err=%v", ok, cerr)
	}

	var (
		out    agent.TeamOutcome
		runErr error
	)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		out, runErr = svc.RunTeam(context.Background(), id, func(agent.TeamEvent) {})
	}()

	select {
	case <-park.started: // the worker is genuinely mid-round
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the worker to park")
	}

	// Live team, unknown member name → ErrChildNotFound (the run keeps going).
	if err := svc.CancelTeammate(ctx, id, "nobody"); !errors.Is(err, server.ErrChildNotFound) {
		t.Errorf("CancelTeammate(unknown member): err = %v, want ErrChildNotFound", err)
	}

	if err := svc.CancelTeammate(ctx, id, "worker"); err != nil {
		t.Fatalf("CancelTeammate(worker): %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("RunTeam did not finish after the member cancel")
	}
	if runErr != nil {
		t.Fatalf("RunTeam: %v", runErr)
	}

	// The worker is disposed stopped/cancelled.
	var worker agent.MemberOutcome
	found := false
	for _, m := range out.Members {
		if m.Name == "worker" {
			worker, found = m, true
		}
	}
	if !found {
		t.Fatalf("no outcome for the worker: %+v", out.Members)
	}
	if !worker.Stopped || worker.Disposition != agent.DispositionStopped || worker.Reason != agent.StopReasonCancelled {
		t.Fatalf("worker disposition = %+v, want stopped/cancelled", worker)
	}
	// Its claimed task was released back to pending.
	tasks := tm.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one task, got %d", len(tasks))
	}
	if tasks[0].State != team.TaskPending || tasks[0].Assignee != "" {
		t.Fatalf("cancelled member's task must be released to pending, got state=%q assignee=%q",
			tasks[0].State, tasks[0].Assignee)
	}
	// The team still delivers its report (the lead's synthesis).
	if !strings.Contains(out.Report, "CONSOLIDATED") {
		t.Fatalf("the lead must still synthesise after a member cancel, got report %q", out.Report)
	}
}

// TestGRPCCancelTeammateMidRunTeam is the full wire e2e: CancelTeammate is called
// over bufconn gRPC mid-RunTeam stream. The stream must still deliver the
// terminal TeamEvent.outcome frame, with the worker disposed stopped/cancelled
// and the lead done (the report TEXT never rides the outcome frame by design —
// "member content beyond the capped finding previews never rides it"; the
// service-level test pins the Report content). The unknown-member NotFound
// mapping is probed on the same live run.
func TestGRPCCancelTeammateMidRunTeam(t *testing.T) {
	park := newParkTool()
	svc, _ := cancelTeammateService(t, parkedWorkerProviders(), park)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	// 30s, not the usual 10-15: the parked-cancel shape has a flake history on
	// loaded -race CI (cf. TestCancelChildWhileParkedOnAsk), and this ctx bounds
	// the whole create+park+cancel+drain sequence.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := client.CreateTeam(ctx, &mecatlv1.CreateTeamRequest{
		SessionId: "source", Name: "test", Goal: "fix the bug",
		Members: []*mecatlv1.TeammateSpec{
			{Name: "lead", Lead: true, InitialPrompt: "coordinate"},
			{Name: "worker", InitialPrompt: "investigate"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	teamID := created.GetTeamId()
	stream, err := client.RunTeam(ctx, &mecatlv1.RunTeamRequest{TeamId: teamID})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}

	// The cancel goes out from a separate goroutine only once the worker is
	// genuinely parked. Both probes select on the test ctx so a never-parking
	// worker fails LOUD at the assertions below instead of deadlocking.
	var (
		unknownCode codes.Code
		cancelErr   error
	)
	var cancelDone sync.WaitGroup
	cancelDone.Add(1)
	go func() {
		defer cancelDone.Done()
		select {
		case <-park.started:
		case <-ctx.Done():
			return
		}
		// Unknown member on a LIVE team → NotFound over the wire.
		_, uerr := client.CancelTeammate(ctx, &mecatlv1.CancelTeammateRequest{TeamId: teamID, Member: "nobody"})
		unknownCode = status.Code(uerr)
		_, cancelErr = client.CancelTeammate(ctx, &mecatlv1.CancelTeammateRequest{TeamId: teamID, Member: "worker"})
	}()

	events := recvAllTeamEvents(t, stream)
	cancelDone.Wait()

	if unknownCode != codes.NotFound {
		t.Errorf("CancelTeammate(unknown member) code = %v, want NotFound", unknownCode)
	}
	if cancelErr != nil {
		t.Fatalf("CancelTeammate(worker): %v", cancelErr)
	}
	if len(events) == 0 {
		t.Fatal("empty RunTeam stream")
	}
	last := events[len(events)-1]
	out := last.GetOutcome()
	if out == nil {
		t.Fatalf("the stream must still end with the terminal outcome frame, got %+v", last)
	}
	workerCancelled, leadDone := false, false
	for _, d := range out.GetDispositions() {
		switch d.GetName() {
		case "worker":
			workerCancelled = d.GetStopped() &&
				d.GetReason() == mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED
		case "lead":
			leadDone = !d.GetStopped()
		}
	}
	if !workerCancelled {
		t.Errorf("outcome must dispose the worker stopped/cancelled, got %+v", out.GetDispositions())
	}
	if !leadDone {
		t.Errorf("the lead must still complete (synthesis ran), got %+v", out.GetDispositions())
	}
}

// TestCancelTeammateIdleBetweenRounds drives the D5 IDLE de-schedule path through
// the server seam (the mid-round test's parked worker always lands the cancel on
// an in-flight drive; this one lands it on a member idle BETWEEN rounds, caught by
// planRound's up-front ctx check): the lead parks round 0 to hold the team in the
// running phase while the worker — no initial prompt, no task yet — sits idle.
// A task is then claimed for the worker (so the NEXT round would schedule it) and
// CancelTeammate fires while it idles. The worker must be de-scheduled without its
// provider EVER being driven, its claimed task released, and the run still return
// an outcome. Mirrors engine-level TestCancelMemberIdleBetweenRounds, which pins
// the same staging through Supervisor.CancelMember directly.
func TestCancelTeammateIdleBetweenRounds(t *testing.T) {
	park := newParkTool()
	workerProv := mockllm.New(mockllm.TextTurn("worker: never reached"))
	providers := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.ToolCallTurn(call("l1", "Park", `{}`)), // round 0: park (holds the run live)
			mockllm.TextTurn("CONSOLIDATED: report"),       // synthesis, if driven
		),
		"worker": workerProv,
	}
	svc, teamOf := cancelTeammateService(t, providers, park)
	ctx := context.Background()

	// The worker has NO initial prompt: round 0 schedules only the lead, so the
	// worker is genuinely idle while the lead parks.
	id, _, err := svc.CreateTeam(ctx, "/ws", "test", "fix the bug", 0, []agent.MemberSpec{
		{Name: "lead", Lead: true, InitialPrompt: "coordinate"},
		{Name: "worker"},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	tm := teamOf()
	if tm == nil {
		t.Fatal("the member factory never bound a team aggregate")
	}

	var (
		out    agent.TeamOutcome
		runErr error
	)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		out, runErr = svc.RunTeam(context.Background(), id, func(agent.TeamEvent) {})
	}()

	select {
	case <-park.started: // round 0 is live (lead parked); the worker idles unscheduled
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the lead to park")
	}

	// Claim a task for the idle worker so the next round WOULD schedule it — the
	// cancel must win via planRound's up-front ctx check instead.
	if _, terr := tm.CreateTask("investigate the bug"); terr != nil {
		t.Fatalf("CreateTask: %v", terr)
	}
	if _, ok, cerr := tm.ClaimNext("worker"); !ok || cerr != nil {
		t.Fatalf("ClaimNext(worker): ok=%v err=%v", ok, cerr)
	}
	if err := svc.CancelTeammate(ctx, id, "worker"); err != nil {
		t.Fatalf("CancelTeammate(idle worker): %v", err)
	}
	// Unwedge: cancel the parked lead so the round ends and the next round plans.
	if err := svc.CancelTeammate(ctx, id, "lead"); err != nil {
		t.Fatalf("CancelTeammate(lead): %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("RunTeam did not finish after the cancels")
	}
	if runErr != nil {
		t.Fatalf("RunTeam: %v", runErr)
	}

	// The idle worker was de-scheduled: stopped/cancelled, never driven, task freed.
	var worker agent.MemberOutcome
	found := false
	for _, m := range out.Members {
		if m.Name == "worker" {
			worker, found = m, true
		}
	}
	if !found {
		t.Fatalf("no outcome for the worker: %+v", out.Members)
	}
	if !worker.Stopped || worker.Reason != agent.StopReasonCancelled {
		t.Fatalf("idle worker disposition = %+v, want stopped/cancelled", worker)
	}
	if got := workerProv.Calls(); got != 0 {
		t.Fatalf("a member cancelled while idle must never be driven, consumed %d turns", got)
	}
	tasks := tm.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one task, got %d", len(tasks))
	}
	if tasks[0].State != team.TaskPending || tasks[0].Assignee != "" {
		t.Fatalf("cancelled member's task must be released to pending, got state=%q assignee=%q",
			tasks[0].State, tasks[0].Assignee)
	}
}

// TestCancelTeammateFinishedMemberNoOp pins the honest-no-op contract: cancelling
// a member that has ALREADY finished its drive (entry present in the supervisor)
// while the team is still running returns nil success — CancelMember fires the
// member's cancel, whose ctx simply goes unobserved. Only an unknown NAME is
// ErrChildNotFound. The lead parks to hold the team in the running phase; the
// worker's terminal result event is the "finished" barrier.
func TestCancelTeammateFinishedMemberNoOp(t *testing.T) {
	park := newParkTool()
	providers := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.ToolCallTurn(call("l1", "Park", `{}`)), // round 0: park (holds the run live)
			mockllm.TextTurn("CONSOLIDATED: report"),       // synthesis, if driven
		),
		"worker": mockllm.New(mockllm.TextTurn("worker: done")),
	}
	svc, _ := cancelTeammateService(t, providers, park)
	ctx := context.Background()

	id, _, err := svc.CreateTeam(ctx, "/ws", "test", "fix the bug", 0, parkedRoster())
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	workerDone := make(chan struct{})
	var once sync.Once
	var (
		out    agent.TeamOutcome
		runErr error
	)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		out, runErr = svc.RunTeam(context.Background(), id, func(te agent.TeamEvent) {
			if te.Member == "worker" && te.Event.Type == session.EvResult {
				once.Do(func() { close(workerDone) })
			}
		})
	}()

	select {
	case <-park.started: // the lead is parked: the team stays in the running phase
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the lead to park")
	}
	select {
	case <-workerDone: // the worker's drive has genuinely finished
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the worker to finish")
	}

	// Already-finished-but-present member → nil success, a benign no-op.
	if err := svc.CancelTeammate(ctx, id, "worker"); err != nil {
		t.Errorf("CancelTeammate(finished worker): err = %v, want nil (honest no-op)", err)
	}

	// Unwedge: cancel the parked lead so the run finishes.
	if err := svc.CancelTeammate(ctx, id, "lead"); err != nil {
		t.Fatalf("CancelTeammate(lead): %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("RunTeam did not finish after cancelling the parked lead")
	}
	// The run must still produce an outcome — a cancelled lead must not wedge or
	// error the synthesis path (it degrades, never disappears).
	if runErr != nil {
		t.Fatalf("RunTeam after cancelling the parked lead: %v", runErr)
	}
	if len(out.Members) != 2 {
		t.Fatalf("RunTeam outcome must still carry both member dispositions, got %+v", out.Members)
	}
}

// TestCancelTeammatePhaseGates pins the not-running phase gate on both sides of a
// run, plus the unknown-team lookup, at the Service AND gRPC layers: a created
// (never run) team and a done (RunTeam returned) team both reject with
// ErrTeamNotRunning → FailedPrecondition; an unknown team id is ErrTeamNotFound
// → NotFound.
func TestCancelTeammatePhaseGates(t *testing.T) {
	svc := teamService(t, mockllm.New(
		mockllm.TextTurn("delegating"), mockllm.TextTurn("report"),
	))
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	id, _, err := svc.CreateTeam(ctx, "/ws", "test", "", 0, []agent.MemberSpec{
		{Name: "lead", Lead: true, InitialPrompt: "go"},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	// teamCreated (created, never run) → ErrTeamNotRunning / FailedPrecondition.
	if err := svc.CancelTeammate(ctx, id, "lead"); !errors.Is(err, server.ErrTeamNotRunning) {
		t.Errorf("CancelTeammate(created team): err = %v, want ErrTeamNotRunning", err)
	}
	_, gerr := h.CancelTeammate(ctx, &mecatlv1.CancelTeammateRequest{TeamId: id, Member: "lead"})
	wantFailedPrecondition(t, gerr, "CancelTeammate(created team)")

	if _, err := svc.RunTeam(ctx, id, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}

	// teamDone (after RunTeam returned) → ErrTeamNotRunning / FailedPrecondition.
	if err := svc.CancelTeammate(ctx, id, "lead"); !errors.Is(err, server.ErrTeamNotRunning) {
		t.Errorf("CancelTeammate(done team): err = %v, want ErrTeamNotRunning", err)
	}
	_, gerr = h.CancelTeammate(ctx, &mecatlv1.CancelTeammateRequest{TeamId: id, Member: "lead"})
	wantFailedPrecondition(t, gerr, "CancelTeammate(done team)")

	// Unknown team → ErrTeamNotFound / NotFound.
	if err := svc.CancelTeammate(ctx, "team-nope", "lead"); !errors.Is(err, server.ErrTeamNotFound) {
		t.Errorf("CancelTeammate(unknown team): err = %v, want ErrTeamNotFound", err)
	}
	_, gerr = h.CancelTeammate(ctx, &mecatlv1.CancelTeammateRequest{TeamId: "team-nope", Member: "lead"})
	if status.Code(gerr) != codes.NotFound {
		t.Errorf("CancelTeammate(unknown team) gRPC: code = %v, want NotFound (err=%v)", status.Code(gerr), gerr)
	}
}

// TestGRPCCancelTeammateArgGuards mirrors the SpawnTeammate/SendTeammateMessage
// arg-guard tests: an empty team_id or member is rejected with InvalidArgument
// before the service is consulted.
func TestGRPCCancelTeammateArgGuards(t *testing.T) {
	svc := teamService(t, mockllm.New(mockllm.TextTurn("x")))
	h := server.NewHarnessServer(svc)
	for _, req := range []*mecatlv1.CancelTeammateRequest{
		{TeamId: "", Member: "lead"},
		{TeamId: "team-x", Member: ""},
	} {
		_, err := h.CancelTeammate(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("CancelTeammate(%+v): code = %v, want InvalidArgument (err=%v)", req, status.Code(err), err)
		}
	}
}

// TestHTTPCancelTeammate pins the REST mirror (POST /v1/teams/{id}/members/cancel,
// member in the JSON body matching the /messages idiom): bad body → 400, missing
// member → 400, unknown team → 404, not-yet-running team → 412, and the success
// path → 204 with the run completing after a mid-round cancel of the parked worker.
func TestHTTPCancelTeammate(t *testing.T) {
	park := newParkTool()
	svc, _ := cancelTeammateService(t, parkedWorkerProviders(), park)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	ctx := context.Background()

	id, _, err := svc.CreateTeam(ctx, "/ws", "test", "fix the bug", 0, parkedRoster())
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	post := func(path, body string) int {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := post("/v1/teams/"+id+"/members/cancel", "{not json"); got != http.StatusBadRequest {
		t.Fatalf("bad body: status = %d, want 400", got)
	}
	if got := post("/v1/teams/"+id+"/members/cancel", `{}`); got != http.StatusBadRequest {
		t.Fatalf("missing member: status = %d, want 400", got)
	}
	if got := post("/v1/teams/team-nope/members/cancel", `{"member":"worker"}`); got != http.StatusNotFound {
		t.Fatalf("unknown team: status = %d, want 404", got)
	}
	// Created but never run → 412 (ErrTeamNotRunning).
	if got := post("/v1/teams/"+id+"/members/cancel", `{"member":"worker"}`); got != http.StatusPreconditionFailed {
		t.Fatalf("not-running team: status = %d, want 412", got)
	}

	// Success path: cancel the parked worker mid-run → 204, and the run completes.
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = svc.RunTeam(context.Background(), id, func(agent.TeamEvent) {})
	}()
	select {
	case <-park.started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the worker to park")
	}
	// Unknown member on the live team → 404 (ErrChildNotFound).
	if got := post("/v1/teams/"+id+"/members/cancel", `{"member":"nobody"}`); got != http.StatusNotFound {
		t.Fatalf("unknown member: status = %d, want 404", got)
	}
	if got := post("/v1/teams/"+id+"/members/cancel", `{"member":"worker"}`); got != http.StatusNoContent {
		t.Fatalf("cancel parked worker: status = %d, want 204", got)
	}
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("RunTeam did not finish after the HTTP member cancel")
	}
}
