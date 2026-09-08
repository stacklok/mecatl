package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
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
	owner *session.Principal
	phase teamPhase

	// run serialises a SpawnTeammate (AddMember writes the supervisor's member maps)
	// against a RunTeam start (sup.Run reads them). See the type doc above.
	run sync.Mutex
}

// CreateTeamOnDefaultPlacement allocates a new agent team over the trusted default
// placement, enrols an optional initial roster, and returns its server-assigned
// id together with the enrolled roster. It builds the supervisor (binding the member-engine factory to
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
// CreateTeamOnDefaultPlacement is the explicit trusted-composition entry for a
// team that is not derived from an existing session.
func (s *Service) CreateTeamOnDefaultPlacement(ctx context.Context, name, goal string, maxTeamTokens int, members []agent.MemberSpec) (string, []team.Member, error) {
	if s.cfg.MemberEngine == nil {
		return "", nil, ErrTeamsDisabled
	}
	binding, err := s.BindPlacement(ctx, DefaultPlacement(), PlacementOperationCreate)
	if err != nil {
		return "", nil, err
	}
	if binding.Close != nil {
		defer func() { _ = binding.Close() }()
	}
	return s.createTeamInEnvironment(ctx, binding.Environment, name, goal, maxTeamTokens, members)
}

// CreateTeamForSession creates a team in an owning session's exact authorized
// environment. The caller supplies no path or selector; no-FS is never upgraded.
func (s *Service) CreateTeamForSession(ctx context.Context, source session.SessionID, name, goal string, maxTeamTokens int, members []agent.MemberSpec) (string, []team.Member, error) {
	if source == "" {
		return "", nil, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	_, env, err := s.ownedSessionEnvironment(ctx, source)
	if err != nil {
		return "", nil, err
	}
	if env.Workspace() == nil || env.Ref().Kind == session.EnvKindNoFS {
		return "", nil, fmt.Errorf("%w: session has no filesystem placement", ErrFailedPrecondition)
	}
	return s.createTeamInEnvironment(ctx, env, name, goal, maxTeamTokens, members)
}

func (s *Service) createTeamInEnvironment(ctx context.Context, base tool.Environment, name, goal string, maxTeamTokens int, members []agent.MemberSpec) (string, []team.Member, error) {
	if s.cfg.MemberEngine == nil {
		return "", nil, ErrTeamsDisabled
	}
	workspace := base.Workspace().Root()

	t := team.New(name)
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
	if s.cfg.RootAuthority != nil {
		opts = append(opts, agent.WithRootAuthority(s.cfg.RootAuthority(session.SessionKindTeamMember)))
	}
	// Attribute members to the creating caller. CreateTeam runs with zero parent
	// caps by design, so without this AddMember publishes durable member sessions
	// with Owner == nil — unreadable by the team's own owner, and skipped by every
	// retention path, so they can never be reaped. Appended unconditionally:
	// WithTeamOwner no-ops on a nil principal, which is the ownerless path.
	opts = append(opts, agent.WithTeamOwner(session.PrincipalFromContext(ctx)))
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
	opts = append(opts, agent.WithTeamReadLedgerFactory(func() tool.ReadLedger { return memledger.New() }))
	if s.cfg.SharedBaseWorkspace != nil {
		opts = append(opts, agent.WithTeamSharedBaseWorkspace(s.cfg.SharedBaseWorkspace))
	}
	if s.cfg.TeamHooks != nil {
		opts = append(opts, agent.WithTeamHooks(s.cfg.TeamHooks))
	}
	if s.cfg.Store != nil {
		opts = append(opts, agent.WithMemberStore(s.cfg.Store))
	}
	if s.cfg.SessionLiveness != nil {
		opts = append(opts, agent.WithMemberLiveness(s.cfg.SessionLiveness))
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

	// Claim a registry slot BEFORE enrolling. Enrolment acquires each member's
	// cross-process lease and publishes a durable member snapshot, so checking the
	// cap afterwards would refuse the request having already stranded both: leases
	// no other replica can take until expiry, and member records no owner can reach
	// and no retention path will reap. The reservation is released on every failure
	// path below and converted to a registration on success.
	s.mu.Lock()
	if len(s.teams)+s.teamsReserving >= s.cfg.MaxTeams {
		s.mu.Unlock()
		return "", nil, fmt.Errorf("%w: %d", ErrTooManyTeams, s.cfg.MaxTeams)
	}
	s.teamsReserving++
	s.mu.Unlock()
	registered := false
	defer func() {
		if !registered {
			s.mu.Lock()
			s.teamsReserving--
			s.mu.Unlock()
		}
	}()

	// Enrol the initial roster BEFORE registering the team. A failure here abandons
	// the whole team: the reservation is dropped, every acquired lease is released,
	// every published member snapshot is deleted, and the un-registered supervisor
	// is GC'd — so a refused create leaves nothing behind.
	//
	// The member's cross-process lease is acquired BEFORE AddMember, not after and
	// not in RunTeam: AddMember publishes a durable SessionKindTeamMember snapshot,
	// which is retention-eligible, so a lease taken afterwards leaves a window in
	// which a peer replica can delete a live team's member — unbounded, and infinite
	// for a team that is never run. Leasing first also means a refused lease writes
	// nothing, so the abandon path leaves no orphaned member snapshot behind.
	// Member ids are derivable here because the team id was computed above, before
	// the supervisor. RunTeam's own acquire loop stays as a no-op backstop
	// (acquireLease is idempotent for an id this service already holds).
	if err := s.enrolInitialRoster(ctx, id, sup, members); err != nil {
		return "", nil, err
	}

	s.mu.Lock()
	// The slot was claimed above, so no capacity refusal can occur here — the cap
	// is enforced before any lease or durable write.
	s.teams[id] = &teamState{team: t, sup: sup, base: workspace, owner: session.PrincipalFromContext(ctx).Clone()}
	s.teamsReserving--
	registered = true
	s.mu.Unlock()
	return id, t.Members(), nil
}

// enrolInitialRoster adds every initial member, leaving NOTHING behind on
// failure: each member's cross-process lease is acquired before AddMember
// publishes its durable snapshot, and any failure releases the leases and
// deletes the snapshots taken so far. Extracted from CreateTeam to keep it under
// the gocyclo threshold, the same reason createPerSessionEngine was split out of
// createSession. The returned error is already classified for the wire.
func (s *Service) enrolInitialRoster(ctx context.Context, teamID string, sup *agent.Supervisor, members []agent.MemberSpec) error {
	leased := make([]session.SessionID, 0, len(members))
	published := make([]session.SessionID, 0, len(members))
	unwind := func() {
		// Published snapshots first, then leases: the lease is what stops a peer
		// replica touching the record, so it is released last.
		s.deleteAbandonedMembers(ctx, published)
		for _, id := range leased {
			s.releaseLease(id)
		}
	}
	for _, spec := range members {
		memberID := agent.MemberSessionID(teamID, spec.Name)
		if err := s.acquireLease(ctx, memberID); err != nil {
			unwind()
			return err
		}
		leased = append(leased, memberID)
		if err := sup.AddMember(ctx, spec); err != nil {
			unwind()
			return classifyAddMemberErr(err)
		}
		published = append(published, memberID)
	}
	return nil
}

// deleteAbandonedMembers removes the durable member snapshots an abandoned
// CreateTeam already published. Best-effort and deliberately quiet: the caller
// is already returning the failure that matters, and a store that cannot prune
// (no port.PrunableStore) simply leaves the records for retention — which can
// reach them, because they carry the creating caller's owner.
//
// It runs while this service still holds each member's lease, so no peer replica
// can be mid-flight on the same id.
func (s *Service) deleteAbandonedMembers(ctx context.Context, ids []session.SessionID) {
	if len(ids) == 0 {
		return
	}
	prunable, ok := s.cfg.Store.(port.PrunableStore)
	if !ok {
		return
	}
	for _, id := range ids {
		if !s.mutationLeaseHeld(id) {
			continue
		}
		if err := s.deleteSessionFamily(ctx, id, prunable); err != nil && !errors.Is(err, port.ErrSessionNotFound) {
			s.cfg.Diagnostics.Log(ctx, port.LevelWarn,
				"abandoned team member snapshot could not be deleted; left for retention",
				"session", string(id), "err", err.Error())
		}
	}
}

// lookupTeam returns the registered team state for id, or ErrNotFound.
func (s *Service) lookupTeam(ctx context.Context, id string) (*teamState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, ok := s.teams[id]
	if !ok || !s.ownsResource(ctx, ts.owner) {
		return nil, fmt.Errorf("%w: %q", ErrTeamNotFound, id)
	}
	return ts, nil
}

// SpawnTeammate enrols a member in a team (before RunTeam) and returns its roster
// entry. A Mutating member requires a configured Forker. It is rejected with
// ErrTeamRunning once the team has started running: AddMember mutates the
// Supervisor's unsynchronised member maps, which the in-flight RunTeam is reading.
func (s *Service) SpawnTeammate(ctx context.Context, teamID string, spec agent.MemberSpec) (team.Member, error) {
	ts, err := s.lookupTeam(ctx, teamID)
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
	// Lease before AddMember publishes the durable member snapshot — same ordering
	// and same reason as CreateTeam's enrolment loop above.
	memberID := agent.MemberSessionID(teamID, spec.Name)
	if err := s.acquireLease(ctx, memberID); err != nil {
		return team.Member{}, err
	}
	if err := ts.sup.AddMember(ctx, spec); err != nil {
		s.releaseLease(memberID)
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
func (s *Service) SendTeammateMessage(ctx context.Context, teamID, from, to, body string) error {
	ts, err := s.lookupTeam(ctx, teamID)
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
func (s *Service) CancelTeammate(ctx context.Context, teamID, member string) error {
	ts, err := s.lookupTeam(ctx, teamID)
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
	ts, err := s.lookupTeam(ctx, teamID)
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

	// Team members are durable sessions driven outside StartRunContent, so their
	// cross-process ownership must be established here before Supervisor.Run
	// starts their engines. acquireLease is idempotent for a session already held
	// by this service and keeps the hold until CloseSession or service shutdown.
	for _, member := range ts.team.Members() {
		memberID := member.Session
		if memberID == "" {
			memberID = agent.MemberSessionID(teamID, member.Name)
		}
		if err := s.acquireLease(ctx, memberID); err != nil {
			s.mu.Lock()
			ts.phase = teamCreated
			s.mu.Unlock()
			return agent.TeamOutcome{}, err
		}
	}

	defer func() {
		s.mu.Lock()
		ts.phase = teamDone
		s.mu.Unlock()
	}()
	return ts.sup.Run(ctx, sink), nil
}

// ListTeam returns the team roster, the shared task list, and whether the team
// has reached quiescence.
func (s *Service) ListTeam(ctx context.Context, teamID string) ([]team.Member, []team.Task, bool, error) {
	ts, err := s.lookupTeam(ctx, teamID)
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
func (s *Service) CleanupTeam(ctx context.Context, teamID string) error {
	s.mu.Lock()
	ts, ok := s.teams[teamID]
	if !ok || !s.ownsResource(ctx, ts.owner) {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrTeamNotFound, teamID)
	}
	if ts.phase == teamRunning {
		s.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrTeamRunning, teamID)
	}
	delete(s.teams, teamID)
	s.mu.Unlock()

	// Release every member lease this team acquired. Without this the lease and its
	// renewer goroutine outlive the team and are only reclaimed at process exit, so a
	// long-lived server that churns teams accumulates both, and the member ids stay
	// claimed against peer replicas that could legitimately take them over. The phase
	// check above guarantees the team is not running, so no member drive can still
	// need its lease, and releaseLease is a no-op for an id not held.
	//
	// This runs OUTSIDE s.mu deliberately: releaseLease re-takes it, and a
	// sync.Mutex is not reentrant. Team.Members takes the team's own lock, not
	// s.mu, so reading the roster here is safe.
	for _, member := range ts.team.Members() {
		memberID := member.Session
		if memberID == "" {
			memberID = agent.MemberSessionID(teamID, member.Name)
		}
		s.releaseLease(memberID)
	}
	return nil
}
