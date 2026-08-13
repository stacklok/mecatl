package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// ErrTeamsDisabled is returned by the team methods when no MemberEngine is wired
// in Config — agent teams are an opt-in capability the composition root enables.
var ErrTeamsDisabled = errors.New("server: agent teams are not enabled")

// ErrTeamRunning is returned when an operation is rejected because the team is
// already running: a second concurrent RunTeam, a SpawnTeammate after the team has
// started, or a CleanupTeam on a still-running team. It maps to
// codes.FailedPrecondition.
var ErrTeamRunning = errors.New("server: team is already running")

// ErrTeamNotRunning is returned when an operation requires a RUNNING team but the
// team's phase is teamCreated (created, never run) or teamDone (RunTeam already
// returned) — a CancelTeammate has no in-flight run to reach into. It maps to
// codes.FailedPrecondition.
var ErrTeamNotRunning = errors.New("server: team is not running")

// ErrTooManyTeams is returned by CreateTeam when the live-team registry is already
// at Config.MaxTeams. It bounds the leak from teams created but never cleaned up.
// It maps to codes.ResourceExhausted.
var ErrTooManyTeams = errors.New("server: too many live teams")

// teamPhase is a team's lifecycle phase in the registry, guarded by Service.mu. A
// team is created on CreateTeam, transitions atomically to running on RunTeam
// (rejecting a second RunTeam and any SpawnTeammate), and to done when RunTeam
// returns. The phase serialises access to the Supervisor's unsynchronised
// members/order maps: only one RunTeam may drive a team, and no member may be
// spawned once driving has begun.
type teamPhase int

const (
	teamCreated teamPhase = iota
	teamRunning
	teamDone
)

// MemberEngineFactory builds a team member's engine (and its optional per-member
// permission mode), binding it to the shared team (so the member's catalog includes
// that team's coordination tools) and shaping it from the member spec (read-only
// base vs mutating tools, the member's agent definition). The composition root
// supplies it via Config.MemberEngine; CreateTeam adapts it to an agent.MemberEngine
// by capturing the per-team aggregate. Returning agent.MemberBuild (rather than a
// bare *Engine) is how a def's permissionMode reaches the supervisor's per-member
// session.
//
// routedModel is the OPT-IN model router's classification (ADR 0034) — the
// ALREADY-RESOLVED concrete model id for an UNDEFINED member, "" otherwise (no router, a
// miss, or a DEFINED member whose def pins its own model). It is the same shape as
// agent.TeamMemberEngineFactory so one factory satisfies both. On the gRPC RunTeam path
// the supervisor runs ZERO-CAPS (no parent caps ⇒ no routeTask), so routedModel is always
// "" there and the member is built byte-identically to today.
type MemberEngineFactory func(t *team.Team, spec agent.MemberSpec, routedModel string) agent.MemberBuild

// teamState couples a team's shared coordination aggregate with the supervisor
// that drives it and the base workspace it was created over.
//
// Two locks guard a teamState, with DISTINCT jobs (do not collapse them):
//
//   - Service.mu guards the registry MAP (s.teams) and is the only lock taken to
//     read/write the `phase` field. It is held only briefly — never across the
//     fork I/O that AddMember performs.
//   - run is a PER-TEAM mutex serialising the two operations that drive the
//     Supervisor's UNSYNCHRONISED members/order maps against each other:
//     SpawnTeammate's AddMember (a writer) and RunTeam's start (a reader, via
//     sup.Run). Without it, SpawnTeammate's TOCTOU — it checks phase under
//     Service.mu, releases it, THEN calls AddMember — let a concurrent RunTeam win
//     the teamCreated→teamRunning transition in the gap and start iterating the
//     member maps while AddMember was writing them (a concurrent map write/iterate
//     panic). Both SpawnTeammate (across its phase-check AND AddMember) and RunTeam
//     (across its phase-check AND transition) hold `run`, so a spawn and a run-start
//     can no longer interleave. `run` is never held across the whole Supervisor.Run
//     (that would deadlock CleanupTeam-style introspection); RunTeam releases it the
//     instant the phase has flipped to teamRunning, after which the phase check in any
//     later SpawnTeammate rejects the spawn cleanly.
type teamState struct {
	team  *team.Team
	sup   *agent.Supervisor
	base  string
	phase teamPhase

	// run serialises a SpawnTeammate (AddMember writes the supervisor's member maps)
	// against a RunTeam start (sup.Run reads them). See the type doc above.
	run sync.Mutex
}

// CreateTeam allocates a new agent team over the given base workspace, enrols the
// optional initial roster, and returns its server-assigned id together with the
// enrolled roster. It builds the supervisor (binding the member-engine factory to
// the shared team), then adds each member before the team is registered.
//
// Enrolment is ATOMIC: if any member fails to enrol the whole team is abandoned —
// it is never registered (no partial team leaks) and does not consume a MaxTeams
// slot. The supervisor's AddMember already cleans up a member's own fork on its own
// failure, and an un-registered supervisor (with whatever members it did add) is
// simply garbage-collected. The failing member's error is classified via
// classifyAddMemberErr. members may be empty: the team is created empty and the
// client can still SpawnTeammate before RunTeam.
//
// maxTeamTokens is the per-request team-wide token budget, folded into the
// server-configured Config.TeamTokenBudget TIGHTEN-ONLY at create time
// (agent.TightenTeamTokenBudget, issue #36): a non-positive value inherits the
// server budget; a positive value applies only when it is lower.
//
// It returns ErrTeamsDisabled when teams are not enabled.
func (s *Service) CreateTeam(ctx context.Context, workspace, name, goal string, maxTeamTokens int, members []agent.MemberSpec) (string, []team.Member, error) {
	if s.cfg.MemberEngine == nil {
		return "", nil, ErrTeamsDisabled
	}
	if workspace == "" {
		return "", nil, fmt.Errorf("%w: workspace is required", ErrInvalidArgument)
	}

	t := team.New(name)
	baseWS := s.cfg.Workspaces(workspace)
	// Build the team's base Environment: bind the runner for the team's root
	// (the main runner when it's the launch root, a root-bound runner otherwise,
	// shell-less when no factory is wired for a differing root). The forker builds
	// its OWN runners for forked members, so this is the base-sharing member
	// runner only.
	var baseRunner tool.CommandRunner
	if workspace == s.cfg.DefaultWorkspace {
		baseRunner = s.cfg.CommandRunner
	} else if s.cfg.CommandRunnerFactory != nil {
		baseRunner = s.cfg.CommandRunnerFactory(workspace)
	}
	// The workspace comes from the client-controlled CreateTeam request, so it
	// MUST NOT panic on a nil return from the Workspaces factory (a misconfigured
	// factory, a bad root, etc.). NewEnvironment rejects a nil Workspace with a
	// normal error; wrap it as ErrInvalidArgument so the caller sees a bad-request
	// status rather than a server crash.
	base, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace}, baseWS, baseRunner)
	if err != nil {
		return "", nil, fmt.Errorf("%w: team workspace could not be built: %w", ErrInvalidArgument, err)
	}
	factory := func(spec agent.MemberSpec, routedModel string) agent.MemberBuild {
		return s.cfg.MemberEngine(t, spec, routedModel)
	}

	// Compute the team id FIRST (it needs only NewID, no dependency on the supervisor)
	// so the member-session prefix can namespace member ids by it — keeping the gRPC
	// path's stored ids collision-free across concurrent teams and aligned with
	// agent.MemberSessionID (which the inspect tool derives). Both prefixes spell the
	// exported agent.TeamSessionPrefix so the stored ids stay inside the engine's
	// id-minting convention (and thereby in scope for the child-session GC).
	id := agent.TeamSessionPrefix + string(s.cfg.NewID())

	opts := []agent.SupervisorOption{
		agent.WithTeamGoal(goal),
		agent.WithMemberSessionPrefix(agent.TeamSessionPrefix + id),
	}
	// The goal is the team's TRUSTED top-level instruction by default (the deployment
	// owns the gRPC front door, so the goal's provenance is the operator/principal,
	// not a peer). A multi-tenant / relay deployment that may interpolate untrusted
	// end-user text into the goal flips Config.TeamGoalUntrusted to re-fence it as
	// data. Peer messages and task descriptions stay fenced regardless.
	if s.cfg.TeamGoalUntrusted {
		opts = append(opts, agent.WithUntrustedGoal(true))
	}
	if s.cfg.Forker != nil {
		opts = append(opts, agent.WithForker(s.cfg.Forker))
	}
	if s.cfg.ReadOnlyForker != nil {
		opts = append(opts, agent.WithReadOnlyForker(s.cfg.ReadOnlyForker))
	}
	if s.cfg.SharedBaseWorkspace != nil {
		opts = append(opts, agent.WithTeamSharedBaseWorkspace(s.cfg.SharedBaseWorkspace))
	}
	if s.cfg.TeamHooks != nil {
		opts = append(opts, agent.WithTeamHooks(s.cfg.TeamHooks))
	}
	if s.cfg.Store != nil {
		opts = append(opts, agent.WithMemberStore(s.cfg.Store))
	}
	// Clamp the per-request budget against the server's ceiling at create time:
	// tighten-only, so the wire can never loosen the operator's bound.
	if budget := agent.TightenTeamTokenBudget(s.cfg.TeamTokenBudget, maxTeamTokens); budget > 0 {
		opts = append(opts, agent.WithTeamTokenBudget(budget))
	}
	// The gRPC RunTeam direct path deliberately runs with ZERO parent caps: no
	// surface-to-human seam AND no child-ask adjudicator (issue #31) — a member's
	// unresolved permission ask headless-auto-denies exactly as before. The in-loop
	// Team TOOL is the path that inherits the parent run's caps (surfacing and, when
	// configured, the automated reviewer).
	sup := agent.NewSupervisor(t, base, factory, opts...)

	// Enrol the initial roster BEFORE registering the team. A failure here abandons
	// the whole team: we return without inserting it into s.teams, so it neither leaks
	// nor counts against the cap, and the un-registered supervisor is GC'd.
	for _, spec := range members {
		if err := sup.AddMember(ctx, spec); err != nil {
			return "", nil, classifyAddMemberErr(err)
		}
	}

	s.mu.Lock()
	// Count only un-cleaned teams (the live registry) against the cap; CleanupTeam
	// frees a slot. The check and the insert share the lock so concurrent CreateTeams
	// cannot both slip past a full registry.
	if len(s.teams) >= s.cfg.MaxTeams {
		s.mu.Unlock()
		return "", nil, fmt.Errorf("%w: %d", ErrTooManyTeams, s.cfg.MaxTeams)
	}
	s.teams[id] = &teamState{team: t, sup: sup, base: workspace}
	s.mu.Unlock()
	return id, t.Members(), nil
}

// lookupTeam returns the registered team state for id, or ErrNotFound.
func (s *Service) lookupTeam(id string) (*teamState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.teams[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrTeamNotFound, id)
	}
	return ts, nil
}

// SpawnTeammate enrols a member in a team (before RunTeam) and returns its roster
// entry. A Mutating member requires a configured Forker. It is rejected with
// ErrTeamRunning once the team has started running: AddMember mutates the
// Supervisor's unsynchronised member maps, which the in-flight RunTeam is reading.
func (s *Service) SpawnTeammate(ctx context.Context, teamID string, spec agent.MemberSpec) (team.Member, error) {
	ts, err := s.lookupTeam(teamID)
	if err != nil {
		return team.Member{}, err
	}
	// Hold the per-team `run` mutex across BOTH the phase check AND the AddMember
	// call, so a concurrent RunTeam (which also takes `run` to flip the phase) cannot
	// start sup.Run in the gap and iterate the supervisor's member maps while
	// AddMember is writing them. Service.mu is taken only to READ the phase, briefly,
	// and released before AddMember's fork I/O (we must not hold the global registry
	// lock across that). The ordering is `run` then `s.mu` here and in RunTeam, so the
	// two never deadlock.
	ts.run.Lock()
	defer ts.run.Unlock()

	s.mu.Lock()
	started := ts.phase != teamCreated
	s.mu.Unlock()
	if started {
		return team.Member{}, fmt.Errorf("%w: cannot spawn into a team that has started", ErrTeamRunning)
	}
	if err := ts.sup.AddMember(ctx, spec); err != nil {
		return team.Member{}, classifyAddMemberErr(err)
	}
	for _, m := range ts.team.Members() {
		if m.Name == spec.Name {
			return m, nil
		}
	}
	return team.Member{}, fmt.Errorf("%w: member %q not found after spawn", ErrInternal, spec.Name)
}

// classifyAddMemberErr maps a Supervisor.AddMember failure to the server sentinel
// whose wire status fits the failure CLASS, instead of collapsing every failure to
// InvalidArgument (finding J). The original error message is preserved by wrapping
// it with %v so the model/operator still sees the detail.
//
//   - Bad client request (the caller can fix it by changing the spawn args):
//     empty/duplicate/reserved name, roster full → InvalidArgument.
//   - Server misconfiguration (well-formed request, but the harness is wired wrong;
//     the client cannot fix it): a Mutating member with no forker, or a read-only
//     member handed a workspace-mutating tool → FailedPrecondition.
//   - Server-internal fault: workspace fork I/O failure, or a factory that returned
//     a nil Engine → Internal.
func classifyAddMemberErr(err error) error {
	switch {
	case errors.Is(err, agent.ErrMemberNameRequired),
		errors.Is(err, agent.ErrMemberAlreadyAdded),
		errors.Is(err, team.ErrMemberExists),
		errors.Is(err, team.ErrReservedName),
		errors.Is(err, team.ErrTooManyMembers):
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	case errors.Is(err, agent.ErrNoForker),
		errors.Is(err, agent.ErrReadOnlyShellNoForker),
		errors.Is(err, agent.ErrReadOnlyMemberMutating):
		return fmt.Errorf("%w: %v", ErrFailedPrecondition, err)
	case errors.Is(err, agent.ErrForkWorkspace),
		errors.Is(err, agent.ErrNilEngine):
		return fmt.Errorf("%w: %v", ErrInternal, err)
	default:
		// Unknown failure class: treat as a bad request, preserving the historical
		// default rather than masking it as a server fault.
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
}

// SendTeammateMessage posts a message into a member's inbox, delivered at that
// member's next turn boundary. An empty from defaults to the reserved operator
// identity (team.OperatorSender); a non-empty from is authenticated by team.Send,
// which rejects any value that is neither a current member nor the operator
// (ErrUnknownSender → InvalidArgument) so the wire path cannot forge a sender.
func (s *Service) SendTeammateMessage(_ context.Context, teamID, from, to, body string) error {
	ts, err := s.lookupTeam(teamID)
	if err != nil {
		return err
	}
	if from == "" {
		from = team.OperatorSender
	}
	if err := ts.team.Send(from, to, body); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	return nil
}

// CancelTeammate cancels ONE member of a RUNNING team mid-round, via the
// Supervisor.CancelMember seam (the same per-member cancel the Converse-path
// registry route fires; BACKGROUND-SUBAGENTS D4, issue #29). The member
// de-schedules with the cancelled stop reason and its claimed tasks release; the
// run continues and still delivers its outcome.
//
// The phase is read briefly under Service.mu and released BEFORE touching the
// supervisor — the same no-lock-across-supervisor discipline as
// SendTeammateMessage. The teamRunning requirement is ALSO what makes the
// lock-free CancelMember map read safe: the supervisor's members map is written
// only by AddMember, and every AddMember serialises BEFORE the
// teamCreated→teamRunning flip (SpawnTeammate holds the per-team `run` mutex
// across its phase check + AddMember and rejects once the team has started) —
// admitting a teamCreated team here would race a concurrent SpawnTeammate's map
// write. The race on the OTHER side of the gate is benign: RunTeam may finish
// between the phase read and CancelMember, in which case the late cancel lands
// as an idempotent no-op (the member's ctx just goes unobserved). Cancelling an
// already-stopped-but-PRESENT member is likewise an honest no-op (nil success);
// only an unknown member name returns ErrChildNotFound. A team that is not
// running (teamCreated / teamDone) returns ErrTeamNotRunning.
func (s *Service) CancelTeammate(_ context.Context, teamID, member string) error {
	ts, err := s.lookupTeam(teamID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	phase := ts.phase
	s.mu.Unlock()
	if phase != teamRunning {
		return fmt.Errorf("%w: %q", ErrTeamNotRunning, teamID)
	}
	if !ts.sup.CancelMember(member) {
		return fmt.Errorf("%w: %q", ErrChildNotFound, member)
	}
	return nil
}

// RunTeam drives the team to quiescence, invoking sink for every member event,
// and returns the outcome. It blocks for the team's lifetime; the gRPC handler
// runs it on the request goroutine and forwards events to the stream.
//
// Like the per-session run-entry funnel (acquireLease), RunTeam honours the
// drain gate (ADR 0048, mecak8s): once Drain is armed a draining replica
// refuses a NEW team run BEFORE the phase flip / team claim so a shutting-down
// pod steers team traffic to a survivor. An in-flight RunTeam is NOT cancelled
// by Drain itself (that is the bounded GracefulStop's job). The gate starts
// false — byte-identical default when Drain has not been called.
func (s *Service) RunTeam(ctx context.Context, teamID string, sink func(agent.TeamEvent)) (agent.TeamOutcome, error) {
	// Drain gate (ADR 0048, mecak8s): refuse new team runs on a draining
	// replica before claiming the team — mirrors acquireLease's check.
	if s.draining.Load() {
		return agent.TeamOutcome{}, fmt.Errorf("%w: %q", ErrUnavailable, teamID)
	}
	ts, err := s.lookupTeam(teamID)
	if err != nil {
		return agent.TeamOutcome{}, err
	}
	// Atomically claim the team for this run. A second concurrent RunTeam (or one
	// after a completed run) is rejected — the Supervisor's member state is not safe
	// to drive twice. The transition is guarded by Service.mu so that of two callers
	// racing to claim, exactly one wins. The per-team `run` mutex is ALSO held across
	// the check+transition so an in-flight SpawnTeammate (which holds `run` across its
	// own AddMember) cannot be mid-write to the supervisor's member maps when we flip
	// to teamRunning and hand them to sup.Run. We release `run` the instant the phase
	// has flipped — NOT across sup.Run itself — so a spawn that arrives after the flip
	// sees teamRunning and is rejected cleanly, with no map race. Lock order is `run`
	// then `s.mu`, matching SpawnTeammate, so the two cannot deadlock.
	ts.run.Lock()
	s.mu.Lock()
	if ts.phase != teamCreated {
		s.mu.Unlock()
		ts.run.Unlock()
		return agent.TeamOutcome{}, fmt.Errorf("%w: %q", ErrTeamRunning, teamID)
	}
	ts.phase = teamRunning
	s.mu.Unlock()
	ts.run.Unlock()

	defer func() {
		s.mu.Lock()
		ts.phase = teamDone
		s.mu.Unlock()
	}()
	return ts.sup.Run(ctx, sink), nil
}

// ListTeam returns the team roster, the shared task list, and whether the team
// has reached quiescence.
func (s *Service) ListTeam(_ context.Context, teamID string) ([]team.Member, []team.Task, bool, error) {
	ts, err := s.lookupTeam(teamID)
	if err != nil {
		return nil, nil, false, err
	}
	return ts.team.Members(), ts.team.Tasks(), ts.team.Quiescent(), nil
}

// CleanupTeam drops a created or done team from the registry and frees its slot.
// Forked member workspaces are torn down by the supervisor when RunTeam returns, so
// a done team is safe to drop; this releases the registry entry. A team whose phase
// is teamRunning is NOT dropped — deleting it out from under the in-flight RunTeam
// would orphan the live supervisor — so it returns ErrTeamRunning
// (FailedPrecondition) instead. It returns ErrNotFound for an unknown team.
func (s *Service) CleanupTeam(_ context.Context, teamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.teams[teamID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrTeamNotFound, teamID)
	}
	if ts.phase == teamRunning {
		return fmt.Errorf("%w: %q", ErrTeamRunning, teamID)
	}
	delete(s.teams, teamID)
	return nil
}
