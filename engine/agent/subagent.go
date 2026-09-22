package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// subagentToolName is the catalog name of the subagent delegation tool.
const subagentToolName = "Subagent"

// MinSubagentRunTokens is the floor applied to per-call MaxRunTokensOverride.
// The system prompt + AGENTS.md + project instructions are replayed on every
// turn, costing ~20k+ tokens on the very first turn of a typical workspace run.
// A model-supplied budget below this floor would stop the child before it can
// complete even one useful turn, which is never the model's intent. The floor is
// safe because the operator ceiling still wins via the tighten-only fold in
// effectiveMaxRunTokens: min(override, Deps.MaxRunTokens) — clamping the
// override UP to 25k never raises the effective budget above the operator bound.
//
// Exported so tests and external callers can reference the floor value without
// hard-coding the magic number.
const MinSubagentRunTokens = 25_000

// maxSubagentGoalLen caps the prompt-derived goal label forwarded on
// EvSubagentStart when no explicit description is supplied. It keeps the
// subagent card title compact and bounds how much of the (model-authored) prompt
// is echoed to the event stream.
const maxSubagentGoalLen = 60

// defaultMaxConcurrentChildren bounds how many Subagent children may run at once.
// Subagent is read-only (ReadOnly()==true), so the dispatcher runs Subagent calls
// concurrently and the model can fan MANY out in a single turn; each child consumes
// a child session + an LLM slot (and, when shell-bearing, a forked git worktree —
// disk + a `git worktree add` process), so an unbounded fan-out is real resource
// pressure. The gate bounds ALL Subagent children — forking AND forker-less — so the
// read-parallel fan-out cannot create unbounded child runs at once. The default
// mirrors the team supervisor's defaultTeamConcurrency and the fork concurrency cap.
const defaultMaxConcurrentChildren = 8

// childPersistTimeout bounds the cancel-DETACHED child snapshot save (see persistChild).
// It exists only because detaching cancellation removes the caller's ability to abort a
// wedged store; it is deliberately short, since the save is best-effort and every caller
// is on a latency path the model is waiting on.
const childPersistTimeout = 5 * time.Second

// observableTool is the agent-internal seam by which a tool may forward a
// REDACTED, allowlisted projection of its internal activity to the parent run's
// event stream WITHOUT widening the public tool.Tool interface. A tool that
// implements it is given an emit closure (bound by the dispatcher to the parent
// Run, so events are sequenced and mirrored to the sink exactly like the loop's
// own emits); a tool that does not is executed via the ordinary Execute path.
//
// The Subagent tool implements this to surface subagent.start/tool/end metadata.
// Crucially, the emit closure only sequences and channels events — it NEVER
// touches the parent's session.Conversation — so this observability is orthogonal
// to the context-isolation guarantee (gauntlet #7).
type observableTool interface {
	ExecuteObserved(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event)) (session.ToolResult, error)
}

// parentCaps carries the PARENT run's interactivity and the surface seam down to a
// subagent-spawning tool (Subagent/Team/Fork), so a child's permission ask can be SURFACED
// to the human when the parent is interactive (and auto-denied with an accurate message
// + operator diagnostic when it is headless). It is the symmetric back-channel to the
// emit closure: where emit pushes child observability UP, surfaceAsk routes a parent
// verdict back DOWN to the child Run.
//
// It is layering-clean: every field is an agent-layer closure or a plain bool; no
// adapter/server/proto type crosses. A tool that does not implement childCapableTool
// (or a nil caps) gets the legacy headless auto-deny posture, unchanged.
type parentCaps struct {
	// interactive is the PARENT run's interactivity: true when a human approver is
	// attached (the surfaced ask can be answered), false for a headless run.
	interactive bool
	// surfaceAsk registers the child Run in the parent router (so the parent's
	// Approve routes the verdict to it), records the ask's OWNERSHIP against childID in
	// the parent's child-run registry (so a CancelChild can retract it), and emits a
	// REDACTED parent EvPermissionAsk for the child's ask. It is register-then-emit:
	// registration happens before the emit so a fast verdict cannot race ahead. nil when
	// the parent installed no router (headless / no surface). The emitted ask carries
	// the SURFACED askID (the child's own askID, already parent-distinguishable).
	// childID is the child SESSION id (childPosture.childID) the ask is owned by.
	// the router auto-unregisters the askID on the routed verdict
	// (childAskRouter.route); a stale entry (child cancelled while parked) is a harmless
	// no-op against the idempotent registry, so no explicit unsurface seam is needed.
	// requester is the pre-composed, already-clamp-safe attribution phrase (e.g.
	// `subagent "fix flaky tests"`, `team member "researcher"`, `parallel branch
	// "branch-2"`) the parent frames the surfaced ask with; empty keeps the legacy
	// generic "subagent" framing.
	surfaceAsk func(askID, childID string, child *Run, ask session.PendingAsk, requester string)
	// children is the parent run's child-run registry, handed down DIRECTLY — it is
	// an agent-package type, so passing the handle has zero layering cost (unlike
	// surfaceAsk, which stays a closure because it genuinely composes router
	// registration + redaction + the parent emit). The spawning tool registers each
	// child's per-CALL cancel here (BEFORE the concurrency gate, so a queued child
	// is already cancellable), lands its terminal stop, and reads the
	// clientCancelled disambiguation — all via the nil-safe wrappers below; a later
	// iteration's SubagentStatus reads come free off the same handle. nil when the
	// tool is driven without parent caps (plain Execute/ExecuteObserved) — the
	// child then simply is not client-cancellable, unchanged behaviour.
	children *childRunRegistry
	// diag is the parent run's run-scoped diagnostics, used to emit the headless
	// auto-deny operator diagnostic (LevelInfo, tagged agent=<child identity>: the child
	// session id "subagent-<callID>" for Subagent children; the member name / fork label
	// for the others). nil → no diagnostic (NopDiagnostics-safe via the caller).
	diag port.Diagnostics
	// adjudicate, when non-nil, runs the OPT-IN automated ask reviewer over one
	// HEADLESS child ask: it binds the engine's ChildAskReviewer, this run's
	// askReviewBreaker (serialisation + the consecutive-failure circuit breaker),
	// the askReviewTimeout, and the run's hardAbort (a tearing-down run skips the
	// reviewer). isolated is the child posture's isolation bit, threaded so the
	// reviewer can be told honestly whether the command would run in a throwaway
	// worktree. The returned askReviewOutcome reports allow/deny when a verdict was
	// obtained, else flags the reason (abstention / breaker-open / reviewer
	// failure) so resolveChildAsk emits the matching diagnostic and falls through to
	// the plain headless auto-deny. nil when no reviewer is configured (the default)
	// — resolveChildAsk then behaves exactly as a headless run did before the
	// reviewer existed.
	adjudicate func(ask session.PendingAsk, isolated bool) askReviewOutcome
	// hardAbort is the parent Run's explicit unwedge signal (Run.hardAbort, fired
	// a short grace after Run.Cancel — see hardAbortGrace), handed down so a delegation tool's own
	// internal forwarding sends (the team supervisor's member→evCh forward) can give
	// up when the parent run is cancelled while its consumer stopped draining — the
	// state in which the parent's terminate paths (and so the registry seal) are
	// unreachable. nil on a zero parentCaps (plain Execute/ExecuteObserved): a nil
	// channel in a select blocks forever, which IS the correct no-abort behaviour.
	hardAbort <-chan struct{}
	// forkHistory, when non-nil, returns a DEEP COPY of the PARENT conversation
	// (session.ForkSnapshot — fresh backing array, immutable-Message elements,
	// trailing fork-call orphan stripped so it is tool-pairing-valid) for a
	// fork:true Subagent child to seed from (issue #34). It is bound by the
	// dispatcher over the parent run's session and must be CALLED SYNCHRONOUSLY on
	// the dispatch goroutine while the parent conversation is stable — Subagent
	// snapshots it in run()/startBackground BEFORE any background detach, never from
	// the detached goroutine (the parent keeps appending after detach). nil on the
	// plain Execute path (no parent session threaded) — a fork:true call then errors
	// with an honest "not supported on this run", never a silent fresh-context child.
	forkHistory func() []session.Message
	// routeTask, when non-nil, is the OPT-IN semantic model router (ADR 0031): given a
	// Subagent call's (model-authored, untrusted) task prompt it returns the chosen
	// CATEGORY label and the ALREADY-RESOLVED concrete model id to mint the child on,
	// plus ok. It is bound by the dispatcher (Engine.parentCaps) over the engine's
	// SubagentModelRouter closure + this run's router breaker, so a fan-out's classifier
	// spend is serialised and circuit-broken per run. The Subagent run() hook consults it
	// ONLY for a plain default delegation (no per-call model/agent/fork/resume) and is
	// FAIL-SOFT: ok=false → the call inherits the default explorer model unchanged. nil
	// when no router is wired (the default) or on a child run (no nesting). The returned
	// model is an opaque model string — engine/agent stays model-string-only (the layering
	// rule); composition owns aliases/slots/the cap.
	//
	// reason is the internal why on a MISS (ok=false): the classifier's missReason passed
	// through to diagnostics/event projection, or a harness-synthesised gate constant
	// (session.RoutingReasonBreakerOpen / RoutingReasonAborted) when the classifier was
	// skipped. Empty on a hit. routingReasonPayload reduces it to a closed static code before
	// it rides delegation-start events (issue #397), so arbitrary callback text cannot cross.
	//
	// The ctx is the run's ctx so a Run.Cancel propagates into the classifier turn
	// (issue #94); see SubagentModelRouter.
	routeTask func(ctx context.Context, taskPrompt string) (category, model, reason string, ok bool)
	// owner is the PARENT session's verified owner (ADR 0204 decision 4), handed
	// down so every child session (subagent-/parallel-/team-) is attributed to the
	// same principal as the session that spawned it. It is read off the parent
	// AGGREGATE, deliberately NOT off the ambient context: a child must inherit
	// the SOURCE's owner, never the identity of whichever caller happens to be
	// driving the run — that would be an ownership-laundering path. nil for an
	// OWNERLESS parent (the no-auth path), which yields an ownerless child: never
	// a fabricated one, and never a rejection.
	owner *session.Principal
	// authority is the PARENT aggregate's carried capability set. Delegation must
	// attenuate this value before it creates child runtime resources.
	authority      session.Authority
	authorityBound bool
	// parentSessionID is the PARENT session's own SessionID (review finding 2,
	// issue #368), handed down so every derived child/branch/member session id
	// is namespaced under it. A durable delegation id previously derived ONLY
	// from the provider tool-call id (session.ToolCallID) — a value the LLM API
	// supplies and does not guarantee unique across independent conversations,
	// let alone across owners. Two different top-level sessions (necessarily
	// distinct SessionIDs — session creation is already atomically
	// owner-scoped) issuing equal or adversarially-chosen call ids would
	// otherwise derive the IDENTICAL child SessionID and silently overwrite
	// each other's persisted transcript before any inspect/resume
	// authorization ever runs. Empty for a caps-less drive (plain
	// Execute/ExecuteObserved, no parent session threaded) — the legacy
	// call-id-only id, unaffected outside real dispatch.
	parentSessionID   session.SessionID
	parentIncarnation session.IncarnationID
}

// inheritOwner stamps the parent session's owner onto a freshly-minted child
// session (ADR 0204 decision 4). It is the ONE point every child family goes
// through, so subagent/parallel/team children cannot drift apart. Write-once via
// the aggregate (Session is an aggregate — never poke the field); on a fresh
// child the slot is empty so this cannot collide, and a nil owner is a no-op —
// an ownerless parent yields an ownerless child.
func (c parentCaps) inheritOwner(child *session.Session) {
	if c.owner == nil || child == nil {
		return
	}
	// The only error RestoreLabels can return is ErrOwnerAlreadySet on a DIFFERENT
	// owner; a fresh child has no owner, and a resumed child already carries this
	// same one. Either way the persisted owner stands — the write is write-once by
	// construction and must never overwrite.
	//
	// "a resumed child already carries this same one" is now an enforced invariant,
	// not merely an assumption: resolveResumeSession's callerOwnsTranscript check
	// (issue #368) refuses a resume whose loaded owner differs from the caller
	// before this is ever reached, so the DIFFERENT-owner error case above is
	// unreachable via the resume path.
	_ = child.RestoreLabels(c.owner, session.Authority{})
}

// registerChildRun is the nil-safe registration wrapper a spawning tool calls: a
// zero parentCaps (plain Execute/ExecuteObserved, no parent run threaded) makes it a
// no-op — the child then simply is not client-cancellable, unchanged behaviour.
// background marks a detached-delivery Subagent child (the other families always
// pass false).
func (c parentCaps) registerChildRun(ctx context.Context, childID session.SessionID, family childFamily, goal string, cancel context.CancelFunc, background bool) error {
	if c.children != nil {
		return c.children.registerProtected(ctx, string(childID), family, goal, cancel, background)
	}
	return nil
}

// startChildRun is the nil-safe running-state advance (the child's drive has
// genuinely started: concurrency slot held / first round driven).
func (c parentCaps) startChildRun(childID session.SessionID) {
	if c.children != nil {
		c.children.markRunning(string(childID))
	}
}

// finishChildRun is the nil-safe terminal-stop wrapper (deferred by the spawning
// tool so every exit path lands the terminal stop in the registry).
func (c parentCaps) finishChildRun(childID session.SessionID, stop session.StopReason) {
	if c.children != nil {
		c.children.markDone(string(childID), stop)
	}
}

// finishChildRunResult is finishChildRun plus the rendered terminal ToolResult a
// BACKGROUND child stores for SubagentStatus collection (A2: the sole body
// channel).
func (c parentCaps) finishChildRunResult(childID session.SessionID, stop session.StopReason, res *session.ToolResult) {
	if c.children != nil {
		c.children.markDoneResult(string(childID), stop, res)
	}
}

// attachChildOutputTail is the nil-safe background-Shell tail attach (see
// childRunRegistry.attachOutputTail): no-op without a threaded registry.
func (c parentCaps) attachChildOutputTail(childID session.SessionID, buf *tailBuffer) {
	if c.children != nil {
		c.children.attachOutputTail(string(childID), buf)
	}
}

// setChildExitCode is the nil-safe background-Shell exit-code record (see
// childRunRegistry.setExitCode): no-op without a threaded registry.
func (c parentCaps) setChildExitCode(childID session.SessionID, code int) {
	if c.children != nil {
		c.children.setExitCode(string(childID), code)
	}
}

// abortChildRun is the nil-safe PRE-START abort (the A5 state vocabulary): the
// child never started driving and its failure already surfaced inline, so its
// registry entry is removed rather than left as a done+StopNone phantom.
func (c parentCaps) abortChildRun(childID session.SessionID) {
	if c.children != nil {
		c.children.remove(string(childID))
	}
}

// releaseChildLiveness ends a long-lived team member's maintenance exclusion at
// final supervisor teardown. Team markDone means de-scheduled, not fully quiescent.
func (c parentCaps) releaseChildLiveness(childID session.SessionID) {
	if c.children != nil {
		c.children.releaseLiveness(string(childID))
	}
}

// liveBackgroundChildIDs is the nil-safe read of the currently-live background
// child ids (the gate-full fail-fast error's list — ids only, A9).
func (c parentCaps) liveBackgroundChildIDs() []string {
	if c.children == nil {
		return nil
	}
	return c.children.liveBackgroundIDs()
}

// childWasClientCancelled is the nil-safe clientCancelled read (false when no
// parent caps were threaded).
func (c parentCaps) childWasClientCancelled(childID session.SessionID) bool {
	return c.children != nil && c.children.clientCancelled(string(childID))
}

// childCapableTool is the optional seam by which a subagent-spawning tool also receives
// the parent's capabilities (interactivity + the surface back-channel). A tool that
// implements it is driven via ExecuteWithParent when the dispatcher has a parentCaps to
// pass; one that does not falls back to ExecuteObserved/Execute with the legacy headless
// posture. Subagent/Team/Fork implement it.
type childCapableTool interface {
	ExecuteWithParent(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event), caps parentCaps) (session.ToolResult, error)
}

// defaultChildLimits are the (deliberately tight) stop conditions a subagent run
// is bounded by when the caller does not override them via WithChildLimits. A
// subagent is a one-shot, focused investigation: it must not run away. These
// defaults are intentionally tighter than a typical parent session.
var defaultChildLimits = session.Limits{
	MaxTurns:               500,
	MaxToolCalls:           2000,
	MaxConsecutiveFailures: 5,
}

// DefaultChildLimits returns the default per-child/per-member stop conditions a
// Subagent tool (and a team member, via WithTeamLimits) runs under when the
// caller does not override them. The composition layer uses it as the per-field
// FALLBACK when deriving a def's session.Limits from its maxTurns/maxToolCalls:
// a zero def field inherits the matching default here, so a def that sets neither
// is bounded exactly as before.
func DefaultChildLimits() session.Limits { return defaultChildLimits }

// subagentArgs is the argument payload the model supplies when calling the Subagent tool.
// A subagent gets a single, self-contained instruction (its whole prompt — it has
// no shared context with the parent) and an optional short description used only
// for observability.
type subagentArgs struct {
	// Prompt is the full, self-contained instruction the subagent runs against.
	// Because the child has a FRESH context window, this must include everything
	// the subagent needs; it cannot see the parent conversation.
	Prompt string `json:"prompt"`
	// Description is an optional short label for the delegated task (logging/UX
	// only); it is not required and does not affect execution.
	Description string `json:"description,omitempty"`
	// Agent optionally routes the delegation to a NAMED agent definition (a
	// specialist with its own prompt/model/scoped read-only catalog). When empty,
	// the default anonymous read-only explorer runs (unchanged behaviour). An
	// unknown name returns a model-addressable error listing the valid names.
	Agent string `json:"agent,omitempty"`

	// Resume optionally CONTINUES a previously-run subagent by its persisted session id
	// (the value of the result trailer's `agentId:` line, verbatim). The child's prior
	// conversation is reloaded and Prompt becomes its next instruction; the run gets a
	// FRESH workspace fork (the original worktree is gone — a harness staleness note is
	// prepended so the child knows). Mutually exclusive with Agent AND Model (v1 resumes
	// on the default explorer engine only). Empty = start fresh (unchanged).
	Resume string `json:"resume,omitempty"`

	// MaxTurns optionally TIGHTENS the child's per-run turn limit for THIS call. It is
	// a pointer so an omitted value (nil) is distinguishable from an explicit 0; when
	// present it overrides the inherited limit only if it is LOWER (tighten-only — the
	// model may make its child stricter than the operator's bound, never looser, so a
	// per-call arg can't be used to escape the configured ceiling). A non-positive value
	// is ignored (treated as "no override").
	MaxTurns *int `json:"max_turns,omitempty"`
	// MaxToolCalls optionally TIGHTENS the child's per-run tool-call limit for THIS
	// call. Same pointer + tighten-only + non-positive-ignored semantics as MaxTurns.
	MaxToolCalls *int `json:"max_tool_calls,omitempty"`
	// TimeoutMs optionally imposes a WALL-CLOCK deadline (milliseconds) on this child
	// run via context.WithTimeout. It is a hard ceiling independent of the turn/tool
	// limits: a child that exceeds it is cancelled and the result is a time-budget tool
	// error. A non-positive value is ignored (no deadline).
	TimeoutMs *int `json:"timeout_ms,omitempty"`

	// Model optionally PINS this child to a specific provider model for THIS call
	// (cheaper for fan-out, stronger for deep analysis), overriding the inherited
	// parent/explorer model. It is an opaque provider-model string; the composition
	// root resolves it to a contamination-safe child engine via the injected engine
	// factory (so Compactor/TokenCounter/Env.Model/ContextWindow are re-derived for the
	// override model — never a clone-and-swap of the LLM on an existing engine). An
	// unknown/unroutable model is a model-addressable error. Empty = inherit. Composes
	// with `agent` (WithAgentModelEngineFactory rebuilds the specialist's scoped engine
	// on the override model, running on the def's resolved provider; the override model
	// is passed verbatim — no alias resolution — matching the model-only path's parity)
	// and with mode:"read-write" (issue #285 — WithWritableEngineFactory rebuilds the
	// WRITABLE explorer on the override model, direct-write against the parent tree).
	Model string `json:"model,omitempty"`

	// OutputSchema optionally requests STRUCTURED output: a model-authored JSON schema
	// (a SUBSET — object/array/string/number/integer/boolean/null/properties/required/
	// items/enum). When present the child is given a synthetic SubmitResult tool whose
	// parameters ARE this schema and is instructed to call it to deliver; the submitted
	// payload is validated against the schema (session.ValidateJSON) and, on a mismatch,
	// a model-visible correction is re-injected and the child re-driven, BOUNDED. The
	// validated payload becomes the Subagent result text. Omitted (the default) = today's
	// free-text behaviour, unchanged.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`

	// MaxRunTokens is the PREFERRED per-call TIGHTEN-ONLY cumulative TOKEN budget for this
	// child run (input+output — the loop-level run budget, NOT a provider single-response
	// output ceiling). Omit it to inherit the operator/engine budget, which may itself be
	// bounded or disabled. When set it rides a Run-scoped override on the SHARED child engine
	// (no fresh engine needed), folded tighten-only with the inherited budget: the lower
	// non-zero value wins, so a per-call budget can make the child stricter, never looser. A
	// non-positive value is ignored and inherits that budget. The cumulative boundary is
	// checked between turns, so an in-flight turn completes before StopBudget. Recovered or
	// partial output may be returned when available, but a best-effort summary is NOT
	// guaranteed. A resumed child's cumulative usage is preserved; it may have already spent
	// the inherited budget and stop before doing new work.
	//
	// FLOOR: the system prompt + AGENTS.md/project instructions are replayed every turn
	// (~20k+ tokens on turn 1 alone). A positive value below minSubagentRunTokens (25 000)
	// is silently raised to 25 000 so the child can complete at least one useful turn; the
	// operator ceiling still wins via the tighten-only fold.
	MaxRunTokens *int `json:"max_run_tokens,omitempty"`

	// Background detaches this child: the call returns IMMEDIATELY with a started-
	// result carrying the agentId, the child keeps driving in its own goroutine, and
	// its rendered result is stored in the parent run's child registry for collection
	// via SubagentStatus (the SOLE body channel — A2). It composes with resume/
	// output_schema/limits/timeout_ms/agent/model unchanged. Background lifetime is
	// RUN-scoped (D8): a background child still live when the parent run terminates is
	// cancelled, joined (bounded drain), and persisted — resumable in a later run.
	// Acquiring the shared childGate is FAIL-FAST for background (D12): a full gate is
	// a model-addressable error listing the live background ids, never a cross-turn
	// block the model could deadlock itself on.
	Background bool `json:"background,omitempty"`

	// Mode selects the child's workspace posture. The default ("" or "read-only")
	// runs the historical read-only explorer (no Edit/Write; a shell-bearing child's
	// worktree is discarded after the run). "read-write" runs a WRITABLE explorer
	// with Edit/Write in its catalog that runs DIRECTLY against the PARENT workspace
	// (no fork, no copy, no merge-back) — its Edit/Write/Shell mutate the real tree in
	// place, exactly as the main agent does, so its edits land immediately. There is
	// no isolation; git is the rollback layer (a crashed/cancelled child can leave
	// partial edits behind, recoverable via git diff/checkout/stash — ADR 0077).
	// Because it mutates the parent in-place, the dispatcher runs a read-write call
	// ALONE (mutate-serial, via MutatesParent), never concurrently with a sibling
	// read. Validated to the closed set {"", "read-only", "read-write"}; an unknown
	// value is a model-addressable error. read-write is REJECTED with `background`
	// (a detached child writing the parent tree after the turn advances is unsafe)
	// and with `agent`+`model` together (a v1 scope limit — a writable specialist
	// runs on its own resolved model); read-write+`agent` ALONE is supported when the
	// deployment wires the writable-specialist factory (ADR 0058), running the named
	// specialist's scoped catalog with Edit/Write against the real workspace;
	// read-write+`model` (no `agent`) runs the WRITABLE EXPLORER on that model via the
	// writable engine factory (issue #285 — and the OPT-IN router pick is honoured the
	// same way on a plain writable delegation); it COMPOSES with
	// fork/resume/output_schema/timeout_ms/limits. Under a no-filesystem
	// session there is no writable child engine wired, so read-write is a
	// model-addressable "not supported" error. Omitted (the default) = today's
	// read-only behaviour, unchanged.
	Mode string `json:"mode,omitempty"`

	// Authority optionally tightens this child's carried authority. Every field is
	// tighten-only: requests outside the derived child authority are refused.
	Authority *DelegationTightening `json:"authority,omitempty"`

	// Fork seeds this child from a DEEP COPY of the PARENT conversation (issue #34)
	// instead of an empty context, so it "continues THIS exact investigation with my
	// full context". The copied history is carried VERBATIM, not re-fenced — the fork
	// is TRUST-NEUTRAL, NOT "the parent vetted it": the main loop records tool results
	// RAW/UNFENCED (RecordToolResults appends each ToolResult straight onto the
	// conversation; fencing exists only at the team/adjudicator render boundaries),
	// so the child simply inherits the parent's EXACT raw message posture (whatever
	// fencing the parent applied travels WITH the content) while running in a
	// strictly-LESS-privileged read-only explorer sandbox — no new untrusted ingress.
	// (Re-fencing would also bust the byte-stable prompt-cache prefix the fork relies
	// on for cheapness.) It is SAME-PROVIDER only: a forked child runs on the parent's engine, so Fork is
	// MUTUALLY EXCLUSIVE with `model` (a different model could not replay the
	// parent's provider-private reasoning/phase blobs), with `agent` (a specialist
	// pins its own engine), and with `resume` (a fork inherits THIS conversation; a
	// resume continues a DIFFERENT persisted subagent). The trailing fork-call
	// orphan is stripped so the seeded history stays tool-pairing-valid; a turn-0
	// parent yields an empty snapshot and the fork degrades to a fresh-context child
	// (benign). It composes with background/output_schema/limits/timeout_ms. Omitted
	// (the default) = today's fresh, empty-context child, unchanged.
	Fork bool `json:"fork,omitempty"`
}

// DelegationTightening is the optional per-call capability reduction requested
// for a Subagent. Nil fields inherit the already-derived value.
type DelegationTightening struct {
	Tools                    []string `json:"tools,omitempty"`
	RemainingDelegationDepth *int     `json:"remaining_delegation_depth,omitempty"`
	FileSystem               *bool    `json:"filesystem,omitempty"`
	DirectWrite              *bool    `json:"direct_write,omitempty"`
}

// AgentMeta is the plain (name, description) summary of one registered agent
// definition, surfaced in the Subagent tool's Spec().Description for progressive
// disclosure. It is a layering-clean value type: the composition root translates
// the agents adapter's Registry into a []AgentMeta + a map[string]*Engine and
// injects both via WithAgentEngines, so engine/agent never imports the agents
// adapter.
type AgentMeta struct {
	// Name is the agent def's routing key (the value the model passes as `agent`).
	Name string
	// Description is the one-line summary the model uses to choose a specialist.
	Description string
	// Limits are the per-def session stop conditions the child session runs under
	// when this agent is selected. The composition root derives them from the def's
	// maxTurns/maxToolCalls (per-field falling back to the Subagent tool's default
	// limits), so a def with no limits carries the same bound as the default
	// explorer. A zero Limits value is treated as "no per-def override" — Execute
	// then uses the Subagent tool's default limits, exactly as the no-`agent` path does.
	Limits session.Limits
	// AuthorityCeiling is the resolved read-only definition tool ceiling. It is
	// honoured only when Managed is true; lower tiers can never establish a ceiling.
	AuthorityCeiling governance.CapabilitySet
	// WritableAuthorityCeiling is the corresponding direct-write ceiling used only
	// for a fresh mode:"read-write" delegation to this definition.
	WritableAuthorityCeiling governance.CapabilitySet
	Managed                  bool
}

// subagentSchema is the JSON schema the model sees for the Subagent tool's arguments. The
// `agent` property is always present (optional); the available agent NAMES are
// enumerated in the tool's Spec().Description tail (progressive disclosure), not
// baked into this schema, so the schema stays byte-stable regardless of how many
// defs are configured.
var subagentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "prompt": {
      "type": "string",
      "description": "The full, self-contained instruction for the subagent. It has a FRESH context window and cannot see this conversation, so include everything it needs — and state the expected output format of its final report (e.g. 'a bulleted list of file:line findings with a one-line conclusion')."
    },
    "description": {
      "type": "string",
      "description": "A short (3-8 word) human-readable label for this task, shown wherever the subagent's progress is displayed (e.g. 'audit auth error paths'). Recommended."
    },
    "agent": {
      "type": "string",
      "description": "Optional name of a configured specialist agent to route this delegation to (see the list in the tool description). Omit to use the default read-only explorer."
    },
    "resume": {
      "type": "string",
      "description": "Optional id of a previous subagent to RESUME (the value of the 'agentId:' line on its earlier Subagent result). The subagent continues with its full prior conversation, taking this call's prompt as its next instruction. This works whether it finished cleanly, stopped at a limit, was cancelled, or FAILED mid-task — a subagent that died on a stalled or errored provider call can be resumed to carry on from its transcript. Its CONVERSATION always survives. Its WORKSPACE is TWO separate questions. (1) WHAT SURVIVED depends on the EARLIER run: Only when the earlier run was itself mode:'read-write' are the file edits it already made still in place — that child edited your real workspace directly, so pass mode:'read-write' again to keep working on them in place. A previously read-only subagent ran in a throwaway checkout, so its file changes and build state are GONE whatever mode you pass now. (2) WHERE IT RUNS NOW follows THIS call's mode, not the earlier run's: mode:'read-write' runs DIRECTLY in your real workspace with no isolation, and read-only runs in a fresh throwaway checkout. Cannot be combined with the agent or model arguments. Omit to start a fresh subagent."
    },
    "max_turns": {
      "type": "integer",
      "description": "Optional cap on the subagent's model turns for THIS call. Tighten-only: it can make the subagent stricter than the default, never looser. Omit to use the default."
    },
    "max_tool_calls": {
      "type": "integer",
      "description": "Optional cap on the subagent's total tool calls for THIS call. Tighten-only (as max_turns). Omit to use the default."
    },
    "timeout_ms": {
      "type": "integer",
      "description": "Optional wall-clock deadline in milliseconds for the whole subagent run; if it exceeds this it is cancelled and returns a time-budget error. Omit for no deadline."
    },
    "model": {
      "type": "string",
      "description": "Optional provider model id to pin THIS subagent to (e.g. a cheaper model for wide fan-out, a stronger one for deep analysis). Omit to inherit the parent's model. Combinable with 'agent' (the specialist is rebuilt on this model) and with mode:'read-write' (a writable explorer runs on this model); NOT with 'fork' or 'resume'."
    },
    "max_run_tokens": {
      "type": "integer",
      "description": "Optional per-call TIGHTEN-ONLY override for the child's CUMULATIVE input+output run budget; this is NOT a provider output-token limit. Omit it to inherit the operator/engine budget, which may be bounded or disabled. The lower non-zero budget wins, so this can tighten the inherited budget but never loosen it. Positive values below 25 000 are raised to the 25 000 per-call floor; the inherited operator ceiling still wins. The cumulative boundary is checked between turns, so an in-flight turn completes before the child stops. Partial or recovered output may be returned when available, but a best-effort summary is NOT guaranteed. On resume, earlier cumulative usage remains spent; a child may have already exhausted the inherited budget and stop before new work."
    },
    "output_schema": {
      "type": "object",
      "description": "Optional JSON schema describing the structured result you want back. Use it when you will mechanically consume the result (e.g. comparing or aggregating several subagents' answers); omit for a free-text summary. When present, the subagent must deliver by calling a SubmitResult tool with JSON matching this schema; the validated JSON is returned as the result. Supports a subset: type/properties/required/items/enum."
    },
    "background": {
      "type": "boolean",
      "description": "Run the subagent in the BACKGROUND: this call returns immediately with its agentId and the subagent keeps working while you continue (a note tells you when it finishes). Use it for long investigations whose result you do not need before your next steps. Collect the result with SubagentStatus (pass the agentId; use wait_ms to wait on it). A background subagent still running when this run ends is CANCELLED (its transcript persists and is resumable). Omit (default false) to wait for the result inline."
    },
    "authority": {
      "type": "object",
      "description": "Optional authority tightening for this child: tools, remaining_delegation_depth, filesystem, and direct_write may only reduce your derived authority. A request that would add a tool, hop, or execution posture is refused.",
      "properties": {
        "tools": {"type": "array", "items": {"type": "string"}},
        "remaining_delegation_depth": {"type": "integer"},
        "filesystem": {"type": "boolean"},
        "direct_write": {"type": "boolean"}
      }
    },
    "fork": {
      "type": "boolean",
      "description": "Continue THIS conversation with full context in a focused child: the subagent starts from a copy of everything you have seen so far (instead of a fresh, empty context) and takes 'prompt' as its next instruction. Use it when the task needs the context you have already built up and re-describing it in 'prompt' would be wasteful. It runs on YOUR model (cannot be combined with model, agent, or resume). Omit (default false) for a fresh-context subagent that only sees 'prompt'."
    },
    "mode": {
      "type": "string",
      "enum": ["read-only", "read-write"],
      "description": "Workspace mode (default 'read-only'). 'read-write' gives the subagent the Edit and Write tools and lets it change files DIRECTLY in your workspace — exactly as you do — so its edits land immediately, with no copy or merge step. There is no isolation: a read-write subagent that crashes or goes wrong can leave partial edits in your working tree, the same as any interrupted edit; recover with git (git diff / git checkout / git stash) since your repository is the safety net. Use 'read-write' when you want a focused subagent to actually make and keep file changes (e.g. 'implement this fix and edit the files'); omit (or 'read-only') for an investigation that must not touch your files. Runs serially — never concurrently with your other tools — so it cannot race your own reads or writes. Cannot be combined with 'background' or with 'agent'+'model' together; 'read-write'+'agent' alone runs the named specialist writable (its scoped catalog + Edit/Write against your workspace); 'read-write'+'model' (no 'agent') runs the writable explorer on that model. Composes with 'fork', 'resume', 'output_schema'."
    }
  },
  "required": ["prompt"]
}`)

// SubagentTool is the subagent delegation tool (gauntlet #7). It is a tool.Tool that,
// when executed, spins up a CHILD agent loop with its own fresh Session, its own
// (tighter) Limits, and a SCOPED tool catalog — supplied by the injected child
// *Engine — runs it to completion, and returns ONLY the child's final summary
// string as a single ToolResult.
//
// Workspace: when a child forker is wired (WithChildForker — the composition root
// wires it iff the child catalog includes Shell), each child runs in its OWN isolated
// git WORKTREE (shares the base repo's `.git` ⇒ full history) so the explorer's shell
// can inspect (git log/show, cat, build, test) without its writes touching the shared
// parent workspace; the worktree is torn down after the child drains. Without a
// forker the child has no Shell and runs against the parent workspace, exactly as it
// originally did. Either way Subagent stays read-parallel-safe (see ReadOnly).
//
// Context isolation is the whole point: the parent never observes the child's
// intermediate tool.call / tool.result / message.delta events. The child's Event
// stream is drained entirely inside Execute; only the terminal result text folds
// back into the parent conversation. This keeps a noisy "search → read N files →
// summarize" investigation from bloating the main context window.
//
// The child Engine is built by the composition root (cmd/mecated, WP11) with a
// read-only explorer catalog (Read, Grep, Glob) that NEVER includes the Subagent tool
// itself — so a subagent cannot recurse — and an allow-all policy over those
// read-only tools so the child never needs to prompt a human. See NewSubagentTool.
//
// Resume: when a store is wired (WithSubagentStore), a Subagent call carrying `resume`
// CONTINUES a previously-run child by its persisted id (the result trailer's `agentId:`
// line) — the prior conversation is reloaded and its terminal state recovered (completed
// →Reopen, cancelled→Interrupt, failed→Recover), then it runs on the default
// explorer engine in a FRESH workspace fork (the original worktree is gone; a staleness
// note is prepended). An in-flight guard rejects a concurrent run on the same id.
type SubagentTool struct {
	// childEngine runs the subagent loop. It is pre-wired by the composition root
	// with the scoped catalog, the (optionally cheaper) model, and an allow/deny
	// policy appropriate for a non-interactive child. It is never the parent
	// Engine: the parent Engine is not mutated. It is the fallback for the
	// no-`agent` (default explorer) case.
	childEngine *Engine

	// agentEngines maps an agent-definition NAME to its pre-built, read-only child
	// Engine. The composition root builds one per def (scoped catalog + resolved
	// model + body→Role prompt) and injects the map via WithAgentEngines. A Subagent
	// call with a known `agent` runs that engine instead of childEngine; an empty
	// map (the default) means no specialists are configured and Subagent behaves
	// exactly as before. nil/empty is valid.
	agentEngines map[string]*Engine

	// agentMeta is the (name, description) list surfaced in Spec().Description for
	// progressive disclosure. It is sorted by the composition root for stable
	// output and kept in lockstep with agentEngines.
	agentMeta []AgentMeta

	// agentLimits maps an agent-definition NAME to the per-def session.Limits the
	// child session runs under when that agent is selected. It is derived from
	// agentMeta in WithAgentEngines so it stays in lockstep with agentEngines. A
	// name absent from the map (or a zero Limits) means "use t.limits" — the same
	// default the no-`agent` explorer path uses.
	agentLimits map[string]session.Limits
	// agentCeilings and writableAgentCeilings are populated only from
	// operator-managed definitions. A missing entry deliberately means no specialist
	// ceiling. Keeping the modes separate prevents a read-only child from persisting
	// mutating authority that a later writable resume could activate.
	agentCeilings         map[string]governance.CapabilitySet
	writableAgentCeilings map[string]governance.CapabilitySet

	// limits bound a single child run. Defaults to defaultChildLimits.
	limits session.Limits

	// childMode is the permission mode the child session runs under. Defaults to
	// session.ModeDefault.
	childMode session.PermissionMode

	// hooks fires the SubagentStop lifecycle hook when a child finishes
	// (best-effort; nil disables it). It is separate from the child Engine's own
	// PreToolUse/PostToolUse hooks.
	hooks port.HookRunner

	// childForker, when non-nil, isolates each child run in its OWN forked workspace
	// (a cheap git WORKTREE — shares the base repo's `.git` ⇒ full history) instead of
	// running against the shared parent workspace. The composition root wires it ONLY
	// when the child catalog includes Shell, so a shell-bearing read-only explorer runs
	// its (mutating-classified) Shell in a throwaway worktree, never the shared base —
	// which is what keeps Subagent read-parallel-safe (see ReadOnly). When nil, the child
	// runs against the parent ws exactly as before (no shell wired). A fork FAILURE on
	// this path is a tool error, NOT a silent fallback to the shared ws: the child's
	// catalog has Shell precisely because isolation was available, so running it in the
	// shared base would be the exact hazard isolation exists to prevent.
	childForker tool.EnvironmentForker

	// ledgerFactory mints one fresh read-evidence ledger per child Environment.
	// It is injected so engine/agent never imports a concrete ledger adapter.
	ledgerFactory func() tool.ReadLedger
	// sharedChildWS narrows the Workspace authority exposed to a base-sharing child
	// without replacing its content backend. Composition uses this to remove the
	// main session's relaxed path-escape reach while preserving ACP/remote/custom
	// backend identity. Nil keeps the parent Workspace unchanged.
	sharedChildWS func(tool.Workspace) tool.Workspace

	// childGate bounds how many Subagent children may run CONCURRENTLY — forking AND
	// forker-less. It is a buffered channel used as a counting semaphore, acquired at
	// the top of run() (before any fork) and released when the call returns, so the
	// dispatcher's read-parallel fan-out of N Subagent calls in one turn can never start more
	// than cap children at once (each consumes a child session + an LLM slot, and a
	// shell-bearing child additionally a forked worktree). It is always sized in
	// NewSubagentTool (never nil), so the gate is the single fan-out brake for every Subagent
	// child. Capacity is defaultMaxConcurrentChildren unless overridden by
	// WithMaxConcurrentChildren.
	childGate chan struct{}

	// engineFactory, when non-nil, mints a child engine for a per-call `model`
	// override. It is a composition-supplied closure (WithSubagentEngineFactory) closing
	// over the provider registry: given an opaque model string it returns a child
	// engine built through the SAME contamination-safe per-provider path the named-agent
	// engines use (engineDepsForProvider re-derives Compactor/TokenCounter/Env.Model/
	// ContextWindow for the override model) — NEVER a clone-and-swap of the LLM on an
	// existing engine. It returns ok=false for an unknown/unroutable model, which Subagent
	// renders as a model-addressable error. nil (the default) means no per-call model
	// override is wired (a `model` arg then errors with a clear "not supported" message).
	// It is layering-clean: the closure takes a string and returns *Engine — both
	// agent-layer types — and no adapter/proto/server type crosses (same shape as
	// WithAgentEngines).
	engineFactory func(model string) (*Engine, bool)

	// agentModelFactory, when non-nil, mints a child engine for a per-call
	// `agent`+`model` combination: the model runs on the named specialist's
	// resolved provider, but the def's SCOPED engine (catalog/prompt/hooks/memory) is
	// REBUILT on the override model. It is a composition-supplied closure
	// (WithAgentModelEngineFactory) closing over the agent-def registry + the provider
	// registry, so the override child keeps the specialist's tools/playbook (NOT the
	// generic explorer set) while re-deriving the provider-closing Deps
	// (Compactor/TokenCounter/Env.Model/ContextWindow) for the override model — NEVER a
	// clone-and-swap of an existing engine, and the pre-built agentEngines map is NEVER
	// mutated (a fresh engine is minted per call). It returns ok=false for an
	// unknown/unroutable model, which Subagent renders as a model-addressable error. nil
	// (the default, and ALWAYS on the no-FS path) means agent+model together is not
	// supported in this deployment (a call setting both then errors with a clear "not
	// supported" message). It is layering-clean: the closure takes two strings and
	// returns *Engine — both agent-layer types — and no adapter/proto/server type crosses.
	agentModelFactory func(agentName, model string) (*Engine, bool)

	// routableAgents is the composition-computed SET of agent-def names that expressed NO
	// model intent (absent `model:` — issue #286) and are therefore eligible for the OPT-IN
	// model router: an `agent`-named delegation to one of these, with the applicable routed
	// factory wired, is CLASSIFIED and the def's SCOPED engine is rebuilt on the routed model
	// (via agentModelFactory for read-only or agentWritableModelFactory for writable), fail-soft
	// to the pre-built read-only def engine or freshly-built ordinary writable specialist.
	// A def that expressed model intent (ANY def.Model — `inherit`, a built-in alias, an
	// unknown alias, a concrete id) is PINNED and NEVER in this set. Composition ALSO
	// excludes a def whose `provider:` switches away from the parent (routed ids are
	// parent-provider ids) and a def with INLINE MCP servers (the agent+model factory would
	// decline — excluding avoids wasted classifier spend). nil/empty (the default) means NO
	// def routes — byte-identical to pre-#286. It is consulted ONLY by the router gate
	// (maybeRouteModel); the engine layer stays model-string-only (names only — no adapter/
	// registry/provider type crosses).
	routableAgents map[string]struct{}

	// pinnedAgents is the composition-computed SET of agent-def names that expressed
	// model intent (ANY non-empty def.Model). It is distinct from the complement of
	// routableAgents: provider-switched and inline-MCP defs are also unroutable, but
	// they did not pin a model and must not be attributed as if they had.
	pinnedAgents map[string]struct{}

	// agentWritableModelFactory is agentWritableFactory's routed-model sibling. For an
	// unpinned writable named specialist, it rebuilds the same writable scoped engine on
	// the router-selected model. A decline falls back to agentWritableFactory; the router
	// remains fail-soft and reconcileRoutedModel reports the unavailable target truthfully.
	agentWritableModelFactory func(agentName, model string) (*Engine, bool)

	// agentWritableFactory, when non-nil, mints a WRITABLE child engine for a
	// mode:"read-write"+`agent` call: the named specialist's scoped engine
	// (catalog/prompt/hooks/memory) is REBUILT with allowMutating=true so Edit/Write
	// survive scoping, using the MAIN session's command runner (direct-write parity,
	// ADR 0077 — no fork, no copy, no merge-back); its Edit/Write/Shell mutate the real
	// parent tree in place, exactly as the main agent does, and git is the rollback
	// layer. It is a composition-supplied closure mirroring WithAgentModelEngineFactory
	// (it closes over the agent-def registry + the provider registry + the MAIN runner),
	// so the writable specialist keeps the specialist's tools/playbook (NOT the generic
	// explorer set) while re-deriving the provider-closing Deps for the def's resolved
	// provider/model. The pre-built agentEngines map is read for the name-truth check
	// only; a fresh engine is minted per call, NEVER inserted (no map mutation). It
	// returns ok=false for an unknown agent or an inline-MCP def (a v1 scope limit —
	// the inline manager's live session has no process-lifetime owner on a per-call
	// engine; reference-only MCP is supported, borrowing the process-lifetime mainMgr).
	// nil (the default, and ALWAYS on the no-FS path) means mode:"read-write"+`agent`
	// is not supported in this deployment (a call setting both then errors with a clear
	// "not supported in this deployment" message from validateMode). It is layering-
	// clean: the closure takes a string and returns *Engine — both agent-layer types —
	// and no adapter/proto/server type crosses (same shape as WithAgentModelEngineFactory).
	agentWritableFactory func(agentName string) (*Engine, bool)

	// writableChildEngine runs a mode:"read-write" child: a WRITABLE explorer whose
	// catalog includes Edit/Write (built by the composition root over the read-only
	// explorer catalog + Edit + Write, using the MAIN session's command runner). It
	// is SEPARATE from childEngine (the read-only explorer): a read-write call selects
	// this engine instead, so the read-only fan-out path is byte-identical when
	// read-write is never used. A read-write child runs DIRECTLY against the parent
	// workspace — no fork, no copy, no merge-back (ADR 0077) — so its Edit/Write/Shell
	// mutate the real tree in place, exactly as the main agent does, and git is the
	// rollback layer. nil (the default, and ALWAYS on the no-FS path) means writable
	// subagents are not wired — a read-write arg then surfaces a model-addressable
	// "not supported in this deployment" error.
	writableChildEngine *Engine

	// writableEngineFactory, when non-nil, mints a WRITABLE EXPLORER child engine on a
	// per-call OVERRIDE model for a mode:"read-write" call with NO `agent` (issue #285):
	// the generic writable explorer catalog (read-only explorer + Edit + Write) rebuilt on
	// the requested model, using the MAIN session's command runner (direct-write parity,
	// ADR 0077 — no fork, no copy, no merge-back); its Edit/Write/Shell mutate the real
	// parent tree in place, exactly as the main agent does, and git is the rollback layer.
	// It is a composition-supplied closure mirroring writableChildEngine's build recipe
	// (it closes over the provider registry + the MAIN runner), re-deriving the
	// provider-closing Deps (Compactor/TokenCounter/Env.Model/ContextWindow) for the
	// override model — NEVER a clone-and-swap. It returns ok=false for an unknown/unroutable
	// model, which selectChildEngine renders as a model-addressable error. It is ALSO the
	// fail-soft target for the OPT-IN router on a plain writable delegation (a routed pick is
	// minted here; a miss falls back to writableChildEngine). nil (the default, and ALWAYS
	// on the no-FS path) means mode:"read-write"+`model` (no `agent`) is not supported in
	// this deployment (a call setting both then errors from validateMode). It is
	// layering-clean: the closure takes a string and returns *Engine — both agent-layer
	// types — and no adapter/proto/server type crosses.
	writableEngineFactory func(model string) (*Engine, bool)

	// shellDisabledNote, when non-empty, replaces Spec()'s isolated-worktree-shell
	// clause with an honest read-only-only description carrying this reason (set by
	// the composition root via WithSubagentShellDisabledNote when the workspace-trust
	// gate — not a shell-less deployment — withheld the subagent shell, issue #40).
	// Empty (the default) keeps the description byte-identical to the historical
	// shell-bearing one.
	shellDisabledNote string

	// noFSSpec, when true, makes Spec() describe the NO-FILESYSTEM child surface
	// (set by the composition root via WithSubagentNoFSNote for a "no-fs" profile
	// session): the description claims NO Read/Grep/Glob, NO worktree shell, and
	// NO Parallel alternative — the child works through MCP tools, memory, and web
	// fetch only. DISTINCT from shellDisabledNote (issue #40), which only swaps
	// the shell clause while still claiming the read-only file tools; under no-FS
	// those claims would be lies too, so the WHOLE tool-surface description is
	// replaced. False (the default) keeps the description byte-identical to the
	// historical one (TestSubagentSpecNoFSNoteOption pins both sides).
	noFSSpec bool

	// idPrefix seeds the generated child SessionID so child sessions are
	// distinguishable in logs/stores.
	idPrefix string

	// store, when non-nil, best-effort persists each child session after its run so an
	// out-of-band reader (the InspectSubagent tool) can load its transcript by the
	// agentId the result trailer surfaces. nil disables persistence (persistMember
	// discipline: advisory, save failures swallowed). NAMESPACE NOTE (document-don't-
	// engineer): child ids ("subagent-<callID>") share the one session store with the
	// service's crypto-random session ids and the team members' "team-<teamID>-<member>"
	// ids; the prefixes keep them disjoint by convention, and jsonlstore sanitizes any
	// id into a safe filename, so no collision engineering is needed.
	store port.SessionStore

	// ownershipEnforced records whether the request edge verifies caller identity.
	// When true, a resume requires the persisted child owner to match the caller;
	// a missing principal is never treated as an implicit in-process parent.
	ownershipEnforced bool

	// mu guards inFlight. It is a plain mutex held only for the map's read-modify-write,
	// never across the child run.
	mu sync.Mutex
	// inFlight registers every child session id (FRESH and RESUME) currently being driven
	// by this tool, so a second concurrent Execute on the SAME id is rejected with a
	// model-visible error rather than racing two runs over one unlocked Session aggregate.
	// It is correctness (the aggregate is not goroutine-safe) AND liveness (waiting would
	// park a dispatcher goroutine + a gate slot). Registered BEFORE acquireChildSlot, so
	// the conflict is detected even while the second call would otherwise block on the gate.
	inFlight map[session.SessionID]struct{}
}

// defaultStructuredOutputRetries bounds how many CORRECTION re-drives a
// structured-output child gets after a SubmitResult payload fails schema validation
// (or the child never calls SubmitResult), before the Subagent tool gives up with
// StopStructuredOutput. It mirrors defaultNoProgressNudges (2): the FIRST attempt plus
// this many corrections. It is a bounded retry counter — NOT tool_choice forcing
// (incompatible with Anthropic thinking + the OpenAI reasoning path).
//
// LIMIT SEMANTICS (per-attempt vs cross-attempt): each correction re-drive Reopen()s
// the child session, which RESETS its Counters — so the per-call MaxTurns/MaxToolCalls
// (and a def's WithChildLimits) bound EACH ATTEMPT independently, giving a
// structured-output child effectively up to (1+defaultStructuredOutputRetries)× its
// per-child turn/tool budget across the whole call. That is BOUNDED (a small constant
// multiplier), not a runaway. The cross-attempt ceiling is the TOKEN budget
// (Deps.MaxRunTokens / the per-call max_run_tokens override): driveChild SUMS usage across
// every drive (usage = usage.Add(u)) and RE-PASSES the same runReq (carrying the
// tighten-only override) to each Engine.Run, so the token budget genuinely
// accumulates across attempts and is the real cross-attempt brake.
const defaultStructuredOutputRetries = 2

// salvageWrapUpPrompt is the model-visible re-injection driving ONE bounded wrap-up
// turn when a FREE-TEXT child exhausts its turn/tool-call limit OR its token budget
// WITHOUT producing any summary (issue #48). Without it the parent receives
// "(subagent produced no summary)" after the child spent its whole budget
// fetching/reading and never reached its conclusion. The salvage asks the child to
// stop and summarize its partial findings; it explicitly forbids further tool use
// because the salvage drive is hard-bound to a single turn (see
// salvageEmptyStop). A budget-stopped child gets one internal cleanup allowance
// measured from its current main usage; that baseline never resets accounting.
const salvageWrapUpPrompt = "You have reached your budget and must stop now. " +
	"Do not call any more tools. Summarize concisely what you found so far and give " +
	"your best partial answer as your final response."

// recoveredDigestPrefix frames the last-resort digestChildActivity recovery (issue
// #152): when both the original drive AND the bounded salvage turn produced no final
// text, the child's last non-empty assistant message is surfaced verbatim under this
// prefix. It states ONLY provenance ("recovered … last output") + partial-ness.
//
// It states NEITHER the stop reason NOR the next action, because renderSubagentResult's
// stop-reason note owns BOTH: on the StopNoProgress terminal that prefix and that note
// are rendered one after the other, and the earlier wording duplicated the clause
// "treat as partial; resume it with the agentId above to continue" BYTE-FOR-BYTE between
// them. The repo's rule is that the stop reason is stated in exactly ONE place; the same
// reasoning applies to the next action, which is if anything worse to duplicate — a
// model reading the same imperative twice, from two different framings, has no way to
// tell whether it is one instruction or two.
//
// It still reads coherently standalone on the note-less empty-StopEndTurn path: that
// path carries provenance + partial-ness, which is all it owes. StopEndTurn is a BENIGN
// clean end (the child finished its turn), so no next action is owed there — and the
// agentId trailer is on the result regardless, so resuming stays available even where it
// is not advertised.
const recoveredDigestPrefix = "[recovered the subagent's last output below — treat as partial]"

// submitResultToolName is the catalog name of the synthetic deliverable tool a
// structured-output child is given. It is run-scoped (RunRequest.ExtraTools), never
// registered into any shared catalog.
const submitResultToolName = "SubmitResult"

// resumeStalenessNote is the honest harness preface prepended to a RESUMED READ-ONLY
// child's prompt: the conversation survives but the workspace does not (the original
// worktree was torn down; this run gets a fresh fork), so the child must not trust
// earlier filesystem observations. Same accuracy discipline as childAutoDenyMessage.
const resumeStalenessNote = "[harness note: your conversation has been resumed, but you are running in a FRESH workspace checkout — file changes, build artifacts, and running processes from your earlier run are GONE. Re-run commands and re-read files before relying on earlier observations.]"

// resumeWritableNote is resumeStalenessNote's direct-write sibling. A writable child NEVER
// forks (ADR 0077 — it edits the real parent tree in place), so on resume it continues in
// the SAME workspace and its earlier edits are still sitting there. Telling it they are
// "GONE" would be false, and actively harmful for the case issue #318 exists to serve: a
// direct-write child recovered from a transient failure must build ON its partial edits,
// not redo or distrust them. What genuinely did NOT survive is process state (build
// artifacts are stale, background processes are dead) and any concurrent change the parent
// made meanwhile — hence the re-read instruction is kept, narrowed to what is actually
// true. Same accuracy discipline as childAutoDenyMessage.
//
// It is selected by prepareChildSession's editsSurvived, NOT by the current call's
// `writable` flag: `mode` may CHANGE across a resume, so a previously READ-ONLY child
// resumed with mode:"read-write" would otherwise be told its edits survived when its
// worktree was torn down (a read-only child has no Edit/Write but DOES have Shell in that
// worktree, so it may really have applied edits). The INVERSE falsehood is worse than the
// one this note fixes — a child that trusts absent edits builds on nothing — so the
// selection is keyed on the persisted workspace path, and the conservative staleness note
// is the fallback whenever the two differ.
const resumeWritableNote = "[harness note: your conversation has been resumed and you are running DIRECTLY in the same workspace as before — the file edits you already made are STILL IN PLACE. Build artifacts and running processes from your earlier run are gone, and the workspace may have changed since, so re-read a file or re-run a command before relying on an earlier observation of it.]"

// resumeWritableFreshNote is the THIRD cell of the resume-note matrix: this call is
// mode:"read-write" (so the child runs DIRECTLY in the operator's real workspace — no fork,
// ADR 0077) but the EARLIER run was read-only, so its throwaway worktree and everything in
// it is gone.
//
// It exists because the matrix has two INDEPENDENT axes and only two notes covered them:
// WHERE the child runs follows THIS call's mode, WHAT survived follows the EARLIER run's.
// A previously read-only child resumed with mode:"read-write" — the exact call
// writableSubagentFailedNote and writableSubagentTimeoutNote now tell the parent to make —
// used to fall to resumeStalenessNote and be told it "is running in a FRESH workspace
// checkout" while holding Edit/Write on the real repository. That is the more dangerous
// half of the falsehood, not the safer one: a child that believes it is in a scratch
// checkout may rewrite or delete files to "start clean", and here those deletions land in
// the operator's working tree. So this note states BOTH facts — real workspace, earlier
// artifacts gone — and neither axis is inferred from the other.
//
// Two wording constraints it earns the hard way, both of the class this file keeps closing:
//
//   - The caution is scoped to the BEHAVIOUR it exists to prevent (wiping files it did not
//     write, to get a clean starting point) rather than the broad "do not delete or rewrite
//     files" it first said. This note goes to a child whose whole job is to rewrite files,
//     as a TRUSTED harness instruction, and the broad reading — which is what a skim of a
//     long bracketed note produces — makes it decline the deletion the parent actually
//     asked for. A child refusing its own task is a confusing failure to debug.
//   - It states no MECHANISM for why the earlier run's work is gone. The selector is
//     `priorWorkspace != ws.Root()` (prepareChildSession), which is ALSO true when the
//     resumed snapshot persisted no workspace at all and when the earlier run was itself
//     direct-write in a DIFFERENT real tree — so "(that run used a throwaway checkout)" can
//     be false while the material claim (nothing from it carries over) stays true in every
//     cell. The same reason resumeStalenessNote and subagentErrorResumeHint state the fact
//     and not the plumbing.
const resumeWritableFreshNote = "[harness note: your conversation has been resumed and you are now running DIRECTLY in the real workspace — your file edits land immediately and git is the only safety net, so do not wipe or revert files you did not write yourself just to get a clean starting point. Nothing from your EARLIER run's workspace carries over: its file changes, build artifacts, and running processes are GONE, so re-read files and re-run commands before relying on earlier observations.]"

// resumePosture is the (writable × editsSurvived) pair the resumed child's harness note is
// a function of. It is a struct rather than two more bool parameters because
// buildSubagentRunRequest already carries `resuming`, and three adjacent bools at a call
// site is precisely the transposition trip-wire this file's other TRIP-WIRE comments exist
// to avoid.
type resumePosture struct {
	// writable is THIS call's mode:"read-write" — it decides WHERE the resumed child runs
	// (the real parent workspace, no fork) and nothing about what survived.
	writable bool
	// editsSurvived is prepareChildSession's verdict on the EARLIER run — the persisted
	// workspace root equals the real parent root, so the file edits that run made are still
	// on disk. It decides WHAT carries over and nothing about where the child runs.
	editsSurvived bool
}

// note resolves the harness preface a RESUMED child's prompt is prefixed with. The two
// axes are independent, so the space is enumerated here ONCE rather than being reconstructed
// at the call site: three arms, no combination falling through to a claim that is false on
// either axis.
//
//	!writable                → resumeStalenessNote      (fresh throwaway checkout; nothing survived)
//	writable && editsSurvived → resumeWritableNote       (real tree; the edits are still there)
//	writable                 → resumeWritableFreshNote   (real tree; the earlier run's work is gone)
//
// The fourth combination (!writable && editsSurvived) is unreachable by construction —
// prepareChildSession ANDs editsSurvived with writable — and lands on resumeStalenessNote,
// which is the honest answer for a read-only call whatever the other axis says: it forks, so
// it genuinely is in a fresh checkout.
func (p resumePosture) note() string {
	switch {
	case !p.writable:
		return resumeStalenessNote
	case p.editsSurvived:
		return resumeWritableNote
	default:
		return resumeWritableFreshNote
	}
}

// SubagentOption configures a SubagentTool.
type SubagentOption func(*SubagentTool)

// testReadLedgerFactory is nil in production. The agent package's tests set it
// so legacy constructor-focused fixtures need not each duplicate composition wiring.
var testReadLedgerFactory func() tool.ReadLedger

// WithChildLimits overrides the subagent's stop conditions. Use it to make a
// child even tighter (or, rarely, looser) than the defaults.
func WithChildLimits(l session.Limits) SubagentOption {
	return func(t *SubagentTool) { t.limits = l }
}

// WithChildMode sets the permission mode the child session runs under (default
// session.ModeDefault). session.ModePlan additionally hides any non-read-only
// tools from the child at the catalog level.
func WithChildMode(m session.PermissionMode) SubagentOption {
	return func(t *SubagentTool) { t.childMode = m }
}

// WithSubagentStopHook injects the HookRunner that fires the SubagentStop hook
// when a child run finishes. It is best-effort: a hook error or block never fails
// the Subagent call. Passing nil disables the hook.
func WithSubagentStopHook(h port.HookRunner) SubagentOption {
	return func(t *SubagentTool) { t.hooks = h }
}

// WithChildSessionPrefix sets the prefix used to derive child SessionIDs (default
// "subagent"). Child ids are of the form "<prefix>-<callID>".
func WithChildSessionPrefix(p string) SubagentOption {
	return func(t *SubagentTool) { t.idPrefix = p }
}

// WithChildForker injects the workspace-isolation seam each child run forks before
// executing. The composition root wires it ONLY when the child catalog includes Shell
// (the read-only explorer's shell), so the child's mutating-classified Shell lands in
// a throwaway git worktree, never the shared parent base — preserving Subagent's
// read-parallel safety (see ReadOnly). It should be the forker's DEFAULT mode (git
// worktree: shares the base repo's `.git` ⇒ full history for git log/show). When the
// forker is nil (the default), the child shares the parent content backend through
// any composition-supplied authority-narrowing Workspace view. A fork failure on this path is a tool error, not a silent fallback.
func WithChildForker(f tool.EnvironmentForker) SubagentOption {
	return func(t *SubagentTool) { t.childForker = f }
}

// WithSubagentReadLedgerFactory injects the mandatory fresh child-ledger factory.
func WithSubagentReadLedgerFactory(factory func() tool.ReadLedger) SubagentOption {
	return func(t *SubagentTool) { t.ledgerFactory = factory }
}

// WithSharedChildWorkspace injects a capability-narrowing view for base-sharing
// children. The function receives the actual parent Workspace and must preserve
// its content backend; it exists so child authority can be stricter than a
// posture-relaxed main-session wrapper without reconstructing storage from Root.
func WithSharedChildWorkspace(view func(tool.Workspace) tool.Workspace) SubagentOption {
	return func(t *SubagentTool) { t.sharedChildWS = view }
}

// WithSubagentStore injects the optional session store each child session is
// best-effort persisted to after its run (ALL terminals: clean, limit-stopped,
// structured-output-exhausted, errored, cancelled/timed out — the final state after
// any structured-output re-drives). Nil disables persistence. Mirrors the team
// supervisor's WithMemberStore/persistMember discipline: the save is advisory and a
// failure is swallowed (no diagnostic). The tool consumes the port.SessionStore
// interface, never a concrete adapter, so no layering rule is crossed.
func WithSubagentStore(store port.SessionStore) SubagentOption {
	return func(t *SubagentTool) { t.store = store }
}

// WithSubagentOwnershipEnforced records whether the request edge verifies caller
// identity. Enabled deployments require a resume caller to match the persisted
// child owner; disabled deployments retain legacy ownerless compatibility.
func WithSubagentOwnershipEnforced(ownershipEnforced bool) SubagentOption {
	return func(t *SubagentTool) { t.ownershipEnforced = ownershipEnforced }
}

// WithMaxConcurrentChildren bounds how many Subagent children may run CONCURRENTLY —
// forking AND forker-less (default defaultMaxConcurrentChildren). It is the single
// fan-out brake on the dispatcher's read-parallel batch: N Subagent calls in one turn each
// block on the gate, so at most cap children run at once. A value < 1 is clamped to 1
// (a zero-capacity gate would deadlock).
func WithMaxConcurrentChildren(n int) SubagentOption {
	return func(t *SubagentTool) {
		if n < 1 {
			n = 1
		}
		t.childGate = make(chan struct{}, n)
	}
}

// WithSubagentShellDisabledNote tells the Subagent tool's Spec() that NO child gets a
// shell on this workspace and why. With it set, the spec's "plus a full shell in an
// isolated, throwaway git worktree …" clause is REPLACED by an honest read-only-only
// description carrying the reason, so the model never plans build/test/git delegation
// the child cannot perform. The composition root sets it ONLY when the workspace-trust
// gate withheld the shell (issue #40) — a shell-less deployment (--no-bash / empty
// shell) keeps the historical description unchanged, exactly like before this option
// existed. An empty reason is a no-op (the default, byte-identical description).
func WithSubagentShellDisabledNote(reason string) SubagentOption {
	return func(t *SubagentTool) { t.shellDisabledNote = reason }
}

// WithSubagentNoFSNote tells the Subagent tool's Spec() that this session has NO
// FILESYSTEM (the "no-fs" session profile), replacing the WHOLE tool-surface
// description: under no-FS the spec must not claim Read/Grep/Glob, a worktree
// shell, kept file changes (Parallel — absent under no-FS), or "a quick read you
// can do with Read/Grep" — it describes the real child surface instead (MCP
// tools, memory, web fetch; no file access, no shell). DISTINCT from
// WithSubagentShellDisabledNote (issue #40), which swaps only the shell clause
// and keeps the read-only file-tool claims that are still true on that path.
// Without this option the description stays byte-identical to the historical one.
func WithSubagentNoFSNote() SubagentOption {
	return func(t *SubagentTool) { t.noFSSpec = true }
}

// WithSubagentEngineFactory injects the composition-supplied factory that mints a child
// engine for a per-call `model` override. The closure closes over the provider
// registry and builds the override child through the contamination-safe per-provider
// path (engineDepsForProvider) — Compactor/TokenCounter/Env.Model/ContextWindow are
// re-derived for the override model, NEVER a clone-and-swap of the LLM on an existing
// engine. It returns (engine, true) for a routable model and (nil, false) otherwise
// (an unknown/unroutable model, which Subagent surfaces as a model-addressable error).
// nil (the default) leaves Subagent without a per-call model override (a `model` arg then
// errors). It is the layering-clean seam: only func(string)(*Engine,bool) crosses into
// engine/agent (same shape as WithAgentEngines).
func WithSubagentEngineFactory(f func(model string) (*Engine, bool)) SubagentOption {
	return func(t *SubagentTool) { t.engineFactory = f }
}

// WithAgentModelEngineFactory injects the composition-supplied factory that mints a child
// engine for a per-call `agent`+`model` combination. The override model runs on the named
// specialist's resolved provider, and the def's SCOPED engine (catalog/prompt/hooks/
// memory) is REBUILT on the override model through the contamination-safe per-provider
// path (engineDepsForProvider re-derives Compactor/TokenCounter/Env.Model/ContextWindow)
// — NEVER a clone-and-swap of an existing engine, and the pre-built agentEngines map is
// never mutated. It returns (engine, true) for a routable (agent, model) and (nil, false)
// otherwise (an unknown/unroutable model, which Subagent surfaces as a model-addressable
// error naming both the agent and the model). nil (the default, and the no-FS path)
// leaves Subagent without agent+model support (a call setting both then errors).
//
// The override model is passed VERBATIM (no alias resolution — an opaque string the
// provider validates at request time), matching the model-only path's parity. The def's
// resolved PROVIDER (def.Provider pinned-and-known → that provider; else the parent's)
// is the only provider dimension; cross-provider override OF the provider by a bare model
// id is out of scope (matches buildSubagentEngineFactory's existing out-of-scope comment).
// A def with INLINE MCP servers is a v1 scope limit (the inline managers' live sessions
// must outlive a per-call engine); the factory returns (nil, false) and selectChildEngine
// surfaces the accurate error. read-write+agent+model is rejected by validateMode
// before this factory is consulted (a v1 scope limit — a writable specialist runs on
// its own resolved model); read-write+agent ALONE routes through the separate
// agentWritableFactory (WithAgentWritableEngineFactory), not this one.
func WithAgentModelEngineFactory(f func(agentName, model string) (*Engine, bool)) SubagentOption {
	return func(t *SubagentTool) { t.agentModelFactory = f }
}

// WithAgentWritableEngineFactory injects the composition-supplied factory that mints a
// WRITABLE child engine for a mode:"read-write"+`agent` call (ADR 0058 — a writable
// named specialist). Given an agent name it REBUILDS the named specialist's scoped
// engine (catalog/prompt/hooks/memory) with allowMutating=true on the def's resolved
// provider/model through the SAME contamination-safe per-provider path the startup
// engines use (buildAgentDefEngine → newChildEngineForProvider re-derives
// Compactor/TokenCounter/Env.Model/ContextWindow), using the MAIN session's command
// runner (direct-write parity, ADR 0077 — no fork, no copy, no merge-back); its
// Edit/Write/Shell mutate the REAL parent workspace in place, exactly as the main
// agent does, and git is the rollback layer. The pre-built agentEngines map is NEVER
// mutated (a fresh engine is minted per call). It returns (engine, true) for a known
// reference-only-MCP def and (nil, false) for an unknown agent or a def with INLINE
// MCP servers (a v1 scope limit — the inline manager's live session has no
// process-lifetime owner on a per-call engine; selectChildEngine surfaces the
// model-addressable error). Reference-only MCP servers ARE supported (they borrow the
// process-lifetime mainMgr, no new connection).
//
// nil (the default, and ALWAYS on the no-FS path) leaves Subagent without writable-
// specialist support: a mode:"read-write"+`agent` call then errors with a clear "not
// supported in this deployment" message from validateMode. Explicit read-write+`agent`+
// `model` arguments together are OUT OF SCOPE for v1 (validateMode rejects them before this
// factory is ever consulted); a router-selected model is handled by the separate
// WithAgentWritableModelEngineFactory. It is layering-
// clean: the closure takes a string and returns *Engine — both agent-layer types —
// and no adapter/proto/server type crosses (same shape as WithAgentModelEngineFactory).
func WithAgentWritableEngineFactory(f func(agentName string) (*Engine, bool)) SubagentOption {
	return func(t *SubagentTool) { t.agentWritableFactory = f }
}

// WithAgentWritableModelEngineFactory injects the composition-supplied factory that
// rebuilds a WRITABLE named specialist on a router-selected model. It is distinct from
// WithAgentModelEngineFactory because the resulting engine retains mutating tools and
// runs directly in the parent environment. A declined routed target falls back to the
// ordinary writable specialist minted by WithAgentWritableEngineFactory.
func WithAgentWritableModelEngineFactory(f func(agentName, model string) (*Engine, bool)) SubagentOption {
	return func(t *SubagentTool) { t.agentWritableModelFactory = f }
}

// WithWritableChildEngine injects the child *Engine a mode:"read-write" Subagent
// call runs on: a WRITABLE explorer whose catalog includes Edit/Write (the
// composition root builds it over the read-only explorer catalog + Edit + Write,
// using the MAIN session's command runner). It is SEPARATE from the read-only
// childEngine; a read-write call selects this engine instead, so the read-only
// fan-out path is byte-identical when read-write is never used. A read-write child
// runs DIRECTLY against the parent workspace — no fork, no copy, no merge-back (ADR
// 0041); git is the rollback layer. nil (the default, and the no-FS path) leaves
// writable subagents unwired (a read-write arg then errors).
func WithWritableChildEngine(e *Engine) SubagentOption {
	return func(t *SubagentTool) { t.writableChildEngine = e }
}

// WithWritableEngineFactory injects the composition-supplied factory that mints a WRITABLE
// EXPLORER child engine on a per-call OVERRIDE model for a mode:"read-write" call with no
// `agent` (issue #285 — a writable explorer honours the per-call `model` and the router
// pick, closing the gap where read-write silently ran on its default model). Given a model
// id it REBUILDS the generic writable explorer engine (read-only explorer catalog + Edit +
// Write) on that model through the SAME contamination-safe per-provider path
// writableChildEngine uses, using the MAIN session's command runner (direct-write parity,
// ADR 0077 — no fork, no copy, no merge-back); its Edit/Write/Shell mutate the REAL parent
// workspace in place, and git is the rollback layer. It re-derives the provider-closing
// Deps (Compactor/TokenCounter/Env.Model/ContextWindow) for the override model — NEVER a
// clone-and-swap. It returns (engine, true) for a routable model and (nil, false) for an
// unknown/unroutable model (or a blank model), which selectChildEngine surfaces as a
// model-addressable error. It is ALSO the fail-soft mint target for the OPT-IN router on a
// plain writable delegation (a routed pick mints here; a miss falls back to
// writableChildEngine).
//
// nil (the default, and ALWAYS on the no-FS path) leaves Subagent without writable-explorer
// per-model support: a mode:"read-write"+`model` (no `agent`) call then errors with a clear
// "not supported in this deployment" message from validateMode (never a silent inherit). It
// is layering-clean: the closure takes a string and returns *Engine — both agent-layer
// types — and no adapter/proto/server type crosses (same shape as WithSubagentEngineFactory).
func WithWritableEngineFactory(f func(model string) (*Engine, bool)) SubagentOption {
	return func(t *SubagentTool) { t.writableEngineFactory = f }
}

// WithAgentEngines injects the per-definition child engines (keyed by agent name)
// and their (name, description) metadata for progressive disclosure. The
// composition root builds each engine with a SCOPED, read-only catalog (the Subagent
// read-only invariant is preserved — see ReadOnly) and the def's resolved
// model/prompt, then passes the map and a name-sorted meta slice here.
//
// engines and meta should describe the same set of names; meta drives the Spec
// enumeration while engines drives routing. A nil/empty map leaves Subagent with only
// the default explorer (no behaviour change). It is the agent-package boundary the
// agents adapter never crosses: only plain map + structs flow in.
func WithAgentEngines(engines map[string]*Engine, meta []AgentMeta) SubagentOption {
	return func(t *SubagentTool) {
		t.agentEngines = engines
		t.agentMeta = meta
		// Index each def's per-run limits by name so Execute can bound the child
		// session with THAT def's limits (instead of the default t.limits) when the
		// agent is selected. A zero Limits is skipped — the name then falls back to
		// t.limits in Execute, identical to the no-`agent` path.
		t.agentLimits = nil
		t.agentCeilings = nil
		t.writableAgentCeilings = nil
		for _, m := range meta {
			if m.Managed {
				if t.agentCeilings == nil {
					t.agentCeilings = make(map[string]governance.CapabilitySet, len(meta))
					t.writableAgentCeilings = make(map[string]governance.CapabilitySet, len(meta))
				}
				t.agentCeilings[m.Name] = m.AuthorityCeiling
				t.writableAgentCeilings[m.Name] = m.WritableAuthorityCeiling
			}
			if m.Limits == (session.Limits{}) {
				continue
			}
			if t.agentLimits == nil {
				t.agentLimits = make(map[string]session.Limits, len(meta))
			}
			t.agentLimits[m.Name] = m.Limits
		}
	}
}

func (t *SubagentTool) agentCeiling(name string, writable bool) (*governance.CapabilitySet, bool) {
	ceilings := t.agentCeilings
	if writable {
		ceilings = t.writableAgentCeilings
	}
	if name == "" || ceilings == nil {
		return nil, false
	}
	ceiling, ok := ceilings[name]
	if !ok {
		return nil, false
	}
	return &ceiling, true
}

// WithRoutableAgents injects the composition-computed SET of agent-def names eligible for
// the OPT-IN model router (issue #286): a def that expressed NO model intent (absent
// `model:`), does not switch provider away from the parent, and has no inline MCP servers.
// An `agent`-named delegation to one of these — read-only, with the agent+model factory
// wired — is CLASSIFIED and its SCOPED engine rebuilt on the routed model (fail-soft to the
// pre-built def engine). Absence from this set does NOT itself mean pinned: provider-switched
// and inline-MCP defs are also ineligible. WithPinnedAgents carries the narrower attribution
// set. nil/empty (the default) means NO def routes — byte-identical to pre-#286. It is
// layering-clean: only def NAME strings flow in. It is inert when the router is off
// (routeTask nil) — wiring it unconditionally is safe.
func WithRoutableAgents(names []string) SubagentOption {
	return func(t *SubagentTool) {
		if len(names) == 0 {
			t.routableAgents = nil
			return
		}
		t.routableAgents = make(map[string]struct{}, len(names))
		for _, n := range names {
			if n = strings.TrimSpace(n); n != "" {
				t.routableAgents[n] = struct{}{}
			}
		}
	}
}

// WithPinnedAgents injects the composition-computed SET of agent-def names that expressed
// model intent (ANY non-empty `model:`, including explicit `inherit`). The set is used only
// to attribute an unrouted named delegation as RoutingReasonAgentDefPinned. It is separate
// from WithRoutableAgents because a def may be unroutable for reasons other than a model pin
// (for example a provider switch or inline MCP server); those cases report router-disabled.
// nil/empty is safe and preserves the pre-option default.
func WithPinnedAgents(names []string) SubagentOption {
	return func(t *SubagentTool) {
		if len(names) == 0 {
			t.pinnedAgents = nil
			return
		}
		t.pinnedAgents = make(map[string]struct{}, len(names))
		for _, n := range names {
			if n = strings.TrimSpace(n); n != "" {
				t.pinnedAgents[n] = struct{}{}
			}
		}
	}
}

// NewSubagentTool constructs the Subagent tool tool over a pre-built child *Engine.
//
// The composition root (cmd/mecated, WP11) is responsible for building childEngine
// with the SCOPED child catalog and policy. The recommended, deterministic wiring
// is:
//
//   - Catalog: a read-only explorer set — Read, Grep, Glob ONLY. It MUST NOT
//     contain the Subagent tool (otherwise a subagent could spawn subagents — infinite
//     recursion) and SHOULD NOT contain mutating tools (Edit/Write/non-RO Shell):
//     the default explorer subagent cannot mutate the workspace.
//   - Policy: allow-all over those read-only tools (e.g.
//     permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}})), so the
//     child never produces a permission "ask". Subagents are one-shot and
//     non-interactive — there is no human on the other end of a child run.
//
// Even with that wiring, Execute defends the non-interactive invariant: if the
// child loop ever pauses on a permission ask, Execute auto-resolves it as DENY so
// the child can never block waiting for a human. This keeps the subagent
// deterministic regardless of the policy it is given.
//
// childEngine must be non-nil; NewSubagentTool panics otherwise, because a Subagent tool
// with no child loop to delegate to is a programming error at the composition
// root.
func NewSubagentTool(childEngine *Engine, opts ...SubagentOption) tool.Tool {
	if childEngine == nil {
		panic("agent: NewSubagentTool requires a non-nil child Engine")
	}
	t := &SubagentTool{
		childEngine: childEngine,
		limits:      defaultChildLimits,
		childMode:   session.ModeDefault,
		idPrefix:    strings.TrimSuffix(SubagentSessionPrefix, "-"), // the exported convention is the source
		inFlight:    make(map[session.SessionID]struct{}),
	}
	for _, o := range opts {
		o(t)
	}
	if t.ledgerFactory == nil {
		t.ledgerFactory = testReadLedgerFactory
	}
	// Always size the child-concurrency gate (forking AND forker-less): a read-parallel
	// fan-out of N Subagent calls in one turn each consumes a child session + an LLM slot, so
	// the gate is the single fan-out brake bounding how many children run at once. The
	// operator may override the default via WithMaxConcurrentChildren.
	if t.childGate == nil {
		t.childGate = make(chan struct{}, defaultMaxConcurrentChildren)
	}
	return t
}

// Spec returns the model-facing specification for the Subagent tool. When named agent
// definitions are configured, their names+descriptions are appended to the
// description (progressive disclosure, like the Skill tool enumerates skills) so
// the model can choose a specialist via the optional `agent` arg.
func (t *SubagentTool) Spec() tool.ToolSpec {
	// NO-FILESYSTEM profile (WithSubagentNoFSNote): the historical description's
	// tool-surface claims (Read/Grep/Glob, the worktree shell, "use Parallel",
	// "a quick read you can do with Read/Grep") are ALL false in a no-fs session,
	// so the whole description is replaced by the honest file-less one — not just
	// the shell clause (that is the narrower issue-#40 note below). Without the
	// option the assembled description stays byte-identical to the historical one
	// (TestSubagentSpecNoFSNoteOption pins both sides).
	if t.noFSSpec {
		return tool.ToolSpec{
			Name:        subagentToolName,
			Description: t.noFSDescription(),
			Schema:      subagentSchema,
		}
	}
	// shellClause is honest per composition. The default (shell wired) claims the
	// isolated-worktree shell: the read-only explorer CAN build/test/inspect history
	// and write scratch files, but it has no Edit/Write and its file changes are
	// discarded after the run. The two modes (read-only default vs read-write) are
	// described as SEPARATE, legible sentences below — this clause covers the shell
	// only, so it no longer buries the read-write clause in a parenthetical (nor
	// contradicts itself about whether edits land). With WithSubagentShellDisabledNote
	// set the clause is REPLACED by a read-only-only description carrying the reason,
	// so the model never delegates build/test/git work the child cannot perform.
	// Without the note the assembled description is byte-stable
	// (TestSubagentSpecShellDisabledNoteOption pins both sides).
	shellClause := "By default the subagent is READ-ONLY: it can Read/Grep/Glob and run build/test/git " +
		"in a throwaway worktree, but it has no Edit/Write and its file changes are discarded after the run"
	if t.shellDisabledNote != "" {
		shellClause = "By default the subagent is READ-ONLY — " + t.shellDisabledNote + " — with no Edit/Write"
	}
	desc := "Delegate ONE focused, self-contained task to a subagent with its own fresh context. " +
		"For two or more independent READ-ONLY tasks, prefer one Subagent call per task in the SAME " +
		"assistant turn; eligible calls execute concurrently and return separate results. Do not wait " +
		"for one before launching the next unless a later task depends on an earlier result. Use it for " +
		"a multi-step investigation ('search → summarize', 'trace this code path'), build/test/git " +
		"work ('run the tests and report failures', 'bisect the history'), or an implementation task " +
		"('implement this fix and edit the files'). " +
		shellClause + ". " +
		"Set mode:\"read-write\" to let it edit and write files DIRECTLY in your workspace — exactly " +
		"as you do — so its edits land immediately, with no copy or merge step. This is how you " +
		"delegate an implementation task and have the edits LAND. Serial execution applies only to a " +
		"mode:\"read-write\" call (it never runs alongside your other tools), so it cannot race you; " +
		"there is no isolation, so review its result with `git diff`/`git status` and undo with " +
		"`git checkout`/`git stash` if needed. " +
		"The subagent cannot delegate further. " +
		"The subagent's FINAL MESSAGE is its deliverable — you receive only that " +
		"(see `prompt`; with `background: true` the call instead returns at once and you collect the " +
		"result later). Do NOT use it when you need the intermediate outputs in this conversation " +
		"(do the work yourself) or when workers must coordinate (use Team) — and don't delegate a " +
		"single quick read you can do with Read/Grep." +
		" Inline context you already hold (e.g. a diff, file contents, prior findings) directly in " +
		"`prompt` rather than making the subagent re-fetch it — that saves its limited turn/tool " +
		"budget for the actual task." +
		" Every result starts with an 'agentId:' line — pass that id to SubagentStatus (this run's " +
		"live state; collects background results), to InspectSubagent to read the full transcript, " +
		"or as `resume` to continue that subagent with a follow-up prompt (fresh workspace; its " +
		"conversation survives)." +
		" Set `fork: true` to seed the subagent with a COPY of THIS conversation (your full context " +
		"so far) instead of a fresh, empty one — for a focused continuation that needs everything " +
		"you have already gathered; it runs on your own model (no model/agent/resume)."
	desc += t.agentEnumeration()
	return tool.ToolSpec{
		Name:        subagentToolName,
		Description: desc,
		Schema:      subagentSchema,
	}
}

// noFSDescription is the Spec description for a NO-FILESYSTEM session: it
// describes the real child surface (MCP tools, memory, web fetch — no file
// access, no shell) and drops every file-bound claim and alternative (no
// Read/Grep/Glob, no worktree shell, no Parallel, no "fresh workspace" resume
// caveat). The agentId/SubagentStatus/InspectSubagent/resume plumbing is
// unchanged — those are conversation-level, not filesystem-level. There is
// deliberately NO agentEnumeration() tail: per-def specialists are SKIPPED under
// no-fs (a def's scoped catalog/workspace expectations are file-oriented — see
// buildNoFSSubagentTool), so the no-fs composition never supplies agents, and
// enumerating any would advertise specialists the `agent` arg cannot honestly
// serve in this session.
func (*SubagentTool) noFSDescription() string {
	desc := "Delegate ONE focused, self-contained task to a subagent with its own fresh context. " +
		"For two or more independent READ-ONLY tasks, prefer one Subagent call per task in the SAME " +
		"assistant turn; eligible calls execute concurrently and return separate results. Do not wait " +
		"for one before launching the next unless a later task depends on an earlier result. Use it for " +
		"a multi-step investigation it can complete WITHOUT any file access ('fetch and cross-check " +
		"these sources → summarize', 'search memory and report what is already known'). This session " +
		"has NO filesystem: the subagent has NO file tools and NO shell — it works through MCP tools, " +
		"memory, and web fetch only, and it cannot delegate further. Delegate only file-free " +
		"investigations. The subagent's FINAL MESSAGE is its deliverable — you receive only that " +
		"(see `prompt`; with `background: true` the call instead returns at once and you collect the " +
		"result later). Do NOT use it when you need the intermediate outputs in this conversation " +
		"(do the work yourself), or when workers must coordinate (use Team)." +
		" Inline context you already hold (e.g. prior findings, fetched content) directly in " +
		"`prompt` rather than making the subagent re-fetch it — that saves its limited turn/tool " +
		"budget for the actual task." +
		" Every result starts with an 'agentId:' line — pass that id to SubagentStatus (this run's " +
		"live state; collects background results), to InspectSubagent to read the full transcript, " +
		"or as `resume` to continue that subagent with a follow-up prompt (its conversation survives)."
	return desc
}

// agentEnumeration renders the "Available agents:" tail listing each configured
// def's "name: description", or "" when none are configured. The list is taken in
// the (already name-sorted) order the composition root supplied, so the spec is
// byte-stable across turns.
func (t *SubagentTool) agentEnumeration() string {
	if len(t.agentMeta) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nAvailable specialist agents (pass the name as `agent`):")
	for _, m := range t.agentMeta {
		fmt.Fprintf(&b, "\n- %s: %s", m.Name, m.Description)
	}
	return b.String()
}

// ReadOnly reports that the Subagent tool is read-only, which lets the parent's
// dispatcher run Subagent CONCURRENTLY with other read-only tools that share the same
// Workspace (read-parallel / mutate-serial; see dispatch.go).
//
// INVARIANT — what keeps this safe is WORKSPACE ISOLATION, not catalog
// read-only-ness. A Subagent child may now WRITE via Shell (the read-only explorer's
// shell — git, build, test, cat), but when a child forker is wired (childForker !=
// nil — the composition root wires it iff the child catalog has Shell) the child runs
// in an ISOLATED git WORKTREE, so its writes land in a throwaway checkout and NEVER
// touch the shared parent workspace the parent's other read-only calls race over.
// The read-parallel guarantee therefore holds exactly as before: no two concurrent
// dispatched tools ever mutate the same tree.
//
// The only surface a worktree child shares with the parent is the `.git` object
// DB/refs (git-locked for concurrent access; config-driven code-execution vectors
// — hooks/pager/fsmonitor/external-diff — neutralized by the sandboxed runner's
// gitenv env in the composition layer), and a detached-HEAD worktree's stray commit
// is dangling and gc-able. A fork FAILURE is surfaced as a tool error, never a
// silent fallback to the shared ws (which WOULD break this), so the invariant cannot
// be violated by a degraded fork.
//
// When NO forker is wired the child has no Shell (the catalog stays a pure read-only
// explorer) and runs against the shared ws — also safe, by catalog read-only-ness,
// exactly as it always was. Either way Subagent is read-parallel-safe and ReadOnly()
// honestly returns true.
//
// ReadOnly() stays true for read-only fan-out; a mode:"read-write" CALL mutates the
// parent workspace IN PLACE during the run (direct-write, ADR 0077 — no fork, no
// merge), so it is excluded from the concurrent read batch via MutatesParent
// (dispatch-serial, run alone — see parentMutatingCaller) so its in-place edits never
// overlap a sibling parent read.
func (*SubagentTool) ReadOnly() bool { return true }

// MutatesParent implements the optional parentMutatingCaller seam: it reports
// whether THIS specific call will mutate the PARENT workspace. ReadOnly() stays true
// so read-only Subagent fan-out keeps batching in parallel; a call for which this
// returns true is excluded from the concurrent read batch (dispatch-serial, flushed
// alone via runOne) so the writable child's IN-PLACE Edit/Write/Shell against the real
// tree never overlaps a sibling parent Read/Grep/Glob — a torn read. This is the
// LOAD-BEARING correctness fix for direct-write (ADR 0077): a mode:"read-write" child
// mutates the real workspace DURING its run (no fork, no merge), so the dispatcher
// MUST keep it mutate-serial — independent of any merger (there no longer is one). It
// returns true ONLY for a call that will ACTUALLY run writable: mode:"read-write"
// with the writable child engine wired (the writable explorer) OR the agent writable
// factory wired (a writable named specialist mutates the real tree too — ADR 0058);
// the two are OR'd so a deployment wiring either form keeps its writable calls
// dispatch-serial. A malformed/unparseable args payload returns false (the call
// errors later anyway, and never writes).
func (t *SubagentTool) MutatesParent(call session.ToolCall) bool {
	if t.writableChildEngine == nil && t.agentWritableFactory == nil {
		return false
	}
	var args subagentArgs
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return false
	}
	return strings.TrimSpace(args.Mode) == subagentModeReadWrite
}

// Execute runs one subagent: it builds a FRESH child Session (own conversation,
// own Limits, its configured mode) — or, on `resume`, reloads the persisted child
// session and recovers its terminal state (completed→Reopen, cancelled→Interrupt,
// failed→Recover) before driving it in a NEW workspace fork — runs the
// child loop via the injected child Engine, drains the child's entire Event stream
// internally, and returns only the child's final summary text as a single
// ToolResult. The parent therefore never observes the child's intermediate
// events (gauntlet #7).
//
// The child run is bounded by the parent ctx: cancelling the parent cancels the
// child. Any permission ask the child raises is auto-denied so the child is
// non-interactive. When the child finishes, the SubagentStop hook fires
// best-effort.
func (t *SubagentTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	// The plain Execute path forwards nothing: a nil emit makes the run silent, so
	// existing callers (and the team supervisor's reuse of the drain contract) are
	// unaffected by the observability seam.
	return t.run(ctx, call, env, nil, parentCaps{})
}

// ExecuteObserved runs the subagent like Execute but, when emit is non-nil,
// forwards a REDACTED, metadata-only projection of the child's activity to the
// parent run's event stream via the three subagent.* events. emit only sequences
// and channels events; it never touches the parent's Conversation, so this is
// orthogonal to context isolation (gauntlet #7): the child's CONTENT still never
// enters the parent context. It is the observableTool seam the dispatcher calls.
func (t *SubagentTool) ExecuteObserved(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event)) (session.ToolResult, error) {
	return t.run(ctx, call, env, emit, parentCaps{})
}

// ExecuteWithParent is the childCapableTool seam: it runs the subagent like
// ExecuteObserved but threads the PARENT's capabilities (interactivity + the surface
// back-channel) into the child posture, so a child Shell ask that A1/A2 did not
// auto-resolve is SURFACED to the human (interactive) or auto-denied with the accurate
// message + operator diagnostic (headless).
func (t *SubagentTool) ExecuteWithParent(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event), caps parentCaps) (session.ToolResult, error) {
	return t.run(ctx, call, env, emit, caps)
}

// run is the shared implementation behind Execute (emit == nil) and
// ExecuteObserved (emit != nil). It builds a FRESH child session, runs the child
// loop against the SAME workspace, drains the child's entire Event stream
// internally, optionally forwards a redacted projection of that activity, and
// returns only the child's final summary text as a single ToolResult.
// selectChildEngine resolves the child engine + base session limits for a Subagent call
// from its `agent` / `model` arguments. The five cases, in precedence order:
//   - `agent`+`model` together: REBUILDS the named specialist's scoped engine on the
//     override model via agentModelFactory (the override runs on the def's resolved
//     provider). The pre-built agentEngines map is never mutated. Without a wired factory
//     the combination is unsupported (an honest error, never a silent fallback); an
//     unknown agent or an unroutable model are model-addressable errors. (Unreachable
//     with writable=true — validateMode rejects read-write+agent+model first.)
//   - `agent` only + writable: routes through agentWritableFactory (a WRITABLE
//     specialist, ADR 0058) on its resolved model, or through agentWritableModelFactory
//     when the OPT-IN router selects a model for an unpinned def. A routed construction
//     decline falls back to agentWritableFactory; per-def limits remain untouched.
//   - `agent` only (read-only): routes to the pre-built specialist engine (agentEngines)
//     via selectReadOnlyAgentEngine — which, for a ROUTABLE def (issue #286), applies the
//     OPT-IN router's routedModel by rebuilding the def's scoped engine on it (fail-soft to
//     the pre-built engine on a factory decline; per-def limits untouched).
//   - `model` only (no agent): mints a generic explorer child for the requested model via
//     engineFactory (read-only) or writableEngineFactory (writable, issue #285).
//   - neither: the default explorer + the Subagent tool's default limits (or the router's
//     routedModel for a plain default delegation).
//
// It returns ok=false with a model-addressable error ToolResult on a bad selection (an
// unsupported combination, an unknown agent, an unwired/unroutable model), and the chosen
// engine + limits on success.
func (t *SubagentTool) selectChildEngine(callID session.ToolCallID, args subagentArgs, writable bool, routedModel string) (engine *Engine, limits session.Limits, errResult session.ToolResult, routed, ok bool) {
	wantAgent := strings.TrimSpace(args.Agent)
	wantModel := strings.TrimSpace(args.Model)

	// `agent`+`model` together: rebuild the specialist's scoped engine on the override
	// model. The model runs on the def's resolved provider (the factory owns that
	// resolution), but the specialist's catalog/prompt/hooks/memory are kept — NOT the
	// generic explorer set. The pre-built agentEngines map is read for the name-truth
	// check only; a fresh engine is minted per call, never inserted. (validateMode
	// rejects read-write+agent+model before this is reached with writable=true, so this
	// arm only fires for the read-only agent+model override.)
	if wantAgent != "" && wantModel != "" {
		if t.agentModelFactory == nil {
			return nil, session.Limits{}, session.NewToolError(callID,
				"Subagent: `agent`+`model` together is not supported in this deployment"), false, false
		}
		if _, found := t.agentEngines[wantAgent]; !found {
			return nil, session.Limits{}, session.NewToolError(callID, "Subagent: "+t.unknownAgentHint(wantAgent)), false, false
		}
		eng, found := t.agentModelFactory(wantAgent, wantModel)
		if !found || eng == nil {
			return nil, session.Limits{}, session.NewToolError(callID,
				fmt.Sprintf("Subagent: unknown or unroutable model %q for agent %q; omit `model` to run the specialist on its own model", wantModel, wantAgent)), false, false
		}
		engine = eng
		if l, found := t.agentLimits[wantAgent]; found {
			limits = l
		} else {
			limits = t.limits
		}
		return engine, limits, session.ToolResult{}, false, true
	}

	// Route to a named specialist when requested (each arm returns): a WRITABLE specialist
	// (mode:"read-write"+agent, ADR 0058) via selectWritableSpecialistEngine, else the
	// read-only specialist (selectReadOnlyAgentEngine — which also applies the issue-#286
	// routable-def routed override). An unknown name is a model-addressable error inside
	// those helpers; it never silently falls back to the wrong scope/prompt.
	if wantAgent != "" {
		if writable {
			return t.selectWritableSpecialistEngine(callID, wantAgent, routedModel)
		}
		return t.selectReadOnlyAgentEngine(callID, wantAgent, routedModel)
	}

	// No `agent`: the default-explorer path. A writable EXPLORER (issue #285) honours a
	// per-call `model`/router pick on the WRITABLE engine (selectWritableExplorerEngine);
	// otherwise the read-only default explorer honours them (selectReadOnlyModelEngine) or
	// keeps the default explorer. Both return BEFORE any read-only clobber, so a writable
	// call NEVER runs a read-only engine (the pre-#285 bug the writable clobber caused).
	engine = t.childEngine
	limits = t.limits
	if writable {
		return t.selectWritableExplorerEngine(callID, wantModel, routedModel, limits)
	}
	return t.selectReadOnlyModelEngine(callID, wantModel, routedModel, engine, limits)
}

// selectReadOnlyAgentEngine resolves a read-only `agent` delegation to its pre-built SCOPED
// specialist engine (agentEngines) + per-def limits, then applies the issue-#286 routable-def
// routed override: when the OPT-IN router classified this delegation (routedModel != "", set
// ONLY for a def in t.routableAgents — maybeRouteModel gates it) and the
// agent+model factory is wired, the def's SCOPED engine is REBUILT on the routed model via
// agentModelFactory. It is FAIL-SOFT — a factory decline (e.g. an inline-MCP def) or a miss
// keeps the pre-built def engine, never an error (the router is never load-bearing). Per-def
// limits are UNTOUCHED — only the engine swaps (the def's own turn/tool bounds still bind). An
// unknown name is the same model-addressable error the pinned path returns (never a silent
// fallback to the wrong scope). Extracted from selectChildEngine for the gocyclo budget.
func (t *SubagentTool) selectReadOnlyAgentEngine(callID session.ToolCallID, wantAgent, routedModel string) (*Engine, session.Limits, session.ToolResult, bool, bool) {
	eng, found := t.agentEngines[wantAgent]
	if !found {
		return nil, session.Limits{}, session.NewToolError(callID, "Subagent: "+t.unknownAgentHint(wantAgent)), false, false
	}
	limits := t.limits
	if l, ok := t.agentLimits[wantAgent]; ok {
		limits = l
	}
	if routedModel != "" && t.agentModelFactory != nil {
		if eng2, ok := t.agentModelFactory(wantAgent, routedModel); ok && eng2 != nil {
			return eng2, limits, session.ToolResult{}, true, true
		}
	}
	return eng, limits, session.ToolResult{}, false, true
}

// selectReadOnlyModelEngine resolves the READ-ONLY explorer engine for a per-call `model`
// override or the OPT-IN router pick (extracted from selectChildEngine for the gocyclo
// budget). A per-call `model` (R9/D6) mints via engineFactory — an unwired factory or an
// unroutable model is a LOUD model-addressable error, never a silent inherit. Else a routed
// pick (ADR 0031) mints FAIL-SOFT via engineFactory — a miss (or unwired factory) falls
// through to the inherited default explorer `fallback` (never an error; the router is never
// load-bearing). routedModel is the ALREADY-RESOLVED concrete id. fallback is the engine
// selectChildEngine already chose (the default explorer, or a read-only named specialist).
//
// SIBLING of selectWritableExplorerEngine — DELIBERATELY NOT MERGED. The two share a
// three-case shape (per-call model → routed pick → fallback) but diverge in the factory they
// mint through (engineFactory vs writableEngineFactory), the fallback (a passed-in `fallback`
// vs the writable child engine), and the user-facing error wording ("inherit the parent's
// model" vs "run the writable subagent on its default model"). Merging them onto a shared
// helper would couple the read-only and writable posture and force one of those seams to
// leak into the other — the wrong abstraction. Keep them parallel.
func (t *SubagentTool) selectReadOnlyModelEngine(callID session.ToolCallID, wantModel, routedModel string, fallback *Engine, limits session.Limits) (*Engine, session.Limits, session.ToolResult, bool, bool) {
	if wantModel != "" {
		if t.engineFactory == nil {
			return nil, session.Limits{}, session.NewToolError(callID,
				"Subagent: per-call `model` override is not supported in this deployment"), false, false
		}
		eng, found := t.engineFactory(wantModel)
		if !found || eng == nil {
			return nil, session.Limits{}, session.NewToolError(callID,
				fmt.Sprintf("Subagent: unknown or unroutable model %q; omit `model` to inherit the parent's model", wantModel)), false, false
		}
		return eng, limits, session.ToolResult{}, false, true
	}
	if routedModel != "" && t.engineFactory != nil {
		if eng, found := t.engineFactory(routedModel); found && eng != nil {
			return eng, limits, session.ToolResult{}, true, true
		}
	}
	return fallback, limits, session.ToolResult{}, false, true
}

// selectWritableExplorerEngine resolves a mode:"read-write" call with NO `agent` (issue
// #285) to a WRITABLE explorer engine. It is a method (extracted from selectChildEngine for
// the gocyclo budget, the selectWritableSpecialistEngine sibling). Three cases:
//   - a per-call `model` mints via writableEngineFactory; an unwired factory (validateMode
//     already caught this — defensive belt-and-suspenders) or an unroutable model is a LOUD
//     model-addressable error, NEVER a silent inherit onto the default writable model;
//   - else a routed pick (the OPT-IN router, ADR 0031) mints FAIL-SOFT via the factory — a
//     miss (or an unwired factory) falls back to the default writable explorer engine, never
//     an error (the router is never load-bearing); routedModel is the ALREADY-RESOLVED id;
//   - else the default writable explorer engine (writableChildEngine).
//
// limits is threaded through unchanged (the writable explorer uses the default explorer
// bound, the same as the read-only default path).
//
// SIBLING of selectReadOnlyModelEngine — DELIBERATELY NOT MERGED. They share the
// three-case shape (per-call model → routed pick → fallback) but diverge in the factory
// (writableEngineFactory vs engineFactory), the fallback (writableChildEngine vs a passed-in
// `fallback`), and the error wording — merging would couple the writable and read-only
// posture, the wrong abstraction. See selectReadOnlyModelEngine's note.
func (t *SubagentTool) selectWritableExplorerEngine(callID session.ToolCallID, wantModel, routedModel string, limits session.Limits) (*Engine, session.Limits, session.ToolResult, bool, bool) {
	if wantModel != "" {
		if t.writableEngineFactory == nil {
			return nil, session.Limits{}, session.NewToolError(callID,
				"Subagent: mode:\"read-write\" with a per-call `model` is not supported in this deployment; omit `model` to run the writable subagent on its default model"), false, false
		}
		eng, found := t.writableEngineFactory(wantModel)
		if !found || eng == nil {
			return nil, session.Limits{}, session.NewToolError(callID,
				fmt.Sprintf("Subagent: unknown or unroutable model %q; omit `model` to run the writable subagent on its default model", wantModel)), false, false
		}
		return eng, limits, session.ToolResult{}, false, true
	}
	if routedModel != "" && t.writableEngineFactory != nil {
		if eng, found := t.writableEngineFactory(routedModel); found && eng != nil {
			return eng, limits, session.ToolResult{}, true, true
		}
	}
	return t.writableChildEngine, limits, session.ToolResult{}, false, true
}

// selectWritableSpecialistEngine resolves a mode:"read-write"+`agent` call to a WRITABLE
// specialist. A router-selected model first mints through agentWritableModelFactory; when
// that target is unavailable it falls back to agentWritableFactory, keeping routing
// fail-soft while allowing reconcileRoutedModel to expose truthful fallback metadata. Both
// factories rebuild the def's scoped engine with allowMutating=true and the MAIN session's
// runner, so the child mutates the parent environment directly. The name-truth check fires
// before either factory, and the def's limits bind whichever engine is selected.
func (t *SubagentTool) selectWritableSpecialistEngine(callID session.ToolCallID, wantAgent, routedModel string) (*Engine, session.Limits, session.ToolResult, bool, bool) {
	if t.agentWritableFactory == nil {
		return nil, session.Limits{}, session.NewToolError(callID,
			"Subagent: mode:\"read-write\" with `agent` is not supported in this deployment"), false, false
	}
	if _, found := t.agentEngines[wantAgent]; !found {
		return nil, session.Limits{}, session.NewToolError(callID, "Subagent: "+t.unknownAgentHint(wantAgent)), false, false
	}
	limits := t.limits
	if l, found := t.agentLimits[wantAgent]; found {
		limits = l
	}
	if routedModel != "" && t.agentWritableModelFactory != nil {
		if eng, found := t.agentWritableModelFactory(wantAgent, routedModel); found && eng != nil {
			return eng, limits, session.ToolResult{}, true, true
		}
	}
	eng, found := t.agentWritableFactory(wantAgent)
	if !found || eng == nil {
		return nil, session.Limits{}, session.NewToolError(callID,
			fmt.Sprintf("Subagent: writable specialist %q is unavailable (the def may have inline MCP servers, a v1 scope limit); omit `mode` to run it read-only, or omit `agent` for a writable explorer", wantAgent)), false, false
	}
	return eng, limits, session.ToolResult{}, false, true
}

// resolveMaxRunTokens resolves the per-call cumulative token budget from `max_run_tokens`.
// A nil or non-positive value is "unset": it yields 0, meaning inherit the engine's budget
// (which may itself be bounded or disabled).
func resolveMaxRunTokens(args subagentArgs) int {
	if args.MaxRunTokens != nil && *args.MaxRunTokens > 0 {
		return *args.MaxRunTokens
	}
	return 0
}

// buildSubagentRunRequest assembles the per-call RunRequest, the synthetic SubmitResult
// tool, and the effective child prompt for one Subagent run:
//   - Per-call token ceiling (R4): a Run-scoped TIGHTEN-ONLY MaxRunTokens override carried
//     via Engine.Run, bounding the SHARED child engine WITHOUT minting a fresh engine.
//     0 ⇒ inherit the engine's operator-default budget; it folds tighten-only in the loop.
//   - Resume note: on RESUME the effective prompt is prefixed with the honest harness note
//     BEFORE the structured-output wrap, so it rides inside the structured prompt too. WHICH
//     note is resumePosture.note()'s three-cell decision over the two INDEPENDENT axes —
//     where this call runs (its own `mode`) and what the EARLIER run left behind
//     (editsSurvived, decided in prepareChildSession). Neither axis is inferred from the
//     other: `mode` may CHANGE across a resume, so a previously read-only child's edits are
//     gone whatever mode this call asks for, AND a writable call runs in the real workspace
//     whatever the earlier run did. A fresh call is unchanged.
//   - Structured output (D1/D2): when an output_schema is supplied, a synthetic SubmitResult
//     tool (run-scoped — never registered into the shared catalog) whose parameters ARE the
//     schema is created and the prompt is wrapped to instruct the child to call it. Omitted
//     ⇒ today's free-text path.
func buildSubagentRunRequest(args subagentArgs, resuming bool, posture resumePosture, forkAdvisory string) (RunRequest, *submitResultTool, string) {
	var runReq RunRequest
	// 0 = inherit the engine budget, which may itself be bounded or disabled.
	if v := resolveMaxRunTokens(args); v > 0 {
		// Floor: protect against a model self-imposing an unusably small budget. The
		// system prompt + AGENTS.md + project instructions are replayed every turn and
		// cost ~20k+ tokens on turn 1 alone; a budget below MinSubagentRunTokens would
		// stop the child before it can complete one useful turn. Raising the override to
		// the floor is safe: the operator ceiling still wins via the tighten-only fold
		// in effectiveMaxRunTokens (min(override, Deps.MaxRunTokens)), so this never
		// raises the effective budget above the operator's bound.
		if v < MinSubagentRunTokens {
			v = MinSubagentRunTokens
		}
		runReq.MaxRunTokensOverride = v
	}
	prompt := args.Prompt
	if resuming {
		prompt = posture.note() + "\n\n" + prompt
	}
	// A degraded-fork advisory (the dirty-overlay fell back to committed HEAD) is
	// prepended so it reaches the child LLM's prompt — model-visible, not just a log.
	// It composes with the resume note: both notes lead the prompt when both apply.
	if forkAdvisory != "" {
		prompt = forkAdvisory + "\n\n" + prompt
	}
	var submit *submitResultTool
	if len(args.OutputSchema) > 0 && strings.TrimSpace(string(args.OutputSchema)) != "" {
		submit = newSubmitResultTool(args.OutputSchema)
		runReq.ExtraTools = []tool.Tool{submit}
		runReq.extraToolOptions = map[string]extraToolOptions{
			submitResultToolName: {AuthorityExempt: true},
		}
		// Wrap the (possibly staleness-noted) prompt so a resumed structured-output child
		// still sees the staleness note inside the structured-output instruction.
		prompt = structuredOutputPrompt(prompt, args.OutputSchema)
	}
	return runReq, submit, prompt
}

// resumeSupported reports whether this deployment can serve a `resume:` call at all —
// validateResume's FIRST precondition, and the gate every "you can resume this" affordance
// must agree with. The rule those affordances enforce is stated in three doc-comments
// (subagentErrorResumeHint, subagentTimeoutNote, renderWritableSubagentResult): NEVER
// advertise a resume validateResume will refuse, because instructing the model to take an
// action that cannot succeed is the inverse of the discoverability rule (ADR 0070). Naming
// the predicate keeps that contract in ONE place, so a second deployment-level
// precondition (a read-only store, a config kill-switch) cannot be added to validateResume
// while the advertisements keep saying yes.
func (t *SubagentTool) resumeSupported() bool { return t.store != nil }

// validateResume checks a `resume` Subagent call's preconditions BEFORE any engine
// selection or load: a store must be wired (resume needs persistence), `resume` is
// mutually exclusive with `agent`/`model` (a resumed child runs on the default explorer
// engine only), and the resume id must be a SUBAGENT id (the prefix gate rejects team
// member / service ids — keyed on t.idPrefix+"-", NOT a literal). It returns the default
// explorer engine on success, or a model-addressable error ToolResult (ok=false).
func (t *SubagentTool) validateResume(callID session.ToolCallID, args subagentArgs) (engine *Engine, errResult session.ToolResult, ok bool) {
	if !t.resumeSupported() {
		return nil, session.NewToolError(callID, "Subagent: `resume` is not supported in this deployment (no session store wired)"), false
	}
	if strings.TrimSpace(args.Agent) != "" || strings.TrimSpace(args.Model) != "" {
		return nil, session.NewToolError(callID,
			"Subagent: `resume` cannot be combined with `agent` or `model` — a resumed subagent continues on the default explorer engine"), false
	}
	if !strings.HasPrefix(args.Resume, t.idPrefix+"-") {
		return nil, session.NewToolError(callID,
			fmt.Sprintf("Subagent: resume id %q is not a subagent session; only ids from a Subagent result's 'agentId:' line can be resumed (team member transcripts are read-only via InspectMember)", args.Resume)), false
	}
	return t.childEngine, session.ToolResult{}, true
}

// applyCallTimeout imposes the per-call wall-clock deadline (timeout_ms) on the
// child run: a hard ceiling independent of the turn/tool limits. A non-positive /
// nil value leaves ctx unchanged and returns a nil timeoutCtx + a no-op cancel.
// When a deadline applies, the derived ctx IS the returned timeoutCtx (held
// separately so the terminal rendering can tell a deadline-kill apart from a
// parent cancellation), and cancelTimeout is the explicit cancel the background
// path hands to its detached goroutine.
func applyCallTimeout(ctx context.Context, timeoutMs *int) (context.Context, context.Context, context.CancelFunc) {
	if timeoutMs == nil || *timeoutMs <= 0 {
		return ctx, nil, func() {}
	}
	tctx, cancel := context.WithTimeout(ctx, time.Duration(*timeoutMs)*time.Millisecond)
	return tctx, tctx, cancel
}

// validateFork checks a fork:true Subagent call's preconditions BEFORE engine
// selection (issue #34). Fork seeds the child from a deep copy of the parent
// conversation and runs it on the PARENT's engine, so it is mutually exclusive
// with the args that pin a DIFFERENT engine/conversation: the checks are ordered
// so the FIRST conflict is the model's signal. The last check needs the parent's
// fork-history seam (nil on the plain Execute path, where no parent session is
// threaded) — a fork there is honestly unsupported, never a silent fresh child.
// It returns the SNAPSHOT SLICE (taken SYNCHRONOUSLY here on the dispatch
// goroutine, while the parent conversation is stable — never inside a detached
// background goroutine) when fork is on and the preconditions pass, nil when fork
// is off, or a model-addressable error (ok=false) on a precondition violation. It
// is a free function (no receiver state).
func validateFork(callID session.ToolCallID, args subagentArgs, caps parentCaps) (forkHistory []session.Message, errResult session.ToolResult, ok bool) {
	if !args.Fork {
		return nil, session.ToolResult{}, true
	}
	switch {
	case strings.TrimSpace(args.Resume) != "":
		return nil, session.NewToolError(callID,
			"Subagent: `fork` cannot be combined with `resume` — a fork inherits THIS conversation; resume continues a different persisted subagent"), false
	case strings.TrimSpace(args.Agent) != "":
		return nil, session.NewToolError(callID,
			"Subagent: `fork` cannot be combined with `agent` — a forked subagent runs on the parent's engine, not a specialist"), false
	case strings.TrimSpace(args.Model) != "":
		return nil, session.NewToolError(callID,
			"Subagent: `fork` cannot be combined with `model` — a forked subagent inherits the parent's engine/model"), false
	case caps.forkHistory == nil:
		return nil, session.NewToolError(callID,
			"Subagent: `fork` is not supported on this run"), false
	}
	return caps.forkHistory(), session.ToolResult{}, true
}

// subagent mode constants — the closed set the `mode` arg validates against.
const (
	subagentModeReadOnly  = "read-only"
	subagentModeReadWrite = "read-write"
)

// validateMode validates the `mode` arg, then enforces the read-write combination
// guards (D2/D3/D4). It returns writable=true when the call wants (and may have) the
// writable child; "" / "read-only" return writable=false. On any violation it
// returns a model-addressable error ToolResult (ok=false). The checks (in order):
//   - mode must be in {"", "read-only", "read-write"} — an unknown value is rejected;
//   - read-write + background is rejected (a detached child writing the parent tree
//     after the turn advances would race the parent — D3);
//   - read-write + agent + model together is rejected (a v1 scope limit — a writable
//     specialist runs on its own resolved model; omit `model`, or omit `agent` for a
//     writable explorer on a chosen model);
//   - read-write + agent with no agentWritableFactory wired is rejected as "not
//     supported in this deployment" (the writable-specialist path is unwired — also
//     the no-FS gate, since the no-FS subagent tool wires no writable factory);
//   - read-write + model (no agent) with no writableEngineFactory wired is rejected as
//     "not supported in this deployment" (issue #285 — the writable-explorer-on-a-model
//     path is unwired; a LOUD error, never a silent inherit onto the default model);
//   - read-write with no writable child engine wired (the no-`agent` writable
//     explorer case) is rejected as "not supported in this deployment" (D4).
//
// read-write + agent ALONE is ALLOWED when the deployment wires the writable-
// specialist factory (WithAgentWritableEngineFactory — a writable specialist, ADR
// 0058): the named specialist's scoped engine is rebuilt with allowMutating=true on
// the def's resolved model and runs Edit/Write/Shell against the real parent workspace
// (direct-write parity, ADR 0077). read-write COMPOSES with fork/resume/output_schema/
// timeout_ms/limits (no guard here for those). It is a method only to read
// t.writableChildEngine and t.agentWritableFactory.
func (t *SubagentTool) validateMode(callID session.ToolCallID, args subagentArgs) (writable bool, errResult session.ToolResult, ok bool) {
	switch strings.TrimSpace(args.Mode) {
	case "", subagentModeReadOnly:
		return false, session.ToolResult{}, true
	case subagentModeReadWrite:
		// fall through to the read-write guards below
	default:
		return false, session.NewToolError(callID,
			fmt.Sprintf("Subagent: unknown mode %q; use %q (default) or %q", strings.TrimSpace(args.Mode), subagentModeReadOnly, subagentModeReadWrite)), false
	}
	// Arm order is load-bearing: the agent+model arm MUST fire before the agent+
	// factory-nil arm (an agent+model call sets agent!="" and would otherwise be
	// misclassified as a factory-nil rejection), and the agent arms MUST fire before
	// the no-agent writable-engine-nil arm.
	switch {
	case args.Background:
		return false, session.NewToolError(callID,
			"Subagent: mode:\"read-write\" cannot be combined with `background` — a writable subagent writes directly to your workspace and must run inline (serially) so it cannot race your own edits; omit `background`"), false
	case strings.TrimSpace(args.Agent) != "" && strings.TrimSpace(args.Model) != "":
		return false, session.NewToolError(callID,
			"Subagent: mode:\"read-write\" cannot be combined with both `agent` and `model` — a writable named specialist runs on its own resolved model; omit `model` (or omit `agent` for a writable explorer on a chosen model)"), false
	case strings.TrimSpace(args.Agent) != "" && t.agentWritableFactory == nil:
		return false, session.NewToolError(callID,
			"Subagent: mode:\"read-write\" with `agent` is not supported in this deployment"), false
	case strings.TrimSpace(args.Agent) == "" && strings.TrimSpace(args.Model) != "" && t.writableEngineFactory == nil:
		// A writable EXPLORER on a per-call model (no `agent`) needs the writable engine
		// factory (issue #285). Unwired ⇒ a LOUD error, never a silent inherit that would
		// run the writable subagent on a model the caller did not ask for.
		return false, session.NewToolError(callID,
			"Subagent: mode:\"read-write\" with a per-call `model` is not supported in this deployment; omit `model` to run the writable subagent on its default model"), false
	case strings.TrimSpace(args.Agent) == "" && t.writableChildEngine == nil:
		return false, session.NewToolError(callID,
			"Subagent: mode:\"read-write\" (writable subagent) is not supported in this deployment"), false
	}
	return true, session.ToolResult{}, true
}

// validatePreconditions runs the two pre-engine-selection guards in order —
// validateMode (D2/D3/D4) then validateFork (issue #34) — and returns the writable
// flag, the fork-history snapshot, and the FIRST violation's model-addressable
// error (ok=false). Combining them keeps run()'s guard cascade to a single
// branch (the gocyclo budget) without losing the first-conflict-wins ordering.
func (t *SubagentTool) validatePreconditions(callID session.ToolCallID, args subagentArgs, caps parentCaps) (writable bool, forkHistory []session.Message, errResult session.ToolResult, ok bool) {
	writable, errRes, mok := t.validateMode(callID, args)
	if !mok {
		return false, nil, errRes, false
	}
	forkHistory, errRes, fok := validateFork(callID, args, caps)
	if !fok {
		return false, nil, errRes, false
	}
	return writable, forkHistory, session.ToolResult{}, true
}

// maybeRouteModel consults the OPT-IN semantic model router (ADR 0031) and returns the
// classified category + the ALREADY-RESOLVED concrete model id to mint the child on (both
// empty when not routed). PRECEDENCE is enforced by GATING: an explicit per-call `model`, a
// `fork`, or a `resume` already pins the child's engine/conversation, so the router never
// fires for them — it fills the gap, never overrides an explicit choice. caps.routeTask is
// nil when no router is wired (the byte-identical default) or on a child run (no nesting —
// a child has no Subagent tool, so structurally no parentCaps.routeTask). FAIL-SOFT: a
// router miss (ok=false) returns empty strings and the caller inherits its default engine.
//
// reason is the BARE-METADATA why the router did NOT classify (issue #397): EMPTY on a
// routed hit, otherwise a session.RoutingReason* gate constant (resume / fork /
// router-disabled / pinned-model / agent-def-pinned-model) or the missReason routeTask
// returned (classifier miss / breaker-open / aborted). It rides the subagent.start event's
// RoutingReason field — never the task prompt or classifier output (gauntlet #7).
//
// Two gate shapes:
//   - NO `agent` (a plain default delegation): routes, EXCEPT a writable delegation whose
//     writable engine factory is unwired (issue #285 — the pick would be DISCARDED by
//     selectChildEngine's writable arm, so don't spend the classifier). That gate reports
//     RoutingReasonRouterDisabled: the pick could not be consumed, as if no router existed.
//   - a NAMED `agent` (issue #286): routes ONLY a ROUTABLE def — one that expressed NO model
//     intent (composition put it in t.routableAgents) — with the applicable model factory
//     wired. A writable named def additionally requires agentWritableFactory as its fail-soft
//     fallback. A pinned def (ANY def.Model, incl. explicit `inherit`) reports
//     RoutingReasonAgentDefPinned; an incomplete factory set reports
//     RoutingReasonRouterDisabled (the pick could not be consumed — as good as no router).
//
// It is a method (not a free func) to read t.writableEngineFactory / t.agentModelFactory /
// t.routableAgents. The ctx is the run's ctx, threaded to routeTask so a Run.Cancel
// propagates into the classifier turn (issue #94).
func (t *SubagentTool) maybeRouteModel(ctx context.Context, args subagentArgs, resuming, writable bool, caps parentCaps) (category, model, reason string) {
	// PRECEDENCE (issue #397): the explicit CHOICE gates (resume / fork / per-call model /
	// agent def) attribute BEFORE the router-absent gate, so a delegation that pinned its
	// model is never mislabeled "router-disabled" when no router is wired. The choice
	// exists regardless of whether a router could have consumed a pick; naming it is the
	// accurate why.
	switch {
	case resuming:
		return "", "", session.RoutingReasonResume
	case args.Fork:
		return "", "", session.RoutingReasonFork
	case strings.TrimSpace(args.Model) != "":
		return "", "", session.RoutingReasonPinnedModel
	}
	if wantAgent := strings.TrimSpace(args.Agent); wantAgent != "" {
		// A NAMED agent: a def that expressed model intent attributes to its own gate. A
		// ROUTABLE def needs the applicable routed factory plus its ordinary writable
		// fallback when writable; an incomplete factory set cannot consume a pick.
		if _, pinned := t.pinnedAgents[wantAgent]; pinned {
			return "", "", session.RoutingReasonAgentDefPinned
		}
		if _, routable := t.routableAgents[wantAgent]; !routable {
			return "", "", session.RoutingReasonRouterDisabled
		}
		if writable {
			if t.agentWritableFactory == nil || t.agentWritableModelFactory == nil {
				return "", "", session.RoutingReasonRouterDisabled
			}
		} else if t.agentModelFactory == nil {
			return "", "", session.RoutingReasonRouterDisabled
		}
	} else if writable && t.writableEngineFactory == nil {
		// A plain WRITABLE delegation whose writable engine factory is unwired would DISCARD
		// the pick (issue #285) — router-disabled (as good as absent).
		return "", "", session.RoutingReasonRouterDisabled
	}
	if caps.routeTask == nil {
		return "", "", session.RoutingReasonRouterDisabled
	}
	cat, m, missReason, ok := caps.routeTask(ctx, args.Prompt)
	if ok {
		return cat, strings.TrimSpace(m), ""
	}
	return "", "", missReason
}

// reconcileRoutedModel makes delegation-start metadata agree with the engine that will
// actually run. Router factories are deliberately fail-soft: a selected target may be
// unavailable and the delegation then inherits its fallback engine. In that case the
// routed category/model must be cleared so consumers do not report a model that never ran,
// and the static reason records the fallback. The factory-acceptance bit handles every
// path (plain, writable, named specialist, and Parallel) without guessing from model ids.
func reconcileRoutedModel(category, routedModel, reason string, accepted bool) (string, string, string) {
	if strings.TrimSpace(routedModel) != "" && !accepted {
		return "", "", session.RoutingReasonTargetUnavailable
	}
	return category, routedModel, reason
}

// resolveEngineAndLimits resolves one Subagent call's child engine and base
// session limits, then applies the per-call tighten-only overrides.
//
// Resume mode CONTINUES a previously-run subagent by its persisted id: it forces
// the default explorer engine (v1: a resumed child runs on childEngine only) and
// is mutually exclusive with `agent`/`model` (those pin their own engine) — the
// validation runs BEFORE selectChildEngine so the exclusivity error is the
// model's first signal, and the prefix check rejects non-subagent ids (e.g. a
// team member's transcript). On resume, limits is later overwritten from the
// LOADED session's preserved Limits (resolveResumeSession), then tightened
// against the per-call args there.
//
// The tighten-only discipline: the model may make THIS child stricter than the
// inherited bound, never looser, so a per-call arg can't escape the operator's
// ceiling (tightenLimit ignores nil / non-positive values and only lowers).
// routedModel, when non-empty, is the ALREADY-RESOLVED concrete model id the OPT-IN
// model router (ADR 0031) classified this plain default delegation into. It is threaded
// to selectChildEngine, which mints the child on it through the SAME per-call factory
// path args.Model uses (decide-once, contamination-safe). It is only ever non-empty on a
// plain default delegation (the run() hook gates it on no model/agent/fork/resume), so it
// never collides with an explicit args.Model/args.Agent — and a resume ignores it (a
// resumed child runs on the default explorer engine only).
func (t *SubagentTool) resolveEngineAndLimits(callID session.ToolCallID, args subagentArgs, resuming, writable bool, routedModel string) (engine *Engine, limits session.Limits, errResult session.ToolResult, routed, ok bool) {
	if resuming {
		eng, errRes, vok := t.validateResume(callID, args)
		if !vok {
			return nil, session.Limits{}, errRes, false, false
		}
		engine = eng
	} else {
		eng, lim, errRes, accepted, sok := t.selectChildEngine(callID, args, writable, routedModel)
		if !sok {
			return nil, session.Limits{}, errRes, false, false
		}
		engine, limits, routed = eng, lim, accepted
	}
	// RESUME + read-write: force the default writable explorer engine (issue #285). A
	// resumed child rejects `model`/`agent` and gates the router (validateResume +
	// maybeRouteModel), so selectChildEngine never runs for it — the resume branch above
	// set `engine` from the LOADED session's default explorer path. A writable resume must
	// instead continue on the WRITABLE explorer so its Edit/Write survive. This preserves
	// today's writable+resume behaviour verbatim. (A NON-resume writable call already got
	// its correct engine from selectChildEngine's writable arm: the writable explorer, the
	// per-call-`model` writable engine, the routed writable engine, or the writable
	// specialist — so there is NO unconditional clobber here anymore, which is exactly what
	// let a per-call `model`/router pick take effect for a writable explorer — the #285 fix.)
	// The writable child runs DIRECTLY against the parent workspace (no fork — ADR 0077);
	// git is the rollback layer. read-write COMPOSES with fork/resume/output_schema/limits.
	if resuming && writable {
		engine = t.writableChildEngine
	}
	limits.MaxTurns = tightenLimit(limits.MaxTurns, args.MaxTurns)
	limits.MaxToolCalls = tightenLimit(limits.MaxToolCalls, args.MaxToolCalls)
	return engine, limits, session.ToolResult{}, routed, true
}

// prepareChildSession resolves everything between the concurrency slot and the
// drive for a FOREGROUND call: the resume load (+ terminal recovery), the
// workspace fork, and the child session build.
//
// Ordering is load-bearing: resume load + terminal recovery happen BEFORE the
// fork, so the common error cases (unknown id, failed/non-resumable state,
// broken store) FAIL FAST without paying a fork/unfork round-trip; the recovered
// session is re-homed AFTER the fork, once the fresh root exists (see
// buildChildSession). A fork failure is a tool error, never a silent fallback
// (forkChildEnvironment). The returned cleanup is ALWAYS non-nil (a no-op when
// nothing survives) so the caller can defer it unconditionally; on a
// session-build failure the just-created fork is torn down here.
//
// It also decides editsSurvived — whether the resumed child's earlier FILE EDITS are
// still on disk — because this is the only place that sees the resumed session's
// PERSISTED workspace before buildChildSession re-homes it. See editsSurvived's
// doc-comment on the named result below and resumeWritableNote.
func (t *SubagentTool) prepareChildSession(ctx context.Context, call session.ToolCall, env tool.Environment, args subagentArgs, resuming, writable bool, childID, parentID session.SessionID, parentIncarnation session.IncarnationID, limits session.Limits, forkHistory []session.Message) (child *session.Session, runEnv tool.Environment, cleanup func() error, advisory string, editsSurvived bool, errResult session.ToolResult, ok bool) {
	noop := func() error { return nil }
	var resumedChild *session.Session
	priorRef := session.EnvironmentRef{}
	if resuming {
		loaded, errRes, rok := t.resolveResumeSession(ctx, call.ID, childID, args)
		if !rok {
			return nil, tool.Environment{}, noop, "", false, errRes, false
		}
		resumedChild = loaded
		priorRef = loaded.EnvironmentRef
	}
	// A mode:"read-write" child runs DIRECTLY against the parent workspace (no fork —
	// ADR 0077): its Edit/Write/Shell mutate the real tree in place, exactly as the
	// main agent does, and git is the rollback layer. So a writable call passes NO
	// forker (nil) — forkChildEnvironment then shares the parent content backend
	// through any composition-supplied authority-narrowing Workspace view. A
	// read-only child still uses t.childForker (a throwaway git worktree when it has a
	// shell, else the shared parent ws — read-parallel-safe via worktree isolation).
	forker := t.childForker
	if writable {
		forker = nil
	}
	runEnv, cleanupWS, advisory, errRes, fok := t.forkChildEnvironment(ctx, call.ID, env, subagentGoal(args), forker)
	if !fok {
		return nil, tool.Environment{}, noop, "", false, errRes, false
	}
	// editsSurvived: this call is writable AND the prior run executed in the very tree
	// this call runs in, so any file edits it made are still there. `writable` ALONE is
	// not enough — it comes only from the CURRENT call's `mode`, and validateMode
	// deliberately lets `mode` CHANGE across a resume, so resuming a previously
	// READ-ONLY child with mode:"read-write" would otherwise be handed
	// resumeWritableNote ("the file edits you already made are STILL IN PLACE") when its
	// worktree was torn down. A read-only child has no Edit/Write but DOES have Shell in
	// that worktree, so it may genuinely have applied edits that are now GONE: telling it
	// otherwise is the exact falsehood resumeWritableNote exists to prevent, inverted.
	// The path comparison is the honest test and needs no new persisted field.
	editsSurvived = writable && priorRef.Valid() && priorRef == env.Ref()
	child, errRes, bok := t.buildChildSession(call.ID, childID, parentID, parentIncarnation, resumedChild, runEnv, limits, forkHistory)
	if !bok {
		_ = cleanupWS()
		return nil, tool.Environment{}, noop, "", false, errRes, false
	}
	// RESUME-START persist (issue #38): children otherwise persist only at their
	// TERMINAL, so a resumed child loaded for a new long run would keep its OLD
	// snapshot ModifiedAt — the composition layer's child-session GC age pass
	// could delete it MID-RUN. Re-saving the just-loaded (recovered + re-homed)
	// session refreshes the snapshot's last-modified time, so an in-flight
	// resumed child is always "fresh" to the sweep. Same best-effort
	// persistChild discipline as the terminal save (failures swallowed).
	if resuming {
		t.persistChild(ctx, child)
	}
	return child, runEnv, cleanupWS, advisory, editsSurvived, session.ToolResult{}, true
}

//nolint:gocyclo // Delegation validation order is security-significant and intentionally explicit.
func (t *SubagentTool) run(ctx context.Context, call session.ToolCall, env tool.Environment, emit func(session.Event), caps parentCaps) (session.ToolResult, error) {
	var args subagentArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "Subagent: "+msg), nil
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return session.NewToolError(call.ID, "Subagent: 'prompt' is required and must be non-empty"), nil
	}
	// Agent selection, limits, routing, and managed authority ceilings all use the
	// same canonical lookup key. Normalize once so whitespace cannot select an
	// engine while bypassing its authority ceiling or changing its identity.
	args.Agent = strings.TrimSpace(args.Agent)

	// Precondition guards (D2/D3/D4 mode + issue-#34 fork), evaluated BEFORE engine
	// selection so the FIRST conflict is the model's signal: validateMode normalizes
	// the `mode` arg and enforces the read-write bans (background/agent/unwired);
	// validateFork enforces fork's mutual exclusions and returns the synchronous
	// parent-conversation snapshot. writable selects the writable child engine, which
	// edits the parent tree directly during the run (no fork, no merge — ADR 0077).
	writable, forkHistory, errResult, ok := t.validatePreconditions(call.ID, args, caps)
	if !ok {
		return errResult, nil
	}

	resuming := strings.TrimSpace(args.Resume) != ""
	if resuming && caps.authorityBound {
		persisted, resumeResult, loaded := t.loadOwnedResumeSession(ctx, call.ID, session.SessionID(args.Resume))
		if !loaded {
			return resumeResult, nil
		}
		persistedAuthority, bound := persisted.BoundAuthority()
		if writable && bound && !persistedAuthority.CapabilitySet.DirectWrite {
			return session.NewToolError(call.ID, "Subagent: resume authority refused: persisted child authority does not permit direct write"), nil
		}
		if authorityErr := validateResumedAuthority(caps.authority, persistedAuthority, bound); authorityErr != nil {
			return session.NewToolError(call.ID, "Subagent: resume authority refused: "+authorityErr.Error()), nil
		}
	}
	var delegatedAuthority session.Authority
	if !resuming && caps.authorityBound {
		candidate := caps.authority.CapabilitySet
		candidate.DirectWrite = writable
		ceiling, managed := t.agentCeiling(args.Agent, writable)
		var authorityErr error
		delegatedAuthority, authorityErr = deriveDelegatedAuthority(caps.authority, candidate, ceiling, args.Authority)
		if authorityErr != nil {
			return session.NewToolError(call.ID, "Subagent: delegation authority refused: "+authorityErr.Error()), nil
		}
		if managed {
			delegatedAuthority.DefinitionIdentity = "explicit:" + args.Agent
		}
	}

	// OPT-IN semantic model router (ADR 0031): for a PLAIN default delegation, classify
	// the task and mint the child on the routed model via the per-call factory path. The
	// gating + fail-soft live in maybeRouteModel; an empty routedModel inherits the
	// default explorer. routingReason names WHY the router did not classify (empty on a
	// hit) and rides the subagent.start event (issue #397). The run's ctx threads down so
	// a Run.Cancel propagates into the classifier turn (issue #94).
	routedCategory, routedModel, routingReason := t.maybeRouteModel(ctx, args, resuming, writable, caps)

	engine, limits, errResult, routedAccepted, ok := t.resolveEngineAndLimits(call.ID, args, resuming, writable, routedModel)
	if !ok {
		return errResult, nil
	}
	if errResult, ok := t.authorizeResumeLookup(ctx, call.ID, session.SessionID(args.Resume), resuming); !ok {
		return errResult, nil
	}
	routedCategory, routedModel, routingReason = reconcileRoutedModel(
		routedCategory, routedModel, routingReason, routedAccepted)

	// Background requires the parent run's child registry: it is where the started
	// child's rendered result lands for SubagentStatus collection and what the
	// run-end drain joins. A caps-less drive (plain Execute/ExecuteObserved) has
	// neither, so a background request there is an honest error, never a silent
	// foreground fallback (the model was promised detached delivery).
	if args.Background && caps.children == nil {
		return session.NewToolError(call.ID,
			"Subagent: `background` is not supported on this run (no child registry); omit it to run in the foreground"), nil
	}

	// Compute the child session id early (a FRESH call derives it from the parent call id;
	// a RESUME continues the persisted id verbatim) so the in-flight guard can register it
	// BEFORE acquiring the concurrency slot. The guard rejects a SECOND concurrent run on
	// the SAME id with a model-visible error rather than waiting: two runs over one unlocked
	// Session aggregate is a data race (correctness), and waiting would park a dispatcher
	// goroutine + a gate slot (liveness). Registered BEFORE acquireChildSlot so the conflict
	// is detected even while the second call would otherwise block on the gate. A BACKGROUND
	// child holds its id until its detached goroutine ends, so `resume` of a still-running
	// background child is rejected here, unchanged.
	childID := t.childSessionID(caps.parentSessionID, call.ID)
	if resuming {
		childID = session.SessionID(args.Resume)
	}
	if !t.tryAcquireChildID(childID) {
		return session.NewToolError(call.ID,
			fmt.Sprintf("Subagent: subagent %q is already running; wait for its result before resuming it", childID)), nil
	}
	// From here every exit path must release childID: the foreground path defers it
	// below; the background path transfers ownership to the detached goroutine.

	// Per-call wall-clock deadline: a hard ceiling on the whole child run, independent
	// of the turn/tool limits. A child that exceeds it is ctx-cancelled (the loop
	// terminates with StopCancelled), which the terminal rendering turns into a
	// time-budget tool error. nil / non-positive ⇒ no deadline (ctx unchanged). The
	// timeout ctx is held separately (timeoutCtx) so the terminal rendering can tell a
	// deadline-kill (DeadlineExceeded) apart from a parent cancellation. cancelTimeout
	// is an explicit func (not a bare defer) because the background path hands it to
	// the detached goroutine.
	var (
		timeoutCtx    context.Context
		cancelTimeout context.CancelFunc
	)
	ctx, timeoutCtx, cancelTimeout = applyCallTimeout(ctx, args.TimeoutMs)

	// Per-child cancel: mint the per-CALL cancelable context the parent's CancelChild
	// targets and register it in the parent run's child registry. The per-CALL ctx (not
	// any single drive's) is the cancel target so a structured-output re-drive sequence
	// is cancelled as a whole; it wraps the (possibly timeout-bearing) ctx, so the
	// per-call timeout and a parent-run cancel keep their existing semantics — the
	// timeoutCtx deadline check below stays first, and a parent cancel leaves
	// clientCancelled false. Registration happens BEFORE acquireChildSlot so a child
	// queued on the gate is already cancellable (the gate's ctx select unblocks). A
	// `resume` of an id already run THIS run re-registers and OVERWRITES the done
	// entry (fresh doneCh — A5).
	ctx, cancelCall := context.WithCancel(ctx)
	if err := caps.registerChildRun(ctx, childID, childFamilySubagent, subagentGoal(args), cancelCall, args.Background); err != nil {
		cancelCall()
		cancelTimeout()
		t.releaseChildID(childID)
		return session.NewToolError(call.ID, fmt.Sprintf("Subagent: child session could not be protected: %v", err)), nil
	}

	// BACKGROUND (D7/D8): fail-fast gate, synchronous start event, detach the drive.
	if args.Background {
		return t.startBackground(ctx, backgroundChild{
			call: call, env: env, emit: emit, caps: caps, args: args,
			engine: engine, limits: limits, resuming: resuming, childID: childID, authority: delegatedAuthority,
			forkHistory:    forkHistory,
			routedCategory: routedCategory, routedModel: routedModel, routingReason: routingReason,
			timeoutCtx: timeoutCtx, cancelCall: cancelCall, cancelTimeout: cancelTimeout,
		}), nil
	}

	// FOREGROUND: the call owns its whole lifecycle inline.
	defer t.releaseChildID(childID)
	defer cancelCall()
	defer cancelTimeout()
	// Terminal accounting (the A5 state vocabulary): a child whose drive STARTED
	// lands its real terminal stop; a pre-start CANCELLATION (gate wait) lands the
	// meaningful StopCancelled; any other pre-start failure (resume-load / fork /
	// session-build — its error already returned inline) ABORTS the entry (removed,
	// never a done+StopNone phantom in the SubagentStatus roster).
	started := false
	var terminalStop session.StopReason
	defer func() {
		switch {
		case started || terminalStop != session.StopNone:
			caps.finishChildRun(childID, terminalStop)
		default:
			caps.abortChildRun(childID)
		}
	}()

	// Bound concurrent children FIRST, for ALL Subagent children (forking AND forker-less):
	// the dispatcher fans Subagent calls out read-parallel, and each child consumes a child
	// session + an LLM slot (and, when shell-bearing, a forked worktree). Acquire at the
	// top of the call and release when it returns, so at most cap children run at once.
	release := t.acquireChildSlot(ctx)
	if release == nil {
		// ctx cancelled while waiting for a slot — surface it as a tool error; the parent
		// ctx governs the whole call. A CLIENT cancel (CancelChild while queued) is named
		// accurately so the model knows the user withdrew this delegation, not that the
		// run is collapsing. Either way the cancellation is a MEANINGFUL pre-start
		// terminal (StopCancelled), not an abort.
		terminalStop = session.StopCancelled
		if caps.childWasClientCancelled(childID) {
			return session.NewToolError(call.ID,
				"Subagent: subagent was cancelled by the user while waiting for a concurrency slot"), nil
		}
		return session.NewToolError(call.ID, "Subagent: cancelled before acquiring a concurrency slot"), nil
	}
	defer release()
	caps.startChildRun(childID)

	// Resume load + fork + session build (see prepareChildSession). The cleanup is
	// always non-nil and tears the worktree down after the child fully drains (the
	// run is drained below in this call), so a deferred cleanup is correct.
	child, runEnv, cleanupWS, forkAdvisory, editsSurvived, errResult, ok := t.prepareChildSession(ctx, call, env, args, resuming, writable, childID, caps.parentSessionID, caps.parentIncarnation, limits, forkHistory)
	if !ok {
		return errResult, nil
	}
	// The child is attributed to the PARENT session's owner (ADR 0204 decision 4),
	// or carries delegated authority when the parent run is authority-bound.
	if resuming && caps.authorityBound {
		persisted, bound := child.BoundAuthority()
		if authorityErr := validateResumedAuthority(caps.authority, persisted, bound); authorityErr != nil {
			_ = cleanupWS()
			return session.NewToolError(call.ID, "Subagent: resume authority refused: "+authorityErr.Error()), nil
		}
		caps.inheritOwner(child)
	} else if resuming {
		caps.inheritOwner(child)
	} else if caps.authorityBound {
		if authorityErr := stampDelegatedLabels(child, caps.owner, delegatedAuthority); authorityErr != nil {
			_ = cleanupWS()
			return session.NewToolError(call.ID, "Subagent: failed to stamp delegated authority: "+authorityErr.Error()), nil
		}
	} else {
		caps.inheritOwner(child)
	}
	// A FRESH child is published create-only: its id derives from a provider
	// tool-call id, so an overwrite would clobber another owner's transcript. A
	// resume loads an existing snapshot and must not be re-created.
	if !resuming {
		if err := createSessionIfSupported(ctx, t.store, child); err != nil {
			_ = cleanupWS()
			return session.NewToolError(call.ID, fmt.Sprintf("Subagent: durable child session could not be created: %v", err)), nil
		}
	}
	// Tear down the run workspace after the child fully drains. For a writable
	// (direct-write) child this is a no-op — cleanupWS is the no-op returned by
	// forkChildEnvironment for a nil forker (the child ran against the parent ws, which
	// the parent owns); for a read-only worktree child it tears the throwaway
	// checkout down.
	defer func() { _ = cleanupWS() }()

	// Announce the subagent before it runs, carrying only the parent call id, the
	// child id, and a short, plain-text goal label (sanitization happens in the
	// UI). No child content.
	if emit != nil {
		emit(session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{
			ParentCallID:     string(call.ID),
			ChildID:          string(childID),
			ChildIncarnation: child.Incarnation(),
			Goal:             subagentGoal(args),
			RoutedCategory:   routedCategory,
			RoutedModel:      routedModel,
			RoutingReason:    routingReasonPayload(routingReason),
			Model:            engine.Model(),
		}})
	}

	// Build the run request (per-call token ceiling), the synthetic SubmitResult tool (when
	// structured output is requested), and the effective prompt (with the degraded-fork
	// advisory + the mode-appropriate resume note prepended BEFORE the structured-output
	// wrap). See buildSubagentRunRequest.
	runReq, submit, prompt := buildSubagentRunRequest(args, resuming,
		resumePosture{writable: writable, editsSurvived: editsSurvived}, forkAdvisory)

	// A read-only child forking a worktree (childForker wired) runs ISOLATED, so its
	// Shell asks are eligible for the A2 worktree-safe auto-approve; a forker-less
	// read-only child is base-sharing (no auto-approve). A WRITABLE (direct-write)
	// child is NEVER isolated — it shares the REAL parent tree (ADR 0077, it forked
	// nothing) REGARDLESS of whether the read-only childForker is wired — so its Shell
	// resolves at MAIN-SESSION PARITY through the child policy/posture under the
	// operator's posture (the A2 isolation auto-approve correctly does NOT apply: its
	// Shell now hits the real repo). Hence the `!writable` guard: in production BOTH the
	// read-only childForker and the writable engine are wired, so keying isolation on
	// `t.childForker != nil` alone would WRONGLY mark a direct-write child isolated. The
	// parent caps carry interactivity + the surface back-channel for an interactive
	// parent; headless leaves them zero (auto-deny).
	posture := childPosture{isolated: !writable && t.childForker != nil, caps: caps, role: string(childID),
		childID:  string(childID),
		askLabel: fmt.Sprintf("subagent %q", subagentGoal(args))}

	start := engine.now()
	started = true
	// Drain the child's Event stream entirely INSIDE the Subagent tool. Nothing from the
	// child surfaces to the parent except the final summary string and, when observed,
	// the redacted subagent.* metadata. The structured-output retry loop re-drives the
	// SAME child session (Reopen) with a correction prompt on a validation miss; the
	// free-text path runs exactly one drive.
	final, stop, cause, usage, toolCount := driveChild(ctx, engine, child, runEnv, prompt, runReq, emit, call, childID, posture, submit, args.OutputSchema)
	terminalStop = stop

	// Persist the terminal failure CAUSE on the child snapshot (issue #332): the
	// child is StateFailed here (the loop's terminate ran on StopError), so the
	// state guard passes. This makes the snapshot the single durable cause source
	// independent of the parent's subagent.end emit — belt-and-suspenders for the
	// foreground path (the emit here is synchronous), load-bearing for the
	// background path where the emit can lose the race with the run-end seal.
	recordChildCauseOnSnapshot(child, stop, cause)

	// Best-effort persist of the child's FINAL state (after any structured-output
	// re-drives) so InspectSubagent can load it by the trailer id. persistMember
	// discipline: nil store disables; a save failure is advisory and swallowed.
	// NOTE: ctx may already be cancelled here (parent cancel / timeout_ms). persistChild
	// therefore detaches cancellation and applies its own short deadline, so a
	// ctx-honouring store (redisstore, grpcdriver) still records the terminal snapshot the
	// advertised resume needs.
	t.persistChild(ctx, child)

	if emit != nil {
		emit(subagentEndEvent(call.ID, childID, stop, cause, subagentEndMetrics{
			incarnation: child.Incarnation(), toolCount: toolCount, usage: usage, durationMs: engine.now().Sub(start).Milliseconds(),
		}))
	}

	// Fire SubagentStop best-effort, regardless of how the child ended.
	t.fireSubagentStop(ctx, child)

	// Terminal rendering (time-budget / client-cancel / writable note). Split out so
	// run() stays readable — see finishForegroundRun.
	return t.finishForegroundRun(ctx, foregroundFinish{
		call: call, childID: childID,
		final: final, stop: stop, cause: cause, submit: submit, writable: writable,
		timeoutCtx: timeoutCtx, timeoutMs: args.TimeoutMs,
		clientCancelled: caps.childWasClientCancelled(childID),
	}), nil
}

// foregroundFinish bundles the terminal-rendering inputs for finishForegroundRun.
type foregroundFinish struct {
	call    session.ToolCall
	childID session.SessionID
	final   string
	stop    session.StopReason
	// cause is the child run's terminal FAILURE detail (session.ResultPayload.Error,
	// non-empty only on a StopError terminal) — the actionable half of a failed
	// delegation, threaded from driveChild so the render chokepoint can lead with it
	// instead of the child's last chat line (issue #319).
	cause           string
	submit          *submitResultTool
	writable        bool
	timeoutCtx      context.Context //nolint:containedctx // deadline-disambiguation handle, mirrors run()'s timeoutCtx local
	timeoutMs       *int
	clientCancelled bool
}

// finishForegroundRun renders a foreground Subagent run's terminal result: the
// time-budget error first (a real deadline beats every other label), then the
// ordinary stop-reason rendering. For a mode:"read-write" call (direct-write, ADR
// 0041) the result honestly says the child had DIRECT access to the workspace and tells
// the model to inspect git diff/status for any changes — there is no merge step and the
// renderer has no evidence that an edit occurred.
// Factored out of run() so the loop body stays within the complexity budget.
//
// The receiver is used for ONE thing: t.resumeSupported() is validateResume's first
// precondition, so it decides whether a failed terminal may advertise the resume
// affordance (see subagentErrorResumeHint).
func (t *SubagentTool) finishForegroundRun(_ context.Context, f foregroundFinish) session.ToolResult {
	// Time-budget terminal: the per-call deadline fired (timeoutCtx deadline exceeded)
	// rather than a parent cancellation, so the child stopped because it ran out of its
	// allotted wall-clock time. Render it as a model-addressable time-budget tool error
	// so the model learns the call hit its own limit (distinct from a generic failure).
	if f.timeoutCtx != nil && f.timeoutCtx.Err() == context.DeadlineExceeded {
		return t.timeoutResult(f.call.ID, f.childID, *f.timeoutMs, f.writable)
	}

	// Client cancel (CancelChild): distinguished from a parent-run cancel by the
	// registry flag, read AFTER the timeout check above so a real deadline keeps its
	// time-budget error.
	if f.writable {
		return renderWritableSubagentResult(f.call.ID, f.childID, f.final, f.stop, f.cause, f.submit, f.clientCancelled, t.resumeSupported())
	}
	return renderSubagentResult(f.call.ID, f.childID, f.final, f.stop, f.cause, f.submit, f.clientCancelled,
		subagentResumeHint(t.resumeSupported()))
}

// timeoutResult renders the per-call TIME-BUDGET terminal — the ONE composition point for
// it, shared by the foreground (finishForegroundRun) and background (driveBackground)
// paths, which built it verbatim-identically before. Two copies of a model-facing terminal
// whose own doc-comments warn "so the timeout path cannot drift from the StopError path's
// policy" is exactly the drift risk those comments describe, so the sharing is structural.
//
// The next action comes from subagentTimeoutNote: a timed-out child lands StateCancelled,
// which resume has always recovered, so this terminal is NOT a dead end — and for a
// direct-write child (ADR 0077) the note also warns that any edits it made may be
// PARTIAL, since a writable child killed MID-TASK can leave half-finished work there.
// The renderer does not claim that an edit occurred. One gate resolves both axes.
//
// The background path always passes writable=false (mode:"read-write"+background is
// rejected in validateMode), so the writable arm is reachable from the foreground only.
func (t *SubagentTool) timeoutResult(callID session.ToolCallID, childID session.SessionID, timeoutMs int, writable bool) session.ToolResult {
	msg := fmt.Sprintf("Subagent: subagent exceeded its time budget (%dms) and was stopped\n\nagentId: %s", timeoutMs, childID)
	if note := subagentTimeoutNote(writable, t.resumeSupported()); note != "" {
		msg += "\n\n" + note
	}
	return session.NewToolError(callID, msg)
}

// backgroundStartedBody is the immediate started-result body a background Subagent
// call returns (D7, amended by A2: SubagentStatus is the SOLE body channel — the
// model is told to COLLECT, not promised an auto-delivery). It is rendered through
// renderSubagentTrailer, so the agentId rides the FIRST line exactly like every
// other Subagent result (the existing trailer convention the model and the resume
// path already parse).
const backgroundStartedBody = "subagent started in the background.\n\n" +
	"It keeps working while you continue; a note will tell you when it finishes. " +
	"Collect its result with SubagentStatus (or wait on it with wait_ms); it will be " +
	"cancelled if it is still running when this run ends."

// backgroundChild bundles everything one detached background Subagent drive owns:
// the original call/workspace/emit/caps, the resolved engine+limits, and the
// lifecycle handles (per-call cancel, timeout cancel, gate release, in-flight id)
// whose ownership the synchronous path TRANSFERS to the goroutine.
type backgroundChild struct {
	call     session.ToolCall
	env      tool.Environment
	emit     func(session.Event)
	caps     parentCaps
	args     subagentArgs
	engine   *Engine
	limits   session.Limits
	resuming bool
	// resumed is the loaded+recovered session on a resume call (loaded SYNCHRONOUSLY
	// in startBackground so an unknown id / non-resumable state fails fast inline,
	// not as a collectible background error).
	resumed *session.Session
	// forkHistory is the deep copy of the parent conversation a fork:true child
	// seeds from, captured SYNCHRONOUSLY in run() on the dispatch goroutine BEFORE
	// the detach (the parent keeps appending after detach, so the snapshot must not
	// be taken in driveBackground). nil on a non-fork call.
	forkHistory []session.Message
	// routedCategory / routedModel are the OPT-IN model router's classification (ADR
	// 0031) for this background child, captured SYNCHRONOUSLY in run() (the routeTask
	// closure must fire on the dispatch goroutine, not the detached one). They ride the
	// synchronous EvSubagentStart so the routed metadata is observable; empty when the
	// child was not routed (no router, or a fail-soft miss). The engine field already
	// carries the routed engine — these are the LABELS only. routingReason is the
	// bare-metadata why-not (issue #397), captured with them (empty on a routed hit).
	routedCategory string
	routedModel    string
	routingReason  string
	childID        session.SessionID
	authority      session.Authority
	// timeoutCtx is non-nil iff a per-call timeout_ms deadline applies (the
	// DeadlineExceeded disambiguation read, same as the foreground path).
	timeoutCtx    context.Context //nolint:containedctx // deadline-disambiguation handle, mirrors run()'s timeoutCtx local
	cancelCall    context.CancelFunc
	cancelTimeout context.CancelFunc
	release       func()
}

// startBackground is the synchronous half of a background Subagent call: fail-fast
// gate acquisition (D12), fail-fast resume load, the SYNCHRONOUS EvSubagentStart
// (A5 — deterministic start-before-started-result ordering on the stream), then
// the detached goroutine spawn and the immediate started-result. Every pre-spawn
// failure unwinds completely (registry entry removed — no phantom queued entries,
// A5 — gate slot and in-flight id released) and returns inline.
func (t *SubagentTool) startBackground(ctx context.Context, b backgroundChild) session.ToolResult {
	abort := func() {
		b.caps.abortChildRun(b.childID)
		b.cancelCall()
		b.cancelTimeout()
		t.releaseChildID(b.childID)
	}
	// D12: background acquisition is FAIL-FAST — a background child holds its slot
	// ACROSS turns, so blocking here could deadlock the model against itself. The
	// error lists the live background ids (ids only — A9) and the recoverable
	// actions. ORDER is load-bearing: abort() FIRST, so the failing call's own
	// pre-gate registration is gone before the live-ids read — read first, the
	// error would list the very id that just failed to start as "currently
	// running" (and a failed resume attempt would shadow the prior done entry the
	// abort reinstates).
	release, ok := t.tryAcquireChildSlot()
	if !ok {
		abort()
		ids := b.caps.liveBackgroundChildIDs()
		return session.NewToolError(b.call.ID, backgroundGateFullError(ids))
	}
	b.release = release
	// Resume load fails FAST and inline (a cheap store read): an unknown id or a
	// non-resumable state is the model's immediate, addressable error — never a
	// "started" result whose collection later reveals the call never could run.
	if b.resuming {
		loaded, errResult, lok := t.resolveResumeSession(ctx, b.call.ID, b.childID, b.args)
		if !lok {
			b.release()
			abort()
			return errResult
		}
		b.resumed = loaded
		if b.caps.authorityBound {
			persisted, bound := loaded.BoundAuthority()
			if authorityErr := validateResumedAuthority(b.caps.authority, persisted, bound); authorityErr != nil {
				b.release()
				abort()
				return session.NewToolError(b.call.ID, "Subagent: resume authority refused: "+authorityErr.Error())
			}
		}
	}
	b.caps.startChildRun(b.childID)
	// A5: the start event is emitted SYNCHRONOUSLY before the goroutine spawns, so
	// subagent.start always precedes the started-result on the stream.
	if b.emit != nil {
		b.emit(session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{
			ParentCallID:   string(b.call.ID),
			ChildID:        string(b.childID),
			Goal:           subagentGoal(b.args),
			Background:     true,
			RoutedCategory: b.routedCategory,
			RoutedModel:    b.routedModel,
			RoutingReason:  routingReasonPayload(b.routingReason),
			Model:          b.engine.Model(),
		}})
	}
	go t.driveBackground(ctx, b)
	return session.NewToolResult(b.call.ID, renderSubagentTrailer(b.childID, backgroundStartedBody))
}

// driveBackground is the detached goroutine owning a background child's whole
// remaining lifecycle: fork → drive → persist → markDone(rendered result + stop).
// It is run-scoped (D8): its ctx derives from the parent run's, the run-end drain
// cancels and joins it (doneCh closes in the deferred finishChildRunResult), and
// every emit it makes goes through the registry's seal guard, so a residual emit
// after an abandon is a safe no-op. The deferred block releases the gate slot,
// the in-flight id, and both cancels — the ownership transferred from run() —
// strictly BEFORE the doneCh close, so a drain-join happens-after every release.
func (t *SubagentTool) driveBackground(ctx context.Context, b backgroundChild) {
	var (
		res  session.ToolResult
		stop = session.StopError
	)
	defer func() {
		// Ownership releases FIRST, the done signal LAST. markDoneResult (inside
		// finishChildRunResult) closes the registry doneCh the run-end drain JOINS
		// on, so everything a successor run can observe — the gate slot, the
		// in-flight child id — must be released happens-before that close.
		// Signalling first let a freshly-joined drain's NEXT run race the deferred
		// releaseChildID and bounce a legitimate resume of this very child with a
		// spurious "already running" (the TestBackgroundChildCancelledAtRunEnd CI
		// flake — a real model-visible bug, not test noise).
		b.cancelCall()
		b.cancelTimeout()
		b.release()
		t.releaseChildID(b.childID)
		b.caps.finishChildRunResult(b.childID, stop, &res)
	}()

	goal := subagentGoal(b.args)
	// A post-spawn failure (fork / session build) is NOT a registry abort: the model
	// already holds the started-result, so the failure must be COLLECTIBLE — it lands
	// as a done(StopError) entry whose stored result is the error text, and the start
	// event gets its closing subagent.end so no client lane dangles.
	//
	// It carries a Cause like the other two subagent.end emit sites: the field's
	// contract is "non-empty whenever Stop is StopError", and a client that reads it
	// that way must not be handed stop=error/cause="" on the very path where the event
	// is the ONLY channel — a background child's failure never reaches an inline card
	// (the model already holds the started-result), so a roster row would otherwise
	// show stop:error with no why. errResult.Content is the HARNESS-composed error text
	// ("Subagent: workspace isolation failed: …"), never child-authored output, so the
	// gauntlet-#7 footing is identical to the drive-failure cause.
	endOnError := func(errResult session.ToolResult) {
		res = errResult
		if b.emit != nil {
			b.emit(subagentEndEvent(b.call.ID, b.childID, session.StopError, errResult.Content, subagentEndMetrics{}))
		}
	}
	// Background is read-only only (mode:"read-write"+background is rejected in
	// validateMode), so the background path always forks the read-only worktree.
	runEnv, cleanupWS, forkAdvisory, errResult, ok := t.forkChildEnvironment(ctx, b.call.ID, b.env, goal, t.childForker)
	if !ok {
		endOnError(errResult)
		return
	}
	defer func() { _ = cleanupWS() }()
	child, errResult, ok := t.buildChildSession(b.call.ID, b.childID, b.caps.parentSessionID, b.caps.parentIncarnation, b.resumed, runEnv, b.limits, b.forkHistory)
	if !ok {
		endOnError(errResult)
		return
	}
	if b.resuming || !b.caps.authorityBound {
		b.caps.inheritOwner(child)
	} else if authorityErr := stampDelegatedLabels(child, b.caps.owner, b.authority); authorityErr != nil {
		endOnError(session.NewToolError(b.call.ID, "Subagent: failed to stamp delegated authority: "+authorityErr.Error()))
		return
	}
	// Create-only for a fresh background child, same reason as the foreground path.
	if !b.resuming {
		if err := createSessionIfSupported(ctx, t.store, child); err != nil {
			endOnError(session.NewToolError(b.call.ID,
				fmt.Sprintf("Subagent: durable child session could not be created: %v", err)))
			return
		}
	}
	// RESUME-START persist, mirroring prepareChildSession: refresh the resumed
	// snapshot's last-modified time so the child-session GC's age pass never
	// deletes an in-flight resumed child (best-effort, failures swallowed).
	if b.resuming {
		t.persistChild(ctx, child)
	}

	// A zero resumePosture (writable=false, editsSurvived=false) unconditionally:
	// mode:"read-write"+background is rejected in validateMode, so a background child is
	// always the read-only forked kind and gets the fresh-checkout resume note.
	runReq, submit, prompt := buildSubagentRunRequest(b.args, b.resuming, resumePosture{}, forkAdvisory)
	posture := childPosture{isolated: t.childForker != nil, caps: b.caps, role: string(b.childID),
		childID:  string(b.childID),
		askLabel: fmt.Sprintf("subagent %q", goal)}

	start := b.engine.now()
	final, st, cause, usage, toolCount := driveChild(ctx, b.engine, child, runEnv, prompt, runReq, b.emit, b.call, b.childID, posture, submit, b.args.OutputSchema)
	stop = st

	// Persist the terminal failure CAUSE on the child snapshot BEFORE persistChild
	// (issue #332, the load-bearing change): the child is StateFailed here (the
	// loop's terminate ran on StopError), so the state guard passes. The snapshot
	// becomes the single durable cause source independent of the post-seal emit
	// race — the background end-emit below can lose the race with the run-end seal
	// (drainChildren's abortEmits), so without this the cause would be lost when a
	// background child's subagent.end never reaches the parent's event stream.
	recordChildCauseOnSnapshot(child, st, cause)

	// Best-effort persist on EVERY terminal — including the run-end drain's cancel —
	// so the child is resumable in a later run (the loss-mitigation that makes
	// cancel-at-end acceptable). persistChild detaches the (usually already cancelled) ctx,
	// so the snapshot survives on a ctx-honouring store too.
	t.persistChild(ctx, child)

	if b.emit != nil {
		b.emit(subagentEndEvent(b.call.ID, b.childID, st, cause, subagentEndMetrics{
			incarnation: child.Incarnation(), toolCount: toolCount, usage: usage, durationMs: b.engine.now().Sub(start).Milliseconds(),
		}))
	}
	t.fireSubagentStop(ctx, child)

	// Terminal rendering mirrors the foreground call exactly (renderSubagentResult is
	// the single rendering chokepoint): time-budget first, then the client-cancel
	// disambiguation. The rendered result is what SubagentStatus delivers verbatim.
	if b.timeoutCtx != nil && b.timeoutCtx.Err() == context.DeadlineExceeded {
		// The SAME composition point as the foreground path (timeoutResult), so the two
		// cannot drift. writable=false unconditionally: mode:"read-write"+background is
		// rejected in validateMode, so a background child is always the read-only forked
		// kind.
		res = t.timeoutResult(b.call.ID, b.childID, *b.args.TimeoutMs, false)
		return
	}
	// background is read-only only (mode:"read-write"+background is rejected in
	// validateMode), so the generic resume hint is the right one here.
	res = renderSubagentResult(b.call.ID, b.childID, final, st, cause, submit, b.caps.childWasClientCancelled(b.childID),
		subagentResumeHint(t.resumeSupported()))
}

// tryAcquireChildSlot is acquireChildSlot's NON-BLOCKING sibling for background
// acquisition (D12 fail-fast): it returns (release, true) when a slot is free and
// (nil, false) when the gate is full — never waits.
func (t *SubagentTool) tryAcquireChildSlot() (func(), bool) {
	if t.childGate == nil {
		return func() {}, true
	}
	select {
	case t.childGate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-t.childGate }) }, true
	default:
		return nil, false
	}
}

// backgroundGateFullError renders the D12 fail-fast error: model-addressable,
// listing the currently-live background ids (ids ONLY — A9, nothing
// model/child-authored) and the recoverable actions.
func backgroundGateFullError(ids []string) string {
	msg := "Subagent: background subagent concurrency limit reached"
	if len(ids) > 0 {
		msg += "; currently running in the background: " + strings.Join(ids, ", ")
	}
	return msg + ". Wait for one to finish with SubagentStatus (use wait_ms), or run this task in the foreground."
}

// maxSubagentCausePreview caps how many runes of a child's FAILURE CAUSE cross onto the
// subagent.end event payload (session.SubagentPayload.Cause). It is deliberately larger
// than maxTeamPreview (200): a provider/transport error body is longer than a text
// preview — a truncated one is unactionable, which is the whole point of issue #319 —
// but it must still stay BOUNDED so a pathological error body cannot dump unbounded
// bytes onto the event stream.
const maxSubagentCausePreview = 400

// subagentCausePayload normalises a child's failure cause for the delegation EVENT
// fields (session.SubagentPayload.Cause, and — since issue #331 —
// session.TeamPayload.Cause): whitespace collapsed to single spaces, then clamped to
// maxSubagentCausePreview. It is the ONE place that projection is built, shared by all
// three EvSubagentEnd emit sites and the EvTeamMember result projection
// (projectTeamEvent in teamtool.go) — two delegation families, one chokepoint.
//
// The collapse belongs HERE rather than in each consumer. The event field is LINE-ORIENTED
// by contract — a mecatui roster/focus row, an ACP status line, a mecademo log line — while a
// provider error body routinely carries real newlines (the anthropic adapter returns the
// upstream `error.message` unquoted). Three consumers each re-deriving the same collapse
// means every future consumer inherits the obligation and one of them will forget. The
// MODEL-facing body (subagentErrorBody) deliberately keeps its newlines: it is prose in a
// conversation, not a row in a table.
func subagentCausePayload(cause string) string {
	return clampRunes(strings.Join(strings.Fields(cause), " "), maxSubagentCausePreview)
}

// maxRoutingReasonPreview caps how many runes of a routing REASON cross onto a
// delegation-start event payload (session.SubagentPayload/ParallelPayload/
// TeamMemberSpec.RoutingReason). The reasons are harness/composition constants (the
// session.RoutingReason* gates, the RouterMiss* values, composition's category-mapping
// strings) — short by construction — but the string channel is OPEN (composition may
// author new ones), so the projection stays BOUNDED like every other event field.
const maxRoutingReasonPreview = 200

// routingReasonGeneric is the wire value substituted for a routing reason that is NOT on
// the event-safe allowlist (routingReasonEventSafe). The in-tree engine gates and the
// reference composition author only allowlisted metadata, but Deps.SubagentModelRouter is
// an EXPORTED callback — an external engine composition could return a provider error body,
// classifier output, or a task excerpt as its missReason. That text is NOT gauntlet-#7
// safe, so the wire projection substitutes this generic label and keeps the verbatim reason
// in the operator-diagnostics channel only (logRouterMissReason), which is where dynamic
// detail belongs.
const routingReasonGeneric = "routing-miss"

// routingReasonEmptyModel attributes the fail-soft case where a router reported a hit but
// returned no usable model id. It is static event-safe metadata; the classifier detail, if
// any, remains diagnostic-only.
const routingReasonEmptyModel = "empty-model"

// The reference composition's two category-mapping miss codes. Composition appends
// operator-facing detail to these values for diagnostics, but the event projection always
// reduces that detail back to the static code: Deps.SubagentModelRouter is an exported
// callback, so no arbitrary suffix is allowed to cross the client wire.
const (
	routingReasonCategorySelectorEmpty      = "category-selector-empty"
	routingReasonCategoryTargetUnresolvable = "category-target-unresolvable"
)

// routingReasonEventSafe is the CLOSED set of reason strings permitted to cross onto a
// delegation-start event verbatim. It is the union of the harness gate constants
// (session.RoutingReason*), the engine classifier-miss constants (RouterMiss*), and the
// reference composition's static category-mapping codes. Category/selector detail remains
// diagnostic-only. Anything else an external router returns is replaced by
// routingReasonGeneric. Built once at init.
var routingReasonEventSafe = func() map[string]struct{} {
	safe := []string{
		session.RoutingReasonPinnedModel,
		session.RoutingReasonAgentDefPinned,
		session.RoutingReasonResume,
		session.RoutingReasonFork,
		session.RoutingReasonRouterDisabled,
		session.RoutingReasonTargetUnavailable,
		session.RoutingReasonBreakerOpen,
		session.RoutingReasonAborted,
		RouterMissDegenerateInput,
		RouterMissClassifierError,
		RouterMissCancelled,
		RouterMissTimeout,
		RouterMissLowConfidence,
		RouterMissInputOverLimit,
		RouterMissCapacityTimeout,
		RouterMissBadVerdict,
		RouterMissUnknownCategory,
		routingReasonEmptyModel,
		// The reference composition's category-mapping miss CODES. Its callback may append
		// operator-authored detail for diagnostics; routingReasonPayload strips that detail
		// before the value reaches an event.
		routingReasonCategorySelectorEmpty,
		routingReasonCategoryTargetUnresolvable,
	}
	m := make(map[string]struct{}, len(safe))
	for _, s := range safe {
		m[s] = struct{}{}
	}
	return m
}()

// routingReasonPayload normalises a routing reason for the RoutingReason EVENT field:
// whitespace collapsed to single spaces, clamped to maxRoutingReasonPreview, AND confined
// to the event-safe allowlist. It is the ONE place that projection is built, shared by
// every delegation-start emit site (subagent.start foreground + background, parallel
// branch_start, team.start roster), mirroring subagentCausePayload's single-chokepoint
// discipline. The OUTPUT is BARE METADATA (a static harness/classifier/composition code,
// never the task prompt or classifier output — gauntlet #7); the collapse exists because
// the consumers are single-line surfaces, and the allowlist exists because the missReason
// INPUT channel is OPEN to external engine compositions (see routingReasonGeneric).
func routingReasonPayload(reason string) string {
	reason = clampRunes(strings.Join(strings.Fields(reason), " "), maxRoutingReasonPreview)
	if reason == "" {
		return ""
	}
	if _, ok := routingReasonEventSafe[reason]; ok {
		return reason
	}
	// The in-tree composition adds parenthesised category/selector detail for the operator
	// diagnostic. Reduce those known shapes to a CLOSED static wire code. Even if an external
	// callback forges the same prefix/shape, its suffix is discarded rather than copied to
	// the event (gauntlet #7). A bare prefix is already handled by the exact allowlist above.
	switch {
	case strings.HasPrefix(reason, routingReasonCategorySelectorEmpty+" ("):
		return routingReasonCategorySelectorEmpty
	case strings.HasPrefix(reason, routingReasonCategoryTargetUnresolvable+" ("):
		return routingReasonCategoryTargetUnresolvable
	}
	return routingReasonGeneric
}

// subagentEndMetrics is the observability half of an EvSubagentEnd payload: the
// measurements, as opposed to the outcome. The background PRE-RUN failure site has none of
// them (nothing ran), which is the only difference between the three emit sites.
type subagentEndMetrics struct {
	incarnation session.IncarnationID
	toolCount   int
	usage       session.Usage
	durationMs  int64
}

// subagentEndEvent builds the EvSubagentEnd event. It exists so that setting
// session.SubagentPayload.Cause and normalising it through subagentCausePayload are the
// SAME act: the field's doc-comment promises "every emit site normalises it through one
// helper", and three hand-built struct literals made that a convention a fourth site could
// silently break — with the ACP projector's and mecademo's own collapses now removed
// (deliberately: they are in-process consumers), a miss would reach a client as a multi-row
// status line with no test firing.
func subagentEndEvent(parentCallID session.ToolCallID, childID session.SessionID, stop session.StopReason, cause string, m subagentEndMetrics) session.Event {
	return session.Event{Type: session.EvSubagentEnd, Subagent: &session.SubagentPayload{
		ParentCallID:     string(parentCallID),
		ChildID:          string(childID),
		ChildIncarnation: m.incarnation,
		ToolCount:        m.toolCount,
		Usage:            m.usage,
		Stop:             stop,
		Cause:            subagentCausePayload(cause),
		DurationMs:       m.durationMs,
	}}
}

// subagentErrorBody composes the model-facing body of a StopError subagent result.
// The CAUSE (the harness/provider failure detail the loop put on
// session.ResultPayload.Error) leads, because it is the actionable half; the child's
// last assistant text follows as clamped context when present, because "how far did it
// get" is load-bearing for recovering a direct-write child's partial edits (ADR 0077).
//
// Before #319 the cause was dropped and `final` alone was rendered AS the error, so a
// chatty child's last sentence was presented to the parent as the failure reason (a
// stream stall surfaced as "Now let me check the tests." — unactionable and actively
// misleading). The empty-cause rows preserve that shape for terminals that carry no loop
// cause, so nothing regresses for non-loop StopError paths.
//
// This is the ONE place the StopError body is composed — renderSubagentResult (hence
// renderWritableSubagentResult) and the Parallel branch-failure path both go through it,
// so there is no second policy to drift. Being that one place, it is also the one place
// that BOUNDS the body: BOTH halves are clamped here, because this string is recorded
// into the PARENT's conversation and persisted, so the parent re-pays for every rune of
// it on every subsequent turn — an unbounded provider error body (an HTML error page, a
// giant JSON envelope) must not become permanent context. The cause gets the larger
// maxSubagentCausePreview budget for the same reason the event payload does: a truncated
// provider error is unactionable, and the child's text the smaller maxTeamPreview one —
// the SAME bound digestChildActivity already applies to the same content class (one
// preview's worth of the child's own prose), so this path borrows it rather than minting a
// second 200.
//
// The parameters are ordered as they RENDER (cause first, then final): the two are both
// strings and adjacent, so a positional swap compiles — and a swap here would silently
// reproduce the exact bug #319 fixed (the chat line presented as the failure reason).
// Reading a call site in output order is the cheap defence.
//
// The wording is caller-NEUTRAL ("failed without producing a summary", no noun): the
// Subagent path prefixes it with "Subagent: " and the Parallel path renders it under a
// `=== branch-N [FAILED] ===` header, so neither reads as the other's vocabulary.
//
// BOTH halves are also framing-NEUTRALISED here (neutraliseChildText, fence.go's rule for
// any trusted-but-model-influenced value). Neither half is harness-authored: `cause` is a
// provider/transport error body VERBATIM (the anthropic adapter returns the upstream
// `error.message` unquoted, so real newlines survive it) and `final` is child-authored
// prose. Both land in the PARENT's persisted conversation immediately adjacent to the
// harness's own imperatives — the agentId trailer the model resumes by and the resume-hint
// note — so an error string echoed from a hostile MCP server or a fetched page could
// otherwise forge one of those lines and impersonate the harness (CWE-1427 / OWASP LLM01).
// Neutralising runs BEFORE the clamp so a redaction can never be half-truncated, and
// clampRunes' ellipsis is what keeps that ordering safe in the other direction (see its
// doc-comment: a clean truncation could otherwise land exactly on an exact-match header).
func subagentErrorBody(cause, final string) string {
	cause = clampRunes(neutraliseChildText(strings.TrimSpace(cause)), maxSubagentCausePreview)
	final = neutraliseChildText(strings.TrimSpace(final))
	switch {
	case cause != "" && final != "":
		return cause + "\n\nLast activity before the failure: " + clampRunes(final, maxTeamPreview)
	case cause != "":
		return cause
	case final != "":
		return final
	default:
		return "failed without producing a summary"
	}
}

// subagentErrorResumeHint is the MODEL-VISIBLE next-action instruction stamped on a
// FAILED delegation. A failed child is now recoverable through `resume` (issue #318,
// docs/adr/0200-resume-a-failed-subagent.md), and a capability the model is never told
// about is a capability it cannot use — the model-visible-affordance rule (ADR 0070),
// the same reason the StopNoProgress note and the no-summary floor carry their own resume
// hints.
//
// It lives HERE, in renderSubagentResult's StopError arm, and deliberately NOT inside
// subagentErrorBody: that helper is shared with the Parallel branch-failure path, whose
// branch ids are `parallel-<callID>-<n>` and are REJECTED by validateResume's
// t.idPrefix gate (a Parallel branch is inspectable, not resumable). Emitting this hint
// there would instruct the model to take an action that cannot succeed — the exact
// inverse of the discoverability rule. subagentErrorBody stays the single composition
// point for the CAUSE; this is the single composition point for the RESUME affordance.
//
// For the SAME reason it is gated on `resumable` (the caller's `t.resumeSupported()`):
// validateResume's FIRST precondition is a wired session store, so a SubagentTool built
// without WithSubagentStore — a supported construction for an engine-module consumer
// (ADR 0036) — would otherwise tell the model to resume and then refuse the call with
// "`resume` is not supported in this deployment". Same defect, different precondition.
//
// It is appended AFTER the agentId trailer line, so "the agentId above" is literally
// accurate on the StopError layout (where the trailer comes LAST, unlike the success
// family's leading trailer), and it names BOTH options so a model facing an obviously
// permanent cause still re-delegates instead of retrying forever.
//
// The wording states WHAT carries over and what does NOT, because the earlier
// "continue where it left off" over-promised: a resumed child's CONVERSATION is
// restored but its WORKSPACE is not — resumeStalenessNote greets it with "file changes
// … from your earlier run are GONE" (its git worktree was torn down; this run gets a
// fresh fork). A parent told only "continue where it left off" can re-delegate a
// follow-up that assumes half-written files survived, which is the same
// harness-asserts-a-falsehood class as the two messages #319 fixed. The clause is
// mode-ACCURATE both ways: the workspace claim is about the FAILED child's throwaway
// checkout, so it holds whether the resume comes back read-only (a fresh fork) or is
// upgraded to mode:"read-write" (the real parent tree — still not the dead worktree).
// It is kept in lockstep with resumeStalenessNote by
// TestFailureResumeHintMatchesTheResumedChildsRealWorkspacePosture.
const subagentErrorResumeHint = "[the subagent failed mid-task — its conversation is preserved; if the failure looks transient (a stalled or errored provider call), resume it with the agentId above to continue from its transcript. Its WORKSPACE does not carry over: it ran in a throwaway checkout, so any files it wrote are GONE and it must re-read files and re-run commands. Otherwise start a fresh subagent.]"

// subagentResumeHint resolves whether a READ-ONLY child's StopError result may advertise
// the generic resume affordance. An empty string means "say nothing", which is the honest
// answer when no session store is wired: validateResume's FIRST precondition fails, so the
// resume this would advertise comes straight back as "not supported in this deployment".
//
// The direct-write (mode:"read-write") arm does not come through here at all — it passes
// the literal "" and owns its own SINGLE combined decision (writableSubagentFailedNote),
// because a writable failure has TWO possible next actions (finish on top of the partial
// edits, or discard them) and stating them as two independent imperatives invites a model
// to do both: discard the edits, then resume a child that resumeWritableNote greets with
// "the file edits you already made are STILL IN PLACE" — now false. That suppression is
// pinned behaviourally by TestWritableSubagentFailureRendersOneCombinedNextAction.
func subagentResumeHint(resumable bool) string {
	if !resumable {
		return ""
	}
	return subagentErrorResumeHint
}

// The two MODEL-VISIBLE next actions a per-call TIME-BUDGET terminal can carry.
//
// A timed-out child lands in StateCancelled, which resolveResumeSession has ALWAYS
// recovered, so the terminal is recoverable — but before this it was the one failure
// path with no next action at all, while every neighbouring terminal (StopError, the
// limit stops, StopNoProgress, the no-summary floor) names one. Left silent it reads as
// the exception: the model is taught everywhere else that a bad terminal is resumable
// and here it infers the delegation is simply dead (ADR 0070's model-visible-affordance
// rule — an unnamed capability is an unusable one).
//
// The timeout wording is its OWN pair rather than a reuse of the StopError notes for two
// reasons: the cause is not a failure at all (the child ran out of clock, not out of
// competence, so "if the failure looks transient" would misdescribe it), and the operator
// knob that fixes it is nameable — `timeout_ms` on the resuming call.
//
//   - subagentTimeoutResumeHint (read-only): the same conversation-survives /
//     workspace-does-not split subagentErrorResumeHint states, for the same reason.
//   - writableSubagentTimeoutNote (direct-write, resumable): ONE combined decision, not
//     two independent imperatives — identical reasoning to writableSubagentFailedNote
//     (a model handed "undo the edits" and "resume" separately can do both, then meet
//     resumeWritableNote's "your edits are STILL IN PLACE", which the undo just
//     falsified). "the agentId above" is literally accurate here: the timeout layout
//     stamps the trailer FIRST and appends the note.
//
// A store-less deployment keeps the partial review-or-undo note because
// validateResume's first precondition is a wired store — advertising a resume it will
// refuse is the same defect subagentResumeHint's `resumable` gate prevents.
const (
	subagentTimeoutResumeHint   = "[the subagent ran out of its per-call time budget, not out of work — its conversation is preserved; resume it with the agentId above to continue from its transcript (pass a larger `timeout_ms` if the task genuinely needs longer), or start a fresh subagent with a narrower goal. Its WORKSPACE does not carry over: it ran in a throwaway checkout, so any files it wrote are GONE and it must re-read files and re-run commands.]"
	writableSubagentTimeoutNote = "[the subagent had direct write access to your workspace and was stopped MID-TASK by its time budget — any edits it made may be PARTIAL and remain in your working tree. If changes exist, either resume it with the agentId above AND mode:\"read-write\" to finish on top of them (pass a larger `timeout_ms` if the task genuinely needs longer), or discard them with `git checkout`/`git stash` (review first with `git diff`/`git status`). Do not do both.]"
)

// subagentTimeoutNote resolves the next-action body a per-call time-budget terminal
// carries. It is the ONE gate for that terminal, mirroring subagentResumeHint's shape
// (and its two honest-silence cases) so the timeout path cannot drift from the StopError
// path's policy:
//
//	writable + resumable → the single combined resume-or-discard decision
//	writable             → today's review-or-undo partial note (no resume to offer)
//	resumable            → the generic timeout resume hint
//	neither              → "" (say nothing: no store, nothing to advertise)
func subagentTimeoutNote(writable, resumable bool) string {
	switch {
	case writable && resumable:
		return writableSubagentTimeoutNote
	case writable:
		return writableSubagentPartialNote
	case resumable:
		return subagentTimeoutResumeHint
	default:
		return ""
	}
}

// The two MODEL-VISIBLE next actions a StopStructuredOutput terminal can carry.
//
// This was the LAST delegation terminal that named a cause and no action: the model read
// "did not produce output matching the requested schema: <validation error>" plus the
// agentId, and nothing about what to do — while StopError, the limit stops, StopNoProgress,
// the time-budget stop and even the no-summary floor all name one (ADR 0070's
// model-visible-affordance rule). The reason recorded for leaving it bare was that "a
// resume would need the same `output_schema` passed again", which is not an obstacle:
// validateResume rejects only `agent`/`model`, buildSubagentRunRequest builds the submit
// tool from args.OutputSchema unconditionally, and the schema is the PARENT's own argument
// — re-passing it costs one field. The real (weaker) argument is that a child which failed
// validation through its whole correction budget may fail again, so the resume is offered
// as the SECOND option behind fixing the schema or the instruction, and says so.
//
// Both open with "[the subagent " so framingHeader already recognises them: they are
// harness imperatives sitting next to child-authored text, so a forged copy has to be
// redactable (TestDelegationResultNeutralisationCoversHarnessNoteFamily pins that).
//
// The gate is the same `resumable` one subagentResumeHint and subagentTimeoutNote use — a
// store-less deployment must not advertise a resume validateResume will refuse — but unlike
// those two there is no fourth "say nothing" cell: the fix-and-re-delegate half needs no
// affordance at all, so a store-less deployment still gets a next action.
const (
	subagentStructuredOutputNote = "[the subagent could not produce a payload matching `output_schema` within its correction budget — the validation error above names what was wrong. Simplify the schema or restate the instruction, then delegate again.]"

	subagentStructuredOutputResumeNote = "[the subagent could not produce a payload matching `output_schema` within its correction budget — the validation error above names what was wrong. Simplify the schema or restate the instruction and delegate again, or resume it with the agentId above (passing the SAME `output_schema`) for one more attempt — though a child that failed validation this often may fail again.]"
)

// structuredOutputNote resolves the next action a StopStructuredOutput terminal carries.
// resumable is the caller's t.resumeSupported() — renderSubagentResult reads it off the
// resumeHint it was handed, which is that gate's one existing channel into this renderer.
func structuredOutputNote(resumable bool) string {
	if resumable {
		return subagentStructuredOutputResumeNote
	}
	return subagentStructuredOutputNote
}

// maxSubagentStopLabelPreview bounds an open-taxonomy stop label before it is echoed to
// the model. Stop reasons can originate with a provider or host, so they are data, not
// trusted harness vocabulary.
const maxSubagentStopLabelPreview = 80

// subagentTerminalNote classifies every non-error Subagent terminal for model-visible
// rendering. Its second result is true only for the explicit complete terminal,
// StopEndTurn; writable rendering consumes that same classification rather than keeping a
// second stop list. Known bounded or workflow terminals get specific notes; the default
// fails safe because StopReason is an open string taxonomy and providers/hosts may add
// values independently of this package.
//
// StopError and StopStructuredOutput have dedicated error-result renderers. Cancellation
// is incomplete too: the classifier supplies the generic parent-run note, while the
// caller-aware renderer replaces it with the specific user-cancelled wording.
func subagentTerminalNote(stop session.StopReason) (note string, explicitlyComplete bool) {
	switch stop {
	case session.StopEndTurn:
		return "", true
	case session.StopError, session.StopStructuredOutput:
		return "", false
	case session.StopCancelled:
		return "[subagent cancelled because its parent run ended — treat as partial/incomplete]", false
	case session.StopMaxTurns:
		return "[subagent stopped: reached its max-turns limit — treat as partial; its partial work remains available under the agentId above; resume it only if continued work is appropriate within the operator's limits]", false
	case session.StopMaxToolCalls:
		return "[subagent stopped: reached its max-tool-calls limit — treat as partial; its partial work remains available under the agentId above; resume it only if continued work is appropriate within the operator's limits]", false
	case session.StopMaxConsecutiveFailures:
		return "[subagent stopped: reached its consecutive-tool-failure limit — treat as partial/incomplete]", false
	case session.StopBudget:
		return "[subagent stopped: reached its token budget — raise max_run_tokens if the inherited ceiling permits, or narrow the task's scope and delegate a fresh subagent]", false
	case session.StopNoProgress:
		return "[subagent stopped: ended without a final summary — treat as partial; resume it with the agentId above to continue]", false
	case session.StopTimeout:
		return "[subagent stopped: reached its time limit — treat as partial/incomplete]", false
	case session.StopPlanApproved:
		return "[subagent stopped after plan approval before completing the delegated work — treat as partial/incomplete]", false
	case session.StopPlanIterate:
		return "[subagent stopped for plan revision before completing the delegated work — treat as partial/incomplete]", false
	case session.StopNone:
		return "[subagent stopped without a terminal reason — treat as partial/incomplete]", false
	default:
		label := clampRunes(neutraliseChildText(string(stop)), maxSubagentStopLabelPreview)
		return fmt.Sprintf("[subagent stopped with unrecognized terminal reason %q — treat as partial/incomplete]", label), false
	}
}

// renderSubagentResult labels the child's terminal by stop reason (D4 — the typed result
// taxonomy), surfaced in the MODEL-VISIBLE result, and stamps the agentId trailer (D5).
// The mapping:
//   - StopError                         → tool error (the child crashed), its body
//     composed by subagentErrorBody: the loop's FAILURE CAUSE leads, the child's last
//     assistant text follows as clamped context (issue #319), and the caller-supplied
//     resumeHint (see subagentResumeHint) names the recovery path when there is one to
//     name (issue #318). An empty hint is deliberate, not a default — a store-less
//     deployment has no resume to offer, and the writable arm owns its own combined
//     next-action instead.
//   - StopStructuredOutput              → tool error carrying the last validation
//     failure (the child never produced a schema-valid payload within the retry budget),
//     plus a next action (see structuredOutputNote): fix the schema or the instruction and
//     re-delegate, or — where a store is wired — resume with the same `output_schema`.
//   - StopMaxTurns / StopMaxToolCalls   → success-with-note (stopped at a limit).
//   - StopMaxConsecutiveFailures        → success-with-note (repeated tool failures;
//     partial/incomplete).
//   - StopBudget / StopTimeout          → success-with-note (stopped at a budget).
//   - StopNoProgress                    → success-with-note "[subagent stopped: ended
//     without a final summary]": a reasoning-only / empty-turn end discarded the child's
//     work the same way a limit stop did (issue #152). driveChild has already run the
//     salvage + last-resort digest, so the body normally carries recovered content.
//   - StopCancelled + clientCancelled   → success-with-note "[subagent cancelled by
//     user]": the partial work is usable and the trailer keeps the child resumable
//     (cancel-then-resume-with-a-narrower-prompt is the intended workflow); an error
//     result would teach the model the delegation mechanism failed.
//   - StopCancelled + !clientCancelled  → success-with-note that the parent run ended and
//     the child output is partial/incomplete.
//   - StopPlanApproved / StopPlanIterate → success-with-note (the plan workflow ended,
//     but the delegated implementation is incomplete).
//   - StopNone / unknown host stop       → safe generic partial/incomplete note; an
//     unknown label is bounded, neutralised, and quoted.
//   - StopEndTurn                        → unannotated success (the positive allow-list).
//
// On EVERY terminal the result text carries the structured payload (when a
// schema was satisfied) else the free-text summary, prefixed with the agentId trailer
// so the parent MODEL can discover the child id (mirroring renderTeamResult's Team-id
// line — the runtime-discoverability axis: the id must be where the model reads it, not
// only on the client-only subagent.* events). The body is NEVER a silent empty string:
// driveChild salvages then digests an empty free-text terminal (issue #48 / #152), and
// even the floor "(subagent produced no summary)" is paired with a stop-reason note.
func renderSubagentResult(callID session.ToolCallID, childID session.SessionID, final string, stop session.StopReason, cause string, submit *submitResultTool, clientCancelled bool, resumeHint string) session.ToolResult {
	// EVERY arm below wraps the harness's own markers — the agentId trailer the model
	// resumes by, the bracketed stop-reason and next-action notes — around this
	// child-authored text, so it is neutralised ONCE here rather than per arm (CWE-1427 /
	// OWASP LLM01: a read-only child has WebFetch/WebSearch/Read, so a hostile page or repo
	// file it summarises is an injection source on the COMMON, non-failure path — a forged
	// "agentId:" line hands the model a second resume handle, and a forged
	// "[the subagent … discard them with `git checkout`]" is a workspace-destruction
	// imperative no read-only child could have earned). The StopError arm's two halves are
	// neutralised again inside subagentErrorBody, which is idempotent and must keep doing
	// it: that composer is shared with the Parallel branch-failure path, which does not come
	// through here.
	//
	// The VALIDATED structured payload below is deliberately NOT neutralised: it is a JSON
	// document that survived session.ValidateJSON, so a raw line break — the thing a
	// line-oriented forgery needs — cannot appear inside one of its string values, while
	// whole-line redaction WOULD corrupt the deliverable the parent is about to parse.
	final = neutraliseChildText(final)
	// Structured-output failure: the retry budget was exhausted without a schema-valid
	// payload. Surface the last validation error AS the tool error (model-visible),
	// never only a log line.
	if stop == session.StopStructuredOutput {
		msg := "subagent did not produce output matching the requested schema"
		if submit != nil {
			if last := submit.lastError(); last != "" {
				msg += ": " + neutraliseChildText(last)
			}
		}
		// The trailer comes LAST on this layout, so "the agentId above" in the note below is
		// literally accurate. renderWritableSubagentResult reaches here with resumeHint "" (it
		// owns its own resume decision), so a writable structured-output terminal gets the
		// no-resume half plus its own PARTIAL-edits note — two next actions that do not
		// conflict (fix the schema and re-delegate; review or undo what is in the tree).
		return session.NewToolError(callID, "Subagent: "+msg+"\n\nagentId: "+string(childID)+
			"\n\n"+structuredOutputNote(resumeHint != ""))
	}
	if stop == session.StopError {
		body := "Subagent: " + subagentErrorBody(cause, final) + "\n\nagentId: " + string(childID)
		if resumeHint != "" {
			body += "\n\n" + resumeHint
		}
		return session.NewToolError(callID, body)
	}

	// Success family. A structured-output run returns the validated payload; otherwise
	// the free-text summary.
	body := final
	if submit != nil {
		if payload := submit.payload(); payload != "" {
			body = payload
		}
	}
	if strings.TrimSpace(body) == "" {
		// Reached only when BOTH the bounded salvage turn AND the last-resort
		// digestChildActivity recovery produced nothing — i.e. the child genuinely never
		// emitted any assistant text on ANY terminal. The stop-reason note below still
		// names WHY the child stopped, so even this floor is honest rather than opaque; the
		// resume hint keeps it from being a dead end (the agentId trailer is on the result,
		// so the parent can continue the child rather than abandoning the delegation).
		body = "(subagent produced no summary — resume it with the agentId above to continue)"
	}
	// StopEndTurn is the sole unannotated normal completion. Every other non-error
	// terminal is either handled specially above/below or receives a fail-safe note from
	// subagentTerminalNote, so a newly introduced provider/host label cannot silently look
	// like success.
	note, _ := subagentTerminalNote(stop)
	if stop == session.StopCancelled && clientCancelled {
		// Preserve the specific CancelChild wording instead of stacking it with the generic
		// parent-run cancellation note.
		note = "[subagent cancelled by user]"
	}
	if note != "" {
		body = note + "\n\n" + body
	}
	return session.NewToolResult(callID, renderSubagentTrailer(childID, body))
}

// The three direct-write (ADR 0077) advisory bodies a mode:"read-write" child's terminal
// can carry. None asserts an edit actually occurred — the child had the CAPABILITY to edit
// (direct write access to the real workspace, no fork/quarantine), but whether it DID
// write anything is unknown to the renderer. The model should inspect git diff/status to
// discover actual changes rather than trust the terminal label.
//
//   - CLEAN — StopEndTurn ONLY: the child completed an ordered turn without hitting a
//     bound, limit, or anomaly. It had direct write access; any changes it made are in
//     the tree and reviewable with git.
//   - PARTIAL — every OTHER non-Error terminal (bounds, plan controls, an anomalous
//     empty Reason, a host/provider-defined label like "max_tokens", or a mid-task kill
//     without a resume path). The child ended before a clean completion; any edits it
//     made may be partial/incomplete.
//   - FAILED — a StopError in a deployment that CAN resume (a session store is wired).
//     This is the case that needs ONE decision rather than two: "the edits may be partial,
//     undo them with git" and "resume to continue where it left off" are both true, and
//     a model handed them as independent imperatives can do both — discarding the edits
//     and then resuming a child that resumeWritableNote greets with "the file edits you
//     already made are STILL IN PLACE", which the discard just made false. So the two
//     options are stated as mutually exclusive, ending in an explicit "Do not do both",
//     and renderSubagentResult's generic resume hint is suppressed for this arm (see
//     subagentResumeHint). "the agentId below" is literally accurate here: the note is
//     prepended to the body while the StopError layout puts the trailer LAST.
//
// Both resume-offering notes name mode:"read-write" EXPLICITLY, because `writable` is
// derived only from the CURRENT call's mode (validateMode) — run() never infers it from
// the loaded session. A bare Subagent{resume: id, prompt: …} therefore comes back as the
// READ-ONLY explorer: no Edit/Write, a fresh worktree fork off committed HEAD (so the
// partial edits are not even visible to it), and resumeStalenessNote telling the child its
// changes are GONE — while the operator's real tree still holds the half-finished work.
// "resume it to finish on top of them" without the argument that makes it true is the same
// harness-asserts-a-falsehood class these notes exist to close, on the most expensive
// failure path in the tool. Pinned by TestWritableResumeNotesNameTheReadWriteMode.
const (
	writableSubagentCleanNote   = "[the subagent had direct write access to your workspace — check `git diff`/`git status` to review any changes (and `git checkout`/`git stash` to undo)]"
	writableSubagentPartialNote = "[the subagent had direct write access to your workspace and did NOT finish cleanly — any edits it made may be PARTIAL. Inspect with `git diff`/`git status` and undo with `git checkout`/`git stash` if needed]"
	writableSubagentFailedNote  = "[the subagent had direct write access to your workspace and did NOT finish cleanly — any edits it made may be PARTIAL and remain in your working tree. If changes exist, make one choice: Either resume it with the agentId below AND mode:\"read-write\" to finish on top of them, or discard them with `git checkout`/`git stash` (review first with `git diff`/`git status`). Do not do both.]"
)

// renderWritableSubagentResult renders a mode:"read-write" child's terminal like
// renderSubagentResult (the SAME stop-reason taxonomy + agentId trailer) and adds an
// honest DIRECT-WRITE capability note. The renderer has no mutation evidence: it says the
// child HAD direct access to the real workspace (no fork or merge), and tells the model to
// inspect git diff/status for any changes rather than asserting that an edit occurred.
//
// The shared subagentTerminalNote classifier selects the clean note only for its explicit
// completion terminal, StopEndTurn. Every bounded, cancelled, anomalous, plan-control, or
// host/provider-defined terminal gets the partial/conditional note; a newly introduced
// StopReason therefore fails safe without a second stop list here.
//
// On a FAILED terminal in a deployment that can resume, the note becomes the SINGLE
// combined next-action (writableSubagentFailedNote) and renderSubagentResult's generic
// resume hint is suppressed — hence the literal "" passed as the hint below, never
// subagentResumeHint. See the note constants above for why two independent imperatives
// were unsafe. `resumable` selects WHICH note this arm owns, not whether a second one is
// also emitted.
func renderWritableSubagentResult(callID session.ToolCallID, childID session.SessionID, final string, stop session.StopReason, cause string, submit *submitResultTool, clientCancelled, resumable bool) session.ToolResult {
	res := renderSubagentResult(callID, childID, final, stop, cause, submit, clientCancelled, "")
	_, explicitlyComplete := subagentTerminalNote(stop)
	note := writableSubagentPartialNote
	switch {
	case stop == session.StopError && resumable:
		// The one arm that owns BOTH options, stated as one exclusive decision.
		note = writableSubagentFailedNote
	case explicitlyComplete:
		note = writableSubagentCleanNote
	}
	// renderSubagentResult always stamps the agentId trailer as the FIRST line
	// (renderSubagentTrailer: "agentId: <id>\n\n<body>"); insert the direct-write note
	// as a leading body line right after it so both the trailer and the note survive.
	trailer := "agentId: " + string(childID)
	prefix := trailer + "\n\n"
	if strings.HasPrefix(res.Content, prefix) {
		res.Content = prefix + note + "\n\n" + strings.TrimPrefix(res.Content, prefix)
	} else {
		// Defensive: keep the note even if the trailer shape ever changes.
		res.Content = note + "\n\n" + res.Content
	}
	return res
}

// renderSubagentTrailer prepends the model-visible agentId line to a Subagent result body,
// mirroring renderTeamResult's Team-id line. The childID is rendered VERBATIM (the
// deterministic t.childSessionID(callID)) so a human can correlate the overlay row and
// the parent can refer to "the subagent that did X" by id. It is the model's REAL
// handle: InspectSubagent loads the persisted child transcript by this id (verbatim —
// the id IS the session id, no derivation) and `resume` continues the child by it.
func renderSubagentTrailer(childID session.SessionID, body string) string {
	return fmt.Sprintf("agentId: %s\n\n%s", childID, body)
}

// driveChild runs the child loop and, when a structured-output schema is in play,
// applies the bounded SubmitResult validation-retry. It returns the terminal text,
// stop reason, the terminal FAILURE CAUSE (the loop's ResultPayload.Error — non-empty
// only on a StopError terminal; issue #319), cumulative usage, and observed tool-call
// count. As with finalText/stop, the cause reported by the LAST drive wins.
//
// TRIP-WIRE — the child-outcome return chain. driveChild and drainChildObserved return
// FIVE values (text, stop, cause, usage, toolCount), i.e. two `string` results separated
// by a non-string, so a call site that transposes text and cause COMPILES. That is
// deliberately left un-refactored: the definitions use named results, there are only two
// call sites carrying both strings, and the reordered subagentErrorBody(cause, final) plus
// its tests catch a transposition on the paths that matter — extracting a value type today
// would be churn without a second consumer. A SIXTH value on this chain is the point to
// extract a `childOutcome` struct, not before. The chain is deliberately kept SHORT at both
// ends: handleChildEvent stays at three values (drainChildObserved reads the cause off
// ev.Result beside Usage instead), and drainChild keeps its 2-value signature (five
// fail-soft callers want no cause). This mirrors the same named-trip-wire discipline the
// three delegation-event families carry in engine/session/event.go, where a FOURTH family
// is the extraction point.
//
// FREE-TEXT path (submit == nil): exactly one Engine.Run drive — byte-identical to
// the prior engine.Run(...) behaviour.
//
// STRUCTURED path (submit != nil): drive the child; if it called SubmitResult with a
// VALID payload, the run is done (submit.payload() holds it). If the submitted payload
// was INVALID or SubmitResult was never called, re-inject a model-visible correction
// (Reopen + re-drive) up to defaultStructuredOutputRetries times, then give up with
// StopStructuredOutput. The retry is a SEPARATE bounded loop owned here (NOT a change
// to finishTurnNoTools — that hot shared path stays Subagent-agnostic, decision D2), and
// uses NO tool_choice forcing (incompatible with the reasoning paths).
func driveChild(ctx context.Context, engine *Engine, child *session.Session, runEnv tool.Environment, prompt string, runReq RunRequest, emit func(session.Event), call session.ToolCall, childID session.SessionID, posture childPosture, submit *submitResultTool, schema json.RawMessage) (finalText string, stop session.StopReason, cause string, usage session.Usage, toolCount int) {
	drivePrompt := prompt
	// attempts = 1 (initial) + defaultStructuredOutputRetries corrections, but only the
	// structured path retries; the free-text path runs once.
	maxAttempts := 1
	if submit != nil {
		maxAttempts = 1 + defaultStructuredOutputRetries
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// A re-drive reuses the SAME child session: Reopen the completed session so
		// RecordUserPrompt accepts the correction prompt (the run drove it to a terminal
		// state). A non-recoverable session (failed/cancelled) ends the retry loop.
		// NOTE: Reopen() RESETS Counters, so the per-call MaxTurns/MaxToolCalls bound EACH
		// attempt independently (≤(1+defaultStructuredOutputRetries)× across the call —
		// bounded). The cross-attempt ceiling is the TOKEN budget: usage is summed below
		// and runReq (carrying the tighten-only override) is re-passed to every drive.
		if attempt > 0 {
			if err := child.Reopen(); err != nil {
				return finalText, stop, cause, usage, toolCount
			}
		}
		// Copy the base run-scoped request (MaxRunTokensOverride + ExtraTools) and set
		// ONLY the Text for this drive, so no run-scoped field can be dropped across the
		// structured-output retry loop or the free-text salvage re-drive.
		driveReq := runReq
		driveReq.Text = drivePrompt
		run := engine.Run(ctx, child, runEnv, driveReq)
		text, st, c, u, tc := drainChildObserved(run, emit, string(call.ID), string(childID), posture)
		finalText, stop, cause = text, st, c
		usage = usage.Add(u)
		toolCount += tc

		// Free-text path: done — but first try to salvage a partial summary if the child
		// ended on an empty terminal (turn/tool-call limit, token budget, no-progress, or
		// an empty clean end) without producing any text (issue #48 / #152).
		if submit == nil {
			finalText, usage = salvageEmptyStop(ctx, engine, child, runEnv, finalText, stop, usage, runReq, emit, call, childID, posture)
			// Last-resort digest: if the bounded salvage drive ALSO produced nothing
			// (e.g. the model emitted another empty/reasoning-only turn), recover the
			// child's last non-empty assistant text from its own history so the parent
			// still gets the child's work instead of "(subagent produced no summary)".
			// Mirrors joinTeamFallback's member-LastText recovery (engine/agent/teamtool.go).
			// The prefix states ONLY provenance + partial-ness + the resume hint — it does
			// NOT restate the stop reason (renderSubagentResult's StopNoProgress note owns
			// the "why"), so the StopNoProgress note + this prefix never double-state it,
			// and it still reads coherently standalone on the note-less empty-StopEndTurn path.
			if strings.TrimSpace(finalText) == "" && isEmptyTerminalStop(stop) {
				if digest := digestChildActivity(child); digest != "" {
					finalText = recoveredDigestPrefix + "\n\n" + digest
				}
			}
			return finalText, stop, cause, usage, toolCount
		}
		// A structured run that produced a valid payload: done.
		if submit.valid() {
			return finalText, stop, cause, usage, toolCount
		}
		// A child that crashed or was cancelled must not be re-driven — surface it.
		if stop == session.StopError || stop == session.StopCancelled || ctx.Err() != nil {
			return finalText, stop, cause, usage, toolCount
		}
		// A budget stop is terminal too: the token ceiling is now CUMULATIVE across the
		// retry Reopens (cloud-native Phase 1 — session.Usage survives Reopen), so a child
		// that crossed the ceiling mid-retry would only re-trip on the next attempt's first
		// boundary. Surface StopBudget verbatim (the Subagent result renders it as a clean
		// success-with-note) rather than burning the remaining Reopens and mislabelling the
		// terminal as StopStructuredOutput.
		if stop == session.StopBudget {
			return finalText, stop, cause, usage, toolCount
		}
		// Structured miss: build the correction prompt for the next attempt (if any).
		drivePrompt = structuredCorrectionPrompt(schema, submit.lastError())
	}
	// Retry budget exhausted with no valid payload: a CLEAN terminal the Subagent result
	// renders as a model-visible validation-failure tool error (recoverable, not failed).
	return finalText, session.StopStructuredOutput, cause, usage, toolCount
}

// isEmptyTerminalStop reports whether stop is one of the terminals on which a
// FREE-TEXT child can plausibly have ended WITHOUT a usable summary, so the salvage +
// digest recovery (issue #48 / #152) should run. It is a positive ALLOW-SET — the
// bounded terminals (turn/tool-call limit, token budget, no-progress, per-fire
// wall-clock timeout) plus an EMPTY clean end (StopEndTurn). The caller pairs it
// with a blank-finalText guard so a NORMAL StopEndTurn that produced text is never
// disturbed. StopTimeout follows StopBudget's classification (a clean bounded
// terminal, recoverable); it remains subject to the carried token budget like
// StopNoProgress and StopEndTurn.
func isEmptyTerminalStop(stop session.StopReason) bool {
	switch stop {
	case session.StopMaxTurns, session.StopMaxToolCalls, session.StopBudget,
		session.StopNoProgress, session.StopTimeout, session.StopEndTurn:
		return true
	default:
		return false
	}
}

// digestChildActivity recovers the child's LAST non-empty assistant message text from
// its own conversation history (walking backwards, the closeOutInterruptedTurn idiom in
// engine/session/session.go), clamped to a bounded preview. It is the LAST-RESORT
// recovery when both the original drive AND the bounded salvage turn produced no final
// text (issue #152): rather than discard the child's work behind "(subagent produced no
// summary)", surface whatever the child last said. Returns "" when the child never
// emitted any assistant text. Mirrors joinTeamFallback's member-LastText recovery
// (engine/agent/teamtool.go) — clampRunes (not clampPreview) so multi-line reasoning
// survives intact (this is the child's OWN output going back to the parent MODEL, not a
// peer-controlled preview crossing a trust boundary).
func digestChildActivity(child *session.Session) string {
	if child == nil {
		return ""
	}
	msgs := child.Conversation.Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != session.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(msgs[i].Text); text != "" {
			return clampRunes(text, maxTeamPreview)
		}
	}
	return ""
}

// salvageEmptyStop drives ONE bounded wrap-up turn to recover a partial summary
// from a FREE-TEXT child that ended on an EMPTY terminal without producing any text
// (issue #48 / #152) — so the parent gets a usable (if partial) deliverable instead of
// "(subagent produced no summary)". It returns the (possibly salvaged) final text and
// the cumulative usage (the salvage turn's usage ADDED, never double-counted).
//
// It is strictly best-effort and bounded:
//   - Triggers on an EMPTY-terminal allow-set — StopMaxTurns / StopMaxToolCalls /
//     StopBudget / StopNoProgress / StopEndTurn — with a blank finalText. A non-empty
//     StopEndTurn (a normal clean finish that DID produce text) is left untouched by the
//     blank-finalText guard below; every stop outside the allow-set falls straight
//     through. (StopNoProgress/empty-StopEndTurn are the issue-#152 additions: a
//     reasoning-only or silently-empty turn discarded the child's work the same way a
//     limit stop did.)
//   - StopBudget uses an internal baseline captured immediately before the
//     cleanup re-drive. The baseline grants only this bounded cleanup turn and
//     never resets the child's lifetime accounting.
//   - Reuses the SAME child session via Reopen() (which resets Counters), mirroring the
//     structured-output retry seam. A non-recoverable session (failed/cancelled) simply
//     keeps the empty result.
//   - Hard-bounds the salvage to ONE model call by temporarily pinning the child's
//     Limits to MaxTurns=1 (restored on return, so a resumed child keeps its real
//     limits). The wrap-up prompt forbids further tool use; the one-turn cap is the
//     enforcement so a salvage can never loop or fetch.
//   - Re-passes the run's existing runReq so the salvage drive carries the same
//     tighten-only token ceiling (MaxRunTokensOverride / Deps.MaxRunTokens) the original
//     drive used. The hard MaxTurns=1 cap is the real brake: the salvage is at most one
//     extra model call, so it can never loop or fetch its way past a budget.
//
// The ORIGINAL stop reason is preserved by the caller: the salvage only fills in a
// body; renderSubagentResult still stamps the honest "[subagent stopped: reached its
// max-turns limit]" / "[subagent stopped: reached its token budget]" / "[subagent
// stopped: ended without a final summary]" note. If the wrap-up errors or yields
// nothing, the caller's last-resort digest (digestChildActivity) runs next, and only
// then the empty placeholder stands.
func salvageEmptyStop(ctx context.Context, engine *Engine, child *session.Session, runEnv tool.Environment, finalText string, stop session.StopReason, usage session.Usage, runReq RunRequest, emit func(session.Event), call session.ToolCall, childID session.SessionID, posture childPosture) (string, session.Usage) {
	if !isEmptyTerminalStop(stop) {
		return finalText, usage
	}
	if strings.TrimSpace(finalText) != "" {
		return finalText, usage
	}
	if ctx.Err() != nil {
		return finalText, usage
	}
	// For a budget stop, only salvage if the child actually spent tokens in THIS run
	// (usage.TotalTokens() > 0, where usage is the per-run EvResult.Usage — the
	// loop-local accumulator, NOT session.Usage). A resumed child whose cumulative
	// session.Usage already exceeds the ceiling trips StopBudget at the FIRST boundary
	// before any model call, producing zero per-run spend — there is nothing to salvage,
	// and a wrap-up drive would immediately observe the same budget.
	if stop == session.StopBudget && usage.TotalTokens() == 0 {
		return finalText, usage
	}
	// Reopen requires StateCompleted; a limit/budget stop terminates via terminateComplete,
	// so the child is completed here. A failed/cancelled session is not recoverable — bail.
	if err := child.Reopen(); err != nil {
		return finalText, usage
	}
	// Pin the salvage to exactly ONE turn, restoring the real limits afterwards.
	savedLimits := child.Limits
	child.Limits.MaxTurns = 1
	defer func() { child.Limits = savedLimits }()

	// Copy the base run-scoped request and set ONLY the salvage prompt as Text, so the
	// tighten-only MaxRunTokensOverride and run-scoped ExtraTools survive the salvage drive.
	salvageReq := runReq
	salvageReq.Text = salvageWrapUpPrompt
	var run *Run
	if stop == session.StopBudget {
		run = engine.runWithCurrentMainUsageBaseline(ctx, child, runEnv, salvageReq)
	} else {
		run = engine.Run(ctx, child, runEnv, salvageReq)
	}
	text, _, _, u, _ := drainChildObserved(run, emit, string(call.ID), string(childID), posture)
	// Sum the salvage turn's usage (mirror the structured-output usage accumulation);
	// the caller's usage already excludes this drive, so there is no double-count.
	usage = usage.Add(u)
	if strings.TrimSpace(text) != "" {
		finalText = text
	}
	return finalText, usage
}

// tightenLimit applies a per-call TIGHTEN-ONLY override to an inherited session limit:
// it returns the LOWER of the inherited value and the requested override, ignoring a
// nil or non-positive request. Because a 0 inherited value means "unlimited", a present
// positive override always wins against 0; otherwise the override applies only when it
// is strictly lower than the inherited bound. The model can therefore make its child
// stricter than the operator's configured limit, never looser.
//
// It relies on session.Limits treating a 0 field as UNLIMITED (see session.Limits): a
// positive override against an unlimited (0) inherited bound TIGHTENS, which is why
// inherited<=0 returns the override rather than the (looser) 0. If session.Limits ever
// changes its zero-semantics, this branch must change with it.
func tightenLimit(inherited int, override *int) int {
	if override == nil || *override <= 0 {
		return inherited
	}
	if inherited <= 0 || *override < inherited {
		return *override
	}
	return inherited
}

// acquireChildSlot acquires one slot of the per-child concurrency gate, blocking
// until a slot is free or ctx is cancelled. It returns a release func to return the
// slot (idempotent-safe to call once), or nil if ctx was cancelled while waiting — the
// caller then aborts the call without spawning a child. A nil gate (no bound) returns
// an inert release immediately, though NewSubagentTool always sizes one so every Subagent child
// is bounded.
func (t *SubagentTool) acquireChildSlot(ctx context.Context) func() {
	if t.childGate == nil {
		return func() {}
	}
	select {
	case t.childGate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-t.childGate }) }
	case <-ctx.Done():
		return nil
	}
}

// tryAcquireChildID registers childID as in-flight, returning false if a run on the
// SAME id is already in progress (a fresh duplicate is impossible — call ids are unique
// — so this fires only for a concurrent RESUME of the same persisted id, or a fresh+resume
// collision). It is the correctness brake: two runs over one unlocked Session aggregate
// would race; the second call gets a model-visible "already running" error, not a wait.
func (t *SubagentTool) tryAcquireChildID(childID session.SessionID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inFlight == nil {
		t.inFlight = make(map[session.SessionID]struct{})
	}
	if _, busy := t.inFlight[childID]; busy {
		return false
	}
	t.inFlight[childID] = struct{}{}
	return true
}

// releaseChildID unregisters a previously-acquired in-flight child id.
func (t *SubagentTool) releaseChildID(childID session.SessionID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inFlight, childID)
}

// authorizeResumeLookup performs the read-only ownership gate before the shared
// in-flight guard, for a `resume` call only — a no-op (ok=true) when resuming is
// false, so run() carries a single branch here instead of nesting "if resuming"
// around a separate "if !owns". It makes a foreign or unknown id indistinguishable
// without recovering or otherwise mutating the loaded session.
func (t *SubagentTool) authorizeResumeLookup(ctx context.Context, callID session.ToolCallID, id session.SessionID, resuming bool) (session.ToolResult, bool) {
	if !resuming {
		return session.ToolResult{}, true
	}
	_, res, ok := t.loadOwnedResumeSession(ctx, callID, id)
	return res, ok
}

// loadOwnedResumeSession is the read-only load+ownership fold shared by
// authorizeResumeLookup (the pre-in-flight-guard gate) and resolveResumeSession
// (the later, authoritative load that actually gets mutated/persisted). Each
// caller performs its OWN fresh Load at its OWN point in time — this helper
// shares only the CHECK LOGIC, never a loaded session across calls: the
// pre-guard Load has no exclusion (tryAcquireChildID has not run yet), while
// resolveResumeSession's Load runs only after a successful claim, under that
// id's exclusion, on a session about to be Reopen/Interrupt/Recover'd and
// re-persisted (and, on the background path, resolved later on a DETACHED
// goroutine) — threading the pre-guard snapshot forward would be a stale read
// used for a write. A foreign owner's session is treated as absent, not
// refused: a distinguishing error would itself leak that the id exists under
// another owner (this plan's "a refusal is indistinguishable from absence"
// principle), and a model steered to probe ids could use the distinction as
// an oracle.
func (t *SubagentTool) loadOwnedResumeSession(ctx context.Context, callID session.ToolCallID, id session.SessionID) (*session.Session, session.ToolResult, bool) {
	loaded, err := t.store.Load(ctx, id)
	if err == nil && !callerOwnsTranscriptWhenEnforced(ctx, loaded, t.ownershipEnforced) {
		err = port.ErrSessionNotFound
		loaded = nil
	}
	switch {
	case errors.Is(err, port.ErrSessionNotFound) || (err == nil && loaded == nil):
		return nil, session.NewToolError(callID,
			fmt.Sprintf("Subagent: no subagent found for resume id %q; use the id exactly as shown on the 'agentId:' line of a previous Subagent result", id)), false
	case err != nil:
		return nil, session.NewToolError(callID,
			fmt.Sprintf("Subagent: failed to load subagent %q for resume: %v", id, err)), false
	default:
		return loaded, session.ToolResult{}, true
	}
}

// resolveResumeSession loads a persisted subagent session for a `resume` call, recovers
// its terminal state to StateIdle so it is runnable again, and tightens its preserved
// Limits by the per-call args. It runs BEFORE the workspace fork so the common error
// cases (unknown id, non-resumable state, broken store) fail fast without paying a
// fork/unfork round-trip; the caller re-homes the returned session onto the fresh fork
// root afterwards (Session.Rehome — a field-consistency repair, not a prompt input).
//
// The per-state switch stays SEPARATE from the service layer's loadAndReopen (a child
// resume has its own preconditions — the in-flight guard, the tighten-only limits, the
// fresh fork) but now matches its DISCIPLINE exactly: all THREE terminals recover.
// StateCompleted → Reopen, StateCancelled → Interrupt, StateFailed → Recover (all three
// history-repairing where needed), StateIdle → run as-is, any other state → not in a
// resumable state.
//
// StateFailed used to be refused here, justified by "a failed child carries no
// accumulated-user-context cost, so the parent re-delegates instead of retrying a broken
// transcript". Issue #318 falsified that premise: a long-running mode:"read-write" child
// (ADR 0077) accumulates 50+ turns of exploration AND mutations already applied to the
// REAL tree, so discarding it is strictly more expensive than retrying a main session's
// transcript — and the failure that gets it here is typically TRANSIENT (the terminal
// 180s stream-idle stall, which becomes StopError rather than StopCancelled because the
// run ctx is never cancelled). The inversion was stark: an operator-configured
// timeout_ms lands in StateCancelled and was already resumable, while a network hiccup
// was permanent. session.Session.Recover runs closeOutInterruptedTurn with the
// FAILURE-accurate wording, so a tool call orphaned by the failed turn gets a synthetic
// error result and the replayed history stays provider-valid. Per Recover's own
// contract, recovery makes retry POSSIBLE, not guaranteed: a permanent-cause child
// re-fails cleanly, which is strictly better than never being able to try. See
// docs/adr/0200-resume-a-failed-subagent.md.
//
// It returns the recovered session on success, or a model-addressable error ToolResult
// (ok=false) on a load failure or non-resumable state.
func (t *SubagentTool) resolveResumeSession(ctx context.Context, callID session.ToolCallID, id session.SessionID, args subagentArgs) (*session.Session, session.ToolResult, bool) {
	loaded, res, ok := t.loadOwnedResumeSession(ctx, callID, id)
	if !ok {
		return nil, res, false
	}
	switch loaded.State {
	case session.StateCompleted:
		if rerr := loaded.Reopen(); rerr != nil {
			return nil, session.NewToolError(callID,
				fmt.Sprintf("Subagent: subagent %q is not in a resumable state (%q): %v", id, loaded.State, rerr)), false
		}
	case session.StateCancelled:
		if rerr := loaded.Interrupt(); rerr != nil {
			return nil, session.NewToolError(callID,
				fmt.Sprintf("Subagent: subagent %q is not in a resumable state (%q): %v", id, loaded.State, rerr)), false
		}
	case session.StateIdle:
		// Already runnable; run as-is.
	case session.StateFailed:
		// Issue #318: a failed child is RECOVERABLE (see the doc-comment). Recover
		// repairs the failed turn's history with the failure-accurate close-out wording
		// before returning to idle; the error wrapping mirrors the two arms above so a
		// transition that somehow fails is still a model-addressable tool error rather
		// than a silent retry of a genuinely broken transcript.
		if rerr := loaded.Recover(); rerr != nil {
			return nil, session.NewToolError(callID,
				fmt.Sprintf("Subagent: subagent %q is not in a resumable state (%q): %v", id, loaded.State, rerr)), false
		}
	default:
		return nil, session.NewToolError(callID,
			fmt.Sprintf("Subagent: subagent %q is not in a resumable state (%q)", id, loaded.State)), false
	}
	// Tighten the LOADED session's preserved Limits by the per-call args (tighten-only).
	// Reopen/Interrupt/Recover all reset Counters (resetToIdle), so each per-call bound
	// applies afresh.
	loaded.Limits.MaxTurns = tightenLimit(loaded.Limits.MaxTurns, args.MaxTurns)
	loaded.Limits.MaxToolCalls = tightenLimit(loaded.Limits.MaxToolCalls, args.MaxToolCalls)
	return loaded, session.ToolResult{}, true
}

// forkChildEnvironment selects the environment one child run executes against, using the
// supplied forker (the caller passes t.childForker for a read-only child — a git
// worktree — or nil for a mode:"read-write" child, which runs DIRECTLY against the
// parent workspace, ADR 0077). When the forker is wired (a read-only child catalog
// has Shell), the child gets its OWN isolated checkout so its writes never touch the
// shared parent base — what keeps read-only Subagent read-parallel-safe (see
// ReadOnly). A fork FAILURE is a tool error (ok=false), NOT a silent fallback to the
// shared ws: the child has Shell precisely because isolation was available, so running
// it shared would be the exact hazard. With a nil forker the child runs against the
// parent content backend through any configured authority-narrowing Workspace
// view (the read-only no-shell path AND the writable direct-write path). The returned cleanup is ALWAYS non-nil (a no-op when nothing was forked) so
// the caller can defer it unconditionally.
//
// advisory is the forker's OPTIONAL degraded-fork note (empty in the normal case): a
// dirty-overlay forker returns it when the parent had uncommitted work that could not
// be mirrored into the child's checkout, so the child sees committed HEAD only. The
// caller prepends it to the child's prompt so the child reasons honestly about the
// degradation instead of silently reporting "nothing to review".
func (t *SubagentTool) forkChildEnvironment(ctx context.Context, callID session.ToolCallID, env tool.Environment, label string, forker tool.EnvironmentForker) (runEnv tool.Environment, cleanup func() error, advisory string, errResult session.ToolResult, ok bool) {
	if forker == nil {
		if t.ledgerFactory == nil {
			return tool.Environment{}, nil, "", session.NewToolError(callID, "Subagent: child read ledger is not configured"), false
		}
		workspace := env.Workspace()
		if t.sharedChildWS != nil {
			if childWorkspace := t.sharedChildWS(workspace); childWorkspace != nil {
				workspace = childWorkspace
			}
		}
		ledger := t.ledgerFactory()
		childEnv, err := tool.NewEnvironment(env.Ref(), workspace, ledger, env.CommandRunner())
		if err != nil {
			return tool.Environment{}, nil, "", session.NewToolError(callID, "Subagent: child environment failed: "+err.Error()), false
		}
		return childEnv, func() error { return nil }, "", session.ToolResult{}, true
	}
	forkEnv, forkCleanup, advisory, err := forker.Fork(ctx, env, label)
	if err != nil {
		return tool.Environment{}, nil, "", session.NewToolError(callID, "Subagent: workspace isolation failed: "+err.Error()), false
	}
	if t.ledgerFactory == nil {
		if forkCleanup != nil {
			_ = forkCleanup()
		}
		return tool.Environment{}, nil, "", session.NewToolError(callID, "Subagent: child read ledger is not configured"), false
	}
	forkEnv, err = tool.NewEnvironment(forkEnv.Ref(), forkEnv.Workspace(), t.ledgerFactory(), forkEnv.CommandRunner())
	if err != nil {
		if forkCleanup != nil {
			_ = forkCleanup()
		}
		return tool.Environment{}, nil, "", session.NewToolError(callID, "Subagent: child environment failed: "+err.Error()), false
	}
	if forkCleanup == nil {
		forkCleanup = func() error { return nil }
	}
	return forkEnv, forkCleanup, advisory, session.ToolResult{}, true
}

// buildChildSession produces the session one child run drives. On a FRESH call
// (resumedChild == nil): a new session — own conversation, own (tighter) Limits, scoped
// to the run workspace root (the isolated worktree when forked, else the parent base).
// On RESUME: the session was already reloaded + recovered (before the fork, in
// resolveResumeSession); here it is RE-HOMED onto the run root so its recorded workspace
// stays consistent with where this run actually executes — the original worktree is
// torn down, and without the re-home the re-persisted snapshot would record a dead
// path. (The child's prompt cwd is independently sourced from the engine's PromptConfig
// and is NOT affected by this field.)
func (t *SubagentTool) buildChildSession(callID session.ToolCallID, childID, parentID session.SessionID, parentIncarnation session.IncarnationID, resumedChild *session.Session, env tool.Environment, limits session.Limits, forkHistory []session.Message) (*session.Session, session.ToolResult, bool) {
	if resumedChild == nil {
		// When a named agent def pins limits, the child runs under THOSE; otherwise it uses
		// the Subagent tool's default limits.
		var child *session.Session
		var err error
		if parentID == "" {
			// Direct Tool.Execute has no parent aggregate identity. Classify that
			// custom-host path unknown rather than fabricating lineage or granting main.
			child = newChildSessionInEnvironment(childID, t.childMode, env, limits, t.childEngine.now())
			err = child.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{})
		} else {
			child, err = newSubagentSessionInEnvironment(childID, t.childMode, env, limits, t.childEngine.now(), parentID, parentIncarnation, callID)
		}
		if err != nil {
			return nil, session.NewToolError(callID,
				fmt.Sprintf("Subagent: failed to stamp child relationship metadata: %v", err)), false
		}
		// fork:true (issue #34): seed the FRESH child from the deep copy of the parent
		// conversation taken synchronously in run()/startBackground. SeedHistory is
		// idle-only and re-validates tool pairing (the snapshot is already
		// orphan-stripped by ForkSnapshot — double-defended). A seeding failure tears
		// down the fork via the caller's cleanup and surfaces a model-addressable error,
		// mirroring the resume re-home failure path. forkHistory is nil on a non-fork
		// call (or a turn-0 empty snapshot, which SeedHistory accepts trivially).
		if forkHistory != nil {
			if err := child.SeedHistory(forkHistory); err != nil {
				return nil, session.NewToolError(callID,
					fmt.Sprintf("Subagent: failed to seed forked subagent %q from the parent conversation: %v", childID, err)), false
			}
		}
		return child, session.ToolResult{}, true
	}
	if err := rehomeSessionInEnvironment(resumedChild, env); err != nil {
		return nil, session.NewToolError(callID,
			fmt.Sprintf("Subagent: failed to re-home resumed subagent %q: %v", childID, err)), false
	}
	return resumedChild, session.ToolResult{}, true
}

// subagentGoal derives the short, plain-text goal label forwarded on
// EvSubagentStart: the explicit description when supplied, else the (model-
// authored) prompt. It is metadata for the card title; it is NOT child content
// (the prompt is the parent's own instruction to the child). Both paths are
// clamped identically so the goal always stays a single, bounded line — an
// explicit description is just as capable of being long or multi-line as a prompt.
func subagentGoal(args subagentArgs) string {
	if g := strings.TrimSpace(args.Description); g != "" {
		return truncateGoal(g)
	}
	return truncateGoal(strings.TrimSpace(args.Prompt))
}

// truncateGoal normalizes a goal label into a single bounded line: it collapses
// any newlines (and tabs) to spaces so a multi-line value can't break the one-line
// Subagent-card title, then clamps to maxSubagentGoalLen runes, appending an ellipsis
// when it overflows. It is rune-aware so it never splits a multi-byte character.
func truncateGoal(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) <= maxSubagentGoalLen {
		return s
	}
	return strings.TrimRight(string(r[:maxSubagentGoalLen]), " ") + "…"
}

// drainChildObserved consumes the child Run's Event channel to completion,
// applying the non-interactive child contract (auto-deny asks) via
// handleChildEvent, and returns the terminal result text, stop reason, the
// terminal FAILURE CAUSE (ResultPayload.Error — non-empty only on a StopError
// terminal; issue #319), the child's cumulative usage, and the number of child tool
// calls observed.
//
// When emit is non-nil it ALSO forwards a REDACTED, BOUNDED projection of the
// child's activity (ADR 0079): a tool NAME + error bool + running count, plus
// BOUNDED previews — a tool.call's args and a tool.result's body ride Detail
// (clamped by clampPreview), a message.delta's and the terminal result's text
// ride Text (clamped), with InnerKind naming the inner kind the preview came
// from. Every preview passes through clampPreview (control-byte scrub + rune
// cap), and a child's permission.ask (like every other kind outside the four
// projected ones) is DROPPED — its possibly secret-bearing reason never
// reaches the stream. This keeps gauntlet #7 intact: the projection is
// client-only and nothing enters the parent's Conversation. With a nil emit
// it discards every intermediate event exactly as the original drainChild
// did.
func drainChildObserved(run *Run, emit func(session.Event), parentCallID, childID string, posture childPosture) (finalText string, stop session.StopReason, cause string, usage session.Usage, toolCount int) {
	// Track child callID → tool name so a tool.result can be attributed to its
	// tool.call name without re-deriving it from the (clamped) args preview.
	names := map[session.ToolCallID]string{}
	// turnUsage accumulates the child's CUMULATIVE provider-reported usage across the
	// drain, independent of the terminal `usage` (read from EvResult). Each EvTurnEnd
	// projection carries the running total so a client renders a live ↑↓ mid-run
	// instead of a zero until subagent.end. Estimates are EXCLUDED (the issue-#82
	// fallback is display-only, never cumulative/budget — provider truth only).
	var turnUsage session.Usage
	for ev := range run.Events() {
		if emit != nil {
			// Project ONLY the preview kinds (ADR 0079) plus EvTurnEnd (the live-usage
			// projection); a non-projected event is dropped before any allocation.
			toolCount, turnUsage = projectChildEvent(emit, ev, names, parentCallID, childID, toolCount, turnUsage)
		}
		// handle:
		if text, st, ok := handleChildEvent(run, ev, posture); ok {
			finalText, stop = text, st
			if ev.Result != nil {
				// The terminal FAILURE CAUSE (non-empty only on a StopError terminal;
				// issue #319) is read here, on the same line as Usage, rather than
				// threaded out of handleChildEvent — see its doc-comment.
				usage, cause = ev.Result.Usage, ev.Result.Error
			}
		}
	}
	return finalText, stop, cause, usage, toolCount
}

// projectChildEvent maps ONE child event to its redacted subagent.tool projection and
// emits it, advancing the running cumulative totals. It is the per-event half of
// drainChildObserved, split out to keep that drain loop's complexity readable. It
// returns the updated toolCount and turnUsage.
//
// ToolCount and Usage are stamped on EVERY projection — both are CUMULATIVE and always
// current, so a client assigns either with no InnerKind guard (no projection reads
// 0-after-positive). toolCount is tools-STARTED (a call, not a result: a result may
// never arrive on a cancel); turnUsage is provider-reported usage (the issue-#82
// display-only estimate is never folded in).
func projectChildEvent(emit func(session.Event), ev session.Event, names map[session.ToolCallID]string, parentCallID, childID string, toolCount int, turnUsage session.Usage) (int, session.Usage) {
	// Project ONLY the preview kinds (ADR 0079) plus EvTurnEnd (the live-usage
	// projection); a non-projected event is dropped before any allocation.
	switch ev.Type {
	case session.EvToolCall, session.EvToolResult, session.EvMessageDelta,
		session.EvResult, session.EvTurnEnd:
	default:
		return toolCount, turnUsage
	}
	// Count a tool when it STARTS and accumulate usage BEFORE the payload build, so the
	// current projection and every later one carry the new totals.
	if ev.Type == session.EvToolCall && ev.ToolCall != nil {
		toolCount++
	}
	if ev.Type == session.EvTurnEnd && ev.TurnEnd != nil && !ev.TurnEnd.Estimated {
		turnUsage = turnUsage.Add(ev.TurnEnd.Usage)
	}
	payload := &session.SubagentPayload{
		ParentCallID: parentCallID,
		ChildID:      childID,
		InnerKind:    ev.Type,
		ToolCount:    toolCount,
		Usage:        turnUsage,
	}
	var project bool
	switch ev.Type {
	case session.EvToolCall:
		project = projectChildToolCall(payload, ev, names)
	case session.EvToolResult:
		project = projectChildToolResult(payload, ev, names)
	case session.EvMessageDelta:
		project = strings.TrimSpace(ev.Text) != ""
		if project {
			payload.Text = clampPreview(ev.Text)
		}
	case session.EvResult:
		project = ev.Result != nil
		if project {
			payload.Text = clampPreview(ev.Result.Text)
		}
	case session.EvTurnEnd:
		// EvTurnEnd carries no content fields (Text/Detail stay empty); its usage was
		// applied at the accumulator above. Project only when it carries REAL usage —
		// a zero or estimated turn adds nothing new.
		project = ev.TurnEnd != nil && !ev.TurnEnd.Estimated && ev.TurnEnd.Usage != (session.Usage{})
	}
	if project {
		emit(session.Event{Type: session.EvSubagentTool, Subagent: payload})
	}
	return toolCount, turnUsage
}

// projectChildToolCall populates the payload from a child EvToolCall and records the
// callID→name mapping so a later tool.result can be attributed without re-deriving it
// from the clamped args preview. Returns false when the event carries no call.
func projectChildToolCall(payload *session.SubagentPayload, ev session.Event, names map[session.ToolCallID]string) bool {
	if ev.ToolCall == nil {
		return false
	}
	names[ev.ToolCall.ID] = ev.ToolCall.Name
	payload.ToolName = ev.ToolCall.Name
	payload.Detail = clampPreview(string(ev.ToolCall.Args))
	return true
}

// projectChildToolResult populates the payload from a child EvToolResult, resolving the
// tool name from the recorded call. Returns false when the event carries no result.
func projectChildToolResult(payload *session.SubagentPayload, ev session.Event, names map[session.ToolCallID]string) bool {
	if ev.ToolResult == nil {
		return false
	}
	payload.ToolName = names[ev.ToolResult.CallID]
	payload.IsError = ev.ToolResult.IsError
	payload.Detail = clampPreview(ev.ToolResult.Content)
	return true
}

// drainChild consumes the child Run's Event channel to completion, auto-denying
// any permission ask (subagents are non-interactive), and returns the terminal
// result text and stop reason. It deliberately discards every intermediate event
// (turn.start, message.delta, tool.call, tool.result, hook, compaction) so none
// of them can reach the parent — this is the context-isolation guarantee of
// gauntlet #7. It is the silent variant used by fork.go and the team supervisor;
// the observed Subagent path uses drainChildObserved.
func drainChild(run *Run, posture childPosture) (finalText string, stop session.StopReason) {
	final, st, _, _, _ := drainChildObserved(run, nil, "", "", posture)
	return final, st
}

// childPosture carries the per-child permission resolution context applied to a child/
// member run's permission asks (the 4-step model). It is threaded by every drain path
// (drainChildObserved, drainChild, the team supervisor's driveOneTurn) so the resolution
// order — floored-configured-allow / configured-ask gate (issue #32) → isolation
// auto-approve → surface-to-human → headless auto-deny — is identical everywhere and
// cannot drift.
//
// The zero value is the legacy posture: not isolated, not interactive, no surface →
// every ask auto-denies (with the accurate message). A child run that is isolated sets
// isolated=true; an interactive parent supplies caps with a non-nil surfaceAsk.
type childPosture struct {
	// isolated reports that the child runs in an ISOLATED workspace (a git worktree or a
	// force-copy fork) — so an IsolationApprovable Shell ask (read-only ∪ worktree-safe
	// go verbs) auto-APPROVES (A2). false for a base-sharing child (no auto-approve).
	isolated bool
	// caps carries the parent's interactivity + surface back-channel (zero value =
	// headless: no surface). When caps.interactive && caps.surfaceAsk != nil, an ask that
	// steps 1-2 did not resolve is SURFACED to the human; otherwise it auto-denies.
	caps parentCaps
	// childID is the child SESSION id (the registry/cancel handle: "subagent-<callID>",
	// "team-<teamID>-<member>", "parallel-<callID>-<i>") — threaded EXPLICITLY because
	// role does NOT universally carry it (a team posture's role is the member NAME, a
	// parallel posture's the branch label). surfaceAsk passes it to recordAsk so a
	// surfaced ask's ownership is recorded uniformly across all three families.
	childID string
	// role is the child's identity for the headless auto-deny operator diagnostic: the
	// child session id ("subagent-<callID>") for Subagent children, the member name for
	// team members, the branch/judge label for forks. Empty falls back to a generic label.
	role string
	// askLabel is the pre-composed human-facing requester phrase passed to surfaceAsk so
	// a surfaced ask is ATTRIBUTED to its delegation (e.g. `subagent "fix flaky tests"`,
	// `team member "researcher"`, `parallel branch "branch-2"`). It is plain metadata
	// (no raw args; the goal/name/label is already collapsed + clamped at the call site),
	// re-clamped by the parent before emission. Empty for headless postures that never
	// surface (fork judge, user-model review) — they keep the legacy framing.
	askLabel string
}

// childAutoDenyMessage is the ACCURATE message a headless (non-interactive) subagent's
// auto-denied ask carries — NOT the misleading "denied by user … client approval
// required" of the interactive path. It names the real cause (a non-interactive subagent
// shell) and what the model can do about it. reason is the policy's ask reason.
// configured selects the suffix: a CONFIGURED Ask (a real permission rule gates the
// command) gets rule-oriented advice — the substitution-rephrase advice would be a lie
// there (no rephrasing satisfies a configured rule; only an approver can).
func childAutoDenyMessage(reason string, configured bool) string {
	if configured {
		return "not permitted in a non-interactive subagent shell: " + reason +
			"; this command requires approval by a configured permission rule and no interactive approver is attached" +
			" — use a command the rule does not gate, or report back that approval is required"
	}
	return "not permitted in a non-interactive subagent shell: " + reason +
		"; rephrase to avoid command substitution/subshell grouping, or use an auto-approved tool (read-only commands, or go test/build/vet/list)"
}

// childReviewedDenyMessage is the ACCURATE message an ADJUDICATED deny carries
// (issue #31): the automated policy reviewer examined the command and declined
// it, so the model is told a reviewer (not a user, not a blanket shell rule)
// said no — with the reviewer's clamped rationale — and what it can do about
// it. reason is the policy's ask reason; verdictReason is the reviewer's
// rationale (clamped here: it is reviewer-model-authored text riding into the
// child's tool result). Only a CONFIGURED-Ask-free, headless, reviewed deny
// gets this message; the not-reviewed paths keep childAutoDenyMessage so the
// model is never told a reviewer declined when none did.
func childReviewedDenyMessage(reason, verdictReason string) string {
	vr := strings.TrimSpace(verdictReason)
	if vr == "" {
		vr = "no reason given"
	}
	return "not permitted in a non-interactive subagent shell: " + reason +
		"; an automated policy reviewer declined this command (" + clampPreview(vr) + ")" +
		" — rephrase to a read-only form or report back that approval is required"
}

// handleChildEvent applies the per-child permission contract to a single event of a
// child/member run and, when the event is the terminal result, reports its text and
// stop reason via isResult=true. It is the SINGLE definition of that contract, shared
// by drainChildObserved (Subagent), drainChild (Fork, silent), and the team supervisor's
// driveOneTurn, so the resolution order cannot drift.
//
// Resolution order on a permission ask (the 4-step model, plus the issue-#32
// config axis riding the ask's decision bits):
//  0. CONFIGURED ASK: ask.ConfiguredAsk (a real configured rule gates the call) →
//     SKIP every auto-approve (both the floored-allow and the isolation paths)
//     and fall through to surface/headless — the configured-Ask-never-suppressed
//     invariant extended to step A2. Checked FIRST so the illegal both-bits-true
//     state (the two bits are mutually exclusive by construction, but ride an
//     externally-reachable PendingAsk) fails SAFE (gated), never auto-approves.
//  1. FLOORED CONFIGURED ALLOW: ask.FlooredConfiguredAllow (the ask exists ONLY
//     because of the substitution floor, a configured child-scoped Allow covers
//     every floored segment, every recursively-extracted INNER is positively
//     read-only — the configured Allow vouches only for the OUTER literal —
//     and the blanked outer passes the worktree-escape rejections) → AllowOnce
//     without surfacing. Deny is impossible here — a deny resolves in the
//     ordinary fold and never becomes an ask.
//  2. ISOLATION auto-approve (A2): an isolated child whose ask is IsolationApprovable
//     (read-only ∪ worktree-safe go verbs, no worktree-escape verb) → AllowOnce. (A1's
//     read-only substitution carve-out already turns most read-only substitutions into
//     Allow upstream so they never reach here as an ask; this catches the worktree-safe
//     `go test` superset.)
//  3. SURFACE to human: an interactive parent with a surface seam registers the child in
//     the parent router and emits a REDACTED parent EvPermissionAsk, then RETURNS without
//     resolving — the child's authorize stays parked in await until the parent routes a
//     verdict back via the router (child.Approve). The drain loop blocks on this child's
//     channel until then (single-child) or keeps consuming peers (concurrent members).
//     3b. HEADLESS ask review (OPT-IN): a parent engine configured with a
//     ChildAskReviewer consults the automated reviewer for a NON-configured ask
//     that reached the headless branch (a configured Ask demands a human — never
//     the reviewer). Reviewed allow → AllowOnce; reviewed deny → auto-deny with
//     childReviewedDenyMessage; abstention/breaker-open/reviewer-failure → fall
//     through to 4 (a failure emits its own INFO first).
//  4. HEADLESS auto-deny: no surface (headless / no router) → Deny with the ACCURATE
//     message (rule-oriented when the ask was configured, substitution-rephrase advice
//     otherwise) + a correlated operator diagnostic (LevelInfo, agent=<child-session-id>
//     for Subagent children, e.g. "subagent-<callID>"; member name / fork label for the
//     others) — never the misleading "denied by user".
//
// It deliberately does NOT return the terminal failure CAUSE, even though issue #319 needs
// it: drainChildObserved already reads `usage` straight off ev.Result on the line after
// this call, so `cause = ev.Result.Error` belongs there too. Threading a second `string`
// out of here would add another transposable text/cause pair to the return chain the
// TRIP-WIRE above driveChild worries about, and force a `_` on the supervisor call site
// that has no use for it.
func handleChildEvent(run *Run, ev session.Event, posture childPosture) (text string, stop session.StopReason, isResult bool) {
	if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
		resolveChildAsk(run, *ev.Ask, posture)
	}
	if ev.Type == session.EvResult && ev.Result != nil {
		return ev.Result.Text, ev.Result.Stop, true
	}
	return "", session.StopNone, false
}

// resolveChildAsk applies the 4-step resolution (plus the issue-#32 config axis;
// see handleChildEvent's ordering doc) to one child permission ask.
func resolveChildAsk(run *Run, ask session.PendingAsk, posture childPosture) {
	// Config axis, BEFORE the isolation auto-approve. The two bits are mutually
	// exclusive BY CONSTRUCTION (the evaluator never sets both), but they ride a
	// PendingAsk that crosses run boundaries and is externally reachable, so the
	// gate ORDER is the fail-safe for the illegal both-true state: ConfiguredAsk
	// (surface / auto-deny) is checked FIRST, so a both-true ask fails SAFE
	// (gated), never auto-approves.
	//   - A CONFIGURED Ask must NEVER be auto-approved (the configured-Ask-never-
	//     suppressed invariant, extended to A2): skip BOTH the floored-allow
	//     auto-approve and the isolation auto-approve and fall through to
	//     surface-to-human / headless auto-deny.
	//   - Otherwise a substitution-floored ask whose every floored segment a
	//     CONFIGURED Allow covers — with every extracted INNER positively
	//     read-only and the blanked outer escape-rejection-free — resolves
	//     AllowOnce: the configured child Allow vouches for the OUTER, and the
	//     inner/escape bound was checked in the evaluator. Deny never reaches here
	//     (it resolves in the ordinary fold).
	if ask.ConfiguredAsk {
		// Fall through to surface / headless auto-deny (skip every auto-approve).
	} else if ask.FlooredConfiguredAllow {
		run.Approve(ask.AskID, session.VerdictAllowOnce)
		return
	} else if posture.isolated && ask.Tool == "Shell" && governance.IsolationApprovable(shellCmdFromArgs(ask.Args)) {
		// Step A2: isolated child + isolation-approvable Shell → auto-approve.
		run.Approve(ask.AskID, session.VerdictAllowOnce)
		return
	}
	// Surface to the human when the parent is interactive and a surface seam is
	// wired. Register-then-emit lives inside surfaceAsk; we DO NOT resolve here — the
	// child stays parked until the parent routes a verdict back.
	if posture.caps.interactive && posture.caps.surfaceAsk != nil {
		posture.caps.surfaceAsk(ask.AskID, posture.childID, run, ask, posture.askLabel)
		return
	}
	// HEADLESS ask review (OPT-IN): between surface-to-human and the blanket
	// auto-deny, an engine configured with a ChildAskReviewer consults an automated
	// reviewer — but NEVER for a configured Ask (an LLM reviewer is not the human
	// approver a deliberately-configured rule demands; configured Deny/Ask still win,
	// same invariant as A2). A reviewed ALLOW resolves AllowOnce (never AllowAlways —
	// nothing is learned); a reviewed DENY auto-denies with the reviewer's clamped
	// rationale folded into the model-facing message. Every variant emits a
	// correlated operator INFO at this SAME child-ask diagnostic chokepoint (the
	// sanctioned emission OUTSIDE the loop's three-line contract — see
	// docs/adr/0020-diagnostics.md). A NOT-reviewed outcome (abstention / breaker-open
	// / reviewer failure) falls through to the plain auto-deny below with the
	// EXISTING message — no false "reviewer declined" claim — after, on a failure,
	// emitting the distinct reviewer-failure INFO (and, once per run, the
	// breaker-opened INFO) so an operator can tell a flaky reviewer from a blanket
	// deny. The approved/denied command is named in the audit line (clamped) — an
	// autonomous approval must record WHAT it ran, not only the policy reason.
	if !ask.ConfiguredAsk && posture.caps.adjudicate != nil {
		outcome := posture.caps.adjudicate(ask, posture.isolated)
		cmdPreview := clampPreview(surfacedCommandPreview(ask))
		// One-time breaker-opened INFO, emitted regardless of WHICH non-allow outcome
		// crossed the threshold (a deny verdict or a failure) — so it is checked here,
		// before the outcome switch returns. Emitted once per run; further skipped
		// asks stay quiet.
		if outcome.breakerJustOpened && posture.caps.diag != nil {
			posture.caps.diag.Log(context.Background(), port.LevelInfo,
				"subagent ask reviewer circuit breaker opened after consecutive non-allows; remaining asks this run auto-deny without review",
				"agent", posture.role)
		}
		switch {
		case outcome.reviewed && outcome.allowed:
			if posture.caps.diag != nil {
				posture.caps.diag.Log(context.Background(), port.LevelInfo,
					"subagent permission ask allowed by the automated policy reviewer",
					"agent", posture.role, "tool", ask.Tool, "reason", ask.Reason,
					"command", cmdPreview, "decision", "reviewed-allow",
					"verdict_reason", clampPreview(outcome.reason))
			}
			run.Approve(ask.AskID, session.VerdictAllowOnce)
			return
		case outcome.reviewed:
			if posture.caps.diag != nil {
				posture.caps.diag.Log(context.Background(), port.LevelInfo,
					"subagent permission ask auto-denied (non-interactive shell)",
					"agent", posture.role, "tool", ask.Tool, "reason", ask.Reason,
					"command", cmdPreview, "decision", "reviewed-deny",
					"verdict_reason", clampPreview(outcome.reason))
			}
			run.autoDenyChildAsk(ask.AskID, childReviewedDenyMessage(ask.Reason, outcome.reason))
			return
		case outcome.failed && posture.caps.diag != nil:
			// Reviewer ran but produced no verdict (error / timeout / ambiguous
			// output): a DISTINCT INFO so a reviewer model that 404s/times out every
			// call is visible, not silently folded into the blanket auto-deny below.
			posture.caps.diag.Log(context.Background(), port.LevelInfo,
				"subagent ask reviewer failed to produce a verdict; falling back to auto-deny",
				"agent", posture.role, "tool", ask.Tool, "command", cmdPreview,
				"err", clampPreview(outcome.failReason))
		}
		// Fall through to the plain headless auto-deny.
	}
	// Headless / no surface → auto-deny with the accurate message + an operator
	// diagnostic, then resolve the child's own ask. The child's authorize maps a Deny
	// verdict to a denied result carrying decision.Reason (the policy's), so we ALSO emit
	// the operator-visible diagnostic here (the deny otherwise reaches only the child's
	// errored tool-result, which default clients bury). The denied RESULT message the
	// model sees is rebuilt by authorize; the accurate phrasing is surfaced via the
	// diagnostic and the bash tool description. A CONFIGURED ask gets the rule-oriented
	// suffix — the substitution-rephrase advice would be a lie for it.
	if posture.caps.diag != nil {
		posture.caps.diag.Log(context.Background(), port.LevelInfo,
			"subagent permission ask auto-denied (non-interactive shell)",
			"agent", posture.role, "tool", ask.Tool, "reason", ask.Reason)
	}
	run.autoDenyChildAsk(ask.AskID, childAutoDenyMessage(ask.Reason, ask.ConfiguredAsk))
}

// shellCmdFromArgs extracts the Shell command string from a pending ask's raw args,
// delegating to the shared governance extractor (the single source of truth for the
// Shell tool-call args schema). Empty on a parse failure (then
// IsolationApprovable("") is false — fail safe).
func shellCmdFromArgs(args json.RawMessage) string {
	cmd, _ := governance.ShellCommandFromArgs(args)
	return cmd
}

// fireSubagentStop runs the SubagentStop lifecycle hook for a finished child run.
// It is best-effort: a hook error or a block outcome is ignored (a subagent's
// completion cannot be vetoed after the fact).
func (t *SubagentTool) fireSubagentStop(ctx context.Context, child *session.Session) {
	fireNotify(ctx, t.hooks, governance.HookEvent{
		Phase:     governance.PhaseSubagentStop,
		SessionID: string(child.ID),
	})
}

// persistChild best-effort saves the child session to the injected store so the
// InspectSubagent tool can later load its transcript by the agentId trailer. A nil
// store disables persistence; a save failure is advisory and swallowed (persistMember
// discipline).
// recordChildCauseOnSnapshot persists the terminal failure CAUSE on the child
// session snapshot via RecordLastError (issue #332). It is a no-op unless the
// child's drive ended in StopError with a non-empty cause; the child is StateFailed
// at this point (the loop's terminate ran), so the RecordLastError state guard
// passes. Extracted from run()/driveBackground so the cause-stamping does not add
// a branch to either caller's gocyclo budget.
func recordChildCauseOnSnapshot(child *session.Session, stop session.StopReason, cause string) {
	if stop == session.StopError && cause != "" {
		_ = child.RecordLastError(cause)
	}
}

func createSessionIfSupported(ctx context.Context, store port.SessionStore, sess *session.Session) error {
	if store == nil {
		return nil
	}
	creator, ok := store.(port.SessionCreator)
	if !ok {
		return nil
	}
	return creator.Create(ctx, sess)
}

func (t *SubagentTool) persistChild(ctx context.Context, child *session.Session) {
	if t.store == nil {
		return
	}
	// Only detach when the caller's ctx is already DONE. The common persist — a
	// foreground or background child ending on a still-live ctx — needs no new
	// context, so it takes the fast path below with zero allocation (the perf
	// scenario hammers exactly this shape). The detach exists for the two TERMINAL
	// persists whose ctx is already dead: the per-call timeout_ms fired, or the
	// parent run was cancelled. This save is the ONLY thing that makes the child
	// resumable afterwards, which the harness now advertises to the model on exactly
	// those terminals (subagentTimeoutResumeHint, writableSubagentTimeoutNote). The
	// in-tree stores ignore ctx on Save, but redisstore.Save passes it straight to
	// HSet and grpcdriver.SessionStore.Save to the RPC, so on a mecak8s/Redis or
	// remote-driver deployment the advertised resume came back as "no subagent found
	// for resume id" — an advertised affordance that cannot succeed, the exact defect
	// resumeSupported() exists to prevent, one layer down. The deadline is what makes
	// detaching safe: with no live ctx to cancel it, a wedged store would otherwise
	// block the tool call indefinitely.
	if ctx.Err() == nil {
		_ = t.store.Save(ctx, child)
		return
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), childPersistTimeout)
	defer cancel()
	_ = t.store.Save(saveCtx, child)
}

// unknownAgentHint builds the model-addressable error text for a Subagent call that
// names an agent that is not registered. It lists the valid names so the model can
// retry, mirroring the Skill tool's available-names hint.
func (t *SubagentTool) unknownAgentHint(name string) string {
	if len(t.agentMeta) == 0 {
		return fmt.Sprintf("unknown agent %q (no specialist agents are configured; omit `agent` to use the default explorer)", name)
	}
	names := make([]string, 0, len(t.agentMeta))
	for _, m := range t.agentMeta {
		names = append(names, m.Name)
	}
	return fmt.Sprintf("unknown agent %q; available agents: %s", name, strings.Join(names, ", "))
}

// childSessionID derives a stable, unique id for a child session from the
// PARENT SESSION's id plus the parent tool call id (review finding 2, issue
// #368): deriving from the call id ALONE let two different top-level sessions
// (already collision-safe across owners — session creation is atomically
// owner-scoped) issuing equal or adversarially-chosen call ids collide on the
// SAME durable child id, silently overwriting each other's persisted
// transcript. parentID is empty only on a caps-less drive (plain
// Execute/ExecuteObserved, no parent session threaded — tests and the rare
// direct-call path), which keeps the pre-fix call-id-only id; every real
// dispatch path threads a non-empty parentID via parentCaps.
func (t *SubagentTool) childSessionID(parentID session.SessionID, callID session.ToolCallID) session.SessionID {
	if parentID == "" {
		return session.SessionID(fmt.Sprintf("%s-%s", t.idPrefix, callID))
	}
	return session.SessionID(fmt.Sprintf("%s-%s-%s", t.idPrefix, parentID, callID))
}

// Compile-time assertion that SubagentTool satisfies the Tool contract and the
// agent-internal observableTool seam.
var (
	_ tool.Tool      = (*SubagentTool)(nil)
	_ observableTool = (*SubagentTool)(nil)
)
