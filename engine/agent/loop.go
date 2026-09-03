// Package agent is the use-case heart of mecatl: the streaming agent loop
// that ties the ports together. It records the user prompt, calls the
// LLMProvider, streams assistant deltas, dispatches tool calls (read-parallel /
// mutate-serial) through the permission policy and hook lifecycle, pauses on
// permission asks, compacts the history at the context-window threshold, and
// emits a single ordered stream of session.Events terminating in a result.
//
// Import rule: this package imports ONLY engine/session, engine/port,
// engine/tool, engine/governance, engine/prompt, engine/team (the domain
// coordination substrate for agent teams), and the standard library. Adapters are
// injected as ports; the loop never names a concrete adapter or the api layer.
// (Tests may import adapters.)
package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// defaultCompactionRatio is the fraction of the context window at which the loop
// triggers compaction when Deps.CompactionRatio is unset.
const (
	defaultCompactionRatio = 0.8
	// compactionTargetRatio is the complete-request size the automatic cascade
	// reduces toward after subtracting request-local irreducible overhead.
	compactionTargetRatio = 0.6
)

// defaultNoProgressNudges is the safety-net cap applied in NewEngine when
// Deps.MaxNoProgressNudges is zero (unset). It bounds how many continuation nudges
// the loop injects after an empty/reasoning-only turn before it terminates the run
// clearly with StopNoProgress. A negative MaxNoProgressNudges disables nudging.
const defaultNoProgressNudges = 2

// noProgressNudgeText is the GENTLE continuation injected as a NEW user message on the
// EARLY no-progress attempt(s) (every nudge except the final one before give-up; see
// finishTurnNoTools). It is a fresh user message (NOT a forced text block and NOT a
// re-send of the prior array), so it complies with the reasoning-model field guidance
// ("do not naively retry the same message array"; "avoid emitting text blocks right
// after tool results") and never forces tool use (no tool_choice). The substring
// "Make concrete progress on the task using your tools" is a stable test key — do not
// change it.
const noProgressNudgeText = "Please continue. Make concrete progress on the task using your tools, " +
	"or — if you are blocked or believe the task is complete — say so explicitly in a short message."

// noProgressExtractiveNudgeText is the FINAL no-progress nudge, injected on the LAST
// attempt before give-up (the nudge where *noProgressNudges == nudgeCap-1; see
// finishTurnNoTools). Unlike the gentle nudge it does not ask for "progress"; it is a
// last-resort EXTRACTION: stop investigating and produce a best-effort deliverable NOW
// from information already gathered, rather than ending the run with nothing. It is a
// fresh user message (NOT a forced text block, NOT a re-send, NO tool_choice) — same
// provider-neutral compliance as noProgressNudgeText. It instructs the model AGAINST
// running tools but does NOT set tool_choice (the no-tool_choice-forcing invariant). The
// extractive nudge is structurally guaranteed one more model turn (it rides the same
// return-false re-drive path), so a model that answers after it completes with
// StopEndTurn, NOT StopNoProgress. The substring "Stop investigating now" is a stable
// test key (extractiveNudgeMessagesIn) — do not change it.
const noProgressExtractiveNudgeText = "Stop investigating now and do not run any " +
	"more commands or tools. Using only the information you have already gathered, " +
	"write your best final answer to the original task as a direct message now, even " +
	"if it is incomplete or uncertain — note any gaps briefly. Do not plan further " +
	"steps; deliver what you have."

// shellLessPostureNote is the SINGLE model-visible shell-less posture clause, appended
// to the per-request system prompt's VOLATILE suffix by buildRequest when the LIVE
// tool.Environment handed to Run has no CommandRunner (env.CommandRunner() == nil) and
// is not the no-FS profile (whose noFSPostureNote already states "no shell"). It is the
// ADR-0070 affordance for the shell-less default-FS posture: the model must learn from
// the PROMPT that the Bash tool is absent (the spec is also dropped from the advertised
// tools), not from a trail of unknown-tool errors, so it plans around the file tools and
// its own reasoning instead of burning turns attempting a shell it cannot call. It is
// the per-request, environment-capability-truthed analogue of composition's noFSPostureNote
// (which covers the no-filesystem-at-all case): environment capability is per-run/per-turn,
// so it rides the volatile suffix and is NOT baked into the cache-stable prefix. The
// "NO shell" substring is a stable test key — do not change it.
const shellLessPostureNote = "This session has NO shell: the Bash tool is not available. " +
	"Do not attempt to run commands, build, test, or invoke git — work through the file " +
	"tools (Read/Write/Edit/Grep/Glob), your other tools (MCP, memory, web fetch), and " +
	"your own reasoning."

// backgroundNoticeText renders the turn-boundary background-completion NOTICE
// injected as a harness-framed user message at Step 2a of drive (A2 —
// notice-only injection). It carries ONLY harness-authored metadata: child ids
// + their session.StopReason labels, NOTHING child-authored (no goal labels, no
// result text — the delegation bodies' sole channel is SubagentStatus, the bash
// jobs' BashStatus). The rendering is FAMILY-AWARE: the delegation clause keeps
// its exact historical wording (the substring "background subagent(s) finished"
// is a stable test key — do not change it) and a background-Bash clause is
// APPENDED only when bash jobs are among the finished, so a subagent-only run
// renders byte-identically to before.
func backgroundNoticeText(finished []childStatus) string {
	var delegationIDs, bashItems []string
	for _, st := range finished {
		if st.family == childFamilyBashCmd {
			bashItems = append(bashItems, fmt.Sprintf("%s (%s)", st.id, st.stop))
		} else {
			delegationIDs = append(delegationIDs, fmt.Sprintf("%s (%s)", st.id, st.stop))
		}
	}
	var b strings.Builder
	if len(delegationIDs) > 0 {
		fmt.Fprintf(&b, "[harness note: %d background subagent(s) finished: %s. "+
			"Collect each result with SubagentStatus before relying on it.]",
			len(delegationIDs), strings.Join(delegationIDs, ", "))
	}
	if len(bashItems) > 0 {
		fmt.Fprintf(&b, "[harness note: %d background command(s) finished: %s. "+
			"Collect each output with BashStatus before relying on it.]",
			len(bashItems), strings.Join(bashItems, ", "))
	}
	return b.String()
}

// backgroundPendingNudgeText renders the ONCE-per-run background-pending nudge
// (D10 as amended) injected as a harness-framed user message when the run would
// otherwise end CLEANLY while background children are still live. It lists ids
// ONLY (A9 — no goal labels, nothing model/child-authored). Like the notice it
// is FAMILY-AWARE: the delegation clause keeps its exact historical wording
// (the substring "background subagent(s) still running" is a stable test key —
// do not change it) and a background-Bash clause is APPENDED only for live bash
// jobs, each clause naming its own collection channel.
func backgroundPendingNudgeText(ids []string) string {
	var delegationIDs, bashIDs []string
	for _, id := range ids {
		if strings.HasPrefix(id, BashCmdJobPrefix) {
			bashIDs = append(bashIDs, id)
		} else {
			delegationIDs = append(delegationIDs, id)
		}
	}
	var b strings.Builder
	if len(delegationIDs) > 0 {
		fmt.Fprintf(&b, "[harness note: %d background subagent(s) still running: %s. "+
			"Collect or wait for them with SubagentStatus, cancel them, or finish — "+
			"anything still running when you finish will be cancelled.]",
			len(delegationIDs), strings.Join(delegationIDs, ", "))
	}
	if len(bashIDs) > 0 {
		fmt.Fprintf(&b, "[harness note: %d background command(s) still running: %s. "+
			"Collect or wait for them with BashStatus, cancel them, or finish — "+
			"anything still running when you finish will be cancelled.]",
			len(bashIDs), strings.Join(bashIDs, ", "))
	}
	return b.String()
}

// Deps are the injected ports and configuration a single Engine is built from.
// Every field is a port (an interface) or plain config, so the agent package
// never depends on a concrete adapter. The composition root wires real or fake
// adapters in.
type Deps struct {
	// LLM is the model-call seam.
	LLM port.LLMProvider
	// Catalog is the tool registry; the loop reads Specs(mode) and Lookup(name).
	Catalog *tool.Catalog
	// Policy evaluates each tool call (deny → ask → allow).
	Policy port.PermissionPolicy
	// AuthorityEvaluator authorizes executions for sessions carrying a derived
	// authority set. Bound sessions require an evaluator; composition selects either
	// enforcement or the explicit noop evaluator when it mints bound sessions.
	AuthorityEvaluator port.AuthorityEvaluator
	// Hooks runs the PreToolUse / PostToolUse lifecycle hooks.
	Hooks port.HookRunner
	// Store persists session state (optional; nil disables persistence).
	Store port.SessionStore
	// SessionLiveness is the optional lifecycle-exclusion seam for engine-owned
	// child sessions. The parent run registers each delegation child before it can
	// queue or drive and releases it on every terminal or pre-start-abort path.
	// Composition may back it with both process-local tracking and SessionLease;
	// registration failure prevents the child from becoming runnable.
	SessionLiveness port.SessionLiveness
	// Clock supplies wall time for tool-call timing (optional; nil → no timing).
	Clock port.Clock
	// ToolCallRecorder records tool-execution observability — the tool-call audit
	// seam (optional; nil → no recording).
	ToolCallRecorder port.ToolCallRecorder
	// Sink, when non-nil, also receives every Event the loop emits, in addition
	// to the Run.Events() channel which is always the primary surface.
	Sink port.EventSink
	// EnableDurableEvidence emits debugger-only request manifests and accepts
	// sanitized provider-attempt observations. It is opt-in because constructing
	// that evidence requires hashing/counting, contexts, maps, and slices; hosts
	// should enable it only when their relay persists events to a durable EventLog
	// for later inspection. Sink presence is not a durability signal.
	EnableDurableEvidence bool
	// Diagnostics is the general-purpose operational logging seam (optional; nil →
	// port.NopDiagnostics, applied in NewEngine, so the engine never nil-panics and
	// stays silent when no sink is injected). It is DISTINCT from ToolCallRecorder
	// (the per-tool audit seam) and Sink (the model's conversation stream).
	Diagnostics port.Diagnostics
	// LearningMode and LearningObserver install the optional completed-trajectory
	// observation seam. Observation is synchronous and runs once after each eligible
	// clean completion. The zero mode, Off, or a nil observer is inert.
	LearningMode     learning.Mode
	LearningObserver learning.Observer
	// Compactor compresses history at the threshold; nil → HeuristicCompactor.
	Compactor Compactor
	// TokenCounter estimates history size for the compaction trigger (and is
	// shared with the Compactor); nil → HeuristicTokenCounter.
	TokenCounter TokenCounter
	// Instructions assembles the project-instruction messages recorded once at
	// the start of a run; nil → prompt.RootAssembler (root-only AGENTS.md /
	// CLAUDE.md, the v1 default).
	Instructions prompt.InstructionAssembler
	// CommandExpander rewrites a raw user prompt into the text the model sees,
	// expanding slash-command invocations (e.g. "/review foo.go") against the
	// workspace before the prompt is recorded; nil → prompt.NoopExpander (no
	// expansion, the v1 default).
	CommandExpander prompt.CommandExpander
	// PromptConfig seeds the cache-stable system prompt (role/tone/safety). The
	// loop fills in Tools and the volatile Env per turn.
	PromptConfig prompt.Config
	// PromptBuilder assembles the layered system prompt each turn. nil →
	// prompt.Build (the v1 default: coding-agent role/tone/safety + tool
	// inventory), byte-identical to v0.0.1. A host that needs a fully host-owned
	// system prompt (e.g. a non-coding agent) supplies a prompt.Builder here; it
	// receives the same Config the loop builds (Tools + volatile Env and live
	// operator profile filled per turn) and returns the Layered prompt. Only the
	// MAIN loop's buildRequest routes through this field — the compaction summarizer
	// (cascade.go) builds its own prompt.Layered directly and is unaffected.
	PromptBuilder prompt.Builder
	// OperatorProfileSource supplies full durable user facts. When non-nil it is
	// read before every model request; failures are fail-soft and retain the last
	// good snapshot within the run.
	OperatorProfileSource prompt.OperatorProfileSource

	// Model is the provider model identifier sent on every request.
	Model string
	// ContextWindow returns the model's context window in tokens, resolved LIVE at
	// the point of use (the compaction check / Engine.ContextWindow) rather than
	// frozen at construction — so a post-construction live-catalog refresh self-
	// corrects on the next turn without rebuilding the engine. A nil closure OR a
	// <=0 return DISABLES compaction (preserving the old "zero disables"). The
	// closure is a stdlib func value; it is built in composition (internal/app)
	// over the FIXED (provider, model) so the engine itself imports no adapter.
	ContextWindow func() int
	// CompactionRatio overrides defaultCompactionRatio when in (0,1].
	CompactionRatio float64

	// Role is the operator-facing label this engine logs under in diagnostics: the
	// empty string for the MAIN engine (correlated by session only), or a non-empty
	// role for a child/subagent engine (e.g. "task" for the Subagent tool, a team
	// member's name, or a fork-branch label) so interleaved child diagnostics are
	// readable. It is read once per run when binding the run-scoped Diagnostics (see
	// drive): empty → only the "session" key; set → "session"+"agent" keys. It is
	// NOT plumbed into Sink/ToolCallRecorder (those stay off for children) and never
	// reaches the model.
	Role string

	// MaxNoProgressNudges bounds how many times the loop injects a continuation
	// ("please continue") user message after a completed turn that produced NEITHER a
	// tool call NOR meaningful assistant text (a reasoning-only / empty turn). It is
	// the blast-radius knob for the no-progress handler. Semantics (applied in
	// NewEngine): a ZERO value (the default; existing Deps built without it) uses the
	// safety-net default defaultNoProgressNudges (2); a NEGATIVE value DISABLES nudging
	// entirely (a no-progress turn terminates immediately with StopNoProgress, the old
	// behaviour minus the silent StopEndTurn mislabel); a positive value overrides the
	// default. It is a loop concern, exactly like CompactionRatio — NOT a
	// port.LLMRequest field (the request stays provider-neutral). Composition plumbs it
	// from Config so it is operator-tunable; children inherit the default.
	MaxNoProgressNudges int

	// Interactive reports whether a HUMAN approver is attached to this engine's runs:
	// true for the bidi Converse / HTTP-SSE surfaces where a client can answer a
	// permission ask, false for a headless/in-process run (the demo, a RunTeam with no
	// attached client). It is composition's knowledge of whether an approval UI exists,
	// set on the MAIN engine only and read once per run (drive) to (a) install the
	// child-ask router so a subagent's ask can be SURFACED to the human, and (b) tell a
	// subagent posture whether to surface (interactive) or auto-deny (headless). It is a
	// plain bool — NOT a port.LLMRequest field and never reaches the model. Child engines
	// leave it false (a child never surfaces further). DEFAULT false (fail-safe: a run
	// with no declared approver auto-denies a subagent ask rather than hanging).
	Interactive bool

	// PlanModeAutoApprove is an OPT-IN, OPERATOR-TIER-ONLY, DEFAULT-OFF flag that
	// tells the engine to SURFACE a plan-approval ask (PresentPlan) even when headless
	// (no human approver attached), so the composition layer's Service can auto-resolve
	// it via ApprovePlan without operator interaction. It is DELIBERATELY ONLY the
	// PresentPlan gate — a non-plan ask (policy/hook) is still headless-auto-denied.
	// DEFAULT false (fail-safe: a headless plan ask is auto-denied like every other ask).
	// It is a plain bool — NOT a port.LLMRequest field and never reaches the model.
	// Child engines inherit it from the parent (so a Subagent/team child's plan ask also
	// parks rather than auto-denies), allowing composition to auto-approve at the Service
	// layer.
	PlanModeAutoApprove bool

	// ChildAskReviewer, when non-nil, reviews a child agent's (subagent / team
	// member / parallel branch) permission ask that the harness could not resolve
	// statically and that no attached human can answer — instead of blanket-denying
	// it. It is consulted ONLY for a headless run with no interactive approver, and
	// never for a deliberately-configured "ask" rule (that always demands a human).
	// An allow grants the one call only (nothing is learned); a deny — or any review
	// failure, timeout, or ambiguous verdict — keeps the call denied, so a reviewer
	// is fail-safe and never load-bearing for safety. Reviews are bounded by a
	// 30-second deadline and serialised through a per-run consecutive-failure
	// breaker (ChildAskReviewMaxDenies). DEFAULT nil: a headless ask is
	// blanket-denied, the long-standing behaviour. Set this on the engine whose runs
	// have no human approver; an engine whose runs surface asks to a client leaves
	// it unused (the surface path wins).
	ChildAskReviewer ChildAskReviewer

	// ChildAskReviewMaxDenies is the per-run circuit-breaker threshold for
	// ChildAskReviewer: after this many CONSECUTIVE non-allow review outcomes (deny
	// verdicts, failures, timeouts, ambiguous verdicts — an abstention via
	// ErrNotReviewable does not count) within one run, further asks skip the reviewer
	// and fall through to auto-deny, bounding reviewer spend. An allow resets the
	// count. <=0 (the default) uses a built-in threshold of 3. Only consulted when
	// ChildAskReviewer is set.
	ChildAskReviewMaxDenies int

	// MaxRunTokens is the loop-level cumulative TOKEN ceiling for a single run: when
	// the run's accumulated session.Usage (input+output, via Usage.TotalTokens) crosses
	// this value, the loop terminates CLEANLY at the next turn boundary with
	// session.StopBudget. It is the shared runaway brake the AGENT-TEAMS-SPIKE named the
	// missing token budget — checked in drive Step 2, so it serves EVERY engine: main +
	// Subagent + Team member + lead synthesis + Fork branch. Semantics: 0 (the default;
	// existing Deps built without it) DISABLES the budget (behaviour byte-identical to
	// before); a positive value is the ceiling. It is a turn-BOUNDARY check (never a
	// mid-stream abort), so an in-flight turn always completes and
	// no-replay-after-first-chunk holds; the terminal is non-error, so the session ends
	// COMPLETED and stays Reopen-recoverable (mirrors StopNoProgress exactly). It is a
	// loop concern, like CompactionRatio / MaxNoProgressNudges — NOT a port.LLMRequest
	// field (the request stays provider-neutral). Composition plumbs it from Config so it
	// is operator-tunable; children INHERIT it (childEngineDepsForProvider keeps it) and a
	// per-call override may only TIGHTEN it.
	MaxRunTokens int

	// SubagentModelRouter, when non-nil, is the OPT-IN semantic model router (ADR
	// 0031, the Phase 5 headline feature): given a Subagent call's (model-authored,
	// untrusted) task prompt it returns the ALREADY-RESOLVED concrete model id to mint
	// the child on, plus the category label it classified into, plus the classifier's
	// session.Usage (which the dispatch-path routeTask closure folds into the parent
	// sess.Usage so classifier spend counts against --max-run-tokens — the #92 fix).
	// It is a composition closure — the engine layer is model-string-only (the layering
	// rule): composition owns the classifier engine, the category taxonomy, and the
	// category→model mapping (aliases/slots/the allowlist cap), and hands the engine
	// only func(ctx, string)(string, string, session.Usage, string, bool) (the trailing
	// string is the miss REASON — issue #287, logged VERBATIM at the dispatch chokepoint
	// on a miss; empty on a hit). The reason is OPERATOR-DIAGNOSTIC detail: it also rides
	// the delegation-start event's RoutingReason field, but ONLY after the engine's
	// event-safe allowlist (routingReasonPayload) confines it to the harness/composition
	// metadata constants — an external composition returning a provider error body,
	// classifier output, or a task excerpt sees it substituted with a generic label on the
	// wire (gauntlet #7), while the verbatim text stays in the diagnostics channel. It is consulted by
	// the Subagent run() hook ONLY for a plain default delegation (no per-call model,
	// no agent, no fork, no resume) and is FAIL-SOFT throughout: ok=false (any
	// classifier failure, an unknown category, the breaker open) → the call falls
	// through to the inherited default explorer model, byte-identically to a deployment
	// with no router. DEFAULT nil: no router, the long-standing behaviour. Set on the
	// MAIN engine only (a child has no Subagent tool, so structurally no router);
	// childEngineDepsForProvider forces it nil (the no-nesting recursion guard). Like
	// ChildAskReviewer, the router is built into the per-call parentCaps.routeTask
	// closure in Engine.parentCaps, never called directly by the loop, so it is NOT a
	// port.LLMRequest field and never reaches a request.
	//
	// The ctx is the RUN's ctx (threaded down via parentCaps.routeTask), NOT
	// context.Background(): a Run.Cancel between the breaker's hardAbort check and the
	// classifier call must propagate into RunModelRouter so the classifier turn dies
	// with the run instead of running out its 30s clock (issue #94 — the
	// cancellation-propagation gap the hardAbort TOCTOU otherwise leaves). Fail-soft
	// holds regardless: a cancelled ctx yields StopCancelled → ok=false → inherit the
	// default model, exactly the existing miss path.
	SubagentModelRouter func(ctx context.Context, taskPrompt string) (category, model string, usage session.Usage, missReason string, ok bool)

	// ProgressiveTools, when true, enables progressive tool disclosure
	// (pattern 9): the per-turn request advertises lightweight specs for tools
	// implementing tool.Disclosable plus a built-in ToolSearch tool the model
	// uses to hydrate a full spec on demand. The DEFAULT (false) sends every
	// tool's full spec every turn, exactly as v1 does. The ToolSearch tool is
	// registered into the catalog by NewEngine only when this is enabled.
	ProgressiveTools bool

	// DeliveryQueue, when non-nil, is the DURABLE per-session pending-delivery queue
	// the loop's turn-boundary drain reads (ADR 0075 decision #3, fire-result-delivery
	// Scenario 4). The fire path (composition) enqueues a rendered fire-result note
	// for an origin session that is BUSY or AWAITING (it cannot drive a delivery run
	// without colliding); the loop drains the pending notes at Step 2a, BEFORE
	// BeginTurn — the SAME turn-boundary seam injectBackgroundNotice uses — recording
	// each as an ordinary harness-framed user continuation (recordContinuation) and
	// marking it delivered via MarkDelivered (the session-scoped exactly-once ledger).
	//
	// The drain is registered on the MAIN + per-session engines ONLY — never on child
	// engines (a child origin degrades to pull-only with a WARN at the fire path). nil
	// (the default) is the byte-identical no-delivery path: nothing drains, the
	// fire path's enqueue is a no-op against a nil queue. It is a port (port.DeliveryQueue),
	// so the agent package imports no concrete adapter.
	DeliveryQueue port.DeliveryQueue

	// EnableSteer, when true, arms each Run with an in-memory, best-effort
	// steer inbox (steer-while-running, issue #512): an operator-supplied
	// instruction enqueued mid-run that the loop drains at the next turn
	// boundary (Step 2a, the same seam injectBackgroundNotice /
	// drainPendingDelivery use) and records as an ordinary user continuation
	// via recordContinuation, so it replays to the model and flows through
	// compaction / session.ValidateToolPairing / ADR-0038 rehydration
	// unchanged. It is a plain loop-concern bool — NOT a port.LLMRequest field
	// and never reaches the model as anything but an ordinary recorded user
	// message. DEFAULT false (the zero value): no inbox is armed and the drain
	// is a strict no-op, byte-identical to the pre-steer posture. Composition
	// plumbs it from Config; child engines inherit the shared deps.
	// A pending (un-drained) steer is in-memory only and lost with the run —
	// never persisted (see steer.go).
	EnableSteer bool
}

// Engine builds Runs from a fixed set of ports. It is safe for concurrent use:
// each Run owns its own goroutine and state, and the injected ports are expected
// to be concurrency-safe (the provided adapters are). One Engine typically backs
// the whole process; the API layer (WP10) calls Run per prompt.
type Engine struct {
	deps Deps
}

// now returns the engine's wall-clock time from the injected Clock, or the zero
// time when no Clock is configured (the same optional-Clock contract the loop's
// timing reads already honour: no clock → zero timestamp / zero duration). It is
// the ONLY wall-clock source permitted in the engine core: a direct time.Now() in
// non-test core code is forbidden by engine/arch's TestNoWallClockInEngineCore, so
// the engine is fully clock-injectable (deterministic) for an embedding host that
// drives every wall-clock read through the injected Clock (issue #116).
//
// It is nil-receiver-safe: it replaced direct time.Now() calls, which never
// panicked, so a (provably-non-nil today) child-engine reference that a future
// refactor zeroed must degrade to the zero time, not a panic.
func (e *Engine) now() time.Time {
	if e == nil || e.deps.Clock == nil {
		return time.Time{}
	}
	return e.deps.Clock.Now()
}

// NewEngine constructs an Engine from deps, applying defaults for the optional
// Compactor and CompactionRatio.
func NewEngine(deps Deps) *Engine {
	if deps.TokenCounter == nil {
		deps.TokenCounter = HeuristicTokenCounter{}
	}
	if deps.Compactor == nil {
		deps.Compactor = HeuristicCompactor{}
	}
	if deps.CompactionRatio <= 0 || deps.CompactionRatio > 1 {
		deps.CompactionRatio = defaultCompactionRatio
	}
	// No-progress nudge budget: zero (unset) → the safety-net default; a negative
	// value is an explicit "disable nudging" sentinel and is left as-is (drive treats
	// nudgeCap < 0 as disabled). A positive value overrides the default.
	if deps.MaxNoProgressNudges == 0 {
		deps.MaxNoProgressNudges = defaultNoProgressNudges
	}
	// Ask-review breaker threshold: <=0 (unset) → the safety-net default. Only
	// consulted when a ChildAskReviewer is wired, but normalised unconditionally
	// so the breaker construction in Engine.Run never sees a zero max.
	if deps.ChildAskReviewMaxDenies <= 0 {
		deps.ChildAskReviewMaxDenies = DefaultAskReviewMaxDenies
	}
	if deps.Instructions == nil {
		deps.Instructions = prompt.RootAssembler{}
	}
	if deps.CommandExpander == nil {
		deps.CommandExpander = prompt.NoopExpander{}
	}
	if deps.Diagnostics == nil {
		deps.Diagnostics = port.NopDiagnostics{}
	}
	// Progressive disclosure: register the ToolSearch hydration tool so the model
	// can fetch a full spec on demand. It is registered only when enabled and only
	// if a Catalog is present and does not already carry one (idempotent).
	if deps.ProgressiveTools && deps.Catalog != nil {
		if _, ok := deps.Catalog.Lookup(tool.ToolSearchName); !ok {
			_ = deps.Catalog.Register(tool.NewToolSearch(deps.Catalog))
		}
	}
	return &Engine{deps: deps}
}

// Capabilities reports the multimodal input capabilities of the Engine's LLM
// provider, so a surface adapter can advertise them and gate unsupported prompt
// content. It is a pure pass-through to the injected provider.
func (e *Engine) Capabilities() port.ProviderCapabilities {
	return e.deps.LLM.Capabilities()
}

// ContextWindow reports the model's context window in tokens, resolved LIVE via
// Deps.ContextWindow at the point of call, or 0 when unknown/unset/disabled. The
// team supervisor reads it from each member's engine so a forwarded turn.end can
// carry the denominator for the per-member context meter in the ctrl+a agents
// overlay (the resolver lives in private deps).
func (e *Engine) ContextWindow() int {
	if e.deps.ContextWindow == nil {
		return 0
	}
	return e.deps.ContextWindow()
}

// Model returns the provider model identifier this engine sends on every request
// (deps.Model). It is the read-only seam a delegation emitter uses to surface the
// child's resolved model on its start event, independent of how the model was chosen
// (inherited default, agent-def pin, per-call override, or the opt-in router). The
// string is bare metadata (a model id); the engine stays model-string-only — no
// adapter/proto type crosses here. Used by the Subagent / Parallel / Team delegation
// emit sites to populate the generic Model field (issue #112, ADR 0035).
func (e *Engine) Model() string { return e.deps.Model }

// HasTool reports whether a tool with the given registered name is present in
// the Engine's catalog. It is the read-only seam a surface adapter uses to
// report capabilities (e.g. memory/skills/bash availability) from the BUILT
// catalog rather than a static config flag, so the report can never claim a
// feature the engine did not register. It is nil-safe: a nil catalog yields
// false. It exposes only presence, never the concrete tool, keeping the agent
// package free of any adapter dependency.
func (e *Engine) HasTool(name string) bool {
	if e.deps.Catalog == nil {
		return false
	}
	_, ok := e.deps.Catalog.Lookup(name)
	return ok
}

// SteerEnabled reports whether this engine arms its Runs with the mid-run
// steer inbox (Deps.EnableSteer, steer-while-running issue #512). It is the
// read-only seam composition reads to advertise the feature — the
// ServerCapabilities.steer bit reads the SAME wired knob the runs consult, so
// the advertisement can never claim a steer path the engine did not arm
// (single-source, never recomputed per sink).
func (e *Engine) SteerEnabled() bool { return e.deps.EnableSteer }

// catalogToolInfo is a (name, read-only) summary of one tool in an Engine's
// catalog. The supervisor uses it to verify a member's tool set without
// type-asserting concrete tool types (which would require importing an adapter,
// breaking the layering rule).
type catalogToolInfo struct {
	name     string
	readOnly bool
}

// catalogTools returns a (name, read-only) summary of every tool in the Engine's
// catalog, or nil if the Engine carries no catalog. It is the read-only seam the
// Supervisor uses to enforce the read-only-member / workspace-mutating-tool
// invariant (see Supervisor.AddMember). It deliberately exposes only the
// name+ReadOnly bits, never the concrete tools, keeping the agent package free of
// any adapter dependency.
func (e *Engine) catalogTools() []catalogToolInfo {
	if e.deps.Catalog == nil {
		return nil
	}
	tools := e.deps.Catalog.Tools()
	out := make([]catalogToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, catalogToolInfo{name: t.Spec().Name, readOnly: t.ReadOnly()})
	}
	return out
}

// Run is the handle to one in-flight prompt. It exposes the Event stream plus the
// out-of-band controls the bidi API needs (Approve resolves a permission.ask;
// Cancel aborts the run). The Events channel is closed exactly once, when the run
// terminates.
type Run struct {
	events chan session.Event
	asks   *askRegistry
	cancel context.CancelFunc
	seq    atomic.Int64
	// hardAbort is closed a short grace AFTER Cancel (hardAbortOnce arms the
	// hardAbortGrace timer BEFORE the ctx cancel) — the explicit "stop blocking
	// anywhere" unwedge signal every guarded send on this run selects on (emit,
	// emitOrAbort, the supervisor's member forward). It is DISTINCT from the
	// child registry's emitAbort (which closes only at seal-INTENT, i.e. from the
	// terminate paths — unreachable while the loop is itself parked in a blocked
	// send) and from the run ctx (deliberately NOT a guarded send's escape hatch:
	// a cancelled run with a draining consumer must still deliver in-flight
	// events — see emit/emitOrAbort). The GRACE is what reconciles the two
	// contracts: a cancelled consumer that is merely BACKLOGGED (buffer full but
	// still draining) absorbs the post-cancel tail — the terminal StopCancelled
	// EvResult included — before parked sends give up, while a consumer that
	// genuinely stopped draining unwedges at Cancel+grace. Without the signal, a
	// dead consumer wedges the run permanently: the buffers fill, every send
	// parks, and the seal/abort escape can never be reached. Sticky (a closed
	// channel): once it fires, any send that would BLOCK gives up instead.
	// nil on a hand-built Run (tests); Cancel is nil-safe.
	hardAbort     chan struct{}
	hardAbortOnce sync.Once
	// serial is this Run's process-unique discriminator (minted from runSerial in
	// Engine.Run), suffixed into every askID (newAskID) so two RUNS of the SAME
	// session can never re-mint the same askID. Without it, cancel-a-parked-ask →
	// resume the same child id in the same parent run → Counters reset → the
	// provider re-mints the same call id → the new ask's id COLLIDES with the
	// retracted one, and a stale queued ResumeApproval for the old ask would
	// resolve the new one (CWE-863). Set once before the run goroutine starts and
	// only read after, so it needs no synchronisation.
	serial int64
	// askDiscriminator is the resolved trailing askID component for this run:
	// req.AskIDDiscriminator when the host supplied a valid (non-empty,
	// colon-free) value, else the process-global "r<serial>" fallback. Resolved
	// once in startRun. See newAskID + RunRequest.AskIDDiscriminator + ADR-0044.
	askDiscriminator string
	// runID is the host-minted identity stamped onto every event this run emits
	// (ADR 0249). Read ONLY by emit/emitOrAbort; the loop never branches on it.
	runID string
	// ctx is the run's context, captured at Engine.Run. Engine.emit forwards it
	// to the injected EventSink so telemetry adapters can read a trace span from
	// it and correlate spans/metrics to the originating request. Each run (including
	// child/subagent/fork runs, which enter through their own Engine.Run call)
	// captures its OWN ctx, so a child's emits correlate to the child, not the
	// parent. It is set once before the run goroutine starts and only read after,
	// so it needs no synchronisation.
	ctx context.Context //nolint:containedctx // run-scoped carrier forwarded to the EventSink; never the request's own field
	// workspace is the private runtime root of the live Environment. Durable
	// EnvironmentRef IDs are provider-opaque and must never be interpreted as paths.
	workspace string
	// diag is the run-scoped operational-logging seam: deps.Diagnostics bound to
	// this run's session id (and, for a child engine, its agent role) via With, so
	// every line emitted through it carries the correlation keys. It is bound ONCE
	// per run in Engine.Run — NOT at engine construction, because the engine is
	// built before the session id exists and is often SHARED across sessions. It is
	// Nop-safe: deps.Diagnostics is never nil post-NewEngine, and With on
	// NopDiagnostics returns NopDiagnostics. It is set before the run goroutine
	// starts and only read after, so it needs no synchronisation.
	diag port.Diagnostics
	// saveWarned makes the session-persistence WARN sticky per RUN. A store that
	// is broken is broken for every save, and save runs at least once per turn, so
	// logging unconditionally would emit up to MaxTurns near-identical lines per
	// run per session. The flag lives on the Run and not the Engine because an
	// Engine is often SHARED across sessions — an Engine-level flag would let one
	// session's failure silence another's. Written and read only from the run's own
	// goroutine (all save call sites are on it), so it needs no synchronisation.
	saveWarned bool
	// childAsks, when non-nil, routes a foreign (child-namespaced) askID passed to
	// Approve down to the child Run that owns it (a SURFACED subagent ask). It is set
	// only on an INTERACTIVE main engine's run (drive); a child run's own ask always
	// resolves through r.asks. nil on a child run and on a headless run. It is set before
	// the run goroutine starts and only read after, so it needs no synchronisation; the
	// router itself is concurrency-safe for the cross-goroutine register/route.
	childAsks *childAskRouter
	// askReview is this run's ask-review circuit breaker, created in
	// Engine.Run only when the engine carries a ChildAskReviewer (the
	// headless-review opt-in) — the askReview-non-nil ⇔ reviewer-wired pairing is
	// what the parentCaps closure keys on. Its mutex also SERIALIZES reviews within
	// the run (deterministic consecutive-failure semantics; bounded concurrent
	// reviewer spend). nil on the default (no-reviewer) engine and on child runs.
	// Set before the run goroutine starts and only read after.
	askReview *askReviewBreaker
	// router is this run's model-router circuit breaker (ADR 0031), created in
	// Engine.Run only when the engine carries a SubagentModelRouter — the
	// router-non-nil ⇔ router-wired pairing the parentCaps closure keys on. Its mutex
	// SERIALIZES classifications within the run (deterministic consecutive-miss
	// semantics; bounded concurrent classifier spend under a Subagent fan-out). nil on
	// the default (no-router) engine and on child runs. Set before the run goroutine
	// starts and only read after.
	router *modelRouterBreaker
	// children registers every child run spawned under this run (all three
	// delegation families: Subagent children, Parallel branches, team members),
	// keyed by child session id.
	// Unlike childAsks (interactive-only) it is created UNCONDITIONALLY in
	// Engine.Run: cancel arrives over the wire only on interactive surfaces, but
	// the registry also carries headless bookkeeping, and it is a mutex + map. It is
	// set before the run goroutine starts and only read after; the registry itself
	// is concurrency-safe.
	children *childRunRegistry
	// req are the per-RUN request parameters supplied at Engine.Run (the user prompt
	// PLUS the run-scoped overrides: a TIGHTEN-ONLY token ceiling and a set of
	// run-scoped EXTRA tools, e.g. the synthetic SubmitResult tool for a
	// structured-output Subagent child). They are read-only after the goroutine starts
	// and are NEVER folded into the shared Engine.deps — that is the whole point: a
	// per-call override must not mutate the shared child engine (the "Provider is FIXED
	// per session" / no-clone-swap discipline applied to run-scoped knobs). The zero
	// value of the override fields is the legacy run (no override, no extras).
	req RunRequest
	// planApprovedTarget is the permission mode a plan-approval Allow verdict will
	// flip the session into at the terminal boundary: AllowOnce → ModeDefault,
	// AllowAlways → ModeAccept. It is RUN-SCOPED (zero/"" = no approval pending),
	// set ONLY by surfacePlanAsk / the resolvePendingCall PlanOriginated allow
	// branch, and read ONLY by terminateComplete — which, after the session reaches
	// StateCompleted, flips the mode out of ModePlan and saves. It is deliberately
	// NOT serialized: it is a within-run transient that the StopPlanApproved clean
	// terminal + the serialized PlanOriginated marker already cover cross-process
	// (a parked plan-ask resumes via resolvePendingCall, which re-sets it on the
	// resumed run before runLoop sees it). Set before the run goroutine reaches
	// terminateComplete and only read after, so it needs no synchronisation.
	planApprovedTarget session.PermissionMode
	// planIterateRequested is the run-scoped flag set when the operator DENIES a
	// plan-approval ask (issue #206): the run terminates CLEANLY with StopPlanIterate
	// instead of continuing in-turn (the old behaviour kept the model iterating with
	// NO operator input). It mirrors planApprovedTarget in shape — RUN-SCOPED (zero/
	// false = no iterate pending), set ONLY by surfacePlanAsk's deny branch and by the
	// resolvePendingCall PlanOriginated deny arm (a cross-process resumed plan ask
	// denied via ResumeApproval), and read ONLY by runLoop at its EARLY check and its
	// post-dispatch Step 6 check, which terminateComplete(StopPlanIterate). It does
	// NOT flip the mode — the session stays ModePlan so the operator's next prompt
	// drives the revision (terminateComplete only flips when planApprovedTarget !=
	// ""). Deliberately NOT serialized (a within-run transient; the serialized
	// PlanOriginated marker covers cross-process — a parked plan-ask denied on resume
	// re-sets it before runLoop sees it). Set before the run goroutine reaches
	// runLoop and only read after, so it needs no synchronisation.
	planIterateRequested bool
	// fragments are the EPHEMERAL turn-0 instruction fragments (project instructions /
	// soul / memory index / user model, produced by Deps.Instructions) prepended to the
	// LLMRequest.Messages on EVERY turn of this run (incl. resume) but NEVER persisted into
	// Conversation.Messages, event-carried, or snapshotted (ADR 0043). They are assembled
	// ONCE PER RUN (fragmentsOnce, in buildRequest's first turn) and reused on every
	// subsequent turn, so the message prefix stays byte-stable within the run (preserving
	// the prompt cache) without re-assembling per turn. Assembly is fail-soft: an
	// Instructions.Assemble error leaves fragments nil and the run continues. They are
	// written once (under fragmentsOnce) and read on every turn of the SAME goroutine
	// (the run loop is single-goroutine for buildRequest), so the Once is belt-and-braces.
	fragments             []session.Message
	fragmentManifest      []prompt.InstructionManifest
	fragmentsOnce         sync.Once
	operatorProfile       []tool.MemoryEntry
	operatorProfileLoaded bool
	operatorProfileWarned bool
	// currentPrompt is the accepted genuine prompt for this run. Completion locates
	// it in the final history; if compaction removed it, automatic admission gets an
	// invalid span and fails closed.
	currentPrompt *session.Message
	// steer is this run's in-memory, best-effort pending-steer inbox (steer-
	// while-running, issue #512). It is armed in startRun ONLY when the engine
	// carries Deps.EnableSteer; a nil inbox is the byte-identical no-steer
	// posture (the drain is a strict no-op). A drained steer is recorded into
	// durable history (recordContinuation) and echoed to the client (EvSteer);
	// a PENDING (un-drained) steer lives only here and is lost with the run on
	// crash / Cancel / Abandon — never persisted. See steer.go.
	steer *steerInbox
}

// runSerial mints the process-unique Run.serial discriminator (see Run.serial).
var runSerial atomic.Int64

// childSerial mints a process-unique, monotonic discriminator for the ephemeral
// in-memory child sessions (guardrail checker, fork judge, ask reviewer, model
// router) whose ids were previously derived from time.Now().UnixNano(). A counter
// is collision-free even under a fake (fixed) clock — where UnixNano would repeat
// and alias two children onto one id — so it keeps those ids unique without a
// direct wall-clock read, part of the engine's full clock-injectability (issue
// #116). Mirrors runSerial.
var childSerial atomic.Int64

// ErrNotAwaiting is the terminal cause driveFromAwaiting fails with when it is
// asked to resume a session that is NOT parked on a pending ask (no PendingAsk),
// or whose pending ask does not match the askID the resume targets. It never
// silently completes: a mismatch ends the run as StopError carrying this cause so
// a stale/duplicate Approve cannot drive an unexpected session to a clean
// terminal.
var ErrNotAwaiting = errors.New("agent: session is not awaiting the given approval")

// RunRequest is the single request shape a caller threads into Engine.Run. It carries
// the user prompt (text and/or non-text media parts) PLUS the run-scoped overrides that
// live on the Run, never on the shared Engine.Deps, so a per-call knob (a tighter token
// ceiling, a synthetic deliverable tool) works on a SHARED child engine WITHOUT minting a
// fresh engine or mutating the engine other concurrent runs share. The zero value of the
// override fields is the legacy run (no override, no extras).
type RunRequest struct {
	// Text is the (possibly empty) text of the user prompt. Command expansion and the
	// UserPromptSubmit hook operate on Text only; the media Parts pass through untouched
	// and are recorded verbatim on the user message. Text may be empty when Parts
	// carries the content.
	Text string
	// Parts carries non-text media (image/audio) alongside Text. nil for a text-only
	// prompt. The media passes through to the engine untouched.
	Parts []session.Content
	// MaxRunTokensOverride, when > 0, is a per-run TIGHTEN-ONLY override of the engine's
	// Deps.MaxRunTokens budget: the effective ceiling for THIS run is the lower of the
	// two non-zero values (a per-call ceiling may make the run stricter than the operator
	// default, never looser — mirroring the per-call limit tighten-only discipline). 0
	// (the default) inherits the engine's Deps.MaxRunTokens unchanged.
	MaxRunTokensOverride int
	// ExtraTools are run-scoped tools layered OVER the engine's catalog for THIS run
	// only: their specs are advertised to the model this run and they are dispatchable,
	// but they are never registered into the shared catalog (so concurrent runs of the
	// same engine never see them, and the engine is not mutated). A name collision with a
	// catalog tool resolves to the EXTRA tool (the run-scoped overlay wins) for THIS run.
	// The Subagent tool uses this to inject the synthetic SubmitResult deliverable tool for a
	// structured-output child. Every ExtraTool MUST be ReadOnly (it is dispatched on the
	// read-parallel path); a structured-output SubmitResult records into a per-run sink
	// and performs no workspace mutation, so it is read-only.
	ExtraTools []tool.Tool
	// RunID is the opaque, host-minted identity of THIS run (ADR 0249).
	//
	// It does two things and nothing else. Every event this run emits is stamped
	// with it at Run.emit/emitOrAbort, beside the existing Seq stamp, so no relay,
	// transport, or persistence path downstream can omit it. And when
	// AskIDDiscriminator is empty it also SUPPLIES the ask discriminator, which
	// is what ADR 0044 always meant by "a durable host passes its own RunID" —
	// so a durable host sets ONE field, not two carrying the same value.
	//
	// HOST CONTRACT (inherited from AskIDDiscriminator, because it feeds it): the
	// value must be UNIQUE per run-ATTEMPT and STABLE across processes for the
	// SAME attempt, or the CWE-863 askID replay guard weakens. It should be
	// colon-free; a colon-bearing value still stamps events fine but cannot serve
	// as an ask discriminator (the askID grammar would be ambiguous), so the run
	// falls back to the process-global serial for asks and logs a WARN.
	//
	// Empty (the zero value) is the legacy behaviour exactly: events carry an
	// empty RunID and asks use the process-global serial. mecatui, mecademo, and
	// tests pass nothing and are unaffected.
	//
	// The loop's licence over this value is deliberately narrow: STAMP it, and
	// DERIVE the ask discriminator from it. It must never be branched on, logged,
	// sent to a provider, or used to reach storage — see ADR 0249's consequences.
	RunID string
	// AskIDDiscriminator, when non-empty, REPLACES the trailing process-global
	// "r<serial>" component of every askID minted this run (see agent.newAskID),
	// making the askID reconstructable across processes from persisted state. The
	// askID format is "<sessionID>:<n>:<callID>:<discriminator>". HOST CONTRACT:
	// the host MUST supply a value that is (a) UNIQUE per run-ATTEMPT and (b)
	// STABLE across processes for the SAME attempt — this preserves the CWE-863
	// replay guard the process-global serial provides (the Interrupt/re-mint
	// scenario documented on newAskID): a stale verdict for a retracted ask must
	// never resolve a re-minted ask of a different attempt. (c) It MUST be
	// colon-free to keep the askID grammar unambiguous; a value containing a colon
	// is IGNORED and the run falls back to the process-global "r<serial>" (a WARN
	// is logged) rather than minting an ambiguous id. Empty (the zero value) keeps
	// the legacy "r<serial>" behavior with no change — mecatui, tests, and
	// in-memory hosts pass nothing and are unaffected. A durable host (e.g. a downstream consumer)
	// passes its own RunID. Same opt-in RunRequest seam pattern as
	// MaxRunTokensOverride/ExtraTools. See ADR-0044.
	//
	// FOOTGUN GUARD: after startRun the RESOLVED value (this when valid, else the
	// "r<serial>" fallback) lives on Run.askDiscriminator. askID minting (newAskID,
	// in authorize) MUST read r.askDiscriminator — NEVER this raw, un-validated
	// r.req.AskIDDiscriminator, which may be empty or colon-bearing and would
	// bypass the colon/empty fallback.
	AskIDDiscriminator string
}

// RunID reports the opaque, host-minted identity of this run (ADR 0249), or ""
// when the host supplied none.
//
// It exists so a caller holding a *Run can ASK which run it holds, rather than
// inferring it from the session aggregate. That distinction matters for stale
// controls: a control addressed at a specific run must be compared against the
// run it would actually affect, and a session's aggregate is a step removed from
// that (it names the session's CURRENT run, which after a terminal race may not
// be the one the caller is holding).
func (r *Run) RunID() string { return r.runID }

// Events returns the channel of domain Events for this run. It is closed when the
// run ends (after the terminal result Event has been delivered).
func (r *Run) Events() <-chan session.Event { return r.events }

// Approve resolves the permission.ask identified by askID with the client's
// verdict: VerdictDeny refuses the call, VerdictAllowOnce permits this call only,
// and VerdictAllowAlways permits it AND asks the policy to learn a per-session
// allow rule for the matching tool+pattern. It is non-blocking and safe to call
// from another goroutine; an unknown or already-resolved askID is ignored.
func (r *Run) Approve(askID string, v session.ApprovalVerdict) {
	// Router-first: a foreign (child-namespaced) askID belongs to a SURFACED subagent
	// ask — route the verdict to the owning child Run. Because child askIDs are prefixed
	// by a distinct child session id, they never collide with this run's own asks, so a
	// router miss (route==false) safely falls through to our own registry. A nil router
	// (child run / headless) skips straight to the own-registry path.
	if r.childAsks != nil && r.childAsks.route(askID, v) {
		return
	}
	r.asks.resolve(askID, v)
}

// RetractPermissionAsk withdraws this run's own pending permission ask without
// resolving it and emits one permission.retract event. It returns false when the
// ask is unknown or already resolved. The run remains parked until its host
// cancels it; this narrow seam lets a lease-owning host retract local delivery
// while preserving an already-durable awaiting snapshot for a successor.
func (r *Run) RetractPermissionAsk(askID string) bool {
	if !r.asks.discard(askID) {
		return false
	}
	r.emit(session.Event{Type: session.EvPermissionRetract, Ask: &session.PendingAsk{AskID: askID}})
	return true
}

// registerChildAsk records a surfaced child ask in this run's router so a later
// Approve(askID) is routed to the owning child. It is a no-op when this run has no
// router (a non-interactive or child run never surfaces). The router auto-removes the
// entry on the routed verdict (childAskRouter.route), so there is no explicit
// unregister on the resolution path.
func (r *Run) registerChildAsk(askID string, child *Run) {
	if r.childAsks != nil {
		r.childAsks.registerChild(askID, child)
	}
}

// autoDenyChildAsk resolves THIS (child) run's own ask askID with a Deny carrying the
// accurate, model-facing reason (the headless subagent auto-deny message), so the
// denied tool result the model sees names the real cause rather than "denied by user".
// It is always a self-resolution (a child run has no childAsks router), so it bypasses
// the router-first Approve path and writes directly to the ask registry.
func (r *Run) autoDenyChildAsk(askID, reason string) {
	r.asks.resolveWith(askID, approval{verdict: session.VerdictDeny, denyReason: reason})
}

// hardAbortGrace is how long after Cancel the run's hardAbort signal fires. The
// delay is load-bearing both ways: a cancelled consumer that is still DRAINING
// (just backlogged — a full events buffer behind a slow client) gets this long
// to absorb the post-cancel tail, so the terminal StopCancelled EvResult is
// still delivered (the documented cancelled-but-drained contract, pinned by the
// gRPC/loop cancel tests); a consumer that genuinely STOPPED draining unwedges
// every parked send at Cancel+grace, after which the sticky closed channel makes
// all later would-park sends give up instantly (no per-send re-wait — the
// unwedge cost is paid once per run, never per event). A var only as a test
// seam, mirroring childDrainCap/childDrainGrace.
var hardAbortGrace = time.Second

// Cancel aborts the in-flight run: it first arms the hardAbort grace timer (the
// explicit unwedge signal — any send still parked on a full events channel
// hardAbortGrace later gives up instead of blocking forever), THEN cancels the
// run context. The arm-before-cancel order matters: a ctx-woken goroutine that
// loops back into an emit must already be covered by the pending abort, or it
// could park indefinitely. The AfterFunc timer is deliberately never Stop()ed:
// at worst it fires once, shortly after a run that already ended, and closes a
// channel nobody reads any more — a harmless once-per-run close (contrast
// joinChildren's defer timer.Stop, which reclaims a shared 10s timer early on
// the common all-joined-fast path — a different shape: that timer does real
// work only on expiry; this one's entire job IS to fire). The loop observes the cancellation (mid-stream, mid-tool,
// or while awaiting an approval) and terminates with a result carrying
// StopCancelled.
func (r *Run) Cancel() {
	r.hardAbortOnce.Do(func() {
		if r.hardAbort != nil {
			time.AfterFunc(hardAbortGrace, func() { close(r.hardAbort) })
		}
	})
	r.cancel()
}

// CancelChild requests cancellation of ONE child of this run, addressed by its
// child session id (the `agentId:` trailer on the Subagent result / the overlay
// ChildID — the single handle convention; no per-family interpretation). It is
// the per-child mirror of Approve's routing role: a client frame addressed at a
// child, routed by the parent Run.
//
// It is idempotent and safe from any goroutine; an unknown or already-done id
// returns false (the finished-as-you-pressed race is benign). On a live child it:
// marks the registry entry clientCancelled (so the Subagent terminal renders
// "[subagent cancelled by user]" rather than a generic cancel), SNAPSHOTS the
// child's surfaced askIDs (without clearing — see below), cancels the child's
// per-call context OUTSIDE the registry lock (unwinding a mid-drive turn, a
// gate wait, or a parked askRegistry.await alike), then EAGERLY retracts the
// snapshot via the shared retractAsksVia: each askID is unregistered from the
// parent's childAskRouter atomically with its permission.retract emit (one
// emitMu section) — so a racing late approval falls through to the parent's
// own registry and dies as an unknown-ask no-op (fail-safe ordering), while
// the client dismisses its modal promptly, ahead of the child's unwind. The
// unregister's answered-vs-pending gate also means an ask whose verdict was
// JUST routed (route deleted the router entry first) no longer draws a
// spurious retract.
//
// The eager retract is BEST-EFFORT only — it runs on the caller's goroutine,
// unsynchronized with the run's terminate path, so it can lose a scheduling
// race to the seal (then its emitMu section sees sealed and skips, gate
// untouched). The GUARANTEED leg is the child's own registry terminal
// (markDoneResult — the retraction chokepoint, pre-doneCh-close ⇒ pre-seal),
// which takes the un-cleared set and retracts whatever this path didn't
// deliver; the atomic unregister gate keeps the two legs exactly-once.
func (r *Run) CancelChild(childID string) bool {
	cancel, askIDs, ok := r.children.requestCancel(childID)
	if !ok {
		return false
	}
	if cancel != nil {
		cancel()
	}
	// The explicit-gate variant (rather than the registry's bound gate) so the
	// ordering holds even on a Run built outside Engine.Run (unit tests); in
	// production the bound gate IS this method.
	r.children.retractAsksVia(r.unregisterChildAsk, askIDs)
	return true
}

// unregisterChildAsk is the answered-vs-pending retraction gate: it drops askID
// from this run's childAskRouter and reports whether an entry was actually
// removed (false ⇒ the verdict was already routed, or this run never surfaces —
// do NOT retract). Engine.Run binds it onto the child-run registry so the
// child-terminal retraction (markDoneResult) and the drain's abandoned sweep
// consult the SAME gate Run.CancelChild does.
func (r *Run) unregisterChildAsk(askID string) bool {
	return r.childAsks != nil && r.childAsks.unregister(askID)
}

// validateRunEnvironment enforces exact durable/live placement identity before
// any provider or tool activity.
func validateRunEnvironment(sess *session.Session, env tool.Environment) error {
	ref := env.Ref()
	if !sess.EnvironmentRef.Valid() || !ref.Valid() || sess.EnvironmentRef != ref {
		return errors.New("agent: environment identity mismatch")
	}
	return nil
}

func (e *Engine) prepareRunEnvironment(ctx context.Context, r *Run, sess *session.Session, env tool.Environment) bool {
	if err := validateRunEnvironment(sess, env); err != nil {
		e.emit(r, session.Event{Type: session.EvSessionInit})
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{}, err, false)
		return false
	}
	r.workspace = env.Workspace().Root()
	return true
}

// Run starts processing req against sess with the exact supplied environment and
// returns immediately with a handle to the background run.
func (e *Engine) Run(ctx context.Context, sess *session.Session, env tool.Environment, req RunRequest) *Run {
	return e.startRun(ctx, sess, req, "", func(ctx context.Context, r *Run) {
		if !e.prepareRunEnvironment(ctx, r, sess, env) {
			return
		}
		e.drive(ctx, r, sess, env, req.Text, req.Parts)
	})
}

// RetryFailedStep resumes the failed model step without submitting another user
// prompt. Persisted conversation and tool state are reused, while live turn-0
// instructions and system prompt inputs are re-resolved by the normal request builder.
// sess must carry durable failed-step retry intent prepared by the host.
func (e *Engine) RetryFailedStep(ctx context.Context, sess *session.Session, env tool.Environment) *Run {
	return e.startRun(ctx, sess, RunRequest{}, "", func(ctx context.Context, r *Run) {
		if !e.prepareRunEnvironment(ctx, r, sess, env) {
			return
		}
		e.emit(r, session.Event{Type: session.EvSessionInit})
		disposition, progress, pending := sess.FailedStepRetryPending()
		if !pending {
			e.terminate(ctx, r, sess, session.StopError, "", session.Usage{}, errors.New("agent: failed-step retry intent is not prepared"), false)
			return
		}
		retryText := "The failed model step is being retried without adding another user prompt."
		if progress == session.StreamProgressVisible {
			retryText = "The prior partial model output failed and is superseded; the failed model step is being retried."
		}
		e.emit(r, session.Event{Type: session.EvModelRetry, Text: retryText, ModelRetry: &session.ModelRetryPayload{
			Disposition: disposition,
			Progress:    progress,
		}})
		e.runLoop(ctx, r, sess, env, session.Usage{}, "", true)
	})
}

// ResumeApproval is the FOURTH, awaiting-ONLY run-entry seam (cloud-native Phase
// 2): it re-enters the loop AT a parked permission ask on a session that is in
// StateAwaiting (typically loaded fresh from a snapshot after the process that
// parked the ask died), applies verdict to the pending tool call, closes out any
// unanswered sibling calls on the same trailing assistant message, then continues
// the loop to completion. It mirrors Engine.Run's Run-construction preamble
// exactly (same events/asks/cancel/ctx/hardAbort/serial/diag/children/router
// discipline) but launches driveFromAwaiting instead of drive.
//
// It is DISTINCT from the three existing terminal-recovery seams (Reopen /
// Interrupt / Recover, all via resetToIdle at a turn boundary): those zero the
// Counters and clear the pending ask; this preserves both via the awaiting-only
// session.ResumeWith seam (the live loop's own resume path), so the re-entered run
// continues the SAME logical turn with its spend and limits intact. It adds NO new
// aggregate transition verb (ResumeWith already exists); the "fourth seam" is the
// run-entry orchestration here + Service.resumeFromAwaiting, not a domain change.
//
// PRECONDITION FAILURES surface ON THE RUN, not as a return value (ResumeApproval
// only ever returns a live *Run): if sess is not in StateAwaiting, or its pending ask
// does not match askID, or the trailing assistant message carries no matching tool
// call, driveFromAwaiting terminates the returned run with session.StopError carrying
// a cause matching errors.Is(_, ErrNotAwaiting). A consumer reads this off the run's
// terminal EvResult (Stop == StopError, the Error field), exactly like any other
// terminal — it never silently completes. The pending tool executes EXACTLY ONCE on
// the allow path and NOT AT ALL on a precondition failure or a deny.
func (e *Engine) ResumeApproval(ctx context.Context, sess *session.Session, env tool.Environment, askID string, verdict session.ApprovalVerdict) *Run {
	// The resumed run CONTINUES the run that parked awaiting this ask — it is not
	// a new one — so it carries that run's identity forward, read from the session
	// the host restored it onto (ADR 0249). This is what makes a cross-process
	// Approve after a restart the SAME run to every observer.
	//
	// The fallback is deliberately confined to THIS seam. A prompt entry must
	// never read the id off the session: a reused session still carries the id of
	// the run that just ended, and inheriting it would silently attribute a brand
	// new run's events to the previous one.
	return e.startRun(ctx, sess, RunRequest{RunID: sess.RunID()}, "", func(ctx context.Context, r *Run) {
		if !e.prepareRunEnvironment(ctx, r, sess, env) {
			return
		}
		e.driveFromAwaiting(ctx, r, sess, env, askID, verdict)
	})
}

// askDiscriminatorFor resolves the trailing askID component for a run, and
// reports whether a supplied value was REJECTED for containing a colon.
//
// Precedence: an explicit AskIDDiscriminator wins; otherwise RunID supplies it
// (ADR 0249 decision 2), which is what lets a durable host set ONE field and get
// both a stamped run identity and reconstructable askIDs — the arrangement ADR
// 0044 described as "a durable host passes its own RunID". The derivation is a
// DEFAULT, not a constraint: a caller needing a discriminator that is NOT the run
// id still sets the field directly.
//
// A colon is REJECTED rather than sanitised. The askID grammar is
// "<sessionID>:<n>:<callID>:<discriminator>", so a colon makes it ambiguous — and
// stripping one could collapse two distinct host ids onto a single askID,
// re-opening the CWE-863 replay collision the discriminator exists to close.
// Falling back to the process-global serial is the safe answer.
//
// It is a free function, not a method, precisely so the precedence is testable
// without launching a run goroutine.
func askDiscriminatorFor(req RunRequest, serial int64) (value string, colonRejected bool) {
	d := req.AskIDDiscriminator
	if d == "" {
		d = req.RunID
	}
	if d != "" && !strings.Contains(d, ":") {
		return d, false
	}
	return fmt.Sprintf("r%d", serial), d != ""
}

// startRun mints a Run with the full concurrency preamble (events buffer, ask
// registry, run-scoped diagnostics, interactive child-ask router, ask-review
// breaker, child-run registry) and launches body in the run goroutine under the
// LIFO seal/close discipline. It is the single Run-construction site shared by
// Engine.Run (→ drive) and ResumeApproval (→ driveFromAwaiting) so the two
// entry seams cannot drift in their concurrency setup.
func (e *Engine) startRun(ctx context.Context, sess *session.Session, req RunRequest, workspace string, body func(context.Context, *Run)) *Run {
	ctx, cancel := context.WithCancel(ctx)
	serial := runSerial.Add(1)
	ctx = port.WithRunAttemptContext(ctx, sess.ID, serial)
	ctx = withSessionOrigin(ctx, sess.ID)
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	if attribution.Writer == "" {
		attribution.Writer = tool.MemoryWriterModel
	}
	if attribution.Origin == "" {
		attribution.Origin = tool.MemoryOriginExplicit
	}
	if attribution.Origin != tool.MemoryOriginLearning {
		attribution.Source.SessionID = string(sess.ID)
	}
	ctx = tool.WithMemoryAttribution(ctx, attribution)
	r := &Run{
		events:    make(chan session.Event, 64),
		asks:      newAskRegistry(),
		cancel:    cancel,
		ctx:       ctx,
		workspace: workspace,
		req:       req,
		hardAbort: make(chan struct{}),
		serial:    serial,
		// Bind the run-scoped diagnostics ONCE here, where the live session is in
		// scope: correlate every emitted line to this session id, and (for a child
		// engine, Role != "") to its agent role too. The main engine has Role=="" so
		// only the "session" key is bound. With on NopDiagnostics returns Nop, so an
		// engine with no injected sink stays silent.
		diag: e.bindRunDiag(sess.ID),
	}
	// Resolve the trailing askID discriminator once (ADR-0044 / ADR-0249); see
	// askDiscriminatorFor for the precedence and the colon rule.
	r.runID = req.RunID
	resolved, colonRejected := askDiscriminatorFor(req, r.serial)
	if colonRejected {
		r.diag.Log(ctx, port.LevelWarn, "ask-id discriminator contains a colon; falling back to run serial", "session", string(sess.ID))
	}
	r.askDiscriminator = resolved
	// An interactive engine's run installs the child-ask router so a subagent's
	// surfaced ask can be routed back through this (parent) Run.Approve. A headless or
	// child engine leaves it nil (no human to surface to; a child never surfaces
	// further).
	if e.deps.Interactive {
		r.childAsks = newChildAskRouter()
	}
	// An engine carrying the OPT-IN child-ask reviewer arms this run's ask-review
	// breaker beside the router: the parentCaps closure consults it before every
	// reviewer call, so a run whose children keep proposing disallowed commands
	// stops spending reviewer turns after Deps.ChildAskReviewMaxDenies consecutive
	// non-allow outcomes. nil otherwise (the no-reviewer posture, unchanged).
	if e.deps.ChildAskReviewer != nil {
		r.askReview = &askReviewBreaker{max: e.deps.ChildAskReviewMaxDenies}
	}
	// An engine carrying the OPT-IN model router (ADR 0031) arms this run's router
	// breaker, mirroring the ask-review breaker: the parentCaps.routeTask closure
	// consults it before every classification, so a run whose Subagent tasks keep
	// failing to classify stops spending classifier turns after the threshold of
	// consecutive misses. nil otherwise (the no-router posture, byte-identical).
	if e.deps.SubagentModelRouter != nil {
		r.router = &modelRouterBreaker{max: defaultModelRouterMaxMisses}
	}
	// An engine with steer enabled (Deps.EnableSteer) arms this run's in-memory
	// steer inbox so an operator instruction enqueued mid-flight drains at the
	// next turn boundary (Step 2a). nil when disabled — the byte-identical
	// no-steer posture; the drain is then a strict no-op.
	if e.deps.EnableSteer {
		r.steer = newSteerInbox()
	}
	// The child-run registry is created UNCONDITIONALLY (cancel is interactive-only
	// but the registry's bookkeeping is not), with its emit bound to this run's
	// sequenced stream via emitOrAbort — a send that blocks until delivered (the
	// loop's own backpressure semantics, so a cancelled run's in-flight child
	// events still reach the draining consumer — e.g. the bracketing
	// parallel.branch events of a cancelled-before-start branch) and gives up ONLY
	// when the registry seals (run end / drain abandon) or the run's hardAbort
	// closes (Run.Cancel's explicit unwedge), so a child goroutine (or
	// the readControl goroutine inside CancelChild) is never parked past the run's
	// own teardown behind a wedged consumer. ALL child-originated emits route
	// through registry.safeEmit over this binding (A4c); a delivered event mirrors
	// to the sink like every loop emit.
	r.children = newChildRunRegistry()
	r.children.liveness = e.deps.SessionLiveness
	r.children.emit = func(ev session.Event) {
		if r.emitOrAbort(ev, r.children.emitAbort) && e.deps.Sink != nil {
			e.deps.Sink.Emit(r.ctx, ev)
		}
	}
	// unregisterAsk is the answered-vs-pending gate for ask retraction at a
	// child's registry terminal (markDoneResult — the chokepoint every
	// ctx-driven unwind funnels through) and the drain's abandoned sweep:
	// route() removes an ANSWERED ask's router entry, so only an unregister that
	// genuinely removed one (a still-pending surfaced ask) draws a
	// permission.retract. A headless/child run installs no router, so every
	// unregister reports false and no retract is ever emitted — correct:
	// nothing was ever surfaced.
	r.children.unregisterAsk = r.unregisterChildAsk
	go func() {
		// Defers run LIFO: cancel first (releases the run's ctx tree), then the
		// belt-and-braces seal — its abort-before-emitMu ordering closes emitAbort,
		// which is what unwinds any guarded send still parked on a full events
		// channel (Run.emitOrAbort selects on it; the run ctx is NOT a guarded
		// send's escape hatch), then sets sealed so every later emit is a safe
		// no-op — and only THEN close(r.events): seal-before-close is the
		// send-on-closed-channel panic guard. (drive's terminate paths normally
		// drain+seal already; this defer covers them idempotently.)
		defer close(r.events)
		defer r.children.seal()
		defer cancel()
		body(ctx, r)
	}()
	return r
}

// drive runs the loop algorithm for one prompt. It always terminates the session
// (Complete/Stop/Cancel/Fail) and emits exactly one terminal result Event. parts
// carries any non-text media riding alongside userText (nil for a text prompt).
func (e *Engine) drive(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, userText string, parts []session.Content) {
	// Step 0a: emit the run-open signal exactly once per run, before any other
	// event. Telemetry adapters (tracing/metrics) switch on session.init as the
	// signal to open a run span/counter; emitting it here makes that contract
	// honest rather than relying on their defensive fallback. It must precede the
	// SessionStart hook events and the first turn.start.
	e.emit(r, session.Event{Type: session.EvSessionInit})

	// Step 0b: fire SessionStart once before any work, before the prompt is even
	// recorded. This is a BLOCKING run-level gate (symmetric with
	// UserPromptSubmit): a Block outcome (or a hook execution error) aborts the
	// run before any prompt processing or model call.
	if sess.Counters.Turns == 0 {
		if blocked, reason := e.fireSessionStart(ctx, r, sess); blocked {
			e.terminate(ctx, r, sess, session.StopError, reason, session.Usage{}, fmt.Errorf("agent: session rejected by SessionStart hook: %s", reason), false)
			return
		}
	}

	// Step 1: record the user message and assemble project instructions once.
	// recordPrompt expands the prompt, fires the BLOCKING UserPromptSubmit phase
	// (applying any mutation to the effective prompt), and only then records the
	// final text into the aggregate. A Block (or hook error) ends the run before
	// any model call; ok=false signals that without recording anything.
	ok, reason, err := e.recordPrompt(ctx, r, sess, env, userText, parts)
	if err != nil {
		e.terminate(ctx, r, sess, session.StopError, "", session.Usage{}, err, false)
		return
	}
	if !ok {
		e.terminate(ctx, r, sess, session.StopError, reason, session.Usage{}, fmt.Errorf("agent: prompt rejected by UserPromptSubmit hook: %s", reason), false)
		return
	}

	// total is THIS run's per-run usage delta (the EvResult.Usage figure the team
	// supervisor sums per round). It starts at zero each Run. The MaxRunTokens budget
	// brake is evaluated against the AGGREGATE's cumulative Usage (sess.Usage) instead
	// — which RecordUsage below keeps in lock-step and which the snapshot persists —
	// so the budget survives reopen/restart while EvResult.Usage stays per-run.
	e.runLoop(ctx, r, sess, env, session.Usage{}, "", false)
}

// runLoop is the SHARED turn-loop body driven by both the prompt entry (drive,
// after recordPrompt) and the awaiting-approval re-entry (driveFromAwaiting,
// after it resolves the pending tool call + closes out siblings). Factoring it
// out keeps ONE loop, not two: the fourth (awaiting-only) run-entry seam reuses
// the exact same compaction / budget / no-progress / dispatch machinery as a
// fresh prompt, so the two paths cannot drift.
//
// total seeds the per-run usage delta (zero for a fresh prompt; the
// already-spent-this-re-entry delta for driveFromAwaiting, so the EvResult figure
// the team supervisor sums stays accurate). lastText seeds the last meaningful
// assistant text. The budget brake reads sess.Usage directly (persisted spend is
// honoured), independent of total. skipFirstBoundaryInjections is used only by
// failed-step retry reuses conversation state; live instruction sources are re-resolved.
// while every later iteration resumes the ordinary boundary drains.
func (e *Engine) runLoop(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, total session.Usage, lastText string, skipFirstBoundaryInjections bool) {
	// no-progress nudge accounting (Workstream A). noProgressNudges counts the
	// continuation messages injected this run; nudgeCap is the budget (defaulted in
	// NewEngine to defaultNoProgressNudges; a negative cap DISABLES nudging).
	var noProgressNudges int
	nudgeCap := e.deps.MaxNoProgressNudges
	// bgPendingNudged bounds the background-pending nudge (D10 as amended) to ONCE
	// per run, mirroring the noProgressNudges accounting; it is consumed only in
	// finishTurnNoTools' real-clean-end branch.
	var bgPendingNudged bool
	firstIteration := true

	for {
		// Plan-approval gate (issue #206): terminate immediately if a plan verdict
		// (approved OR iterate) is pending. This EARLY check catches the awaiting-
		// resume path (driveFromAwaiting → runLoop, where resolvePendingCall set
		// r.planApprovedTarget / r.planIterateRequested and the pending call's result
		// was already recorded) so the resumed run does NOT loop back to the model.
		// The live path hits the post-dispatch Step 6 check first and returns there,
		// so this is belt-and-suspenders there; for the resume path it is the
		// load-bearing gate. CLEAN terminal (completed path, Reopen-recoverable);
		// terminateComplete flips the mode at the boundary on Allow only (Deny stays
		// ModePlan).
		if e.planApprovalTerminal(ctx, r, sess, lastText, total) {
			return
		}

		// Step 2a: the turn-boundary injection drains (background-completion notice,
		// fire-result delivery, and the operator steer inbox) run BEFORE BeginTurn
		// and BEFORE the preTurnTerminal stop checks — the provider-legal seam where
		// history always ends on a user prompt / tool result / nudge, never inside a
		// tool_use pair. Recording before the stop checks is load-bearing: a message
		// drained at a boundary where the turn-limit / budget brake also trips is
		// STILL durable history (the run then terminates StopMaxTurns / StopBudget
		// normally; the recorded message is addressed by the next run). See
		// runBoundaryInjections.
		if shouldRunBoundaryInjections(firstIteration, skipFirstBoundaryInjections) {
			if err := e.runBoundaryInjections(ctx, r, sess); err != nil {
				e.terminate(ctx, r, sess, session.StopError, lastText, total, err, false)
				return
			}
		}
		firstIteration = false

		// Step 2: stop conditions BEFORE the model call (limit / cancellation / token
		// budget). preTurnTerminal owns the precedence and the matching terminate call;
		// it returns true once the run has ended so this loop stays flat.
		if e.preTurnTerminal(ctx, r, sess, lastText, total) {
			return
		}

		if err := sess.BeginTurn(); err != nil {
			e.terminate(ctx, r, sess, session.StopError, lastText, total, err, false)
			return
		}
		turnIdx := sess.Counters.Turns - 1
		port.SetAttemptTurnIndex(ctx, turnIdx)
		e.emit(r, session.Event{Type: session.EvTurnStart, Turn: turnIdx})

		// Snapshot the turn start for the turn.end elapsed measurement. When no
		// Clock is injected the duration is reported as 0 (guarded like dispatch).
		var turnStart time.Time
		if e.deps.Clock != nil {
			turnStart = e.deps.Clock.Now()
		}

		// Step 3: assemble the complete provider request once, then compact against
		// exactly that model-visible shape. On a successful replacement maybeCompact
		// updates only the request's persisted-history suffix.
		req := e.buildRequest(ctx, r, sess, env)
		e.maybeCompact(ctx, r, sess, turnIdx, &req)

		// Step 4: when durable evidence is configured, emit the content-safe
		// structural manifest immediately before the provider receives this exact
		// final request. The helper keeps the gate outside requestManifest: the
		// disabled/default path must not pay for hashing, counting, maps, or slices.
		e.emitRequestManifest(r, sess, env, req, turnIdx)
		asst, usage, streamStop, timing, err := e.runTurn(ctx, r, req, turnIdx)
		// Provider-reported usage is spend, not semantic visibility. Record it even
		// when the stream fails so retries, cumulative budgets, and EvResult remain
		// honest without retaining failed-attempt content.
		total = total.Add(usage)
		_ = sess.RecordUsage(usage)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				e.terminate(ctx, r, sess, session.StopCancelled, lastText, total, err, false)
				return
			}
			e.terminate(ctx, r, sess, session.StopError, lastText, total, err, permanentCause(err))
			return
		}
		// Zero-input-usage fallback (issue #82) — DISPLAY-ONLY. A turn that produced
		// no ChunkUsage (e.g. an adapter that swallowed a stalled stream, or a
		// provider that omitted the usage frame) leaves usage.InputTokens == 0, which
		// would zero the context meter. We estimate the prompt size from the
		// conversation via the TokenCounter (always non-nil — defaulted to
		// HeuristicTokenCounter in NewEngine) into a LOCAL emitUsage that feeds ONLY
		// the EvTurnEnd payload (the footer meter + the per-turn latency telemetry).
		// The budget brakes (MaxRunTokens / MaxTeamTokens) and the cumulative
		// sess.Usage / EvResult.Usage figure stay on the provider's ACTUAL usage
		// below, so an estimate never moves a token-budget decision — provider truth
		// only. Output tokens are NOT fabricated. estimated records whether the
		// fallback fired so the footer can show a "~" hint. Guarded on a non-empty
		// conversation so a genuinely empty session never gets a phantom estimate.
		emitUsage := usage
		estimated := false
		if est, ok := estimateZeroUsageInput(usage.InputTokens, sess.Conversation.Messages, e.deps.TokenCounter); ok {
			emitUsage.InputTokens = est
			estimated = est > 0 // a zero estimate is no estimate worth flagging
		}
		// Cumulative and per-run usage were recorded immediately after runTurn so the
		// same accounting applies to both successful and failed streams.
		if asst.Text != "" {
			lastText = asst.Text
		}

		// Close the turn's model exchange with its own usage and elapsed time in a
		// typed TurnEndPayload. Emitted only on the success path (never on the
		// error/cancel returns above), before the assistant message is recorded.
		// emitUsage is THIS turn's accounting for DISPLAY (the provider figure, or
		// the zero-usage estimate above when the provider reported none); Event.Usage
		// is left unset so it keeps its single cumulative-on-result meaning.
		var durMs int64
		if e.deps.Clock != nil {
			durMs = e.deps.Clock.Now().Sub(turnStart).Milliseconds()
		}
		e.emit(r, session.Event{Type: session.EvTurnEnd, Turn: turnIdx,
			TurnEnd: &session.TurnEndPayload{
				Usage:            emitUsage,
				Estimated:        estimated,
				DurationMs:       durMs,
				TTFTMs:           timing.ttftMs,
				InterTokenMeanMs: timing.interTokenMeanMs,
				InterTokenMaxMs:  timing.interTokenMaxMs,
			}})

		if err := sess.RecordAssistant(asst); err != nil {
			e.terminate(ctx, r, sess, session.StopError, lastText, total, err, false)
			return
		}

		// Step 5: a tool-call-free turn is either a real answer, a bounded no-progress
		// nudge, or a clean give-up — finishTurnNoTools owns that classification (and
		// the terminate/nudge side effects) so this loop stays flat.
		if len(asst.ToolCalls) == 0 {
			if e.finishTurnNoTools(ctx, r, sess, asst, streamStop, turnIdx, lastText, total, &noProgressNudges, nudgeCap, &bgPendingNudged) {
				return
			}
			continue
		}

		// Step 6: dispatch the tool calls, then loop back to step 2.
		results, cancelled := e.dispatch(ctx, r, sess, env, turnIdx, asst.ToolCalls)
		if cancelled {
			e.terminate(ctx, r, sess, session.StopCancelled, lastText, total, nil, false)
			return
		}
		if err := sess.RecordToolResults(results); err != nil {
			e.terminate(ctx, r, sess, session.StopError, lastText, total, err, false)
			return
		}
		e.save(ctx, r, sess)

		// Plan-approval gate (issue #206): if a plan verdict (approved OR iterate)
		// is pending this turn, terminate instead of looping back to the model. On
		// Allow the run ends with StopPlanApproved (terminateComplete flips the mode
		// at the boundary); on Deny (iterate) the run ends with StopPlanIterate so
		// the operator's next typed prompt drives the revision (the session stays
		// ModePlan — no mode flip). CLEAN terminals (completed path,
		// Reopen-recoverable), parallel to StopBudget/StopNoProgress.
		if e.planApprovalTerminal(ctx, r, sess, lastText, total) {
			return
		}
	}
}

func shouldRunBoundaryInjections(firstIteration, skipFirst bool) bool {
	return !firstIteration || !skipFirst
}

// runBoundaryInjections runs the Step 2a turn-boundary injection drains, in
// order, BEFORE BeginTurn and the preTurnTerminal stop checks: the
// background-completion notice, the fire-result delivery drain (ADR 0075), and
// the operator steer drain (steer-while-running, issue #512). Extracting them
// keeps runLoop's complexity flat; the ordering and the record-before-stop-checks
// discipline are unchanged from when they were inline. Each is provider-legal at
// a turn boundary (history ends on a user prompt / tool result / nudge, never
// inside a tool_use pair) and a no-op when its source is empty/disabled. Any
// drain's error is returned for the caller to terminate StopError.
func (e *Engine) runBoundaryInjections(ctx context.Context, r *Run, sess *session.Session) error {
	// Background-completion NOTICE injection (A2 — notice-only): newly-finished
	// background children are announced in ONE harness-framed user message (ids +
	// stop labels only).
	if err := e.injectBackgroundNotice(ctx, r, sess); err != nil {
		return err
	}
	// Fire-result delivery drain (ADR 0075 decision #3): pending notes queued for
	// THIS session are recorded as ordinary harness-framed user continuations and
	// marked delivered. nil DeliveryQueue is a no-op.
	if err := e.drainPendingDelivery(ctx, r, sess); err != nil {
		return err
	}
	// Operator steer drain (steer-while-running, issue #512): a pending steer off
	// the run's in-memory inbox is recorded as an ordinary user continuation. A
	// nil inbox (EnableSteer off) or empty slot is a strict no-op.
	return e.drainPendingSteer(ctx, r, sess)
}

// finishTurnNoTools classifies and acts on a completed turn that produced NO tool
// calls. The empty assistant message has ALREADY been recorded by the caller, so its
// reasoning blob is on history for replay across a nudge (decision D-4). It returns
// done=true when the run has terminated (a real answer, a provider terminal
// condition, or a no-progress give-up) — the caller returns; done=false means a
// continuation nudge was injected and the caller loops back to Step 2.
// noProgressNudges is mutated through the pointer; nudgeCap < 0 disables nudging.
//
// The continuation nudge is GRADUATED by attempt (selected on the PRE-INCREMENT counter):
// the early attempt(s) get the gentle noProgressNudgeText, and the FINAL attempt before
// give-up (where *noProgressNudges == nudgeCap-1 at entry to the nudge branch) gets the
// forceful noProgressExtractiveNudgeText, which tells the model to stop investigating and
// emit its best-effort final answer NOW. The give-up branch and the increment both read
// the pre-increment value, so the extractive nudge is structurally guaranteed one more
// model turn before give-up (a model that answers on that turn ends StopEndTurn, not
// StopNoProgress). With nudgeCap==1 the single nudge IS the final one → extractive only,
// no gentle attempt.
//
// streamStop discipline (the masking guard): the no-progress nudge applies ONLY when
// the turn ended on a BENIGN end — StopEndTurn or StopNone (the model simply finished
// its turn). Both adapters' mapStop relay a REAL terminal condition (max_tokens /
// refusal / incomplete / failed → StopError; cancelled → StopCancelled; and any limit
// reason) on the ChunkDone stop, NOT as a Go error. Such a turn can ALSO come back
// empty (a truncated/refused response), and nudging "please continue" + relabeling it
// StopNoProgress would MASK the real reason — the exact silent-mislabel we are fixing.
// So a non-benign streamStop is surfaced verbatim (StopError → terminate as a failure;
// any other non-benign stop → terminateComplete carrying that reason), never nudged.
//
// Background-pending nudge (D10 as amended, I3b): a REAL clean end — meaningful
// text on a benign stop, the StopEndTurn terminal — while BACKGROUND children are
// still live is deferred ONCE per run (bgPendingNudged): a harness-framed user
// message (ids only — A9) tells the model to collect/wait/cancel or finish, and
// the loop re-drives one more turn. It MUST live here, before the clean-terminal
// call sites — by the time terminate/terminateComplete run, drainChildren at
// their top has already cancelled the children and sealed the registry (the I3a
// placement note on drainChildren). Deliberate decisions, pinned by tests:
//   - it fires ONLY on this real-clean-end branch: the no-progress machinery owns
//     the empty turn first (an empty turn is nudged/given-up by ITS counter; even
//     the StopNoProgress give-up below does not background-nudge), and every
//     non-clean terminal (error/cancel/limits/budget) skips it entirely — the
//     eventual stop reason is never relabelled.
//   - it is EVENT-SILENT: ordinary recorded history like the notice (no
//     EvNoProgress — that event means "the model stalled", a different taxonomy;
//     clients see the continuation turn's activity instead).
//   - foreground children cannot be live at a turn boundary (their tool calls are
//     synchronous inside dispatch), so liveBackgroundIDs' background-only filter
//     is exact, not an approximation.
//   - on the SECOND would-be clean end (or if the model finishes after collecting)
//     the guard is spent and the normal terminate path runs — drainChildren
//     cancels + persists whatever is still live. A finished-but-never-collected
//     child simply keeps its uncollected result (the entry dies with the run; the
//     persisted child stays inspectable/resumable) — acceptable by design.
//   - the nudged continuation re-enters Step 2 (notice injection + BeginTurn), so
//     Limits.MaxTurns still bounds it exactly like a no-progress nudge.
//
// Steer clean-exit defer: a PARKED steer BLOCKS the clean exit — the run does
// NOT terminateComplete while the inbox holds an un-drained steer. Instead the
// loop re-enters Step 2 with no recorded continuation, so the very next Step 2a
// drains the steer (recorded + echoed there) and the turn it feeds consumes it.
// UNLIKE the background-pending nudge this is not once-only and injects NO
// harness nudge text: the run extends until the steer is consumed, and the
// steer itself is the next turn's input. It lives on the same real-clean-end
// branch as the background-pending check (BEFORE the clean-terminal call sites)
// and runs FIRST — a parked steer always owns the defer (it is the never-drop
// contract, not an optional collect). Bounded by
// Limits.MaxTurns like every loop continuation (the steered turn goes through
// BeginTurn).
func (e *Engine) finishTurnNoTools(ctx context.Context, r *Run, sess *session.Session, asst session.Message, streamStop session.StopReason, turnIdx int, lastText string, total session.Usage, noProgressNudges *int, nudgeCap int, bgPendingNudged *bool) (done bool) {
	// A turn with meaningful text is a real answer: end the run, honouring streamStop.
	if strings.TrimSpace(asst.Text) != "" {
		stop := session.StopEndTurn
		if streamStop != session.StopNone {
			stop = streamStop
		}
		// A parked steer defers the clean exit until it drains at the next Step
		// 2a (the never-drop contract, engine-internal).
		if stop == session.StopEndTurn && r.hasSteer() {
			return false
		}
		if stop == session.StopEndTurn && !*bgPendingNudged && r.children != nil {
			if ids := r.children.liveBackgroundIDsMatching(nil); len(ids) > 0 {
				*bgPendingNudged = true
				if err := e.recordContinuation(r, sess, turnIdx, backgroundPendingNudgeText(ids)); err != nil {
					e.terminate(ctx, r, sess, session.StopError, lastText, total, err, false)
					return true
				}
				e.save(ctx, r, sess)
				return false
			}
		}
		e.terminateComplete(ctx, r, sess, stop, lastText, total,
			stopTerminalCause(stop, lastText))
		return true
	}

	// A non-benign streamStop is a REAL terminal condition the provider reported on
	// the stop chunk (truncation / refusal / incomplete / failed / cancelled / a
	// limit). It is NOT a no-progress stall: surface it, never nudge or relabel it.
	if streamStop != session.StopNone && streamStop != session.StopEndTurn {
		if streamStop == session.StopError {
			e.terminate(ctx, r, sess, session.StopError, lastText, total,
				fmt.Errorf("agent: model ended turn with no output and a terminal stop reason %q", streamStop), false)
			return true
		}
		e.terminateComplete(ctx, r, sess, streamStop, lastText, total,
			stopTerminalCause(streamStop, lastText))
		return true
	}

	// NO PROGRESS: benign end, no tool calls AND no meaningful text (a reasoning-only
	// / empty turn). When nudging is disabled (nudgeCap < 0) or the budget is
	// exhausted, terminate CLEARLY via the completed path with StopNoProgress — never
	// silently as StopEndTurn, and never an unbounded loop. StopNoProgress is a
	// non-error terminal, so the session ends COMPLETED and stays Reopen-recoverable.
	if nudgeCap < 0 || *noProgressNudges >= nudgeCap {
		e.emit(r, session.Event{Type: session.EvNoProgress, Turn: turnIdx,
			Text: "no progress after continuation attempts; ending run"})
		e.terminateComplete(ctx, r, sess, session.StopNoProgress, lastText, total, "")
		return true
	}

	// Inject ONE bounded continuation nudge as a NEW user message, emit a visible
	// (advisory, non-recorded) EvNoProgress event, then signal the caller to loop back
	// to Step 2 (stop conditions, BeginTurn — which independently bounds the loop by
	// Limits.MaxTurns and re-checks cancellation). The empty assistant turn (with its
	// reasoning blob) precedes the nudge, so the next turn replays the model's own
	// reasoning.
	//
	// GRADUATED selection on the PRE-INCREMENT counter: the final nudge before give-up
	// (*noProgressNudges == nudgeCap-1, only reachable here since the give-up branch
	// already short-circuited nudgeCap<0 and *noProgressNudges>=nudgeCap) gets the
	// forceful extractive nudge; earlier attempts get the gentle one. The increment MUST
	// stay AFTER this predicate.
	final := *noProgressNudges == nudgeCap-1
	nudgeText := noProgressNudgeText
	advisory := "model produced no tool call or text; nudging to continue"
	if final {
		nudgeText = noProgressExtractiveNudgeText
		advisory = "model still not progressing; final attempt: requesting a best-effort answer"
	}
	*noProgressNudges++
	e.emit(r, session.Event{Type: session.EvNoProgress, Turn: turnIdx, Text: advisory})
	if err := e.recordContinuation(r, sess, turnIdx, nudgeText); err != nil {
		e.terminate(ctx, r, sess, session.StopError, lastText, total, err, false)
		return true
	}
	e.save(ctx, r, sess)
	return false
}

// injectBackgroundNotice is drive's Step 2a: announce newly-finished background
// children to the model as ONE harness-framed user message (backgroundNoticeText
// — ids + stop labels only, A2) and persist immediately. noticeFinishedBackground
// owns the exactly-once bookkeeping (every candidate is flipped to noticed before
// the record; a record failure ends the run as StopError anyway, so a "noticed
// but unrecorded" entry cannot leak into a live continuation). e.save runs right
// after the record so the notice is durable in the replayed history across
// resume/reload, independent of how the run later ends. A nil-or-empty scan is
// the common case and costs one mutex'd map walk.
func (e *Engine) injectBackgroundNotice(ctx context.Context, r *Run, sess *session.Session) error {
	if r.children == nil {
		return nil
	}
	finished := r.children.noticeFinishedBackground()
	if len(finished) == 0 {
		return nil
	}
	// The notice is recorded at a turn boundary, before the upcoming BeginTurn, so it
	// belongs to the turn about to start (Counters.Turns is the 0-based index of it).
	// recordContinuation records it AND emits the log-only EvUserPrompt so the durable
	// log/fold captures this harness-authored user message like every other.
	if err := e.recordContinuation(r, sess, sess.Counters.Turns, backgroundNoticeText(finished)); err != nil {
		return fmt.Errorf("agent: record background completion notice: %w", err)
	}
	e.save(ctx, r, sess)
	return nil
}

// drainPendingDelivery is drive's Step 2a delivery-drain sibling (ADR 0075
// decision #3): it reads the origin session's pending fire-result delivery
// notes off the DURABLE per-session DeliveryQueue, records each as an ordinary
// harness-framed user continuation (recordContinuation — provider-legal at a
// turn boundary, never inside a tool_use pair), and marks it delivered via
// MarkDelivered (the session-scoped exactly-once ledger). It is the SOLE loop
// recording site for queued notes: the fire path enqueues for a BUSY or AWAITING
// origin (it cannot drive a delivery run without colliding); the loop drains them
// here at the origin's next turn boundary, exactly-once.
//
// The drain is the SAME seam injectBackgroundNotice uses (Step 2a, BEFORE
// BeginTurn), so history always ends on a user prompt / tool result / nudge when
// the notes are recorded — the notes are provider-legal and compaction-safe. It
// is registered on the MAIN + per-session engines ONLY (composition leaves
// DeliveryQueue nil on child engines — a child origin degrades to pull-only with
// a WARN at the fire path). A nil DeliveryQueue (the default, or a child engine)
// is a no-op. An empty pending set is the common case and costs one queue read.
//
// Each note is ALREADY rendered (fenced-untrusted, clamped) by the time it
// arrives here; the drain records it verbatim and does not re-render. A note is
// marked delivered ONLY AFTER its record succeeds (recordContinuation is the
// last error-returning step), so a record fault does not strand a "delivered but
// unrecorded" note — the note stays pending and the run ends StopError (the
// operator sees the fault, the note drains on the next run-entry). The
// exactly-once ledger (MarkDelivered is idempotent) keeps a re-drain after a
// restart from re-recording.
func (e *Engine) drainPendingDelivery(ctx context.Context, r *Run, sess *session.Session) error {
	q := e.deps.DeliveryQueue
	if q == nil {
		return nil
	}
	notes, err := q.Pending(ctx, sess.ID)
	if err != nil {
		// A queue read fault must not fail the run silently. WARN (best-effort,
		// never the fire's failure) and end the run StopError so the operator sees
		// a broken queue rather than a silent note loss. The fire itself is
		// unaffected (delivery is decoupled).
		r.diag.Log(ctx, port.LevelWarn, "agent: delivery queue read failed",
			"session", string(sess.ID), "err", err.Error())
		return fmt.Errorf("agent: read pending delivery: %w", err)
	}
	for _, n := range notes {
		// The note is recorded at a turn boundary, before the upcoming BeginTurn, so
		// it belongs to the turn about to start. recordContinuation records it AND
		// emits the log-only EvUserPrompt so the durable log/fold captures this
		// harness-authored user message like every other.
		if err := e.recordContinuation(r, sess, sess.Counters.Turns, n.Text); err != nil {
			r.diag.Log(ctx, port.LevelWarn, "agent: delivery note record failed",
				"session", string(sess.ID), "seq", n.Seq, "err", err.Error())
			return fmt.Errorf("agent: record delivery note: %w", err)
		}
		// Mark delivered AFTER the record succeeds — the exactly-once ledger. A
		// MarkDelivered fault is best-effort (the note IS recorded; a re-drain after
		// a restart would re-record it, but MarkDelivered is idempotent and the
		// ledger is the same, so the re-drain's MarkDelivered is a no-op success and
		// the re-record is the only cost — a bounded, rare double-record, not a
		// loss). WARN so the operator sees a ledger fault; do not end the run.
		if err := q.MarkDelivered(ctx, sess.ID, n.Seq); err != nil {
			r.diag.Log(ctx, port.LevelWarn, "agent: delivery mark-delivered failed (note recorded; ledger may re-drain)",
				"session", string(sess.ID), "seq", n.Seq, "err", err.Error())
		}
	}
	if len(notes) > 0 {
		e.save(ctx, r, sess)
	}
	return nil
}

// effectiveMaxRunTokens folds the engine's Deps.MaxRunTokens with the run's optional
// per-call override (RunRequest.MaxRunTokensOverride), TIGHTEN-ONLY: when both are
// positive the lower wins (a per-call ceiling can make the run stricter, never looser);
// when only one is positive that one applies; 0+0 means no budget. It is the single
// fold both the budget check and any future budget-reading site must use.
func (e *Engine) effectiveMaxRunTokens(r *Run) int {
	base := e.deps.MaxRunTokens
	over := r.req.MaxRunTokensOverride
	switch {
	case base > 0 && over > 0:
		if over < base {
			return over
		}
		return base
	case over > 0:
		return over
	default:
		return base
	}
}

// budgetExhausted reports whether the session's CUMULATIVE usage has crossed the
// effective loop-level token ceiling (Deps.MaxRunTokens folded with the run's
// tighten-only RunRequest override). A non-positive effective ceiling (the default)
// disables the budget and always returns false.
//
// It reads the AGGREGATE's cumulative Usage (the value RecordUsage accumulates and
// the snapshot persists), NOT the per-run delta, so the budget brake bounds the
// logical run across reopen/restart — a reloaded session resumes with its prior
// spend already counted. The per-run delta stays the EvResult.Usage figure.
func (e *Engine) budgetExhausted(r *Run, cumulative session.Usage) bool {
	ceiling := e.effectiveMaxRunTokens(r)
	return ceiling > 0 && cumulative.TotalTokens() >= ceiling
}

// lookupTool resolves a tool by name for THIS run: the run-scoped ExtraTools overlay
// is consulted FIRST (so a structured-output SubmitResult, or any per-run tool, wins
// over a same-named catalog tool for this run only), then the shared catalog. It is
// the single resolution point dispatch uses so the overlay and the advertised specs
// (buildRequest) never disagree.
func (e *Engine) lookupTool(r *Run, name string) (tool.Tool, bool) {
	for _, t := range r.req.ExtraTools {
		if t.Spec().Name == name {
			return t, true
		}
	}
	if e.deps.Catalog == nil {
		return nil, false
	}
	return e.deps.Catalog.Lookup(name)
}

// preTurnTerminal runs the turn-BOUNDARY stop checks before a model call, in
// precedence order, and terminates the run on the first that fires. It returns true
// once the run has ended (the caller returns). Precedence:
//  1. an already-recorded/limit stop reason (sess.StopReason) → terminate verbatim;
//  2. ctx cancellation → StopCancelled;
//  3. the loop-level token budget (StopBudget) — a CLEAN terminal (completed path,
//     Reopen-recoverable, like StopNoProgress). It is LAST so a tripped limit /
//     cancellation still wins. The boundary check means an in-flight turn always
//     completes (no mid-stream abort → no-replay-after-first-chunk holds).
func (e *Engine) preTurnTerminal(ctx context.Context, r *Run, sess *session.Session, lastText string, total session.Usage) bool {
	if reason, stopped := sess.StopReason(); stopped {
		if _, _, pending := sess.FailedStepRetryPending(); pending && sess.State == session.StateIdle {
			e.deferFailedStepRetry(ctx, r, sess, reason, lastText, total)
		} else {
			e.terminate(ctx, r, sess, reason, lastText, total, nil, false)
		}
		return true
	}
	if ctx.Err() != nil {
		// Cancellation explicitly abandons retry intent through Session.Cancel.
		e.terminate(ctx, r, sess, session.StopCancelled, lastText, total, nil, false)
		return true
	}
	if e.budgetExhausted(r, sess.Usage) {
		if _, _, pending := sess.FailedStepRetryPending(); pending && sess.State == session.StateIdle {
			e.deferFailedStepRetry(ctx, r, sess, session.StopBudget, lastText, total)
		} else {
			e.terminateComplete(ctx, r, sess, session.StopBudget, lastText, total, "")
		}
		return true
	}
	return false
}

// recordPrompt produces the effective user prompt and records it (with, on the
// first turn, the assembled project instructions via Deps.Instructions; default
// RootAssembler reads AGENTS.md / CLAUDE.md at the workspace root) through the
// session root so all history mutation flows through the aggregate.
//
// Ordering is load-bearing:
//  1. Deps.CommandExpander expands a slash-command invocation into its template
//     body; the EXPANDED text is the candidate prompt. The default NoopExpander
//     leaves the text unchanged (v1 behaviour). Expansion is best-effort: a read
//     fault is treated as unchanged rather than aborting the run.
//  2. The BLOCKING UserPromptSubmit hook fires on that expanded text, BEFORE the
//     prompt is recorded. A Block (or hook error) returns ok=false with a reason
//     and records nothing — the caller ends the run before any model call. A
//     non-empty Mutated payload REPLACES the effective prompt text.
//  3. The final (possibly mutated) text is recorded via RecordUserPrompt, so it
//     is exactly what the model receives.
//
// ok=false means the prompt was rejected (reason is set); a non-nil error means a
// recording/assembly fault. Both paths leave the run to terminate.
// parts (non-text media) ride alongside the text untouched: command expansion
// and the UserPromptSubmit hook see and may rewrite only the TEXT; the media is
// neither expanded nor mutated by a hook and is recorded verbatim on the user
// message via RecordUserPromptWithParts.
func (e *Engine) recordPrompt(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, userText string, parts []session.Content) (ok bool, reason string, err error) {
	if expanded, exp, eerr := e.deps.CommandExpander.Expand(ctx, env.Workspace(), userText); eerr == nil && exp {
		userText = expanded
	}
	// UserPromptSubmit fires on the expanded text before recording so a mutation
	// can rewrite the effective prompt and a block can reject it pre-record.
	finalText, blocked, reason := e.fireUserPromptSubmit(ctx, r, sess, userText)
	if blocked {
		return false, reason, nil
	}
	// The turn-0 context fragments (project instructions / soul / memory index /
	// user model) are NO LONGER persisted into the conversation (ADR 0043). They are
	// assembled once per run and prepended to LLMRequest.Messages EPHEMERALLY in
	// buildRequest, so they are present on every run (incl. resume) without bloating
	// the persisted history or recreating the compaction-pin ambiguity. Only the
	// GENUINE prompt (+ media parts) is recorded here; instr is nil (the param stays a
	// valid seam for callers that DO want to persist instructions, e.g. tests).
	if rerr := sess.RecordUserPromptWithParts(finalText, parts, nil); rerr != nil {
		return false, "", fmt.Errorf("agent: record user prompt: %w", rerr)
	}
	if messages := sess.Conversation.Messages; len(messages) > 0 {
		owned := learning.NewTrajectory(sess.ID, env.Workspace().Root(), session.StopNone, session.Usage{}, messages[len(messages)-1:])
		r.currentPrompt = &owned.Messages[0]
	}
	// Seed the session Title ONCE from this genuine prompt (set-once guard in
	// SetTitle: only the first non-empty prompt sticks). The loop calls SetTitle
	// ONLY here at recordPrompt (the genuine site), never at recordContinuation
	// (the synthetic nudge/notice site), so a no-progress nudge or a background
	// notice never seeds or overwrites the title. A multimodal-only prompt
	// (finalText=="" with parts) leaves Title=="" — the lazy fallback applies.
	sess.SetTitle(finalText)
	// Emit the durable, log-only EvUserPrompt so the EventLog records WHAT THE USER
	// ASKED (the relay never re-emits the prompt to the client). Turn 0 — the genuine
	// prompt opens the run. parts ride verbatim so a fold rebuilds a multimodal prompt.
	e.emitUserPrompt(r, 0, finalText, parts)
	return true, "", nil
}

// emitUserPrompt emits the log-only EvUserPrompt event carrying a just-recorded
// user-role message (Text + Parts). It is the SINGLE emit site for the event, called
// at every place the loop records a user message — the genuine prompt and the
// harness-authored synthetic continuations (no-progress nudge, background-pending
// nudge, background-completion notice) — so the durable log (and an event-sourced
// fold) sees a COMPLETE user-turn sequence. The event is log-only: the relay appends
// it and skips it on the live client wire (the client already holds the prompt).
func (e *Engine) emitUserPrompt(r *Run, turnIdx int, text string, parts []session.Content) {
	e.emit(r, session.Event{Type: session.EvUserPrompt, Turn: turnIdx,
		UserPrompt: &session.UserPromptPayload{Text: text, Parts: parts}})
}

// recordContinuation records a harness-authored synthetic user-role continuation
// (a nudge or a background notice — text-only, no media, no project instructions)
// through the aggregate AND emits the matching log-only EvUserPrompt, so no
// user-message site bypasses the durable record. It mirrors recordPrompt's
// record-then-emit shape for the non-genuine sites.
func (e *Engine) recordContinuation(r *Run, sess *session.Session, turnIdx int, text string) error {
	if err := sess.RecordUserPrompt(text, nil); err != nil {
		return err
	}
	e.emitUserPrompt(r, turnIdx, text, nil)
	return nil
}

// turnTiming carries the latency measurements runTurn derives from the model
// stream, in milliseconds. A field is 0 when it could not be measured (no Clock
// injected, no observable output for TTFT, or fewer than two streaming content
// deltas for the inter-token summary) — telemetry treats a 0 here as "not
// measured", never a real observation.
type turnTiming struct {
	ttftMs           int64
	interTokenMeanMs int64
	interTokenMaxMs  int64
}

// turnLatency accumulates the TTFT anchor and the inter-token gap series for a
// single streamed turn off an injected port.Clock. It SPLITS the two concerns the
// stream switch used to conflate (issue #155): TTFT anchors on the FIRST
// OBSERVABLE OUTPUT — text, reasoning, a reasoning replay item, or a tool call
// (noteFirstOutput) — while the inter-token gap series counts ONLY STREAMING
// content deltas (text or reasoning, noteStreamDelta), so a tool call or reasoning
// replay blob anchors TTFT without polluting the gap series. With no Clock injected
// (clock == nil) every measurement degrades to 0.
type turnLatency struct {
	clock              port.Clock
	streamStart        time.Time
	lastContent        time.Time
	firstOutputSeen    bool
	contentChunks      int
	gapSumMs, gapMaxMs int64
	timing             turnTiming
}

// newTurnLatency anchors the stream start (one Clock read when a Clock is present)
// and returns the accumulator for one turn.
func newTurnLatency(clock port.Clock) *turnLatency {
	l := &turnLatency{clock: clock}
	if clock != nil {
		l.streamStart = clock.Now()
	}
	return l
}

// noteFirstOutputAt anchors TTFT on the first observable output at the supplied
// instant. First-only idempotent (firstOutputSeen guards it), so the
// reasoning-delta-THEN-tool-call ordering anchors on the reasoning delta and the
// later tool call is a no-op.
func (l *turnLatency) noteFirstOutputAt(now time.Time) {
	if l.firstOutputSeen {
		return
	}
	l.firstOutputSeen = true
	l.timing.ttftMs = now.Sub(l.streamStart).Milliseconds()
}

// noteFirstOutput anchors TTFT on the first observable output (a single Clock
// read). It is the entry point for the non-streaming observable-output branches
// (reasoning replay item, tool call); the streaming branches go through
// noteStreamDelta, which anchors TTFT off the same read it uses for the gap.
func (l *turnLatency) noteFirstOutput() {
	if l.clock == nil || l.firstOutputSeen {
		return
	}
	l.noteFirstOutputAt(l.clock.Now())
}

// noteStreamDelta records one STREAMING content delta (text or reasoning) for the
// inter-token gap measurement, and also anchors TTFT — both off a SINGLE Clock
// read, so the read count per delta is identical to the old single-purpose
// accounting. Call it exactly once per streaming content delta. The gap series
// counts ONLY these deltas — never tool calls or reasoning replay blobs.
func (l *turnLatency) noteStreamDelta() {
	if l.clock == nil {
		return
	}
	now := l.clock.Now()
	l.noteFirstOutputAt(now)
	l.contentChunks++
	if l.contentChunks >= 2 {
		gap := now.Sub(l.lastContent).Milliseconds()
		l.gapSumMs += gap
		if gap > l.gapMaxMs {
			l.gapMaxMs = gap
		}
	}
	l.lastContent = now
}

// summary finalises the inter-token gap summary: mean over the (contentChunks-1)
// gaps and the largest single gap. With fewer than two streaming content deltas
// there is no gap, so both stay 0 — never a bogus zero observation. The TTFT was
// already anchored in place.
func (l *turnLatency) summary() turnTiming {
	if l.contentChunks >= 2 {
		l.timing.interTokenMeanMs = l.gapSumMs / int64(l.contentChunks-1)
		l.timing.interTokenMaxMs = l.gapMaxMs
	}
	return l.timing
}

// runTurn sends an already-assembled LLMRequest and folds the model stream into
// a single assistant Message. It emits message.delta events for text. It honours
// ctx cancellation mid-stream by returning context.Canceled.
//
// It also measures, via the injected Clock (never time.Now directly, so tests
// drive it with a fake clock): TTFT — the elapsed time from the start of the
// model stream to the FIRST OBSERVABLE OUTPUT (text, reasoning, a reasoning
// replay item, or a tool call) — and the inter-token gaps between consecutive
// STREAMING content deltas (text or reasoning), summarised as mean and max. TTFT
// anchors on whatever observable output arrives first (see noteFirstOutput);
// usage/done/phase chunks carry no output and never anchor it. The inter-token
// gaps, by contrast, count ONLY streaming content deltas (see noteStreamDelta) —
// a tool call or reasoning replay blob is observable output (so it anchors TTFT)
// but is NOT a streamed token, so it never pollutes the gap series. A turn with
// no observable output at all reports no TTFT; a turn with fewer than two
// streaming content deltas reports no inter-token summary (there is no gap).
func (e *Engine) runTurn(ctx context.Context, r *Run, req port.LLMRequest, turnIdx int) (session.Message, session.Usage, session.StopReason, turnTiming, error) {
	if e.deps.EnableDurableEvidence {
		ctx = port.WithAttemptObserver(ctx, func(observation session.NetworkAttemptPayload) {
			id, ok := port.SessionIDFromContext(r.ctx)
			if !ok {
				return
			}
			canonical, ok := session.CanonicalNetworkAttempt(observation, id, r.serial, turnIdx)
			if !ok {
				return
			}
			e.emit(r, session.Event{Type: session.EvNetworkAttempt, Turn: turnIdx, NetworkAttempt: &canonical})
		})
	}
	seq, err := e.deps.LLM.Stream(ctx, req)
	if err != nil {
		return session.Message{}, session.Usage{}, session.StopNone, turnTiming{}, fmt.Errorf("agent: start stream: %w", err)
	}

	// Latency measurement state (anchored off the injected Clock; degrades to 0
	// with no Clock). noteFirstOutput anchors TTFT on the first observable output;
	// noteStreamDelta accumulates the inter-token gap series over streaming content
	// deltas only. See turnLatency.
	lat := newTurnLatency(e.deps.Clock)
	noteFirstOutput := lat.noteFirstOutput
	noteStreamDelta := lat.noteStreamDelta

	// text is the visible assistant text. reasoningBlob is the opaque
	// encrypted_content replayed back to the provider on subsequent turns (stored
	// on Message.Reasoning); it is distinct from the human-readable reasoning
	// summary, which only drives display-only reasoning.delta events.
	var text, reasoningBlob string
	// reasoningItemID is the provider's per-item id carried alongside the
	// ChunkReasoningItem replay blob (the OpenAI Responses reasoning-item id).
	// Last-non-empty-wins, like the phase marker — distinct from the additive
	// reasoning blob. Stored on Message.ReasoningItemID and replayed verbatim
	// next turn so strict gateways get the id back unchanged.
	var reasoningItemID string
	// phase is the OpenAI Responses opaque phase marker (commentary/final_answer),
	// stored verbatim on Message.ProviderPhase and replayed next turn; like the
	// reasoning blob it is never displayed or interpreted. It is NOT observable
	// output (it anchors no TTFT — no noteFirstOutput) and carries no streamed
	// token (no noteStreamDelta).
	var phase string
	var calls []session.ToolCall
	var usage session.Usage
	stop := session.StopNone

	for chunk, cerr := range seq {
		if cerr != nil {
			return session.Message{}, usage, stop, turnTiming{}, fmt.Errorf("agent: stream: %w", cerr)
		}
		if ctx.Err() != nil {
			return session.Message{}, usage, stop, turnTiming{}, context.Canceled
		}
		switch chunk.Kind {
		case port.ChunkText:
			noteStreamDelta()
			text += chunk.Text
			e.emit(r, session.Event{Type: session.EvMessageDelta, Turn: turnIdx, Text: chunk.Text})
		case port.ChunkReasoning:
			// Display-only: surface the human-readable reasoning summary to
			// clients. This text is NOT what gets replayed to the provider (see
			// ChunkReasoningItem below); it must not be stored on Message.Reasoning.
			noteStreamDelta()
			e.emit(r, session.Event{Type: session.EvReasoningDelta, Turn: turnIdx, Text: chunk.Text})
		case port.ChunkReasoningItem:
			// The opaque reasoning replay blob. Stored on Message.Reasoning and sent
			// back verbatim next turn for stateless reasoning continuity. It is
			// observable OUTPUT (it anchors TTFT) but not a streamed token, so it
			// never counts toward the inter-token gap series.
			//
			// An adapter emits ONE per turn: where the provider's replay unit is a
			// LIST (several OpenAI reasoning items, several Anthropic thinking
			// blocks), the adapter packs the ordered list into its own opaque
			// envelope and flushes it at the terminal event. The accumulation here
			// is therefore a degenerate case, and MUST NOT be relied on to assemble
			// a multi-part payload: the fold below keeps only the last item id, so a
			// per-item id would be lost — which is exactly how concatenated OpenAI
			// blobs came to be replayed under the wrong id and rejected
			// (invalid_encrypted_content). Structure belongs in the adapter's
			// envelope, never in this concatenation.
			noteFirstOutput()
			reasoningBlob += chunk.Text
			if chunk.ReasoningItemID != "" {
				reasoningItemID = chunk.ReasoningItemID
			}
		case port.ChunkPhase:
			// The opaque phase marker (commentary/final_answer). Stored on
			// Message.ProviderPhase and replayed verbatim next turn. Last-wins (like the
			// reasoning blob); never displayed or interpreted.
			phase = chunk.Text
		case port.ChunkToolCall:
			// A tool call is observable OUTPUT — a pure tool-call turn must still
			// anchor TTFT (issue #155) — but it is not a streamed token, so it goes
			// through noteFirstOutput, never noteStreamDelta: it must never seed the
			// inter-token gap series.
			noteFirstOutput()
			if chunk.ToolCall != nil {
				calls = append(calls, *chunk.ToolCall)
			}
		case port.ChunkUsage:
			if chunk.Usage != nil {
				usage = usage.Add(*chunk.Usage)
			}
		case port.ChunkProviderRoute:
			// The downstream provider slug that routed this turn (issue #480).
			// Relayed verbatim onto a client-visible event; never stored on the
			// message, never branched on, never replayed. It anchors no TTFT and
			// feeds no usage (like ChunkPhase). Absent on a cache hit.
			e.emit(r, session.Event{Type: session.EvProviderRoute, Turn: turnIdx, Text: chunk.Text})
		case port.ChunkDone:
			stop = chunk.Stop
		}
	}

	// A consumer that exited because ctx was cancelled mid-stream surfaces as a
	// cancellation, not a normal end-of-turn.
	if ctx.Err() != nil {
		return session.Message{}, usage, stop, turnTiming{}, context.Canceled
	}

	timing := lat.summary()

	msg := session.NewAssistantMessage(text, reasoningBlob, calls)
	msg.ProviderPhase = phase
	msg.ReasoningItemID = reasoningItemID
	return msg, usage, stop, timing, nil
}

// buildRequest assembles the provider-neutral LLMRequest for the current turn:
// the layered system prompt (cache-stable prefix + volatile env suffix), the
// EPHEMERAL turn-0 instruction fragments prepended ahead of the persisted
// conversation history, and the mode-filtered tool specs.
//
// The instruction fragments (project instructions / soul / memory index / user
// model) are assembled ONCE per run (r.fragmentsOnce, fail-soft) and prepended to
// Messages on EVERY turn — they are never written into Conversation.Messages, so
// they cost no persisted-history bloat and converge the snapshot + event-sourced
// rehydration paths (ADR 0043). Messages is a FRESH slice each call
// (fragments ++ conversation); Conversation.Messages is never mutated. Assembling
// once per run keeps the message prefix byte-stable within the run (prompt cache).
func (e *Engine) buildRequest(ctx context.Context, r *Run, sess *session.Session, env tool.Environment) port.LLMRequest {
	cfg := e.deps.PromptConfig
	// Progressive disclosure (pattern 9): when enabled, advertise lightweight
	// specs (full spec for non-disclosable tools, including the ToolSearch tool
	// registered by NewEngine) so the model hydrates schemas on demand. When OFF
	// (the default) send every tool's full spec exactly as v1 does.
	if e.deps.ProgressiveTools {
		cfg.Tools = e.deps.Catalog.AdvertisedSpecs(sess.Mode)
	} else {
		cfg.Tools = e.deps.Catalog.Specs(sess.Mode)
	}
	authority, authorityBound := sess.BoundAuthority()
	cfg.Tools = authoritySpecs(cfg.Tools, authority.CapabilitySet, authorityBound)
	// Run-scoped extra tools (RunRequest.ExtraTools) are advertised this run only,
	// after the catalog specs, so a structured-output SubmitResult (or any per-run
	// tool) is visible to the model without being registered into the shared catalog.
	// A name already present in cfg.Tools is REPLACED by the extra's spec (the overlay
	// wins, matching lookupTool's overlay-first resolution) so the advertised set and
	// the dispatch resolution never disagree.
	cfg.Tools = authorityOverlaySpecs(cfg.Tools, r.req.ExtraTools)
	// Shell-less Environment (issue #462 review): the CAPABILITY TRUTH for whether
	// this turn has a shell is the LIVE tool.Environment handed to Run, NOT the
	// shared Engine's catalog/prompt (which were built once from server config and
	// may advertise Bash the per-run Environment cannot serve). A shell-less
	// Environment (env.CommandRunner() == nil) — an ACP/editor override, a --no-bash
	// deployment, or a runner that could not be built — has NO Bash this turn, so:
	// (a) DROP the Bash spec from the advertised tools so the model is not invited
	// to call a shell it cannot reach (a stale/hallucinated Bash call the model
	// makes anyway still resolves from the catalog and surfaces the honest
	// bashNoShellResult "no shell available" tool error — it is never a silent pass),
	// and (b) append ONE shell-less posture clause to the per-request system prompt
	// (the ADR-0070 model-visible affordance) so the model learns from the PROMPT
	// that Bash is absent, not from a trail of errors. The clause rides the VOLATILE
	// suffix — environment capability is per-run/per-turn, so it must NOT be baked
	// into the cache-stable prefix (that would be dishonest to the cache when an
	// override changes it mid-session). The no-FS profile is EXCLUDED: its catalog
	// already omits Bash AND its noFSPostureNote (baked by composition) already
	// states "no shell", so a second clause here would duplicate/contradict. This is
	// the SINGLE per-request choke point: ACP overrides, --no-bash deployments, and
	// any future shell-less Environment all converge here, independent of the
	// shared Engine's catalog.
	shellLess := env.CommandRunner() == nil && env.Ref().Kind != session.EnvKindNoFS
	if shellLess {
		filtered := cfg.Tools[:0]
		for _, ts := range cfg.Tools {
			if ts.Name == tool.BashToolName {
				continue
			}
			filtered = append(filtered, ts)
		}
		cfg.Tools = filtered
	}
	cfg.Env.Model = e.deps.Model
	cfg.Env.Mode = string(sess.Mode)
	if cfg.Env.Cwd == "" {
		cfg.Env.Cwd = env.Workspace().Root()
	}
	e.refreshOperatorProfile(ctx, r, &cfg)
	// PromptBuilder (issue #127): a host-supplied builder replaces prompt.Build
	// when non-nil, so a host embedding the engine for a non-coding agent can
	// fully own the system prompt. nil → prompt.Build (byte-identical to v0.0.1).
	build := e.deps.PromptBuilder
	if build == nil {
		build = prompt.Build
	}
	// Assemble the ephemeral turn-0 instruction fragments ONCE per run, then prepend
	// them ahead of the persisted conversation on EVERY turn (incl. resume). Assembly
	// is fail-soft: an Instructions.Assemble error (or a nil assembler) leaves
	// r.fragments nil and the run proceeds without fragments rather than aborting.
	r.fragmentsOnce.Do(func() {
		if e.deps.Instructions == nil {
			return
		}
		var (
			discovered []session.Message
			aerr       error
		)
		if e.deps.EnableDurableEvidence {
			discovered, r.fragmentManifest, aerr = prompt.AssembleWithManifest(ctx, env.Workspace(), e.deps.Instructions)
		} else {
			discovered, aerr = e.deps.Instructions.Assemble(ctx, env.Workspace())
		}
		if aerr != nil {
			r.diag.Log(ctx, port.LevelWarn, "instruction-fragment assembly failed; continuing without turn-0 fragments", "error", aerr)
			return
		}
		r.fragments = discovered
	})
	// Build a NEW slice — fragments first, then the persisted conversation — so
	// Conversation.Messages is never mutated and the prefix is byte-stable per run.
	msgs := requestMessages(r.fragments, sess.Conversation.Messages)
	system := build(cfg)
	// Append the shell-less posture clause to the VOLATILE suffix (issue #462
	// review) ONLY when the loop owns the system prompt — i.e. the DEFAULT builder
	// (prompt.Build) is in use. A host-supplied PromptBuilder fully owns the system
	// prompt (issue #127: NONE of the coding-agent defaults may leak), so the loop
	// must NOT inject harness-authored text the host did not write; the host still
	// learns the capability truth from the advertised tool specs (Bash is dropped
	// above regardless of builder). The clause follows the <env> block (the
	// capability truth the model reads) and stays out of the cache-stable prefix.
	// The "NO shell" substring is a stable test key.
	if shellLess && e.deps.PromptBuilder == nil {
		if system.VolatileSuffix == "" {
			system.VolatileSuffix = shellLessPostureNote
		} else {
			system.VolatileSuffix = system.VolatileSuffix + "\n\n" + shellLessPostureNote
		}
	}
	return port.LLMRequest{
		System:   system,
		Messages: msgs,
		Tools:    cfg.Tools,
		Model:    e.deps.Model,
	}
}

func requestMessages(fragments, persisted []session.Message) []session.Message {
	if len(fragments) == 0 {
		return persisted
	}
	msgs := make([]session.Message, 0, len(fragments)+len(persisted))
	msgs = append(msgs, fragments...)
	return append(msgs, persisted...)
}

func (e *Engine) refreshOperatorProfile(ctx context.Context, r *Run, cfg *prompt.Config) {
	if e.deps.OperatorProfileSource == nil {
		return
	}
	entries, err := e.deps.OperatorProfileSource.List(ctx, "user/")
	if err != nil {
		if !r.operatorProfileWarned {
			r.operatorProfileWarned = true
			r.diag.Log(ctx, port.LevelWarn, "operator-profile refresh failed; continuing with last good snapshot", "error", err)
		}
	} else {
		r.operatorProfile = append(r.operatorProfile[:0], entries...)
		r.operatorProfileLoaded = true
	}
	if r.operatorProfileLoaded {
		cfg.OperatorProfile.Entries = append([]tool.MemoryEntry(nil), r.operatorProfile...)
	} else {
		cfg.OperatorProfile.Entries = nil
	}
}

func estimateRequestTokens(counter TokenCounter, req port.LLMRequest) int {
	total := countLayered(counter, req.System) + counter.CountMessages(req.Messages)
	for _, spec := range req.Tools {
		total += perToolSpecOverhead
		total += counter.Count(spec.Name)
		total += counter.Count(spec.Description)
		total += countBytes(counter, spec.Schema)
	}
	return total
}

// maybeCompact runs the Compactor when the estimated complete provider request
// crosses the threshold. It replaces the conversation in place, updates only the
// request's message suffix, and emits a compaction Event. It reports whether
// compaction ran.
func (e *Engine) maybeCompact(ctx context.Context, r *Run, sess *session.Session, turnIdx int, req *port.LLMRequest) bool {
	// Resolve the window LIVE here (the self-correction point): a live-catalog
	// refresh after construction is observed on this next check without an engine
	// rebuild. A nil resolver or a <=0 return disables compaction (zero disables).
	window := 0
	if e.deps.ContextWindow != nil {
		window = e.deps.ContextWindow()
	}
	if window <= 0 {
		return false
	}
	threshold := int(float64(window) * e.deps.CompactionRatio)
	requestTokens := estimateRequestTokens(e.deps.TokenCounter, *req)
	if requestTokens < threshold {
		return false
	}
	// Derive the cascade target from the same complete-request measurement. System
	// text, ephemeral fragments, and tool schemas are irreducible here, so only the
	// remaining budget is available to persisted history. A floor of one keeps the
	// budget meaningful when overhead alone is already above the target; candidate
	// admission below still requires reduction.
	persistedTokens := e.deps.TokenCounter.CountMessages(sess.Conversation.Messages)
	irreducible := requestTokens - persistedTokens
	if irreducible < 0 {
		irreducible = 0
	}
	budget := int(float64(window)*compactionTargetRatio) - irreducible
	if budget < 1 {
		budget = 1
	}
	result, compacted, err := e.compactionCandidate(ctx, sess.Conversation, budget)
	if err != nil {
		// Compaction is best-effort: a failure must not abort the run. Keep the
		// existing history and continue — but no longer SILENTLY: surface the
		// degraded mode on the operator channel so a run that keeps growing
		// uncompacted is diagnosable. Behaviour is unchanged (still continue).
		// ErrCompactionWouldOrphan (a compactor refusing to emit a tool-pairing-
		// invalid history) lands here too, reusing this WARN — no new diagnostics
		// line, preserving the "loop emits exactly THREE lines" invariant (the third
		// is the run-end background drain-abandon WARN).
		r.diag.Log(ctx, port.LevelWarn, "compaction failed; continuing without compaction", "error", err)
		return false
	}
	if !result.Changed {
		return false
	}
	// Capture the pre-compaction history BEFORE ReplaceHistory mutates it (cloud-native
	// Phase 3b non-destructive archive). Messages are immutable per-element, so a slice
	// reference is safe to hold across the replace — it stays the genuine pre-compaction
	// history, never the rewritten tail. The archive is emitted only AFTER a successful
	// replace below; the degrade-and-continue branches above emit nothing (no compaction
	// happened, so there is no replaced span to archive).
	archived := sess.Conversation.Messages
	if err := sess.ReplaceHistory(compacted); err != nil {
		// Replacement is legal only while running; if the seam rejects it, keep the
		// existing history rather than aborting the run — and emit a degraded-mode
		// warning rather than swallowing it. Behaviour is unchanged (still continue).
		r.diag.Log(ctx, port.LevelWarn, "compaction produced history the session rejected; continuing without compaction", "error", err)
		return false
	}
	// The system prompt, advertised tools, and ephemeral prefix were already
	// assembled and measured. Preserve them byte-for-byte and replace only the
	// persisted-history suffix that compaction changed.
	req.Messages = requestMessages(r.fragments, sess.Conversation.Messages)
	e.emit(r, session.Event{Type: session.EvCompaction, Turn: turnIdx, Text: result.Summary})
	// Emit the durable non-destructive archive of the span the replace just dropped.
	// The loop only EMITS it; the server relay Appends it to port.EventLog (the loop
	// stays storage-agnostic — it never imports the log port). No-leak: `archived` is
	// the parent's OWN conversation, never child content (gauntlet #7).
	e.emit(r, session.Event{Type: session.EvCompactionArchive, Turn: turnIdx, CompactionArchive: &session.CompactionArchivePayload{Replaced: archived}})
	return true
}

// bindRunDiag returns the run-scoped Diagnostics for a run against the given
// session id: deps.Diagnostics with the "session" key bound, plus the "agent" key
// when Deps.Role is set (a child/subagent engine). The main engine (Role=="") binds
// only "session". deps.Diagnostics is never nil post-NewEngine and With on
// NopDiagnostics returns NopDiagnostics, so the result is always non-nil and safe.
func (e *Engine) bindRunDiag(id session.SessionID) port.Diagnostics {
	if e.deps.Role != "" {
		return e.deps.Diagnostics.With("session", string(id), "agent", e.deps.Role)
	}
	return e.deps.Diagnostics.With("session", string(id))
}

// emit assigns the next monotonic Seq, publishes the event on the Run channel,
// and returns the sequenced event (so callers can mirror it to a secondary sink).
//
// The send is guarded by the run's hardAbort signal (fires hardAbortGrace after
// Cancel): a non-blocking attempt first, then a blocking send that gives up when
// hardAbort fires. The try-send-first shape plus the grace are load-bearing —
// together they preserve the documented rationale that a CANCELLED run with a
// DRAINING consumer still delivers its in-flight events (the terminal
// StopCancelled EvResult included): whenever the buffer has room the event is
// delivered deterministically (a two-arm select alone would pick randomly
// between a ready send and a closed abort, flakily dropping post-cancel events
// on healthy runs), and a send that must PARK still gets the grace for a merely
// BACKLOGGED consumer to catch up before giving up. A dropped event still
// consumes its Seq — monotonic-with-gaps, exactly emitOrAbort's documented
// contract — and is still returned for sink mirroring.
func (r *Run) emit(ev session.Event) session.Event {
	ev.Seq = r.seq.Add(1)
	// The run labels its own events with run-scoped identity. Seq answers "where
	// in this run", RunID answers "which run" — Seq restarts every run, so it
	// cannot distinguish two runs of one session. Stamping here rather than at a
	// relay is ADR 0249 decision 4: there is no single downstream chokepoint that
	// feeds BOTH the durable log and the client wire, so any other placement means
	// stamping at ~9 sites by hand. Empty when the host minted no id.
	ev.RunID = r.runID
	select {
	case r.events <- ev:
		return ev
	default:
	}
	select {
	case r.events <- ev:
	case <-r.hardAbort:
	}
	return ev
}

// emitOrAbort is emit's give-up-at-seal sibling for the OUT-OF-BAND child
// emitters (the child registry's guarded emits — subagent.*, parallel.*,
// surfaced asks, permission.retract). It publishes ev like emit — BLOCKING, the
// intended backpressure, and crucially still delivering on a CANCELLED run whose
// consumer drains until close (a cancelled-before-start branch's bracketing
// events must be represented) — but selects on the abort signals
// instead of blocking past the run's teardown: abort closes at seal-INTENT
// (drainChildren / the run goroutine's deferred seal), so a send parked on a
// full channel behind a consumer that stopped draining unwinds when the run
// ends, releasing the registry's emitMu so seal itself can never deadlock. It
// reports whether the event was delivered; a dropped event consumes its Seq
// (monotonicity holds, gaps are fine — clients order by Seq, they do not count
// it).
//
// It gives up on EITHER of two distinct signals: the registry's seal-abort
// channel (abort — closes at seal-INTENT from the terminate paths) OR the run's
// hardAbort (fires hardAbortGrace after Run.Cancel — the explicit unwedge for a
// send parked behind a consumer that stopped draining, reachable even when the
// terminate paths themselves are blocked). The run ctx remains deliberately
// ABSENT from the select (the preserved rationale above: cancelled-but-drained
// runs still deliver — the grace covers a backlogged-but-draining consumer);
// hardAbort is an explicit signal, not ctx. The try-send-first shape mirrors
// emit: delivery is deterministic whenever the buffer has room, so the abort
// arms only ever claim a send that would genuinely park.
func (r *Run) emitOrAbort(ev session.Event, abort <-chan struct{}) bool {
	ev.Seq = r.seq.Add(1)
	// Same stamp as emit — see the note there. These two are the ONLY sites a run
	// hands an event outward, which is what makes the guarantee structural.
	ev.RunID = r.runID
	select {
	case r.events <- ev:
		return true
	default:
	}
	select {
	case r.events <- ev:
		return true
	case <-abort:
		return false
	case <-r.hardAbort:
		return false
	}
}

// childDrainCap bounds phase 1 of the run-end join on cancelled background
// children. A var only as a TEST seam (the drain tests shorten it; nothing
// outside tests writes it) — operationally it is a constant, deliberately NOT a
// Deps/Config knob (operator tuning would be overkill for a backstop):
// ctx-cancel kills a child's in-flight model stream and Bash process promptly
// (the adapters swallow the cancel; exec.CommandContext kills; the osfs runner's
// cmd.WaitDelay bounds the residual grandchild-pipe wait), so a child that has
// not joined within 10s is either genuinely wedged or parked on its own EMIT
// (consumer backpressure) — which phase 2 disambiguates.
var childDrainCap = 10 * time.Second

// childDrainGrace bounds phase 2 of the join: after aborting emits (abortEmits)
// a child that was merely EMIT-BLOCKED unwinds immediately — the grace only
// needs to cover its post-emit teardown (persist, worktree cleanup). Same
// test-seam-var rationale as childDrainCap.
var childDrainGrace = 1 * time.Second

// drainChildren is the shared pre-terminal hook (A4b — the TOP of both terminate
// paths): cancel every live BACKGROUND child, join their doneChs, WARN once
// about any GENUINELY-abandoned ids (ids only — A9), then SEAL the registry so a
// residual post-seal emit is a safe no-op. Running it BEFORE the terminal emit
// means a drained child's subagent.end precedes the run's EvResult on the stream
// (healthy-consumer ordering unchanged). Foreground/team/parallel children are
// synchronous inside their tool calls and cannot be live here; for the common
// no-background run this is a map scan + a seal.
//
// The join is TWO-PHASE because phase 1's cap can be burned by the run's OWN
// emit backpressure, not a wedged child: a child whose subagent.end send is
// parked on a full events channel (the consumer stopped draining) cannot reach
// markDone until that send aborts — and the abort signal (emitAbort) used to
// close only in seal, AFTER the join had already given up. So: phase 1 joins
// under childDrainCap; on expiry, abortEmits() unblocks every emit-parked child
// NOW and phase 2 re-joins the remainder under childDrainGrace; only children
// STILL unjoined after both phases are abandoned and WARNed — consumer
// backpressure is never misattributed as a wedged child, and the abandon WARN
// (the consciously-amended THIRD loop diagnostics line) stays truthful. Cost on
// the unhealthy path only: the post-abort end events are dropped instead of
// delivered — they were going nowhere.
//
// Retract contract: a JOINED child's still-pending surfaced asks were already
// retracted at its registry terminal (markDoneResult retracts BEFORE closing
// doneCh, so the retract precedes both the seal here and the terminal EvResult
// the terminate paths emit after this hook returns). Only an ABANDONED child
// never reaches that terminal, so its taken asks are swept and retracted here,
// pre-seal — the abandon WARN stays the only diagnostics line (the retract is
// an EVENT), and the abandoned child's eventual late markDoneResult takes an
// empty set against a sealed registry: it emits nothing.
//
// I3b note: the background-pending nudge must be checked in finishTurnNoTools
// BEFORE its clean-terminal calls — by the time this hook runs, the children it
// would ask about are already cancelled and the registry sealed.
func (*Engine) drainChildren(ctx context.Context, r *Run) {
	joins := r.children.cancelLiveBackground()
	if len(joins) > 0 {
		if pending := joinChildren(joins, childDrainCap); len(pending) > 0 {
			// Phase 2: unblock emit-parked children, then grant the short grace.
			r.children.abortEmits()
			if pending = joinChildren(pending, childDrainGrace); len(pending) > 0 {
				abandoned := make([]string, 0, len(pending))
				for _, j := range pending {
					abandoned = append(abandoned, j.id)
				}
				r.diag.Log(ctx, port.LevelWarn,
					"abandoning background subagents that did not stop within the run-end drain cap",
					"ids", strings.Join(abandoned, ","), "cap", (childDrainCap + childDrainGrace).String())
				// Abandoned-only ask sweep (joined children already retracted at
				// their markDoneResult): retract each abandoned child's
				// still-pending surfaced asks BEFORE the seal bars the emits, so
				// no client is left holding a stale modal for a child this run
				// will never answer for.
				for _, j := range pending {
					r.children.retractAsks(r.children.takeAsks(j.id))
				}
			}
		}
	}
	r.children.seal()
}

// joinChildren waits up to d for each join's doneCh under ONE shared timer and
// returns the joins still pending when it expires (nil when all joined).
func joinChildren(joins []backgroundJoin, d time.Duration) []backgroundJoin {
	timer := time.NewTimer(d)
	defer timer.Stop()
	var pending []backgroundJoin
	expired := false
	for _, j := range joins {
		if !expired {
			select {
			case <-j.doneCh:
				continue
			case <-timer.C:
				expired = true
			}
		}
		// Budget elapsed: sweep the rest non-blockingly.
		select {
		case <-j.doneCh:
		default:
			pending = append(pending, j)
		}
	}
	return pending
}

// deferFailedStepRetry closes a retry Run that reached a clean pre-turn brake
// before BeginTurn/provider invocation. Unlike ordinary clean termination it leaves
// the aggregate idle with durable retry intent, so no queued prompt may overtake it.
func (e *Engine) deferFailedStepRetry(ctx context.Context, r *Run, sess *session.Session, reason session.StopReason, text string, usage session.Usage) {
	e.closeSteerDrained(ctx, r, sess)
	e.drainChildren(ctx, r)
	e.fireStop(ctx, r, sess, reason)
	e.emitResult(r, sess, reason, text, usage, "", session.RetryDispositionUnknown, session.StreamProgressUnknown)
	e.save(ctx, r, sess)
}

// terminate ends the run with a non-success terminal state. It moves the session
// to the matching terminal state (Cancel for cancelled, Fail for error, Stop for
// a tripped limit) and emits the terminal result Event. permanent records whether
// a StopError failure is permanent (unrecoverable; retry will fail again).
func (e *Engine) terminate(ctx context.Context, r *Run, sess *session.Session, reason session.StopReason, text string, usage session.Usage, cause error, permanent bool) {
	disposition, progress := failureFacts(cause)
	if disposition == session.RetryDispositionUnknown && permanent {
		disposition = session.RetryDispositionPermanent
	}
	e.closeSteerDrained(ctx, r, sess)
	e.drainChildren(ctx, r)
	switch reason {
	case session.StopCancelled:
		_ = sess.Cancel()
	case session.StopError:
		_ = sess.Fail()
		_ = sess.RecordFailureMetadata(disposition, progress)
	default:
		if !sess.State.IsTerminal() {
			_ = sess.Stop(reason)
		}
	}
	var errMsg string
	if cause != nil && reason != session.StopCancelled {
		errMsg = cause.Error()
	}
	e.fireStop(ctx, r, sess, reason)
	e.emitResult(r, sess, reason, text, usage, errMsg, disposition, progress)
	e.save(ctx, r, sess)
}

// planApprovalTerminal is the shared plan-approval termination check the loop runs
// at its EARLY check (load-bearing for the awaiting-resume path) and its post-
// dispatch Step 6 check (the live path). It terminates the run CLEANLY when a plan
// verdict is pending: StopPlanApproved on Allow (terminateComplete then flips the
// mode at the boundary — AllowOnce→ModeDefault, AllowAlways→ModeAccept), or
// StopPlanIterate on Deny (the iterate pause — the run ENDS so the operator's next
// typed prompt drives the revision; the session stays ModePlan, no mode flip). It
// returns true when it terminated (so the caller returns); false to continue the
// loop. Factored out of runLoop so the two verdict arms do not each add a branch to
// runLoop's cyclomatic complexity.
func (e *Engine) planApprovalTerminal(ctx context.Context, r *Run, sess *session.Session, lastText string, total session.Usage) bool {
	switch {
	case r.planApprovedTarget != "":
		e.terminateComplete(ctx, r, sess, session.StopPlanApproved, lastText, total, "")
		return true
	case r.planIterateRequested:
		e.terminateComplete(ctx, r, sess, session.StopPlanIterate, lastText, total, "")
		return true
	}
	return false
}

// terminateComplete ends the run successfully (the model finished its turn),
// recording the explicit stop reason and emitting the result Event. errMsg is the
// terminal cause (empty for a clean end): a text-bearing turn that ended on a
// NON-benign stop chunk (StopError / StopCancelled) carries one, synthesised by
// stopTerminalCause at the call site, so a delegation never renders the child's
// last text AS the failure on this path either (the #319 terminateComplete shape).
// permanent is always false here (the ChunkDone StopError path has NO Go error
// to classify — honest fail-open).
func (e *Engine) terminateComplete(ctx context.Context, r *Run, sess *session.Session, reason session.StopReason, text string, usage session.Usage, errMsg string) {
	e.closeSteerDrained(ctx, r, sess)
	e.drainChildren(ctx, r)
	if !sess.State.IsTerminal() {
		_ = sess.Stop(reason)
	}
	// Plan-approval gate (issue #206, Wave 2): flip the session out of plan mode
	// when a plan was approved. r.planApprovedTarget is set only by the
	// plan-allow branch (AllowOnce→ModeDefault, AllowAlways→ModeAccept); the
	// session is now StateCompleted, where SetMode is legal (session.go rejects it
	// only from Running/Awaiting — pinned by TestPlanApprovalDoesNotFlipMidTurn).
	// The error path (terminate) does NOT flip: an errored plan run stays in plan
	// mode, honestly. Save the flipped mode so a Reopen/restart continues in the
	// approved posture.
	if r.planApprovedTarget != "" {
		_ = sess.SetMode(r.planApprovedTarget)
		e.save(ctx, r, sess)
	}
	// Persist the terminal aggregate before a synchronous Observer can block or
	// perform model work. The result event remains after observation, preserving
	// event order; persistence failures stay best-effort diagnostics via save.
	e.save(ctx, r, sess)
	e.observeCompletion(ctx, r, sess, reason, usage)
	e.fireStop(ctx, r, sess, reason)
	// terminateComplete has no Go error to classify (the provider relays a stop
	// CHUNK, not an error), so the permanence bit is always false here — honest
	// fail-open. Only the error terminate() path carries a real classified cause.
	e.emitResult(r, sess, reason, text, usage, errMsg, session.RetryDispositionUnknown, session.StreamProgressComplete)
}

// observeCompletion invokes the optional host observer after the aggregate has
// reached a qualifying completed state. The owned message snapshot prevents an
// observer from mutating the live conversation. Failures are operational facts:
// they never alter the terminal result and no session event owns them.
func (e *Engine) observeCompletion(ctx context.Context, r *Run, sess *session.Session, reason session.StopReason, usage session.Usage) {
	if e.deps.LearningMode == learning.Off || e.deps.LearningObserver == nil ||
		sess.State != session.StateCompleted || reason == session.StopError || reason == session.StopCancelled {
		return
	}
	tr := learning.NewTrajectory(sess.ID, r.workspace, reason, usage, sess.Conversation.Messages)
	tr.Principal = sess.Owner.Clone()
	tr.Kind = sess.Kind
	tr.Counters = sess.Counters
	if r.currentPrompt != nil {
		for i := len(tr.Messages) - 1; i >= 0; i-- {
			if session.IsGenuineUserPrompt(tr.Messages[i]) && reflect.DeepEqual(tr.Messages[i], *r.currentPrompt) {
				tr.Current = learning.MessageSpan{Start: i, End: len(tr.Messages)}
				break
			}
		}
	}
	if err := e.deps.LearningObserver.Observe(ctx, tr); err != nil {
		r.diag.Log(ctx, port.LevelWarn, "completed-trajectory observer failed", "error", err)
	}
}

// stopTerminalCause synthesises the terminal REASON for a run that ended on a
// provider stop CHUNK (no Go error) — the terminateComplete counterpart of the
// error terminate() carries. Both adapters relay a real terminal condition
// (max_tokens / refusal / incomplete / failed → StopError; cancelled →
// StopCancelled) on the ChunkDone stop, NOT as a Go error, so without this the
// ResultPayload.Error is empty and a delegation's subagentErrorBody renders the
// child's last text AS the failure — the exact #319 presentation on the
// terminateComplete path (reachable only WITH text: the empty shape already
// routes through terminate with a cause). The phrase deliberately matches the
// empty-shape terminate cause ("agent: model ended turn with no output and a
// terminal stop reason %q") so both stop-only terminals read alike. It is
// harness-authored metadata (a stop label + a shape note), never model text, so
// it is gauntlet-#7 safe on the same footing as every other cause. It is
// StopError-ONLY: the SubagentPayload.Cause contract is "empty on every other
// terminal", and a cancellation already names itself everywhere it matters (the
// Subagent timeout path renders its own time-budget note BEFORE the cause is
// consulted; Parallel's cancelled-branch arm overrides with "cancelled"). It
// returns "" for a benign stop (end_turn / a clean limit), where a cause would
// be noise — StopBudget/StopMaxTurns/StopNoProgress already carry their own
// honest notes.
// failureFacts extracts neutral typed metadata from a terminal provider error.
// Missing interfaces remain conservative unknown values; no text inference occurs.
func failureFacts(err error) (session.RetryDisposition, session.StreamProgress) {
	var disposition session.RetryDisposition
	var classified port.RetryDispositionError
	if errors.As(err, &classified) {
		disposition = classified.RetryDisposition()
		if !disposition.Valid() {
			disposition = session.RetryDispositionUnknown
		}
	} else {
		var pe port.PermanentError
		if errors.As(err, &pe) && pe.Permanent() {
			disposition = session.RetryDispositionPermanent
		}
	}
	var progress session.StreamProgress
	var progressed port.StreamProgressError
	if errors.As(err, &progressed) {
		progress = progressed.StreamProgress()
		if !progress.Valid() {
			progress = session.StreamProgressUnknown
		}
	}
	return disposition, progress
}

// permanentCause reports whether err is a permanent provider rejection that will
// fail again on retry (fail-open: an unclassifiable error returns false).
func permanentCause(err error) bool {
	disposition, _ := failureFacts(err)
	return disposition == session.RetryDispositionPermanent
}

func stopTerminalCause(stop session.StopReason, text string) string {
	if stop != session.StopError {
		return ""
	}
	if strings.TrimSpace(text) != "" {
		return fmt.Sprintf("agent: provider ended the turn with terminal stop reason %q after partial output — the text below is TRUNCATED or refused, not a finished answer", stop)
	}
	return fmt.Sprintf("agent: model ended turn with no output and a terminal stop reason %q", stop)
}

// emitResult publishes the single terminal result Event. errMsg carries the
// failure detail on an error termination (empty for success/limit/cancel).
// permanent records whether a StopError failure is permanent (unrecoverable).
func (e *Engine) emitResult(r *Run, _ *session.Session, reason session.StopReason, text string, usage session.Usage, errMsg string, disposition session.RetryDisposition, progress session.StreamProgress) {
	u := usage
	e.emit(r, session.Event{
		Type: session.EvResult,
		Result: &session.ResultPayload{
			Stop:        reason,
			Text:        text,
			Usage:       usage,
			Error:       errMsg,
			Permanent:   disposition == session.RetryDispositionPermanent,
			Disposition: disposition,
			Progress:    progress,
		},
		Usage: &u,
	})
}

// save best-effort persists the session if a Store is configured.
//
// A failure is WARNed once per run, never propagated: a persist failure must not
// abort a turn that is otherwise fine, and no other channel reports it — there is
// no session.Event for a persistence failure, no EvResult field, and no tool
// result, so the operator log is its only home. Discarding the error outright (as
// this did) made real data loss completely invisible: a child session whose id
// the store could not name was never persisted, and resume, InspectSubagent and
// its event log all silently stopped working with no line anywhere. r.diag
// carries the session id and, for a child engine, the agent role — exactly the
// correlation that absence made impossible to debug.
func (e *Engine) save(ctx context.Context, r *Run, sess *session.Session) {
	if e.deps.Store == nil {
		return
	}
	if err := e.deps.Store.Save(ctx, sess); err != nil && !r.saveWarned {
		r.saveWarned = true
		r.diag.Log(ctx, port.LevelWarn,
			"session persistence failed; this session may not be resumable after a restart",
			"error", err)
	}
}
