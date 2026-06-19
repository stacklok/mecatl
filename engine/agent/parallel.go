package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// parallelToolName is the catalog name of the fork-join fan-out tool.
const parallelToolName = "Parallel"

// defaultMaxBranches caps the fan-out of a single Parallel call. A model that asks
// for an absurd number of branches is rejected rather than allowed to spawn an
// unbounded number of child loops (and forked workspaces). Override with
// WithMaxBranches.
const defaultMaxBranches = 8

// defaultParallelConcurrency bounds how many child branches run at once. Forking and
// running N child loops simultaneously is the point of fork-join, but it is also
// N times the resource cost, so a worker limit keeps it bounded. Override with
// WithParallelConcurrency.
const defaultParallelConcurrency = 4

// Join strategies. join is normalised (trim + lower) before comparison; "" maps
// to joinAll (today's default behaviour) and "best" is an alias of joinJudge.
const (
	joinAll   = "all"
	joinFirst = "first"
	joinJudge = "judge"
	joinBest  = "best" // alias of joinJudge
)

// parallelArgs is the argument payload the model supplies when calling the Parallel tool.
type parallelArgs struct {
	// Tasks is the list of self-contained branch prompts. Each runs in its OWN
	// isolated forked workspace and its OWN fresh child context. Because every
	// child has a fresh context window and cannot see this conversation, each task
	// must be self-contained.
	Tasks []string `json:"tasks"`
	// Shared is an optional instruction prepended to every task prompt (e.g. common
	// context all branches need). It is convenience only; it could equally be
	// repeated into each task.
	Shared string `json:"shared,omitempty"`
	// Join selects how branch results are combined: "all" (default) returns every
	// branch summary; "first" returns the first branch to SUCCEED (by completion
	// order) and cancels the rest; "judge"/"best" runs an LLM judge that picks one
	// winner against Criteria. "" normalises to "all".
	Join string `json:"join,omitempty"`
	// Criteria is optional free-text guidance for the "judge"/"best" strategy (e.g.
	// "prefer the smallest diff"). It is ignored for "all"/"first".
	Criteria string `json:"criteria,omitempty"`
}

// parallelSchema is the JSON schema the model sees for the Parallel tool's arguments.
var parallelSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "tasks": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "description": "Self-contained branch prompts. Each runs in PARALLEL in its own isolated forked workspace and its own fresh context window; include everything each branch needs."
    },
    "shared": {
      "type": "string",
      "description": "Optional shared instruction prepended to every branch's prompt."
    },
    "join": {
      "type": "string",
      "enum": ["all", "first", "judge", "best"],
      "description": "How to combine branch results. 'all' (default): return every branch summary so YOU pick. 'first': return the first branch that succeeds (others are cancelled) — only for genuinely interchangeable branches. 'judge'/'best': an LLM picks the single best branch against 'criteria'; its forked workspace is PRESERVED for inspection/merge."
    },
    "criteria": {
      "type": "string",
      "description": "Optional guidance for 'judge'/'best' selection (e.g. 'prefer the smallest diff', 'must keep the public API stable'). Ignored for 'all'/'first'."
    }
  },
  "required": ["tasks"]
}`)

// ParallelTool is the fork-join fan-out tool (harness pattern 8). When executed it
// forks N ISOLATED child workspaces from the parent's workspace (via the injected
// tool.WorkspaceForker), runs one CHILD agent loop per branch in PARALLEL (bounded
// by a worker limit) over the injected child *Engine — each with its own fresh
// Session, tighter Limits, and the child Engine's scoped catalog — drains every
// child's Event stream internally, and JOINS the results into a SINGLE
// session.ToolResult that summarizes all branches.
//
// Like SubagentTool (gauntlet #7), the parent NEVER observes any child's intermediate
// tool.call / tool.result / message.delta / permission.ask events: each child's
// stream is drained entirely inside Execute and only the terminal summary folds
// back. A child permission ask is auto-denied so children stay non-interactive.
//
// Isolation: each branch runs in its OWN forked workspace, so even a child wired
// with mutating tools writes only to its fork — parallel writes are SAFE because
// they are isolated (this is strictly safer than concurrent SubagentTool calls, which
// share the base). v1 does NOT auto-merge: Execute returns the per-branch
// summaries and the child workspace ROOT paths so a human or the parent can
// inspect/merge the forked trees. cleanup tears each fork down after its summary
// has been captured.
//
// The child Engine is built by the composition root with a scoped catalog (no
// Parallel, no Subagent — children cannot fan out further) exactly as for SubagentTool.
type ParallelTool struct {
	// childEngine runs each branch's child loop. It is pre-wired by the composition
	// root with a scoped catalog and a non-interactive policy. It is never the
	// parent Engine.
	childEngine *Engine

	// engineFactory, when non-nil, mints a per-branch child engine for an OPT-IN
	// model-router-classified model (ADR 0034), exactly the WithSubagentEngineFactory
	// shape: a composition closure that RE-DERIVES the override branch engine's
	// Compactor/TokenCounter/Env.Model/ContextWindow for the routed model (never a
	// clone-and-swap). It is consulted ONLY when the parent run wired routeTask AND the
	// classifier hit (maybeRouteBranchModel); a nil factory, an unwired routeTask, or a
	// classifier miss falls through to the shared childEngine — byte-identical to a
	// deployment with no router. nil by default (WithParallelEngineFactory injects it).
	engineFactory func(model string) (*Engine, bool)

	// forker isolates each branch's workspace from the shared base.
	forker tool.WorkspaceForker

	// limits bound a single child branch run. Defaults to defaultChildLimits.
	limits session.Limits

	// childMode is the permission mode each child session runs under. Defaults to
	// session.ModeDefault.
	childMode session.PermissionMode

	// maxBranches caps the fan-out per Parallel call.
	maxBranches int

	// concurrency bounds how many branches run at once.
	concurrency int

	// hooks fires SubagentStop per finished branch (best-effort; nil disables it).
	hooks port.HookRunner

	// idPrefix seeds generated child SessionIDs.
	idPrefix string

	// judge selects a winner for the "judge"/"best" strategy. It is injected by the
	// composition root via WithParallelJudge (kept as an interface so engine/agent
	// never imports an adapter, and so a non-LLM scorer can be substituted). nil ⇒
	// the "judge"/"best" strategy returns a model-addressable "judging unavailable"
	// error; "all"/"first" never touch it.
	judge BranchJudge

	// winnerReaper bounds the PRESERVED winner forks (join=first / join=judge). When
	// non-nil, a winner's fork cleanup is handed to the reaper instead of dropped, so
	// the reaper can LRU-evict (and tear down) the oldest preserved fork once the cap
	// is exceeded — bounding disk growth across many Parallel calls while keeping the most
	// recent winners inspectable. nil ⇒ the original behaviour: a winner fork is
	// preserved indefinitely (its cleanup is simply never called).
	winnerReaper PreservedForkStore

	// store, when non-nil, best-effort persists each branch's child session after its
	// run so the PULL InspectSubagent tool can later load its transcript by the
	// "branch id:" the result text surfaces (issue #30). Mirrors SubagentTool.store
	// exactly: nil disables persistence (persistChild discipline — the save is
	// advisory, failures swallowed); branch ids ("parallel-<callID>-<i>") share the one
	// session store with subagent ("subagent-<callID>") and team ("team-<teamID>-<member>")
	// ids, the prefixes keeping them disjoint by convention. The store is consumed as
	// the port.SessionStore interface, never a concrete adapter.
	store port.SessionStore
}

// ParallelOption configures a ParallelTool.
type ParallelOption func(*ParallelTool)

// WithParallelChildLimits overrides the per-branch stop conditions (default
// defaultChildLimits).
func WithParallelChildLimits(l session.Limits) ParallelOption {
	return func(t *ParallelTool) { t.limits = l }
}

// WithParallelChildMode sets the permission mode each child branch session runs under
// (default session.ModeDefault).
func WithParallelChildMode(m session.PermissionMode) ParallelOption {
	return func(t *ParallelTool) { t.childMode = m }
}

// WithMaxBranches caps the number of branches a single Parallel call may fan out to
// (default defaultMaxBranches). A non-positive value is ignored.
func WithMaxBranches(n int) ParallelOption {
	return func(t *ParallelTool) {
		if n > 0 {
			t.maxBranches = n
		}
	}
}

// WithParallelConcurrency bounds how many branches run simultaneously (default
// defaultParallelConcurrency). A non-positive value is ignored.
func WithParallelConcurrency(n int) ParallelOption {
	return func(t *ParallelTool) {
		if n > 0 {
			t.concurrency = n
		}
	}
}

// WithParallelSubagentStopHook injects the HookRunner that fires SubagentStop when a
// branch run finishes (best-effort; nil disables it).
func WithParallelSubagentStopHook(h port.HookRunner) ParallelOption {
	return func(t *ParallelTool) { t.hooks = h }
}

// WithParallelChildSessionPrefix sets the prefix used to derive child branch
// SessionIDs (default "parallel"). Child ids are of the form "<prefix>-<callID>-<i>".
func WithParallelChildSessionPrefix(p string) ParallelOption {
	return func(t *ParallelTool) { t.idPrefix = p }
}

// WithParallelJudge injects the BranchJudge used by the "judge"/"best" join strategy
// (nil disables judging — the strategy then returns a model-addressable error).
// The default build wires an engineJudge over a dedicated, tool-less read-only
// child Engine; see internal/app.
func WithParallelJudge(j BranchJudge) ParallelOption {
	return func(t *ParallelTool) { t.judge = j }
}

// WithWinnerReaper injects a bounded PreservedForkStore that caps how many
// PRESERVED winner forks (join=first / join=judge) survive at once: a new winner
// beyond the cap reaps the oldest. nil (the default) preserves winner forks
// indefinitely. See NewLRUForkReaper for the default bounded implementation.
func WithWinnerReaper(s PreservedForkStore) ParallelOption {
	return func(t *ParallelTool) { t.winnerReaper = s }
}

// WithParallelStore injects the optional session store each branch's child session is
// best-effort persisted to after its run (issue #30), so the PULL InspectSubagent tool
// can later load a branch's transcript by the "branch id:" line the Parallel result
// surfaces. Nil disables persistence. Mirrors SubagentTool's WithSubagentStore /
// persistChild discipline exactly (the closest structural sibling — both take a
// *session.Session): the save is advisory and a failure is swallowed (no diagnostic).
// Branch ids share the SHARED store with subagent
// and team-member ids on a DISJOINT prefix ("parallel-"), so no collision engineering is
// needed. The tool consumes the port.SessionStore interface, never a concrete adapter, so
// no layering rule is crossed.
func WithParallelStore(store port.SessionStore) ParallelOption {
	return func(t *ParallelTool) { t.store = store }
}

// WithParallelEngineFactory injects the composition-supplied factory that mints a per-branch
// child engine on an OPT-IN model-router-classified model (ADR 0034). It is the EXACT shape
// WithSubagentEngineFactory takes (func(model string)(*Engine,bool)); the factory re-derives
// the override branch engine's Compactor/TokenCounter/Env.Model/ContextWindow for the routed
// model through the contamination-safe per-provider path (never a clone-and-swap). nil (the
// default) disables per-branch routing — every branch runs on the shared childEngine,
// byte-identical to today. engine/agent stays model-string-only: the factory takes an opaque
// model id and composition owns the category→model→engine mapping.
func WithParallelEngineFactory(f func(model string) (*Engine, bool)) ParallelOption {
	return func(t *ParallelTool) { t.engineFactory = f }
}

// NewParallelTool constructs the Parallel fan-out tool over a pre-built child *Engine
// and a WorkspaceForker. The composition root builds childEngine with the SCOPED
// child catalog and a non-interactive policy (see NewSubagentTool's guidance); the
// child catalog MUST NOT contain Parallel or Subagent (so a branch cannot fan out
// further). childEngine and forker must be non-nil; NewParallelTool panics otherwise,
// because a Parallel tool with no child loop or no isolation seam is a composition-root
// programming error.
func NewParallelTool(childEngine *Engine, forker tool.WorkspaceForker, opts ...ParallelOption) tool.Tool {
	if childEngine == nil {
		panic("agent: NewParallelTool requires a non-nil child Engine")
	}
	if forker == nil {
		panic("agent: NewParallelTool requires a non-nil WorkspaceForker")
	}
	t := &ParallelTool{
		childEngine: childEngine,
		forker:      forker,
		limits:      defaultChildLimits,
		childMode:   session.ModeDefault,
		maxBranches: defaultMaxBranches,
		concurrency: defaultParallelConcurrency,
		idPrefix:    strings.TrimSuffix(ParallelSessionPrefix, "-"), // the exported convention is the source
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Spec returns the model-facing specification for the Parallel tool.
func (*ParallelTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: parallelToolName,
		Description: "Fan out several independent tasks (up to 8) to run in PARALLEL, each in " +
			"its own isolated forked workspace and fresh context, then join their results into " +
			"one summary. Use to explore multiple approaches at once or to split independent " +
			"work. For a single task just do it yourself or use Subagent; for work where the " +
			"branches must coordinate or share state, use Team — Parallel branches are fully " +
			"independent and never communicate. " +
			"Each branch runs in an isolated fork, so a branch may IMPLEMENT by editing, " +
			"writing files, and running shell commands (Bash), not just explore — its changes " +
			"land in its own fork and never touch this workspace (Bash runs with the fork as its " +
			"working directory). " +
			"Each branch cannot see this conversation or the other branches, so make every " +
			"task in `tasks` self-contained (use `shared` for common context). " +
			"`join` controls the result: 'all' (default) returns every branch summary so YOU " +
			"pick; 'first' returns the first branch that SUCCEEDS and cancels the rest (only for " +
			"interchangeable branches); 'judge'/'best' has an LLM pick the single best branch " +
			"against `criteria`. Branches do NOT auto-merge — forked workspace paths are reported " +
			"so you can inspect or merge them yourself; for 'first'/'judge' the WINNER's fork is " +
			"PRESERVED (not torn down) so its changes survive for inspection. " +
			"Each branch reports a `branch id:` line you can pass to InspectSubagent to pull " +
			"that branch's bounded transcript (e.g. to debug a failed or not-selected branch).",
		Schema: parallelSchema,
	}
}

// ReadOnly reports that the Parallel tool is read-only with respect to the PARENT's
// shared workspace, which lets the parent dispatcher run it concurrently with
// other read-only tools (read-parallel / mutate-serial; see dispatch.go).
//
// INVARIANT — this is the same invariant SubagentTool documents, but Parallel makes it
// strictly safer: every child branch runs in its OWN forked workspace, never the
// shared base. So the child's filesystem-mutating tools (Edit / Write) land in the
// isolated fork and CANNOT race on, or mutate, the parent's shared base. Bash is
// now workspace-aware (see app.buildParallelChildEngine): BashTool.Execute passes the
// per-branch forked Workspace.Root() to its CommandRunner as the working directory,
// so a branch's Bash runs in its OWN fork — its DEFAULT cwd is the fork, not the
// shared parent base. (Residual: unlike path-scoped Edit/Write, Bash can still
// escape its cwd via absolute paths or `cd`; that is the inherent Bash trust model,
// the same as the main session. What the fix guarantees is that no ACCIDENTAL
// shared-base mutation happens — a branch's relative-path Bash lands in the fork.)
// That is why ReadOnly() can safely return true even for mutating (Edit/Write/Bash)
// children — for the SAME reason SubagentTool.ReadOnly() stays true: each tool isolates
// its mutating child so the child's writes never touch the shared parent base.
// Isolation, not catalog read-only-ness, is the boundary (after Phase 2 a Subagent child
// with Bash runs in its OWN git worktree exactly as a Parallel branch runs in its own
// force-copy). The remaining distinction is only WHICH tools the child gets: a Parallel
// branch keeps Edit/Write (it is meant to IMPLEMENT in its fork), while a Subagent child
// drops them and is shell-only (a read-only explorer that may run git/build/test but
// cannot edit the project).
func (*ParallelTool) ReadOnly() bool { return true }

// branchResult is the joined outcome of one branch.
type branchResult struct {
	index      int
	label      string
	childRoot  string
	summary    string
	failed     bool
	failReason string

	// childID is this branch's child SESSION id ("parallel-<callID>-<i>"), surfaced
	// VERBATIM as the result text's "branch id:" line so the parent model can pull the
	// branch's transcript via InspectSubagent (issue #30; the runtime-discoverability
	// axis, mirroring Subagent's agentId trailer and the Team result's "Team id:" line).
	// It is the same id the branchEmitter projects as the wire ChildID (D16) and the id
	// WithParallelStore persists under — one id scheme (childSessionID), no divergence.
	// Empty only in the zero-value-emitter unit tests, which never persist. Not a content
	// field: it is prefix+callID+index, never any branch summary/output (gauntlet #7).
	childID string

	// usage is the branch child run's cumulative token accounting, captured for the
	// run-total carried on parallel.end. Not serialized into the result text;
	// orchestration/observability state only.
	usage session.Usage

	// cleanup tears down this branch's fork. Ownership is LIFTED out of runBranch's
	// old defer (see runBranch) so Execute decides, per strategy, which forks to
	// tear down and which to PRESERVE: for "all" every fork is cleaned (today's
	// behaviour); for "first"/"judge" every LOSER is cleaned but the WINNER's
	// cleanup is dropped (never called) so its tree survives. nil when the fork
	// failed before producing a tree. Not serialized — orchestration state only.
	cleanup func() error
}

// runCleanup invokes a branch's fork cleanup if present (idempotent-friendly; the
// forker's cleanup tolerates an already-removed child).
func (r branchResult) runCleanup() {
	if r.cleanup != nil {
		_ = r.cleanup()
	}
}

// preserveWinner hands a winning branch's PRESERVED fork to the bounded reaper (if
// one is wired) so the oldest preserved fork can be LRU-reaped once the cap is
// exceeded. With no reaper the winner's cleanup is simply not called (the original
// behaviour: the fork survives for the operator and is never auto-deleted). The
// winner's fork is the deliverable either way; the reaper only bounds how many
// survive at once.
func (t *ParallelTool) preserveWinner(w branchResult) {
	if t.winnerReaper != nil {
		t.winnerReaper.Preserve(w.childRoot, w.cleanup)
	}
}

// Execute forks N isolated child workspaces (one per task), runs a child loop in
// each IN PARALLEL bounded by the worker limit, drains every child stream
// internally, cleans up each fork, and returns ONE ToolResult that joins all
// branch summaries. The whole operation is bounded by the parent ctx: cancelling
// it cancels every in-flight branch. A branch that fails is reported in the joined
// summary without aborting the others; the call returns a harness-level error only
// for a setup failure (invalid args / cap exceeded).
func (t *ParallelTool) Execute(ctx context.Context, call session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
	return t.run(ctx, call, ws, nil, parentCaps{})
}

// ReadOnly stays true (each branch isolates its writes); see ReadOnly. ParallelTool
// implements childCapableTool so a branch's Bash ask can be surfaced to the human
// (interactive) or auto-denied with the accurate message (headless) — every branch is
// isolated, so most such asks auto-approve via A2 first.

// ExecuteWithParent is the childCapableTool seam: it runs Parallel like Execute but threads
// the PARENT's caps (interactivity + surface back-channel) into each branch's posture AND
// the parent emit closure — so a Parallel run projects its REDACTED parallel.* group
// observability stream (start / per-branch / end) onto the parent event channel, exactly
// as Subagent projects subagent.*.
func (t *ParallelTool) ExecuteWithParent(ctx context.Context, call session.ToolCall, ws tool.Workspace, emit func(session.Event), caps parentCaps) (session.ToolResult, error) {
	return t.run(ctx, call, ws, emit, caps)
}

// branchEmitter carries the run-level emit closure + the parent Parallel call id so the
// fan-out helpers can bracket each branch and the run with redacted parallel.* events
// WITHOUT spraying two params through every helper (the plan's Q1 carrier). A nil emit
// (the plain Execute / silent path) makes every method a no-op, so a Parallel call with
// no observer behaves exactly as before.
type branchEmitter struct {
	emit         func(session.Event)
	parentCallID string
	// childID derives branch i's child SESSION id ("parallel-<callID>-<i>") for the
	// branch_start/branch_end events, so a client can ADDRESS a branch (CancelChild)
	// without deriving the id grammar (D16). It is deterministic (childSessionID), so
	// even a never-ran branch (fork-failed / cancelled-before-start) carries it. nil
	// (zero-value emitter in tests) leaves ChildID empty.
	childID func(i int) string
}

// branchChildID resolves branch i's child session id via the childID closure
// (empty when unwired — the zero-value emitter).
func (e branchEmitter) branchChildID(i int) string {
	if e.childID == nil {
		return ""
	}
	return e.childID(i)
}

// active reports whether emission is wired (an observing parent supplied a closure).
func (e branchEmitter) active() bool { return e.emit != nil }

// start emits the run-level parallel.start event.
func (e branchEmitter) start(join string, branchCount int) {
	if !e.active() {
		return
	}
	e.emit(session.Event{Type: session.EvParallelStart, Parallel: &session.ParallelPayload{
		ParentCallID: e.parentCallID,
		Join:         join,
		BranchCount:  branchCount,
	}})
}

// branchStart emits a parallel.branch{branch_start} event for branch i. routedCategory
// and routedModel are the OPT-IN model router's bare-metadata classification for this
// branch (both empty when the router was off, missed, or the branch never started); they
// ride the branch_start event exactly as SubagentPayload's routed fields ride subagent.start.
// model is the concrete MODEL id this branch ACTUALLY runs on (issue #112, ADR 0035),
// independent of whether the router fired — inherited default or routed. When routed,
// model == routedModel.
func (e branchEmitter) branchStart(i int, goal, routedCategory, routedModel, model string) {
	if !e.active() {
		return
	}
	e.emit(session.Event{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{
		ParentCallID:   e.parentCallID,
		Kind:           session.ParallelBranchStart,
		BranchIndex:    i,
		ChildID:        e.branchChildID(i),
		BranchLabel:    branchLabel(i),
		Goal:           truncateGoal(strings.TrimSpace(goal)),
		RoutedCategory: routedCategory,
		RoutedModel:    routedModel,
		Model:          model,
	}})
}

// branchEnd emits a parallel.branch{branch_end} event for a finished branch. It reads
// ONLY redacted metadata off the branchResult (never res.summary / res.failReason — the
// branch's content stays out of the stream; gauntlet #7).
func (e branchEmitter) branchEnd(res branchResult, stop session.StopReason, usage session.Usage, toolCount int, dur time.Duration) {
	if !e.active() {
		return
	}
	e.emit(session.Event{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{
		ParentCallID: e.parentCallID,
		Kind:         session.ParallelBranchEnd,
		BranchIndex:  res.index,
		ChildID:      e.branchChildID(res.index),
		ToolCount:    toolCount,
		Failed:       res.failed,
		Workspace:    res.childRoot,
		Stop:         stop,
		Usage:        usage,
		DurationMs:   dur.Milliseconds(),
	}})
}

// end emits the run-level parallel.end event after the winner is resolved. winner is a
// real branchResult.index (join=first/judge) or -1 (join=all / none-succeeded); ws is the
// winner's preserved fork root ("" when there is no winner).
func (e branchEmitter) end(join string, branchCount, winner int, winnerWorkspace string, usage session.Usage, stop session.StopReason) {
	if !e.active() {
		return
	}
	e.emit(session.Event{Type: session.EvParallelEnd, Parallel: &session.ParallelPayload{
		ParentCallID:    e.parentCallID,
		Join:            join,
		BranchCount:     branchCount,
		Winner:          winner,
		WinnerWorkspace: winnerWorkspace,
		Usage:           usage,
		Stop:            stop,
	}})
}

// branchTool builds the per-branch translation closure handed to drainChildObserved.
// drainChildObserved (the SINGLE redaction chokepoint, shared with Subagent) emits ONLY
// EvSubagentTool events carrying name+error+count; this closure RE-TAGS each into a
// parallel.branch{branch_tool} event for branch i, copying only those already-redacted
// fields — it opens NO new content path. branch_start/branch_end are emitted by the
// Parallel run itself (it owns the branch label/goal/workspace/failed metadata that
// drainChildObserved does not know about), so this closure handles exactly the tool kind.
func (e branchEmitter) branchTool(i int) func(session.Event) {
	if !e.active() {
		return nil
	}
	return func(ev session.Event) {
		if ev.Type != session.EvSubagentTool || ev.Subagent == nil {
			return
		}
		e.emit(session.Event{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{
			ParentCallID: e.parentCallID,
			Kind:         session.ParallelBranchTool,
			BranchIndex:  i,
			ToolName:     ev.Subagent.ToolName,
			IsError:      ev.Subagent.IsError,
			ToolCount:    ev.Subagent.ToolCount,
		}})
	}
}

// run is the shared implementation behind Execute (emit nil) and ExecuteWithParent.
func (t *ParallelTool) run(ctx context.Context, call session.ToolCall, ws tool.Workspace, emit func(session.Event), caps parentCaps) (session.ToolResult, error) {
	var args parallelArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "Parallel: "+msg), nil
	}
	tasks := nonEmptyTasks(args.Tasks)
	if len(tasks) == 0 {
		return session.NewToolError(call.ID, "Parallel: 'tasks' is required and must contain at least one non-empty prompt"), nil
	}
	if len(tasks) > t.maxBranches {
		return session.NewToolError(call.ID, fmt.Sprintf(
			"Parallel: %d tasks exceeds the maximum fan-out of %d; split the work or batch it",
			len(tasks), t.maxBranches)), nil
	}

	join := normalizeJoin(args.Join)
	switch join {
	case joinAll, joinFirst, joinJudge:
		// ok
	default:
		return session.NewToolError(call.ID, fmt.Sprintf(
			"Parallel: unknown join strategy %q; want all|first|judge|best", strings.TrimSpace(args.Join))), nil
	}
	if join == joinJudge && t.judge == nil {
		return session.NewToolError(call.ID,
			"Parallel: judge selection is unavailable (no judge wired); use join=all and pick a branch yourself"), nil
	}

	be := branchEmitter{emit: emit, parentCallID: string(call.ID),
		childID: func(i int) string { return string(t.childSessionID(call.ID, i)) }}
	be.start(join, len(tasks))

	switch join {
	case joinFirst:
		return t.executeFirst(ctx, call.ID, tasks, args.Shared, ws, be, caps), nil
	case joinJudge:
		return t.executeJudge(ctx, call.ID, tasks, args.Shared, args.Criteria, ws, be, caps), nil
	default: // joinAll
		// Today's behaviour, byte-for-byte: run every branch, clean EVERY fork,
		// return the index-sorted per-branch summary.
		results := t.runBranches(ctx, call.ID, tasks, args.Shared, ws, be, caps)
		for _, r := range results {
			r.runCleanup()
		}
		// join=all has no winner: Winner=-1, no preserved workspace.
		be.end(join, len(tasks), -1, "", sumBranchUsage(results), session.StopReason(""))
		return session.NewToolResult(call.ID, joinBranches(results)), nil
	}
}

// executeFirst runs every branch, returns the FIRST to succeed by completion
// order, cancels the remaining in-flight branches, cleans every loser fork, and
// PRESERVES the winner's fork (its cleanup is dropped). With no success it
// degrades to the all-failed report (every fork cleaned).
func (t *ParallelTool) executeFirst(ctx context.Context, callID session.ToolCallID, tasks []string, shared string, ws tool.Workspace, be branchEmitter, caps parentCaps) session.ToolResult {
	// A per-call child context so we can cancel the losers the instant a winner
	// finishes, without disturbing the parent ctx. Cancelled in all paths.
	branchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results, winner := t.runBranchesFirst(branchCtx, cancel, callID, tasks, shared, ws, be, caps)

	if winner < 0 {
		// No branch succeeded: clean everything and report the failures.
		for _, r := range results {
			r.runCleanup()
		}
		be.end(joinFirst, len(results), -1, "", sumBranchUsage(results), session.StopReason(""))
		return session.NewToolResult(callID, joinBranches(results))
	}
	// Preserve the winner's fork; clean every loser.
	for i := range results {
		if i == winner {
			continue
		}
		results[i].runCleanup()
	}
	t.preserveWinner(results[winner])
	be.end(joinFirst, len(results), winner, results[winner].childRoot, sumBranchUsage(results), session.StopEndTurn)
	return session.NewToolResult(callID, joinFirstResult(results, winner))
}

// executeJudge runs every branch, then (when ≥2 succeeded) asks the injected
// BranchJudge to pick a winner from the branch SUMMARIES only (never transcripts).
// Degradations: 0 successes → all-failed report (all forks cleaned); exactly 1
// success → that branch wins with no judge call. The winner's fork is PRESERVED;
// every loser's fork is cleaned. A misbehaving judge falls back to the first
// successful branch — Parallel never hard-fails because the judge erred.
func (t *ParallelTool) executeJudge(ctx context.Context, callID session.ToolCallID, tasks []string, shared, criteria string, ws tool.Workspace, be branchEmitter, caps parentCaps) session.ToolResult {
	results := t.runBranches(ctx, callID, tasks, shared, ws, be, caps)

	// Successful branches in index order (so "first successful" is deterministic).
	var succeeded []int
	for i := range results {
		if !results[i].failed {
			succeeded = append(succeeded, i)
		}
	}

	if len(succeeded) == 0 {
		for _, r := range results {
			r.runCleanup()
		}
		be.end(joinJudge, len(results), -1, "", sumBranchUsage(results), session.StopReason(""))
		return session.NewToolResult(callID, joinBranches(results))
	}

	winner := succeeded[0]
	rationale := ""
	switch {
	case len(succeeded) == 1:
		rationale = "only one branch succeeded; selected without judging"
	default:
		winner, rationale = t.judgeWinner(ctx, results, succeeded, criteria)
	}

	for i := range results {
		if i == winner {
			continue
		}
		results[i].runCleanup()
	}
	t.preserveWinner(results[winner])
	be.end(joinJudge, len(results), winner, results[winner].childRoot, sumBranchUsage(results), session.StopEndTurn)
	return session.NewToolResult(callID, joinJudgeResult(results, winner, rationale))
}

// judgeWinner asks the injected judge to pick among the SUCCESSFUL branches. It
// maps the judge's position-within-candidates back to the real branchResult.index
// and falls back to the first successful branch on any judge error / out-of-range
// verdict (the judge sees only summaries — never transcripts — preserving
// isolation).
func (t *ParallelTool) judgeWinner(ctx context.Context, results []branchResult, succeeded []int, criteria string) (winner int, rationale string) {
	candidates := make([]BranchSummary, 0, len(succeeded))
	for _, idx := range succeeded {
		candidates = append(candidates, BranchSummary{
			Label:   results[idx].label,
			Summary: results[idx].summary,
			Failed:  false,
		})
	}
	pos, why, err := t.judge.Judge(ctx, candidates, criteria)
	if err != nil || pos < 0 || pos >= len(succeeded) {
		return succeeded[0], "judge unavailable or returned an invalid verdict; selected the first successful branch"
	}
	return succeeded[pos], why
}

// runBranches forks and runs every branch in parallel under a worker-limited
// semaphore, returning the per-branch results in branch order. The caller owns
// cleanup of every returned branchResult.cleanup (lifted out of runBranch).
func (t *ParallelTool) runBranches(ctx context.Context, callID session.ToolCallID, tasks []string, shared string, ws tool.Workspace, be branchEmitter, caps parentCaps) []branchResult {
	results := make([]branchResult, len(tasks))
	sem := make(chan struct{}, t.concurrency)
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(i int, task string) {
			defer wg.Done()
			results[i] = t.launchBranch(ctx, sem, callID, i, task, shared, ws, be, caps)
		}(i, task)
	}
	wg.Wait()
	return results
}

// launchBranch is the shared per-branch goroutine body behind runBranches and
// runBranchesFirst: it mints the branch's OWN cancelable context, registers the
// branch in the parent's child-run registry BEFORE the worker-slot wait (so a
// branch QUEUED on the semaphore is already client-cancellable — the cancel
// unblocks the slot select), acquires a slot, runs the branch, and lands its
// terminal stop in the registry. Every join mode gets the per-branch cancel this
// way; join=first's shared loser-cancel still works because the per-branch ctx
// derives from the strategy ctx. The JUDGE's own run is NOT a registered child
// (it is short and tool-less; the whole-run cancel covers it) — a documented v1
// limitation.
func (t *ParallelTool) launchBranch(ctx context.Context, sem chan struct{}, callID session.ToolCallID, i int, task, shared string, ws tool.Workspace, be branchEmitter, caps parentCaps) branchResult {
	branchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	childID := t.childSessionID(callID, i)
	caps.registerChildRun(childID, childFamilyParallelBranch, branchLabel(i), cancel, false)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-branchCtx.Done():
		caps.finishChildRun(childID, session.StopCancelled)
		return cancelledBeforeStart(i, be, caps.childWasClientCancelled(childID))
	}
	caps.startChildRun(childID)
	res, stop := t.runBranch(branchCtx, callID, i, task, shared, ws, be, caps)
	caps.finishChildRun(childID, stop)
	return res
}

// cancelledBeforeStart records the branchResult for a branch whose context was already
// cancelled before it acquired a worker slot, and emits its bracketing parallel.branch
// start+end so EVERY branch is represented on the observability stream (no missing
// event), even one that never ran — the adversarial "branch never finishes" case.
// clientCancelled distinguishes a per-branch CancelChild (the user killed THIS branch
// while it queued) from a run-level/parent cancel, mirroring runBranch's failReason
// split (A8).
func cancelledBeforeStart(i int, be branchEmitter, clientCancelled bool) branchResult {
	reason := "cancelled before start"
	if clientCancelled {
		reason = "cancelled by user before start"
	}
	// Source the branch id off the emitter's deterministic childID closure (the SAME
	// id scheme as runBranch's childSessionID): non-empty in production, empty in the
	// zero-value-emitter unit tests — acceptable since a never-started branch created no
	// session and so is never persisted/inspectable.
	res := branchResult{index: i, label: branchLabel(i), failed: true, failReason: reason, childID: be.branchChildID(i)}
	// A branch cancelled before it ever started was never routed, so the routed metadata
	// is empty (a router miss and a never-routed branch are indistinguishable on the wire
	// — both carry no category/model, which is correct: the branch ran on nothing).
	be.branchStart(i, "", "", "", "")
	be.branchEnd(res, session.StopCancelled, session.Usage{}, 0, 0)
	return res
}

// sumBranchUsage totals every branch's child-run usage for the run-level parallel.end
// (the GROUP's cumulative cost). It reads only the redacted usage scalars.
func sumBranchUsage(results []branchResult) session.Usage {
	var total session.Usage
	for _, r := range results {
		total = total.Add(r.usage)
	}
	return total
}

// runBranchesFirst runs every branch in parallel under the worker semaphore and
// signals each completion on a channel so the orchestrator can pick the FIRST
// successful branch by completion order and cancel the losers (via cancel). It
// always waits for every goroutine to exit before returning, so no branch goroutine
// outlives the call and every fork is captured in results for cleanup (no leak):
// a loser cancelled mid-flight still returns its (possibly partial) branchResult
// with its cleanup attached. The returned winner is the index of the first
// successful branch, or -1 if none succeeded.
func (t *ParallelTool) runBranchesFirst(ctx context.Context, cancel context.CancelFunc, callID session.ToolCallID, tasks []string, shared string, ws tool.Workspace, be branchEmitter, caps parentCaps) ([]branchResult, int) {
	results := make([]branchResult, len(tasks))
	sem := make(chan struct{}, t.concurrency)
	done := make(chan int, len(tasks)) // carries the index of each finished branch
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(i int, task string) {
			defer wg.Done()
			defer func() { done <- i }()
			results[i] = t.launchBranch(ctx, sem, callID, i, task, shared, ws, be, caps)
		}(i, task)
	}

	// Wait for the first SUCCESS by completion order, then cancel the rest. We keep
	// reading `done` until every branch has reported so wg.Wait below cannot block
	// behind an unread send (done is buffered to len(tasks), so this is also safe).
	winner := -1
	for range tasks {
		i := <-done
		if winner < 0 && !results[i].failed {
			winner = i
			cancel() // tell the still-in-flight losers to stop
		}
	}
	wg.Wait()
	return results, winner
}

// normalizeJoin trims, lower-cases, maps "" → "all" and "best" → "judge".
func normalizeJoin(join string) string {
	j := strings.ToLower(strings.TrimSpace(join))
	switch j {
	case "":
		return joinAll
	case joinBest:
		return joinJudge
	default:
		return j
	}
}

// runBranch forks an isolated workspace, runs one child loop in it, drains the
// child stream internally, fires SubagentStop, and returns the branch's joined
// result WITH its fork cleanup attached (res.cleanup), plus the branch's terminal
// stop reason (which launchBranch lands in the child-run registry). Cleanup
// ownership is deliberately LIFTED out of this function: unlike the original
// (which deferred cleanup here), the caller (Execute and its strategy helpers)
// decides which forks to tear down and which to preserve, so a winning branch's
// fork can survive the call. A fork or child failure is captured in the result,
// never propagated as a harness error (one failing branch must not kill the
// others).
func (t *ParallelTool) runBranch(ctx context.Context, callID session.ToolCallID, i int, task, shared string, ws tool.Workspace, be branchEmitter, caps parentCaps) (branchResult, session.StopReason) {
	label := branchLabel(i)
	// childID is the branch's child session id, set up front so EVERY terminal (incl.
	// fork-failed / errored / cancelled) carries the discoverable "branch id:" — the
	// same id the registry/emitter use and WithParallelStore persists under.
	res := branchResult{index: i, label: label, childID: string(t.childSessionID(callID, i))}

	// OPT-IN model router (ADR 0034): classify this branch's composed prompt ONCE (each
	// branch routes at most once — this is the only call site, on the per-branch
	// goroutine) and select the engine the branch runs on. routedCategory/routedModel are
	// bare metadata for branch_start; branchEngine is the routed override engine on a hit
	// or the shared childEngine on a miss/unwired (fail-soft, byte-identical when off). The
	// routeTask closure holds the per-run breaker mutex, so the concurrent branch
	// classifications + the classifier-usage fold into the parent session are serialised.
	prompt := composePrompt(shared, task)
	routedCategory, routedModel := t.maybeRouteBranchModel(ctx, caps, prompt)
	branchEngine := t.childEngine
	if routedModel != "" && t.engineFactory != nil {
		if eng, found := t.engineFactory(routedModel); found && eng != nil {
			branchEngine = eng
		}
	}

	// Bracket the branch on the observability stream: branch_start carries the
	// (truncated, model-authored) goal + the routed metadata; branch_end (below) carries
	// the redacted terminal metadata. A fork-failed branch still gets its branch_end so
	// EVERY branch is represented (no missing event).
	be.branchStart(i, prompt, routedCategory, routedModel, branchEngine.Model())
	start := time.Now()

	// The Parallel branch forker is force-copy (copyTree carries the parent's dirty
	// state verbatim), so the degraded-fork advisory is never set on this path —
	// discard it. (A mutating branch is not the read-only-overlay case.)
	child, cleanup, _, err := t.forker.Fork(ctx, ws, label)
	if err != nil {
		res.failed = true
		res.failReason = fmt.Sprintf("fork failed: %v", err)
		be.branchEnd(res, session.StopError, session.Usage{}, 0, time.Since(start))
		return res, session.StopError
	}
	res.cleanup = cleanup
	res.childRoot = child.Root()

	childSess := session.New(
		t.childSessionID(callID, i),
		t.childMode,
		child.Root(),
		t.limits,
		time.Now(),
	)

	run := branchEngine.Run(ctx, childSess, child, prompt)
	// A Parallel branch always runs in its OWN isolated fork, so its Bash asks are eligible
	// for the A2 worktree-safe auto-approve; the parent caps carry surface/headless
	// posture (threaded from Execute → runBranches → runBranch). We REUSE
	// drainChildObserved — the SINGLE redaction chokepoint shared with Subagent — and hand
	// it a per-branch translation closure (be.branchTool) that RE-TAGS its redacted
	// subagent.tool emit into a parallel.branch{branch_tool}. A nil closure (silent path)
	// makes drainChildObserved discard intermediate events exactly as drainChild did.
	final, stop, usage, toolCount := drainChildObserved(
		run, be.branchTool(i),
		string(callID), string(childSess.ID),
		childPosture{isolated: true, caps: caps, role: label,
			// childID is the branch SESSION id ("parallel-<callID>-<i>" — NOT the branch
			// label role carries), the uniform ask-ownership/cancel handle (A6): a
			// CancelChild for this id retracts the branch's surfaced asks and cancels
			// the per-branch ctx launchBranch registered.
			childID:  string(childSess.ID),
			askLabel: fmt.Sprintf("parallel branch %q", label)},
	)
	res.usage = usage
	t.fireSubagentStop(ctx, childSess)
	// Best-effort persist the branch's child session so InspectSubagent can later load
	// its transcript by the "branch id:" the result surfaces (issue #30). Every join
	// mode funnels through runBranch, so this covers all modes; the cancelled-before-start
	// path never created a session (nothing to persist, matching the Subagent pre-start
	// abort). ctx is passed as-is: the shipped stores ignore it on Save (persistMember
	// discipline), so a cancelled branch stays inspectable — intentional residual.
	t.persistBranch(ctx, childSess)

	switch stop {
	case session.StopError:
		res.failed = true
		if final == "" {
			final = "branch failed without producing a summary"
		}
		res.failReason = final
	case session.StopCancelled:
		res.failed = true
		// "cancelled" is the run-level/parent cancel; a per-branch CancelChild reads
		// "cancelled by user" so the joined report attributes the kill correctly (A8).
		res.failReason = "cancelled"
		if caps.childWasClientCancelled(childSess.ID) {
			res.failReason = "cancelled by user"
		}
		res.summary = final
	default:
		if final == "" {
			final = "(branch produced no summary)"
		}
		res.summary = final
	}
	be.branchEnd(res, stop, usage, toolCount, time.Since(start))
	return res, stop
}

// maybeRouteBranchModel consults the OPT-IN semantic model router (ADR 0034) for a branch
// and returns the classified category + the ALREADY-RESOLVED concrete model id the branch
// should run on (both empty when not routed). It mirrors maybeRouteModel (the Subagent
// gate): GATING — a branch has no per-call model and no agent def, so the only precondition
// is that the parent wired BOTH the routeTask closure AND a branch engine factory (an
// unwired factory means there is no way to mint the routed engine, so classifying would be
// wasted spend). FAIL-SOFT: a router miss (ok=false) returns empty strings and the caller
// inherits the shared childEngine. ctx is the branch's run ctx, threaded to routeTask so a
// Run.Cancel propagates into the classifier turn (issue #94). routeTask is nil on a child
// run (no nesting — a Parallel branch child has no Parallel tool) and when no router is
// wired (the byte-identical default).
func (t *ParallelTool) maybeRouteBranchModel(ctx context.Context, caps parentCaps, prompt string) (category, model string) {
	if t.engineFactory == nil || caps.routeTask == nil {
		return "", ""
	}
	if cat, m, ok := caps.routeTask(ctx, prompt); ok {
		return cat, strings.TrimSpace(m)
	}
	return "", ""
}

// fireSubagentStop runs the SubagentStop hook for a finished branch run
// (best-effort; mirrors SubagentTool.fireSubagentStop).
func (t *ParallelTool) fireSubagentStop(ctx context.Context, child *session.Session) {
	fireNotify(ctx, t.hooks, governance.HookEvent{
		Phase:     governance.PhaseSubagentStop,
		SessionID: string(child.ID),
	})
}

// persistBranch best-effort saves a branch's child session to the injected store so the
// InspectSubagent tool can later load its transcript by the "branch id:" line the
// Parallel result surfaces (issue #30). A nil store disables persistence; a save failure
// is advisory and swallowed. It mirrors SubagentTool.persistChild / WithSubagentStore
// exactly (the closest structural sibling — both take a *session.Session) — shared store,
// disjoint prefix.
func (t *ParallelTool) persistBranch(ctx context.Context, child *session.Session) {
	if t.store == nil {
		return
	}
	_ = t.store.Save(ctx, child)
}

// childSessionID derives a stable, unique id for a branch's child session.
func (t *ParallelTool) childSessionID(callID session.ToolCallID, i int) session.SessionID {
	return session.SessionID(fmt.Sprintf("%s-%s-%d", t.idPrefix, callID, i))
}

// nonEmptyTasks trims and drops blank task prompts, preserving order.
func nonEmptyTasks(tasks []string) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task) != "" {
			out = append(out, task)
		}
	}
	return out
}

// composePrompt prepends an optional shared instruction to a branch task prompt.
func composePrompt(shared, task string) string {
	if strings.TrimSpace(shared) == "" {
		return task
	}
	return shared + "\n\n" + task
}

// branchLabel is the stable, human-meaningful label for branch i (1-based).
func branchLabel(i int) string {
	return fmt.Sprintf("branch-%d", i+1)
}

// joinBranches renders the per-branch results into a single, clearly-delimited
// summary string. Branches are sorted by index so the joined output is
// deterministic regardless of completion order. Each branch reports its status,
// its isolated workspace path (the no-auto-merge artifact), and its summary.
func joinBranches(results []branchResult) string {
	sorted := sortedByIndex(results)
	ok := countOK(sorted)

	var b strings.Builder
	fmt.Fprintf(&b, "Parallel joined %d branch(es): %d succeeded, %d failed.\n",
		len(sorted), ok, len(sorted)-ok)
	for _, r := range sorted {
		b.WriteString("\n=== ")
		b.WriteString(r.label)
		if r.failed {
			b.WriteString(" [FAILED] ===\n")
			b.WriteString(r.failReason)
			b.WriteString("\n")
		} else {
			b.WriteString(" [OK] ===\n")
		}
		if r.childRoot != "" {
			fmt.Fprintf(&b, "workspace: %s\n", r.childRoot)
		}
		// The discoverable "branch id:" line (issue #30): the parent model reads it and
		// passes it to InspectSubagent to pull this branch's bounded transcript.
		if r.childID != "" {
			fmt.Fprintf(&b, "branch id: %s\n", r.childID)
		}
		if r.summary != "" {
			b.WriteString(r.summary)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// sortedByIndex returns a copy of results sorted by branch index, so rendered
// output is deterministic regardless of completion order.
func sortedByIndex(results []branchResult) []branchResult {
	sorted := make([]branchResult, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].index < sorted[b].index })
	return sorted
}

// countOK counts the branches that did not fail, for the "succeeded/failed"
// tally shared by joinBranches and joinJudgeResult.
func countOK(results []branchResult) int {
	ok := 0
	for _, r := range results {
		if !r.failed {
			ok++
		}
	}
	return ok
}

// joinFirstResult renders the join=first outcome: the winning branch's summary,
// the preserved-workspace note, and a one-line tally of the also-rans. winner is
// a real branchResult.index.
func joinFirstResult(results []branchResult, winner int) string {
	w := results[winner]
	var b strings.Builder
	fmt.Fprintf(&b, "Parallel (join=first): %s succeeded first of %d branch(es).\n", w.label, len(results))
	writeWinnerWorkspace(&b, w)
	fmt.Fprintf(&b, "\n=== %s [WINNER] ===\n", w.label)
	// The winner's discoverable "branch id:" (issue #30) — prominent so the model can
	// inspect the chosen branch's transcript via InspectSubagent.
	if w.childID != "" {
		fmt.Fprintf(&b, "branch id: %s\n", w.childID)
	}
	if w.summary != "" {
		b.WriteString(w.summary)
		b.WriteString("\n")
	}
	others := len(results) - 1
	if others > 0 {
		fmt.Fprintf(&b, "\n(%d other branch(es) cancelled or not selected.)\n", others)
	}
	// Every persisted loser is still inspectable, so surface their ids too (issue #30):
	// a "first"-strategy loser was cancelled mid-flight but its (partial) transcript is
	// persisted, and a parent debugging WHY a branch lost can pull it.
	writeOtherBranchIDs(&b, results, winner)
	return b.String()
}

// writeOtherBranchIDs renders a compact "other branch ids:" line listing every
// non-winner branch's discoverable child id (issue #30), index-sorted for determinism,
// so a parent can inspect a rejected/cancelled branch's persisted transcript. Branches
// with no id (never-started, never-persisted) are omitted.
func writeOtherBranchIDs(b *strings.Builder, results []branchResult, winner int) {
	var ids []string
	for _, r := range sortedByIndex(results) {
		if r.index == winner || r.childID == "" {
			continue
		}
		ids = append(ids, r.childID)
	}
	if len(ids) > 0 {
		fmt.Fprintf(b, "other branch ids: %s\n", strings.Join(ids, ", "))
	}
}

// joinJudgeResult renders the join=judge outcome: the winner's summary, the
// judge's rationale, the preserved-workspace note, and a compact index-sorted
// scoreboard of the not-selected branches. winner is a real branchResult.index.
func joinJudgeResult(results []branchResult, winner int, rationale string) string {
	sorted := sortedByIndex(results)
	ok := countOK(sorted)
	w := results[winner]

	var b strings.Builder
	fmt.Fprintf(&b, "Parallel (join=judge): selected %s of %d branch(es) (%d succeeded, %d failed).\n",
		w.label, len(results), ok, len(results)-ok)
	if rationale != "" {
		fmt.Fprintf(&b, "rationale: %s\n", rationale)
	}
	writeWinnerWorkspace(&b, w)
	fmt.Fprintf(&b, "\n=== %s [WINNER] ===\n", w.label)
	// The winner's discoverable "branch id:" (issue #30).
	if w.childID != "" {
		fmt.Fprintf(&b, "branch id: %s\n", w.childID)
	}
	if w.summary != "" {
		b.WriteString(w.summary)
		b.WriteString("\n")
	}

	b.WriteString("\n--- not selected ---\n")
	for _, r := range sorted {
		if r.index == winner {
			continue
		}
		// Append each rejected branch's discoverable child id (issue #30) so the model
		// can inspect ANY not-selected branch's persisted transcript, not just the winner.
		idNote := ""
		if r.childID != "" {
			idNote = fmt.Sprintf(" (branch id: %s)", r.childID)
		}
		if r.failed {
			fmt.Fprintf(&b, "%s [FAILED]%s: %s\n", r.label, idNote, firstLine(r.failReason))
		} else {
			fmt.Fprintf(&b, "%s [OK]%s: %s\n", r.label, idNote, firstLine(r.summary))
		}
	}
	return b.String()
}

// writeWinnerWorkspace renders the preserved-workspace note for a selected winner.
// LIFETIME / OWNERSHIP: the winner's fork is intentionally NOT auto-deleted — its
// contents (a branch that may have IMPLEMENTED changes in its isolated fork) are
// the deliverable. The harness does not reap it; the CALLER/OPERATOR owns it and
// must clean it up when done. There is no auto-merge to the base (that would mutate
// the parent and break ParallelTool.ReadOnly()==true); merge is a manual follow-up
// against this path.
func writeWinnerWorkspace(b *strings.Builder, w branchResult) {
	if w.childRoot != "" {
		fmt.Fprintf(b, "winner workspace (PRESERVED — not auto-deleted; yours to inspect/merge/clean): %s\n", w.childRoot)
	}
}

// firstLine returns the first non-empty line of s (trimmed), for the compact
// scoreboard in joinJudgeResult.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Compile-time assertions that ParallelTool satisfies the Tool contract and the
// childCapableTool seam (so the dispatcher threads the parent emit + caps in, and the
// redacted parallel.* observability stream flows — the dispatcher prefers
// childCapableTool over observableTool).
var (
	_ tool.Tool        = (*ParallelTool)(nil)
	_ childCapableTool = (*ParallelTool)(nil)
)
