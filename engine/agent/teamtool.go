package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// teamToolName is the catalog name of the team-formation tool.
const teamToolName = "Team"

// maxTeamPreview caps every member tool-call/result preview (TeamPayload.Detail)
// and message text forwarded on a team.member event. A team is meant to be
// WATCHED — so unlike the metadata-only subagent projection, member content IS
// forwarded — but it is forwarded BOUNDED: an unbounded args blob or result body
// can never be copied verbatim onto the parent's event stream. The cap is
// rune-aware (see clampPreview) so it never splits a multi-byte character.
//
// It has TWO consumers outside the team, both MODEL-facing rather than event-facing:
// subagentErrorBody's demoted "Last activity before the failure:" half and
// digestChildActivity's recovered-output digest (both in subagent.go) bound one preview's
// worth of a child's own prose with it. So tuning this number for a team-event reason also
// moves the clamp on the subagent failure body that lands in the PARENT's persisted
// conversation — where the parent re-pays for every rune on every later turn.
const maxTeamPreview = 200

// TeamMemberEngineFactory builds a team member's engine (as a MemberBuild carrying
// the constructed *Engine plus the member's optional per-member permission mode)
// from the shared team and the member spec. It is the SINGLE canonical factory shape
// consumed by every team entry point — the gRPC CreateTeam path
// (server.Config.MemberEngine) AND the Team tool — so the per-member engine wiring
// lives in one place (internal/app) and cannot drift between the two paths. The
// composition root supplies it; it is expected to capture nothing (the team is
// passed per-call) and to shape the member's catalog per the three-tier workspace
// policy (a base-sharing read-only member must NOT be handed workspace-mutating
// tools; a read-only-isolated member may have Bash and sets IsolateReadOnly; a
// Mutating member may have Edit/Write/Bash) plus the team coordination tools
// (MemberTools). Returning a MemberBuild (rather than a bare *Engine) is how a
// member's agent-definition permissionMode reaches the supervisor's per-member
// session — it is the exact shape server.MemberEngineFactory has, so one factory
// serves both paths.
//
// routedModel is the OPT-IN model router's classification (ADR 0034) — the
// ALREADY-RESOLVED concrete model id for an UNDEFINED member, "" otherwise. The factory
// substitutes it for the default child model on the undefined branch only; a DEFINED
// member's factory ignores it (its def pins the model). The supervisor owns the route
// decision (it holds the parent caps) and threads the result through.
type TeamMemberEngineFactory func(t *team.Team, spec MemberSpec, routedModel string) MemberBuild

// TeamMemberArg is one roster entry the model supplies in a Team call. It maps
// 1:1 onto MemberSpec: Name → Name, Role → InitialPrompt, Mutating → Mutating.
type TeamMemberArg struct {
	// Name is the member's unique handle peers address messages to.
	Name string `json:"name"`
	// Role is the member's role briefing — its first-turn instruction. It becomes
	// the member's InitialPrompt.
	Role string `json:"role"`
	// Mutating requests a self-contained copied workspace (own `.git`) with
	// edit/write/shell tools (Edit/Write/Bash). A read-only member (the default,
	// false) runs in an isolated throwaway git worktree with full shell for
	// INSPECTION (git log/show, cat, build, test) but no Edit/Write. Neither tier is
	// merged back into the base.
	Mutating bool `json:"mutating,omitempty"`
}

// teamArgs is the argument payload the model supplies when calling the Team tool.
// The MODEL specifies the roster: the first member is synthesized as the
// coordinating lead.
type teamArgs struct {
	// Goal is the team's top-level objective. It is threaded into the supervisor
	// (WithTeamGoal) and rendered as the team's TRUSTED top-level instruction into
	// every member's round-0 turn and into the lead's synthesis prompt — its
	// provenance is the principal (the parent model authored it from the user's own
	// prompt inside the running session), never a peer.
	Goal string `json:"goal"`
	// Members is the roster the model formed. The first member is the lead.
	Members []TeamMemberArg `json:"members"`
	// MaxTeamTokens is an OPTIONAL per-call team-wide token budget. It is TIGHTEN-ONLY
	// (tightenLimit semantics, like subagentArgs.MaxTurns): it may only lower a
	// server-configured budget, never raise it. Omit or 0 to use the server default.
	MaxTeamTokens *int `json:"max_team_tokens,omitempty"`
}

// teamSchema is the JSON schema the model sees for the Team tool's arguments.
var teamSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "goal": {
      "type": "string",
      "description": "The team's top-level objective, shown to every member as their instruction. Be specific: state the desired outcome and any constraints — a vague goal produces a vague report."
    },
    "members": {
      "type": "array",
      "minItems": 1,
      "description": "The roster. The FIRST member is the coordinating lead. Each member runs as a long-lived subagent coordinating via a shared task list and mailbox.",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string", "description": "Unique member handle peers address messages to."},
          "role": {"type": "string", "description": "The member's role briefing — its first-turn instruction. Lead example: 'Break the goal into tasks, track progress, and synthesise a final report that answers X'. Worker example: 'Investigate the auth code path; RecordFinding each conclusion; message the lead when done'."},
          "mutating": {"type": "boolean", "description": "True if the member needs to edit/write files (runs in a self-contained copied workspace with edit/write/shell). False (default) runs read-only in an isolated throwaway git worktree with full shell for INSPECTION (git log/show, cat, build, test) but no Edit/Write. Neither is merged back."}
        },
        "required": ["name", "role"]
      }
    },
    "max_team_tokens": {"type":"integer","description":"Optional team-wide token budget (input+output summed across all members and rounds). When crossed, no further round is scheduled; the in-flight round and the lead's synthesis still complete and the report states the budget stop. May only TIGHTEN a server-configured budget, never raise it. Omit or 0 to use the server default."}
  },
  "required": ["goal", "members"]
}`)

// TeamTool forms a team of coordinating subagents in-process (see
// teamsupervisor.go). When executed it builds a fresh team.Team, enrols the
// model-supplied roster (synthesizing the first member as the lead), drives the
// existing Supervisor to quiescence over the SAME base workspace as the parent,
// and returns the team's deliverable as a single ToolResult. The deliverable resolves
// through the three-tier deliverable() chain: (1) the lead's consolidated synthesis when
// it is a usable report (non-empty AND not a non-deliverable — isNonDeliverable rejects a
// bare refusal over a populated ledger), (2) a ledger-rich structured fallback (findings
// grouped by member, per-member disposition/reason/completed-tasks/last-text), (3) an
// honest floor when even the ledger is empty. A non-convergence header is prepended when
// the team did NOT converge. The ToolResult is therefore NEVER a bare refusal or empty.
//
// It is the team analogue of SubagentTool/ParallelTool, with two deliberate differences:
//
//   - ReadOnly() == false. A team spawns Mutating members and is long-lived and
//     stateful, so the dispatcher must SERIALISE it (mutate-serial) rather than
//     run it read-parallel like Subagent. (Mutating members run in isolated forks, but
//     the supervisor's member maps are not safe to drive alongside other tools.)
//   - Member activity is OBSERVABLE and fuller. Via the observableTool seam, each
//     member event is projected to the parent run's stream as a team.member event
//     carrying the member's message text and BOUNDED tool previews — a team is
//     meant to be watched. permission.ask is dropped; every preview is capped.
//
// Context isolation holds exactly as for Subagent/Fork: the per-member transcripts are
// never written to the parent Session's Conversation. Only the lead's synthesis
// (the ToolResult) folds back, so the LLM's context stays summary-only. (The PULL
// InspectMember tool may later pull ONE member's transcript on the parent's
// deliberate request — still not auto-injection.)
//
// The composition root injects the member-engine factory, the workspace Forker,
// and the team hooks runner (mirroring Service.CreateTeam's wiring) so this tool
// drives the SAME Supervisor the gRPC team path drives.
type TeamTool struct {
	// factory builds each member's Engine, bound to the per-call team. Required.
	factory TeamMemberEngineFactory
	// forker isolates a Mutating member's workspace (force-copy: own `.git`). Required
	// only if any member is Mutating; a Mutating member without it yields a tool error
	// (the model can retry with a read-only roster).
	forker tool.EnvironmentForker
	// roForker isolates a read-only member that the factory granted a shell, as a
	// cheap git worktree. Required only if the factory marks a read-only member
	// IsolateReadOnly (which the composition root does only when this is wired). When
	// nil, read-only members base-share with no shell.
	roForker tool.EnvironmentForker
	// sharedBaseWS re-views the parent workspace for a BASE-SHARING read-only
	// member (the no-shell fallback tier) so it never inherits the main session's
	// out-of-root relaxation (the path-escape-posture Scenario 5 boundary —
	// threaded into the supervisor as WithTeamSharedBaseWorkspace). nil keeps the
	// historical verbatim base share.
	sharedBaseWS func(root string) tool.Workspace
	// hooks fires the team lifecycle hooks (TeammateIdle) — shared with the member
	// coordination tools by the composition root. nil disables them.
	hooks port.HookRunner
	// store, when non-nil, persists each member session under its team-namespaced id
	// so the parent can later inspect a member transcript via InspectMemberTool. It is
	// the port.SessionStore the composition root passes; nil disables persistence.
	store port.SessionStore
	// tokenBudget is the operator-configured team-wide token budget the tool threads
	// into the supervisor (WithTeamTokenBudget). A per-call max_team_tokens arg may only
	// TIGHTEN it (tightenLimit semantics). 0 disables.
	tokenBudget int
	// idPrefix seeds the generated team name from the parent call id.
	idPrefix string
}

// TeamOption configures a TeamTool.
type TeamOption func(*TeamTool)

// WithTeamToolForker injects the workspace forker used to isolate a Mutating
// member's workspace (force-copy). It is required only if the model forms a Mutating
// roster.
func WithTeamToolForker(f tool.EnvironmentForker) TeamOption {
	return func(t *TeamTool) { t.forker = f }
}

// WithTeamToolReadOnlyForker injects the workspace forker (the worktree-default mode)
// used to isolate a read-only member that the factory granted a shell. It is required
// only if the factory marks a read-only member IsolateReadOnly.
func WithTeamToolReadOnlyForker(f tool.EnvironmentForker) TeamOption {
	return func(t *TeamTool) { t.roForker = f }
}

// WithTeamToolSharedBaseWorkspace injects the NON-relaxed workspace view a
// BASE-SHARING read-only member runs against (threaded into the supervisor as
// WithTeamSharedBaseWorkspace). The composition root wires it whenever the
// parent workspace may carry out-of-root relaxation (the path-escape-posture
// auto/yolo main-session relax): the in-loop Team tool's base IS the parent's
// relaxed workspace at those postures, so a shell-less member must re-view it
// through the non-relaxed construction. nil (the default) is byte-identical to
// the pre-option behaviour.
func WithTeamToolSharedBaseWorkspace(f func(root string) tool.Workspace) TeamOption {
	return func(t *TeamTool) { t.sharedBaseWS = f }
}

// WithTeamToolHooks injects the HookRunner threaded into the Supervisor (and, by
// the composition root, the member coordination tools) so a team's lifecycle hooks
// flow through one runner. nil disables them.
func WithTeamToolHooks(h port.HookRunner) TeamOption {
	return func(t *TeamTool) { t.hooks = h }
}

// WithTeamToolStore injects the session store the Team tool threads into the
// supervisor (WithMemberStore) to persist member sessions for out-of-band
// inspection. nil disables persistence. The Team tool consumes the
// port.SessionStore interface, never a concrete adapter (layering holds).
func WithTeamToolStore(s port.SessionStore) TeamOption {
	return func(t *TeamTool) { t.store = s }
}

// WithTeamToolTokenBudget sets the operator-configured team-wide token budget the
// tool threads into the supervisor (WithTeamTokenBudget). A per-call
// max_team_tokens arg may only TIGHTEN it (tightenLimit semantics). 0 disables.
func WithTeamToolTokenBudget(n int) TeamOption {
	return func(t *TeamTool) {
		if n > 0 {
			t.tokenBudget = n
		}
	}
}

// NewTeamTool constructs the Team tool over a per-member engine factory. factory
// must be non-nil; NewTeamTool panics otherwise (a composition-root programming
// error — a Team tool with no way to build member engines cannot run a team).
func NewTeamTool(factory TeamMemberEngineFactory, opts ...TeamOption) tool.Tool {
	if factory == nil {
		panic("agent: NewTeamTool requires a non-nil member engine factory")
	}
	t := &TeamTool{factory: factory, idPrefix: "team"}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Spec returns the model-facing specification for the Team tool.
func (*TeamTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: teamToolName,
		Description: "Form a team of coordinating subagents when a goal genuinely splits into " +
			"parallel specialist roles — e.g. an investigator + a fixer, or several role-focused " +
			"workers sharing a task list and mailbox. You specify the roster: each member has a " +
			"name, a role (its briefing), and whether it needs to edit files (mutating). The " +
			"FIRST member is the coordinating lead.\n\n" +
			"A team is the MOST EXPENSIVE tool — several long-lived agents over many rounds. For " +
			"a single focused investigation use Subagent; for something you can do directly, do " +
			"it directly. Keep the roster small (2-4 members is typical).\n\n" +
			"Write the FIRST member's role as a lead briefing: how to decompose the goal into " +
			"tasks, what each task should produce, and what the final report must answer. Write " +
			"every other member's role as a worker briefing: its specialty, where to look, and an " +
			"instruction to RecordFinding each conclusion as it works and message the lead when " +
			"done — findings are how the lead builds the consolidated report.\n\n" +
			"Members are READ-ONLY by default: they inspect, build, and test in an isolated " +
			"throwaway git worktree with a full shell, but CANNOT edit files. Set mutating: true " +
			"only for a member that must write code (it runs in a self-contained copied " +
			"workspace). Neither tier is merged back.\n\n" +
			"The returned report is the team's deliverable — USE IT as the result; do NOT redo " +
			"the team's work. For one member's full detail, call InspectMember with the returned " +
			"team id.",
		Schema: teamSchema,
	}
}

// ReadOnly reports that the Team tool is NOT read-only, so the parent dispatcher
// SERIALISES it (mutate-serial) — it never runs concurrently with another tool.
// A team is long-lived, stateful, and may spawn Mutating members; its Supervisor
// drives unsynchronised member state, so it must not race the parent's other tool
// calls. This is the deliberate opposite of SubagentTool/ParallelTool, which are
// read-parallel.
func (*TeamTool) ReadOnly() bool { return false }

// Execute runs a team with no observability (the emit == nil path): the team's
// member activity is not forwarded, only the lead's consolidated report is returned. Existing
// non-observing callers are unaffected by the observability seam.
func (t *TeamTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	return t.run(ctx, call, env, nil, parentCaps{})
}

// ExecuteWithParent is the childCapableTool seam: it runs the team like ExecuteObserved
// but threads the PARENT's capabilities (interactivity + surface back-channel) into the
// supervisor, so a member's permission ask that A2 (isolation auto-approve) did not
// resolve is SURFACED to the human (interactive parent) or auto-denied with the accurate
// message + operator diagnostic (headless). The in-loop Team tool's goal stays trusted;
// only the ask resolution posture changes.
func (t *TeamTool) ExecuteWithParent(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event), caps parentCaps) (session.ToolResult, error) {
	return t.run(ctx, call, env, emit, caps)
}

// ExecuteObserved runs a team like Execute but, when emit is non-nil, forwards a
// BOUNDED projection of member activity to the parent run's stream via the three
// team.* events. emit only sequences and channels events; it never touches the
// parent's Conversation, so member content still never enters the parent context
// (only the lead's synthesis ToolResult does). It is the observableTool seam the
// dispatcher calls.
func (t *TeamTool) ExecuteObserved(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event)) (session.ToolResult, error) {
	return t.run(ctx, call, env, emit, parentCaps{})
}

// run is the shared implementation behind Execute (emit == nil) and
// ExecuteObserved (emit != nil). It validates the roster, builds and drives the
// Supervisor over the SAME base workspace, optionally forwards a bounded
// projection of member activity, and returns the lead's consolidated synthesis
// (or the labelled fallback) as the single ToolResult that folds back into the
// parent conversation.
func (t *TeamTool) run(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event), caps parentCaps) (session.ToolResult, error) {
	var args teamArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "Team: "+msg), nil
	}
	if msg, ok := validateTeamArgs(args); !ok {
		return session.NewToolError(call.ID, "Team: "+msg), nil
	}

	// Namespace the team id under the PARENT session's own id (review finding 2,
	// issue #368; see SubagentTool.childSessionID's doc for the collision
	// rationale): deriving from call.ID alone let two different top-level
	// sessions issuing equal or adversarially-chosen call ids collide on the
	// IDENTICAL team id, so MemberSessionID(teamID, member) — the same
	// derivation InspectMember uses from the model-supplied team_id — could
	// silently overwrite another owner's persisted member transcript.
	// caps.parentSessionID is empty only on a caps-less drive (plain
	// Execute/ExecuteObserved, no parent session threaded), which keeps the
	// pre-fix call-id-only id.
	teamID := string(call.ID)
	if caps.parentSessionID != "" {
		teamID = string(caps.parentSessionID) + "-" + string(call.ID)
	}
	tm := team.New(teamID)
	factory := func(spec MemberSpec, routedModel string) MemberBuild { return t.factory(tm, spec, routedModel) }

	opts := []SupervisorOption{
		// Thread the goal so it frames every member's round-0 turn and the lead's
		// synthesis as the team's TRUSTED top-level instruction (the parent model
		// authored args.Goal from the user's own prompt — its provenance is the
		// principal, not a peer, which is exactly the trusted case), and namespace
		// member-session ids by the team id (parent-session-id + parent call id,
		// see the teamID derivation above) so two concurrent teams sharing a
		// member name get distinct, collision-free stored ids. The
		// prefix MUST match MemberSessionID's scheme so the inspect tool can derive the
		// same id: "team-<teamID>" → ids "team-<teamID>-<member>". No WithUntrustedGoal
		// here: the in-loop Team tool's goal is always principal-authored and trusted.
		WithTeamGoal(args.Goal),
		WithMemberSessionPrefix(memberSessionIDPrefix + teamID),
	}
	if t.forker != nil {
		opts = append(opts, WithForker(t.forker))
	}
	if t.roForker != nil {
		opts = append(opts, WithReadOnlyForker(t.roForker))
	}
	if t.sharedBaseWS != nil {
		opts = append(opts, WithTeamSharedBaseWorkspace(t.sharedBaseWS))
	}
	if t.hooks != nil {
		opts = append(opts, WithTeamHooks(t.hooks))
	}
	if t.store != nil {
		opts = append(opts, WithMemberStore(t.store))
	}
	// Team-wide token budget: the operator-configured ceiling, with the per-call
	// max_team_tokens arg applied TIGHTEN-ONLY (tightenLimit, like subagentArgs.MaxTurns
	// — the model can lower the operator's budget, never raise it). A non-positive
	// effective budget disables it (no option appended).
	budget := tightenLimit(t.tokenBudget, args.MaxTeamTokens)
	if budget > 0 {
		opts = append(opts, WithTeamTokenBudget(budget))
	}
	// Thread the parent's caps so an unresolved member permission ask is surfaced to the
	// human (interactive parent) or auto-denied with the accurate message (headless). The
	// member askIDs are child-namespaced (team-<teamID>-<member>), so the parent router
	// routes a verdict back without a wire change.
	opts = append(opts, withParentCaps(caps))
	sup := NewSupervisor(tm, env, factory, opts...)

	roster := teamRoster(args.Members)
	for i, spec := range memberSpecs(args.Members) {
		if err := sup.AddMember(ctx, spec); err != nil {
			// A bad roster (e.g. a Mutating member with no forker wired) is a tool error
			// the model can recover from. AddMember already tore down the FAILING
			// member's own fork; but Run (whose deferred cleanupAll releases the earlier
			// successful members' forks) is never reached on failure, so we tear those
			// down explicitly here to avoid leaking them.
			sup.cleanupAll()
			return session.NewToolError(call.ID, fmt.Sprintf("forming team failed at member %d (%q): %v", i, spec.Name, err)), nil
		}
	}

	if emit != nil {
		// Project each member's OPT-IN model-router classification (ADR 0034) onto its
		// roster entry: AddMember routed each undefined member once and recorded the bare
		// category/model metadata, which MemberRouting reads back by name. A defined member
		// (its def pinned the model) and a router miss both leave the fields empty. This is
		// the ONLY mutation of the roster the router introduces; everything else stays the
		// metadata-only teamRoster projection (gauntlet #7 — no member content crosses).
		for i := range roster {
			cat, model, reason := sup.MemberRouting(roster[i].Name)
			roster[i].RoutedCategory, roster[i].RoutedModel = cat, model
			roster[i].RoutingReason = routingReasonPayload(reason)
			roster[i].Model = sup.MemberModel(roster[i].Name)
		}
		emit(session.Event{Type: session.EvTeamStart, Team: &session.TeamPayload{
			ParentCallID: string(call.ID),
			TeamID:       teamID,
			Roster:       roster,
		}})
	}

	// The sink forwards each member event as a BOUNDED, redacted team.member event
	// AND accumulates the team's total token cost from the usage-bearing member
	// events (inner turn.end / result), so team.end can report the team total rather
	// than zero. Supervisor.Run serialises sink calls through a single forwarder
	// goroutine, so both the accumulation and emit run one-at-a-time even though
	// member turns run concurrently — total needs no lock.
	var total session.Usage
	var lastTasks []session.TeamTaskSnapshot
	var lastFindings []session.TeamFindingSnapshot
	sink := func(te TeamEvent) {
		total = total.Add(memberEventUsage(te.Event))
		if emit == nil {
			return
		}
		if ev, ok := projectTeamEvent(string(call.ID), teamID, te); ok {
			emit(ev)
		}
		// Project the shared task list as a first-class team.tasks event, but only
		// when it CHANGED since the last snapshot. The de-dup is load-bearing:
		// the sink fires for every member event (deltas, tool calls, turn ends), so
		// emitting an unchanged task snapshot on each would flood the wire. A team
		// has at most MaxTasks tasks, so the equality scan is cheap.
		snap := projectTeamTasksSnapshot(tm.Tasks())
		if !tasksEqual(snap, lastTasks) {
			lastTasks = snap
			emit(projectTeamTasks(string(call.ID), teamID, snap))
		}
		// Mirror the task de-dup for the findings ledger: emit a first-class
		// team.findings event only when the ledger CHANGED (findings only ever grow,
		// so an append is the sole change). Same clamp, same change-detection
		// discipline, so findings observability is byte-for-byte consistent with tasks.
		fsnap := projectTeamFindingsSnapshot(tm.Findings())
		if !findingsEqual(fsnap, lastFindings) {
			lastFindings = fsnap
			emit(projectTeamFindings(string(call.ID), teamID, fsnap))
		}
	}

	// The parent ctx flows into Run: cancelling the parent stops scheduling further
	// rounds and lets the in-flight round finish. Run's deferred cleanupAll tears
	// down every forked member workspace on every exit (success, cancel, or panic).
	outcome := sup.Run(ctx, sink)

	if emit != nil {
		emit(session.Event{Type: session.EvTeamEnd, Team: &session.TeamPayload{
			ParentCallID: string(call.ID),
			TeamID:       teamID,
			Rounds:       outcome.Rounds,
			Stop:         teamStop(outcome),
			Usage:        total,
			// The terminal task snapshot always lands on team.end, so the task
			// sub-view reflects the final state even if no member event followed the
			// last task transition.
			Tasks: projectTeamTasksSnapshot(tm.Tasks()),
			// The terminal findings snapshot likewise always lands, so the final ledger
			// is observable even if no member event followed the last RecordFinding.
			Findings: projectTeamFindingsSnapshot(tm.Findings()),
			// The per-member terminal disposition snapshot lets a watching client render
			// a stopped member distinctly from a clean one instead of recomputing "done".
			Dispositions: projectTeamDispositions(outcome.Members),
		}})
	}

	// The returned deliverable resolves through the three-tier deliverable() chain: the
	// lead's synthesis when it is a usable report, else the ledger-rich structured
	// fallback, else an honest floor — never a bare refusal or an empty string.
	result := deliverable(outcome)
	// Surface the team id INSIDE the returned text so the parent MODEL can discover
	// the id it must pass to InspectMember. The id rides only EvTeamStart otherwise
	// (a client-only event the model never sees), so without this the model can never
	// form a valid InspectMember call. teamID is the EXACT published id (the Team call
	// id), rendered verbatim per the MemberSessionID contract. Applies to BOTH the
	// synthesis-report and the joinTeamFallback path.
	result = renderTeamResult(teamID, result)
	return session.NewToolResult(call.ID, result), nil
}

// renderTeamResult prepends a machine-extractable team-id line so the parent MODEL
// can discover the id it must pass to InspectMember (the id otherwise rides only the
// client-only EvTeamStart event). teamID MUST be the EXACT published id (the Team
// call id) per the MemberSessionID contract — rendered VERBATIM, never trimmed or
// normalised — so MemberSessionID(teamID, member) reconstructs the saved member
// session id byte-for-byte and InspectMember resolves it.
func renderTeamResult(teamID, body string) string {
	return fmt.Sprintf("Team id: %s\n(To read one member's full transcript, call InspectMember "+
		"with this exact team_id and the member's name.)\n\n%s", teamID, body)
}

// validateTeamArgs enforces the roster preconditions: a non-empty goal, at least
// one member, and non-empty unique member names. It returns a model-readable
// message and ok=false on the first violation.
func validateTeamArgs(args teamArgs) (msg string, ok bool) {
	if strings.TrimSpace(args.Goal) == "" {
		return "'goal' is required and must be non-empty", false
	}
	if len(args.Members) == 0 {
		return "'members' is required and must contain at least one member", false
	}
	seen := make(map[string]struct{}, len(args.Members))
	for i, m := range args.Members {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			return fmt.Sprintf("member %d has an empty 'name'", i), false
		}
		if _, dup := seen[name]; dup {
			return fmt.Sprintf("duplicate member name %q", name), false
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(m.Role) == "" {
			return fmt.Sprintf("member %q has an empty 'role'", name), false
		}
	}
	return "", true
}

// memberSpecs maps the model's roster args onto MemberSpec values, synthesizing
// the FIRST member as the coordinating lead. Each member's Role becomes its
// InitialPrompt (its first-turn briefing).
func memberSpecs(members []TeamMemberArg) []MemberSpec {
	specs := make([]MemberSpec, 0, len(members))
	for i, m := range members {
		specs = append(specs, MemberSpec{
			Name:          strings.TrimSpace(m.Name),
			Lead:          i == 0,
			Mutating:      m.Mutating,
			InitialPrompt: m.Role,
		})
	}
	return specs
}

// teamRoster builds the EvTeamStart roster projection from the model's args. It
// forwards only member metadata (name/role/mutating/lead), never member content.
func teamRoster(members []TeamMemberArg) []session.TeamMemberSpec {
	roster := make([]session.TeamMemberSpec, 0, len(members))
	for i, m := range members {
		roster = append(roster, session.TeamMemberSpec{
			Name:     strings.TrimSpace(m.Name),
			Role:     clampPreview(m.Role),
			Mutating: m.Mutating,
			Lead:     i == 0,
		})
	}
	return roster
}

// projectTeamEvent builds a REDACTED, BOUNDED team.member Event from one member's
// inner session event. It is the single chokepoint that enforces the
// fuller-but-bounded contract:
//
//   - message.delta → forward the member's text (capped).
//   - tool.call     → forward the tool NAME + a CAPPED preview of its args.
//   - tool.result   → forward the error bool + a CAPPED preview of its body.
//   - turn.end      → forward the per-turn usage (no content).
//   - result        → forward the terminal text (capped) + cumulative usage.
//   - permission.ask → DROPPED entirely (ok=false): an ask reason can quote
//     secrets/sensitive args, so it is NEVER forwarded.
//   - anything else  → DROPPED (ok=false): unrecognised kinds are not projected, so
//     a new event type cannot leak content by default.
//
// It NEVER copies a raw args blob or result body unbounded — every text/preview
// passes through clampPreview.
func projectTeamEvent(parentCallID, teamID string, te TeamEvent) (session.Event, bool) {
	ev := te.Event
	base := &session.TeamPayload{
		ParentCallID:    parentCallID,
		TeamID:          teamID,
		Member:          te.Member,
		MemberSessionID: te.MemberSessionID,
		InnerKind:       ev.Type,
	}
	switch ev.Type {
	case session.EvMessageDelta:
		if strings.TrimSpace(ev.Text) == "" {
			return session.Event{}, false
		}
		base.Text = clampPreview(ev.Text)
	case session.EvToolCall:
		if ev.ToolCall == nil {
			return session.Event{}, false
		}
		base.ToolName = ev.ToolCall.Name
		base.Detail = clampPreview(string(ev.ToolCall.Args))
	case session.EvToolResult:
		if ev.ToolResult == nil {
			return session.Event{}, false
		}
		base.IsError = ev.ToolResult.IsError
		base.Detail = clampPreview(ev.ToolResult.Content)
	case session.EvTurnEnd:
		if ev.TurnEnd != nil {
			base.Usage = ev.TurnEnd.Usage
			// The per-member context meter (ctrl+a overlay) reads the CURRENT context
			// occupancy — this turn's input-token count — as its numerator, and the
			// producing member engine's window as its denominator. Both ride the
			// turn.end projection so a client can draw a band bar per member lane.
			base.ContextUsed = int64(ev.TurnEnd.Usage.InputTokens)
			base.ContextWindow = int64(te.ContextWindow)
		}
	case session.EvResult:
		if ev.Result != nil {
			base.Text = clampPreview(ev.Result.Text)
			base.Usage = ev.Result.Usage
			// Cause mirrors SubagentPayload.Cause on the per-round member result:
			// the harness/provider FAILURE DETAIL when this round ended StopError
			// (empty otherwise). It is the ONE emit site for TeamPayload.Cause,
			// normalised through the shared subagentCausePayload chokepoint so
			// Subagent and Team collapse identically. Harness metadata, never
			// member-authored output — gauntlet-#7 safe on the same footing as
			// Stop/Usage.
			if ev.Result.Stop == session.StopError {
				base.Cause = subagentCausePayload(ev.Result.Error)
			}
		}
	default:
		// permission.ask, turn.start, hook, compaction, reasoning.delta, session.init,
		// subagent.*, team.* and any future kind are NOT projected — a member's
		// permission.ask in particular is dropped so its (possibly secret-bearing)
		// reason never reaches the stream.
		return session.Event{}, false
	}
	return session.Event{Type: session.EvTeamMember, Team: base}, true
}

// isCleanASCII is the zero-alloc fast-path sentinel for clampPreview: it returns
// true when s consists entirely of printable ASCII bytes (0x20–0x7e) not longer
// than maxRunes, which is the common case for tool-arg/result previews and child
// message text. Because it is a simple byte loop (gc cost ≤ 80) the compiler
// inlines it at every call site; clampPreview then returns s verbatim with zero
// allocation on the fast path. Multi-byte UTF-8, any control byte, or an over-long
// string short-circuits to false so the full rune-aware slow path takes over. The
// scan is per-BYTE, so it conservatively classifies a sequence of multi-byte
// leading/continuation bytes as dirty rather than attempting to decode them — the
// slow path handles those correctly.
func isCleanASCII(s string) bool {
	if len(s) > maxTeamPreview {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// clampPreview normalises a forwarded text/preview (a member tool-call/result
// Detail, a member's message Text, a task Description, or a SURFACED subagent command)
// into a single bounded, control-byte-free line: it replaces every C0 (0x00–
// 0x1f, includes \n \r \t and ESC 0x1b) and C1/DEL (0x7f–0x9f) control byte to a
// space so no escape/ANSI sequence rides a verbatim render, and clamps to
// maxTeamPreview runes, appending an ellipsis on overflow. The control-byte scrub is
// a security boundary (CWE-117/150): a surfaced command or member preview can carry
// PEER-CONTROLLED text, and a non-mecatui gRPC client rendering it verbatim must not
// be exposed to ANSI/escape-sequence injection. It is rune-aware, so it never splits
// a multi-byte character. This is the cap+sanitiser that keeps the fuller member
// content BOUNDED and inert.
//
// The common path (pure ASCII, under the cap) is zero-alloc: isCleanASCII (an
// inlinable byte loop) short-circuits before the strings.Builder is constructed.
// The slow path is a single-pass strings.Builder walk — the rune cap is enforced as
// the builder fills, so an overlong input allocates only the cap-sized prefix, never
// the full scrubbed copy.
func clampPreview(s string) string {
	if isCleanASCII(s) {
		return s
	}
	var b strings.Builder
	b.Grow(min(len(s), maxTeamPreview+1))
	runes := 0
	changed := false
	truncated := false
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			r = ' '
			changed = true
		}
		if runes == maxTeamPreview {
			truncated = true
			break
		}
		b.WriteRune(r)
		runes++
	}
	if !changed && !truncated {
		return s
	}
	out := b.String()
	if truncated {
		out = strings.TrimRight(out, " ") + "…"
	}
	return out
}

// projectTeamTasksSnapshot maps the team's shared task list (team.Task copies) onto
// the domain TeamTaskSnapshot projection carried on the event stream. The mapping
// lives HERE (engine/agent), not in session: session must not import engine/team
// (team imports session, never the reverse), so the team.Task→session.TeamTaskSnapshot
// bridge belongs in the application layer. Descriptions pass through clampPreview
// (the same cap every member-derived preview uses), and Deps are copied as []string.
func projectTeamTasksSnapshot(tasks []team.Task) []session.TeamTaskSnapshot {
	out := make([]session.TeamTaskSnapshot, 0, len(tasks))
	for _, tk := range tasks {
		deps := make([]string, 0, len(tk.Deps))
		for _, d := range tk.Deps {
			deps = append(deps, string(d))
		}
		out = append(out, session.TeamTaskSnapshot{
			ID:          string(tk.ID),
			Description: clampPreview(tk.Description),
			State:       string(tk.State),
			Assignee:    tk.Assignee,
			Deps:        deps,
		})
	}
	return out
}

// projectTeamTasks wraps a task snapshot in a first-class EvTeamTasks event — the
// team-WIDE projection the client routes to the ctrl+a task sub-view. It carries no
// Member (the task list is team-wide, not per-member), so it does not borrow the
// per-member EvTeamMember envelope.
func projectTeamTasks(parentCallID, teamID string, tasks []session.TeamTaskSnapshot) session.Event {
	return session.Event{Type: session.EvTeamTasks, Team: &session.TeamPayload{
		ParentCallID: parentCallID,
		TeamID:       teamID,
		Tasks:        tasks,
	}}
}

// tasksEqual reports whether two task snapshots are equal on the fields that drive
// the task sub-view (id / state / assignee / deps). It is the de-dup guard in run's
// sink: a snapshot equal to the last emitted one is NOT re-sent, bounding wire
// volume (the sink fires per member event). Description is excluded — it never
// changes after CreateTask, so it cannot drive a spurious re-emit.
func tasksEqual(a, b []session.TeamTaskSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].State != b[i].State || a[i].Assignee != b[i].Assignee {
			return false
		}
		if !slices.Equal(a[i].Deps, b[i].Deps) {
			return false
		}
	}
	return true
}

// projectTeamFindingsSnapshot maps the team's findings ledger (team.Finding copies)
// onto the domain TeamFindingSnapshot projection carried on the event stream. The
// mapping lives HERE (engine/agent), not in session: session must not import
// engine/team (team imports session, never the reverse), so the team.Finding →
// session.TeamFindingSnapshot bridge belongs in the application layer. Bodies pass
// through clampPreview (the same cap every member-derived preview uses).
func projectTeamFindingsSnapshot(findings []team.Finding) []session.TeamFindingSnapshot {
	out := make([]session.TeamFindingSnapshot, 0, len(findings))
	for _, f := range findings {
		out = append(out, session.TeamFindingSnapshot{
			Member: f.Member,
			Body:   clampPreview(f.Body),
		})
	}
	return out
}

// projectTeamDispositions maps the supervisor's terminal MemberOutcomes onto the
// domain TeamMemberDisposition snapshot carried on EvTeamEnd. The bridge lives HERE
// (engine/agent), not in session, mirroring the tasks/findings snapshot bridges:
// the MemberOutcome → session.TeamMemberDisposition mapping is application-layer. The
// enum strings are the supervisor's closed MemberDisposition/MemberStopReason values,
// so no member-authored content crosses (Name rides verbatim on the team.start roster
// already) and no preview cap is needed.
func projectTeamDispositions(members []MemberOutcome) []session.TeamMemberDisposition {
	out := make([]session.TeamMemberDisposition, 0, len(members))
	for _, m := range members {
		out = append(out, session.TeamMemberDisposition{
			Name:        m.Name,
			Disposition: string(m.Disposition),
			Reason:      string(m.Reason),
			// A plain count of supervisor verdicts (issue #318) — it crosses for the same
			// reason the enums do: without it a retried-then-finished member projects as
			// indistinguishable from one that never failed.
			ErrorRounds: m.ErrorRounds,
		})
	}
	return out
}

// projectTeamFindings wraps a findings snapshot in a first-class EvTeamFindings event
// — the team-WIDE projection mirroring EvTeamTasks. It carries no Member (the ledger
// is team-wide, not per-member), so it does not borrow the per-member EvTeamMember
// envelope.
func projectTeamFindings(parentCallID, teamID string, findings []session.TeamFindingSnapshot) session.Event {
	return session.Event{Type: session.EvTeamFindings, Team: &session.TeamPayload{
		ParentCallID: parentCallID,
		TeamID:       teamID,
		Findings:     findings,
	}}
}

// findingsEqual reports whether two findings snapshots are equal. It is the de-dup
// guard in run's sink, mirroring tasksEqual: a snapshot equal to the last emitted one
// is NOT re-sent, bounding wire volume (the sink fires per member event). Findings
// only ever grow (append-only ledger), so a length change is the common signal, but
// the per-entry comparison keeps it robust.
func findingsEqual(a, b []session.TeamFindingSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Member != b[i].Member || a[i].Body != b[i].Body {
			return false
		}
	}
	return true
}

// teamStop maps the team outcome to a terminal StopReason for EvTeamEnd: a
// genuinely-quiescent team stopped on success; a non-quiescent team whose team-wide
// token budget tripped stopped on budget (so a client can distinguish a budget-stop
// from a round-cap); any other non-quiescent team hit the round cap or a stuck
// dependency. StopBudget is a string passthrough on the wire (no proto enum).
func teamStop(o TeamOutcome) session.StopReason {
	if !o.Quiescent && o.BudgetExhausted {
		return session.StopBudget
	}
	if o.Quiescent {
		return session.StopEndTurn
	}
	return session.StopMaxTurns
}

// TeamStop maps a TeamOutcome to its terminal StopReason — the SINGLE
// quiescent/budget/round-cap rule teamStop applies for EvTeamEnd, exported so the
// wire layers can stamp the same string-passthrough stop onto the terminal
// RunTeam outcome frame without duplicating the rule (issue #36).
func TeamStop(o TeamOutcome) session.StopReason {
	return teamStop(o)
}

// memberEventUsage extracts the token usage a single member event carries, for the
// running team total accumulated in run's sink. Only the usage-bearing inner kinds
// contribute: turn.end (this turn's usage) and result (the member run's cumulative
// usage). Summing turn.end across a member's turns reconstructs that member's spend
// without double-counting the result's cumulative figure, so summing only turn.end
// events yields the team total. The terminal result is excluded from the sum to
// avoid double-counting; every other kind contributes the zero Usage.
//
// AUTHORITY: the supervisor independently accumulates the team total from the per-drive
// EvResult.Usage (memberRT.tokensUsed → TeamOutcome.Usage) for the budget gate. THIS
// sink's turn.end sum is the EvTeamEnd DISPLAY payload only. The two were equal BY
// CONSTRUCTION (EvResult.Usage == Σ that drive's turn.end usage) until issue #82: a turn
// that reports no usage frame now carries a conversation-size ESTIMATE on its turn.end
// (DISPLAY-ONLY, see loop.go's emitUsage), while EvResult.Usage stays on provider truth
// (0 for that turn). So on a usage-less turn the DISPLAY sum here can exceed the budget
// total — deliberate: the meter figure stays meaningful, the budget gate stays on
// provider truth. They are not reconciled.
func memberEventUsage(ev session.Event) session.Usage {
	if ev.Type == session.EvTurnEnd && ev.TurnEnd != nil {
		return ev.TurnEnd.Usage
	}
	return session.Usage{}
}

// nonDeliverableMaxLen is the rune ceiling below which a refusal-prefixed synthesis is
// treated as a non-deliverable (see isNonDeliverable). ~280 runes is ~2-3 sentences —
// well under any genuine multi-member consolidated report, but comfortably above a
// padded one-line refusal. It is a GUARD on the refusal-prefix match, never a standalone
// signal: a long-but-refusal-prefixed text and a short-but-non-refusal text both pass.
const nonDeliverableMaxLen = 280

// refusalPrefixes is the small, deliberately TINY set of lower-cased phrases a refusal
// synthesis tends to OPEN with. Matched as a PREFIX of the trimmed, lower-cased text
// (never a substring), so a real report that opens substantively and mentions a refusal
// later does not match. The set will miss novel phrasings — acceptable, because (i) the
// trigger requires short + prefix + ledger>0 together, and (ii) the fallback is strictly
// better than a refusal, so a miss costs the status quo while a false positive only
// swaps a terse-but-valid report for the member-authored ledger.
var refusalPrefixes = []string{
	"i'm sorry, but i can",
	"i am sorry, but i can",
	"i'm sorry, i can",
	"i am sorry, i can",
	"i cannot assist",
	"i can't assist",
	"i'm unable to",
	"i am unable to",
	"sorry, i can",
	"i won't be able to",
}

// isNonDeliverable reports whether a lead synthesis is a non-deliverable that must be
// REPLACED by the structured fallback rather than returned to the parent. It is
// CONSERVATIVE by design: it fires ONLY on strong signals so a legitimately terse real
// report is never discarded. It is pure (no I/O, no Supervisor state) and table-tested.
//
// text       — the lead's synthesis (the raw TeamOutcome.Report).
// ledgerLen  — len(outcome.Findings): how many findings the team actually recorded.
//
// Fires when EITHER:
//
//	(a) the trimmed text is empty/whitespace; OR
//	(b) the trimmed text is SHORT (<= nonDeliverableMaxLen runes) AND its lower-cased,
//	    trimmed PREFIX matches one of refusalPrefixes AND the team recorded findings the
//	    short text cannot plausibly contain (ledgerLen > 0).
//
// It does NOT fire on a long text that merely contains a refusal phrase mid-body (a real
// report discussing refusals), nor on a short non-refusal-shaped answer (a valid terse
// report), nor on a refusal-shaped text when the ledger is empty (nothing better to fall
// back to — the honest floor still states non-convergence, so returning the lead's words
// is no worse and may carry context).
func isNonDeliverable(text string, ledgerLen int) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	lower := strings.ToLower(t)
	short := len([]rune(t)) <= nonDeliverableMaxLen
	refusalShaped := false
	for _, p := range refusalPrefixes {
		if strings.HasPrefix(lower, p) {
			refusalShaped = true
			break
		}
	}
	return short && refusalShaped && ledgerLen > 0
}

// convergenceHeader returns the deliverable's leading status line stating round count
// and whether the team converged. On a non-quiescent team (stop:max-turns, or stop:budget
// when the team-wide token budget tripped) it states the team did NOT converge so the
// parent learns this even when the synthesis body looks plausible. On a quiescent team it
// states clean completion. When the team-wide token budget was exhausted it ALSO appends
// the budget line (so the parent learns the stop reason in every tier). deliverable
// prepends the header to tiers 2 and 3 always, and to tier 1 when !o.Quiescent ||
// o.BudgetExhausted (a converged happy-path synthesis needs no banner). The "stop:
// max-turns" / "stop: budget" label mirrors teamStop's non-quiescent mapping.
func convergenceHeader(o TeamOutcome) string {
	stopped := 0
	for _, m := range o.Members {
		if m.Stopped {
			stopped++
		}
	}
	budgetLine := ""
	if o.BudgetExhausted {
		budgetLine = fmt.Sprintf("Team token budget exhausted after %d round(s) (~%d tokens used); "+
			"no further rounds were scheduled — the in-flight round and the lead's synthesis completed.\n\n",
			o.Rounds, o.Usage.TotalTokens())
	}
	if o.Quiescent {
		return fmt.Sprintf("Team finished in %d round(s) (converged): %d member(s), %d stopped.\n\n",
			o.Rounds, len(o.Members), stopped) + budgetLine
	}
	stopLabel := "max-turns"
	if o.BudgetExhausted {
		stopLabel = "budget"
	}
	return fmt.Sprintf("Team ran %d round(s) and did NOT converge (stop: %s): %d member(s), %d stopped.\n\n",
		o.Rounds, stopLabel, len(o.Members), stopped) + budgetLine
}

// deliverable resolves the team's final deliverable through three tiers, guaranteeing a
// non-empty, honest result that is NEVER a bare refusal. Tier 1: the lead's synthesis,
// IF non-empty AND not a non-deliverable (isNonDeliverable). Tier 2: a ledger-rich
// structured fallback (findings ledger grouped by member, per-member dispositions +
// completed tasks, last text). Tier 3: an honest floor — "ran N rounds, did not converge,
// M stopped" — which is always non-empty because round count and dispositions always
// exist. The non-convergence header (convergenceHeader) is prepended to tiers 2 and 3
// always, and to tier 1 when !o.Quiescent || o.BudgetExhausted.
func deliverable(o TeamOutcome) string {
	report := strings.TrimSpace(o.Report)
	if report != "" && !isNonDeliverable(o.Report, len(o.Findings)) {
		// Tier 1 — the lead's usable synthesis. Prepend the non-convergence banner when
		// the team did NOT converge OR the team-wide token budget tripped (so a
		// quiescent-but-budget-stopped run still carries the budget line); a clean
		// converged path needs no banner.
		if !o.Quiescent || o.BudgetExhausted {
			return convergenceHeader(o) + o.Report
		}
		return o.Report
	}

	// Tier 2 — the ledger-rich structured fallback. Build the body header-agnostically;
	// if it is empty (no findings, no completed tasks, no last text across all members)
	// fall through to the tier-3 honest floor. Either way the convergence header leads.
	body := joinTeamFallback(o)
	if strings.TrimSpace(body) == "" {
		return convergenceHeader(o) + honestFloor(o)
	}
	return convergenceHeader(o) + body
}

// honestFloor is tier 3: the absolute floor reached only when the structured fallback
// has no content to show (no findings, no completed tasks, no member last text). Round
// count and dispositions always exist, so it is always non-empty — this is what makes
// the "NEVER empty" invariant structural rather than incidental. The convergence header
// is prepended by deliverable; this returns the floor BODY.
func honestFloor(o TeamOutcome) string {
	var stopped []string
	for _, m := range o.Members {
		if m.Stopped {
			reason := string(m.Reason)
			if reason == "" {
				reason = "unknown"
			}
			stopped = append(stopped, fmt.Sprintf("%s: %s", m.Name, reason))
		}
	}
	var b strings.Builder
	b.WriteString("The lead did not produce a usable consolidated report, and the team recorded " +
		"no findings, completed tasks, or final member output to fall back to.")
	if len(stopped) > 0 {
		fmt.Fprintf(&b, " Stopped members: %s.", strings.Join(stopped, ", "))
	}
	b.WriteString(" The work did not produce a recoverable result; consider re-running with a " +
		"narrower goal or a smaller roster, or do the work directly.")
	return b.String()
}

// joinTeamFallback renders the team's degraded deliverable BODY: the member-authored
// findings ledger grouped by member FIRST, then a per-member status block (disposition +
// reason + completed tasks + last text). It is HEADER-AGNOSTIC — deliverable prepends the
// non-convergence header (convergenceHeader), so round/stop info appears once across all
// tiers. It is the DEGRADED path: returned when the lead's synthesis is empty or a
// non-deliverable, so the parent receives a non-empty, honest, member-data-rich result
// instead of a refusal or an empty string. It returns "" when there is genuinely nothing
// to show (no findings AND no per-member content), letting deliverable use the tier-3
// honest floor.
func joinTeamFallback(o TeamOutcome) string {
	var b strings.Builder

	// Findings ledger, grouped by member in first-seen append order (the gold).
	byMember := make(map[string][]string)
	var order []string
	for _, f := range o.Findings {
		if _, seen := byMember[f.Member]; !seen {
			order = append(order, f.Member)
		}
		byMember[f.Member] = append(byMember[f.Member], f.Body)
	}
	if len(o.Findings) > 0 {
		b.WriteString("The lead did not produce a usable consolidated report. Here is what the team gathered:\n\n")
		b.WriteString("Recorded findings:\n")
		for _, member := range order {
			fmt.Fprintf(&b, "\nFrom %s:\n", member)
			for _, body := range byMember[member] {
				fmt.Fprintf(&b, "- %s\n", body)
			}
		}
	}

	// Per-member status — disposition + reason + completed tasks + last text.
	var statusBody strings.Builder
	for _, m := range o.Members {
		statusBody.WriteString("\n=== ")
		statusBody.WriteString(m.Name)
		if m.Stopped {
			reason := string(m.Reason)
			if reason == "" {
				reason = "unknown"
			}
			fmt.Fprintf(&statusBody, " [STOPPED: %s] ===\n", reason)
		} else {
			statusBody.WriteString(" [DONE] ===\n")
		}
		if len(m.Completed) > 0 {
			fmt.Fprintf(&statusBody, "completed: %s\n", strings.Join(m.Completed, "; "))
		}
		// Skip the lead's LastText: after synthesis it IS the (empty/truncated/rejected)
		// synthesis the fallback exists to replace, so echoing it would re-surface the
		// discarded text (e.g. the refusal the headline guard removes).
		if !m.Lead && strings.TrimSpace(m.LastText) != "" {
			statusBody.WriteString(m.LastText)
			statusBody.WriteString("\n")
		}
	}
	// Emit the per-member status block only when it carries content beyond the bare
	// disposition lines, OR when there are findings to anchor it. A roster of all-DONE
	// members with no completed tasks and no last text and no findings yields "" so
	// deliverable falls through to the honest floor.
	if len(o.Findings) > 0 || hasMemberContent(o.Members) {
		if len(o.Findings) == 0 {
			b.WriteString("The lead did not produce a usable consolidated report. Here is what the team gathered:\n")
		}
		b.WriteString("\nPer-member status:\n")
		b.WriteString(statusBody.String())
	}

	return b.String()
}

// hasMemberContent reports whether any member carries fallback-worthy content beyond its
// bare disposition: a completed task or non-empty last text. It is the tier-2-vs-tier-3
// boundary signal — when no member has content AND there are no findings, the structured
// fallback is effectively empty and deliverable uses the honest floor.
func hasMemberContent(members []MemberOutcome) bool {
	for _, m := range members {
		if len(m.Completed) > 0 {
			return true
		}
		// The lead's LastText is excluded (see joinTeamFallback) — it is the rejected
		// synthesis, not fallback-worthy content — so it does not count here either.
		if !m.Lead && strings.TrimSpace(m.LastText) != "" {
			return true
		}
	}
	return false
}

// Compile-time assertion that TeamTool satisfies the Tool contract and the
// agent-internal observableTool seam.
var (
	_ tool.Tool      = (*TeamTool)(nil)
	_ observableTool = (*TeamTool)(nil)
)
