package ui

import (
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// maxTraceEntries caps how many trace entries a delegation lane (a subagent block,
// a fleet lane, a parallel branch, a team member) retains for the expanded/focus
// view, mirroring the line-cap idiom used elsewhere (e.g. maxToolResultLines).
// Older entries are dropped once the cap is reached so a long investigation never
// unbounds the card. Shared by all three delegation families (ADR 0079 — the trace
// model is one shape).
const maxTraceEntries = 12

// maxSubagentTrace is kept as the subagent-facing alias of the shared trace cap so
// existing call sites stay readable; it is the same constant.
const maxSubagentTrace = maxTraceEntries

// maxTeamTrace is kept as the team-facing alias of the shared trace cap; it is the
// same constant.
const maxTeamTrace = maxTraceEntries

// teamTraceKind distinguishes the two flavours of a delegation-lane trace entry: a
// forwarded message line versus a tool chip. The expanded view renders messages as
// clamped prose and tools as glyph+name chips. (The kind name stays team-prefixed
// for the historical type it rides; it is shared by all three families.)
type teamTraceKind int

const (
	teamTraceMessage teamTraceKind = iota // a forwarded child/member/branch message line
	teamTraceTool                         // a child/member/branch tool call (name + ok/error glyph)
)

// teamTrace is one capped entry in a delegation lane's trace — shared by the Team
// member lanes, the Subagent inline/fleet lanes, and the Parallel branch lanes
// (ADR 0079: the trace model converged on this one shape). It is either a message
// line (kind=teamTraceMessage, text set) or a tool chip (kind=teamTraceTool, name
// + detail + isError set). detail is the server-bounded arg/result preview shown
// next to the chip in the expanded view. All text is bounded server-side
// (clamp-scrubbed, ≤200 runes); the ui caps it again on render.
type teamTrace struct {
	kind    teamTraceKind
	text    string // message text (teamTraceMessage)
	name    string // tool name (teamTraceTool)
	detail  string // bounded arg/result preview (teamTraceTool)
	isError bool   // tool errored (teamTraceTool)
}

// pushTrace appends a trace entry to a lane slice and enforces the shared cap,
// dropping the oldest entries once it overflows. It is the single accumulator
// behind every delegation family's trace (subagent inline + fleet, parallel
// branch, team member), so the cap can never drift between surfaces.
func pushTrace(trace []teamTrace, t teamTrace) []teamTrace {
	trace = append(trace, t)
	if len(trace) > maxTraceEntries {
		trace = trace[len(trace)-maxTraceEntries:]
	}
	return trace
}

// traceAppendMessage adds a message line to a lane trace. Consecutive
// message.delta fragments coalesce onto the trailing message entry (so streamed
// text reads as one line, not a chip storm); a new line is started when the last
// entry is a tool chip. The trace stays capped at maxTraceEntries.
func traceAppendMessage(trace []teamTrace, text string) []teamTrace {
	if text == "" {
		return trace
	}
	if n := len(trace); n > 0 && trace[n-1].kind == teamTraceMessage {
		trace[n-1].text += text
		return trace
	}
	return pushTrace(trace, teamTrace{kind: teamTraceMessage, text: text})
}

// traceAppendTool adds a tool chip (pending; error + result detail resolved later
// by traceMarkToolResult). detail here is the call's bounded arg preview.
func traceAppendTool(trace []teamTrace, name, detail string, isError bool) []teamTrace {
	return pushTrace(trace, teamTrace{kind: teamTraceTool, name: name, detail: detail, isError: isError})
}

// traceMarkToolResult finalises the most recent matching pending tool chip's error
// state, and replaces its detail with the result preview when one is provided (a
// result preview is more informative than the call's arg preview; an empty result
// detail keeps the arg preview). If no matching pending chip is found (e.g. a
// dropped tool.call), it appends a resolved chip so a result is never silently lost.
func traceMarkToolResult(trace []teamTrace, name, detail string, isError bool) []teamTrace {
	for i := len(trace) - 1; i >= 0; i-- {
		t := &trace[i]
		if t.kind == teamTraceTool && t.name == name {
			t.isError = isError
			if detail != "" {
				t.detail = detail
			}
			return trace
		}
	}
	return traceAppendTool(trace, name, detail, isError)
}

// teamLane is the live projection of one team member's activity, accumulated from
// the team.member events tagged with that member's name. It holds the member's
// stable roster metadata (mutating / lead), its current tool or state label, a
// running token usage, and a capped trace of message lines + tool chips. It never
// holds unbounded member content — the server caps every preview and the trace is
// capped at maxTraceEntries.
type teamLane struct {
	name string
	// sessionID is the member's child SESSION id ("team-<teamID>-<member>") — the
	// CancelChild handle, backfilled from the first team.member event carrying it
	// (D16). Empty until the member produces an event (or from an older server); the
	// x cancel key no-ops then.
	sessionID string
	role      string // the member's roster role (e.g. "researcher"); shown in the f6 overlay roster, omitted from the calm inline card
	mutating  bool
	lead      bool
	// routedCategory/routedModel are the opt-in model router's bare metadata for this
	// member (a category label + a model id), set on team.start only when the router
	// classified it (ADR 0031 / ADR 0034); "" when unrouted (no router, fail-soft miss,
	// or a DEFINED member that pinned its own model). BARE metadata — never member
	// content — so gauntlet #7 holds.
	routedCategory string
	routedModel    string
	// routingReason names WHY the router did not classify this member (issue #397 /
	// ADR 0083); "" on a routed hit. BARE metadata — never member content.
	routingReason string
	// routingDecision is the configured router's immutable start snapshot (ADR 0352).
	// Nil preserves historical events without reconstructing evidence.
	routingDecision *client.RoutingDecision
	// model is the concrete model id the member's engine ACTUALLY runs on (issue #112 /
	// ADR 0035), regardless of how it was chosen; == routedModel when routed. BARE
	// metadata — never member content — so gauntlet #7 holds.
	model string

	current   string // last tool name run, or "" when none yet
	toolCount int
	usage     client.Usage
	trace     []teamTrace
	// idle marks that the member finished its current ROUND (it reported a per-round
	// result); it is awaiting the next round or the lead's synthesis. It is NOT
	// terminal — a member is re-driven each round, so this is cleared the moment new
	// activity (delta / tool.call / turn.end) arrives. Team-terminal lives on
	// block.teamDone (set only at team.end), never here.
	idle bool

	// stopped + stopReason are the member's TERMINAL disposition, set ONLY at team.end
	// (setTeamEnd applies the team.end Dispositions snapshot). They are distinct from
	// `idle` (a per-round, non-terminal state cleared by new activity): once the team has
	// ended (block.teamDone), a stopped lane renders "✗ stopped — <reason>" instead of the
	// blanket "✓ done". Empty stopReason / stopped=false on a clean member.
	stopped    bool
	stopReason string
	// errorRounds is how many of this member's rounds ended in a run-level error, also
	// applied at team.end. A bounded retry (issue #318) means a member can fail a round
	// and still finish, so a lane with errorRounds > 0 and stopped == false renders
	// "done (retried)" — never a bare "done", which would contradict the supervisor.
	errorRounds int

	// cause is the member run's per-round FAILURE DETAIL on a result event that
	// ended StopError (issue #331, mirroring subagentLane.cause) — the
	// harness/provider error, not member-authored output, so gauntlet #7 holds. Set
	// on a "result" TeamMsg when Cause is non-empty; LAST non-empty value wins (a
	// retried member's failed rounds each surface their own cause). Rendered only
	// when the member is benched (stopped/error) at team.end; a done member with a
	// prior cause does not render it (it recovered).
	cause string

	// ctxUsed / ctxWindow back the per-member context meter in the f6 agents
	// overlay. ctxUsed is the CURRENT context occupancy — the most recent turn's
	// input-token count (ASSIGNED, not summed, each turn.end, mirroring the main
	// meter's m.contextTokens = turn input tokens) — and ctxWindow is the member
	// engine's context window (sticky: kept across turns, only overwritten by a
	// positive value). When ctxWindow is 0 no meter is drawn for the lane.
	ctxUsed   int64
	ctxWindow int64
}

// teamTask is the ui-local projection of one entry in the team's shared task list,
// rendered by the f6 agents task sub-view. It mirrors client.TeamTask; it holds
// only task metadata (id / state / assignee / deps), never member content.
type teamTask struct {
	id       string
	desc     string
	state    string
	assignee string
	deps     []string
}

// teamFinding is the ui-local projection of one entry in the team's shared findings
// ledger, rendered by the f6 agents findings view. It mirrors client.TeamFinding;
// it holds only the recording member's name and a bounded body preview.
type teamFinding struct {
	member string
	body   string
}

// blockKind classifies a scrollback block so the renderer knows how to style it.
type blockKind int

const (
	blockUser      blockKind = iota // a user prompt
	blockAssistant                  // streamed assistant markdown (+ optional reasoning summary)
	blockTool                       // a tool call (+ its resolved result)
	blockNotice                     // compaction / muted info
	blockTurnStat                   // muted per-turn usage + elapsed stat line
	blockError                      // an error notice
	blockHook                       // a structured hook notice (phase + decision)
	blockDelivery                   // a fire-result delivery note (scheduled-task affordance + outcome)
)

// block is one entry in the conversation scrollback. Assistant blocks accumulate
// streamed deltas in raw (re-rendered through glamour each delta); tool blocks
// carry their call args and, once resolved, their result. Keeping the raw text
// on the block (not a pre-rendered string) lets a theme/width change re-render
// the whole history correctly.
type block struct {
	id   uint64
	kind blockKind

	// rev is the block's render revision: bumped on EVERY post-append mutation of a
	// render-visible field. The renderer's per-block cache (renderer.blockCache)
	// keys on it, so a settled block (rev unchanged) joins the conversation string
	// from cache while a mutated block re-renders fresh. Blocks must therefore be
	// mutated only through conversation methods — the bump sites are exactly four
	// gateways (currentAssistant, subagentBlock, teamBlock, and resolveTool's
	// matched branch), which together front every post-append mutator. The
	// cache-equivalence oracle in render_cache_test.go is the drift tripwire: a
	// mutation path that bypasses a bump renders stale and fails the oracle.
	rev int

	raw string // user text, assistant markdown buffer, or notice text

	// media holds one placeholder line per non-text part attached to a USER block
	// (blockUser), e.g. "image/png (inline)". It is populated when a prompt
	// attaches media via the @-mention menu (mention.go → client.ExpandMentions):
	// each part renders a clear "📎 …" placeholder line below the text so a
	// multimodal prompt is never silently shown as text-only. Empty for a text-only
	// prompt.
	media []string

	// Reasoning is an ATTRIBUTE of the assistant block, not a sibling: a turn's
	// reasoning-summary deltas and answer-text deltas can interleave on the wire
	// (separate SSE events), so all of a turn's reasoning accumulates here and
	// renders as one dim, collapsed header above the merged answer. reasoning is
	// the accumulated summary text; reasoningStreaming is true while reasoning is
	// still arriving and the answer text has not started (drives the live
	// "reasoning…" affordance).
	reasoning          string
	reasoningStreaming bool

	// Tool-block fields.
	toolID      string
	toolName    string
	toolArgs    string
	resolved    bool
	resultBody  string
	resultError bool
	// permanent marks a blockError as a PERMANENT provider rejection — retrying
	// cannot succeed. When true, the renderer shows a one-line human summary
	// instead of the raw error text, with the raw payload available on expand
	// (ctrl+t). Meangless for non-error blocks.
	permanent bool
	// recover marks a blockNotice as a recover-notice advisory (a session that
	// failed on a PERMANENT provider error was recovered for re-entry). The
	// renderer styles it as a WARNING (⚠ glyph) rather than a muted compaction
	// notice, so it stands out as actionable. It is a DURABLE scrollback block
	// (not a transient statusMsg) so the run's first event does not overwrite it
	// before the user reads it. Meaningful only for blockNotice.
	recover bool
	// resultBlocks carries the typed content blocks relayed from the server for a tool
	// result (when the result carried structured Parts — resource links, images, …).
	// The renderer surfaces user-audience artifacts (resource links, images) IN
	// ADDITION to the model-facing resultBody text, so e.g. a github MCP resource_link
	// shows as a distinct artifact line rather than buried in/below the text body.
	// nil (the common text-only case) leaves the existing render path byte-unchanged.
	resultBlocks []client.ContentBlock

	// deliveryFireID is the fire id of a blockDelivery note (empty for every
	// other block kind). It labels the delivery card's subtitle so the operator
	// can correlate the card to a `sched--<name>-…` fire session without it being
	// buried in the fenced body.
	deliveryFireID string

	// Subagent fields (attached to a Subagent tool block): the BOUNDED projection
	// of the Subagent's child run (ADR 0079). subagent is true once a subagent.start
	// has been attributed to this block; subGoal is the card title; subCurrent is
	// the child's live current-tool name; subTrace is the capped shared trace of
	// child tool chips + message lines; subToolCount is the running/final child
	// tool count; subUsage, subStop, and subDurationMs are the resolved end stats
	// (subDone gates them). The previews are bounded/scrubbed/client-only — they
	// never enter the parent conversation (gauntlet #7).
	subagent      bool
	subGoal       string
	subCurrent    string // latest child tool name, "" when none yet
	subTrace      []teamTrace
	subToolCount  int
	subUsage      client.Usage
	subStop       string
	subDurationMs int64
	subDone       bool
	// subRoutedCategory/subRoutedModel are the opt-in model router's bare metadata
	// for this delegation (a category label + a model id), set on subagent.start
	// only when the router classified it (ADR 0031). Empty when no router ran. BARE
	// metadata — never child content — so gauntlet #7 holds.
	subRoutedCategory string
	subRoutedModel    string
	// subRoutingReason names WHY the opt-in router did NOT classify this delegation
	// (issue #397 / ADR 0083): empty on a routed hit, else a bounded gate/miss string.
	// BARE metadata — never child content — so gauntlet #7 holds.
	subRoutingReason   string
	subRoutingDecision *client.RoutingDecision
	// subModel is the concrete model id the child ACTUALLY ran on (issue #112 /
	// ADR 0035), regardless of how it was chosen. When routed, equals subRoutedModel.
	// BARE metadata — never child content — so gauntlet #7 holds.
	subModel string

	// Team fields (attached to a Team tool block): the BOUNDED projection of an
	// in-process team's run. team is true once a team.start has been attributed to
	// this block; teamLanes are the per-member lanes in roster order (the order the
	// model formed the team), looked up by name when routing team.member events;
	// teamRounds/teamStop/teamUsage are the resolved end stats (teamDone gates
	// them). Member content lives in each lane's capped trace; nothing here enters
	// the parent conversation.
	team         bool
	teamID       string // the team's stable id (e.g. "team-p1"), shown in the live footer summary segment
	teamLanes    []teamLane
	teamTasks    []teamTask    // the team's shared task list (f6 task sub-view)
	teamFindings []teamFinding // the team's shared findings ledger (f6 findings view)
	teamRounds   int
	teamStop     string
	teamUsage    client.Usage
	teamDone     bool

	// Hook-block fields (blockHook): the structured phase/tool/decision used to
	// render a hook notice distinctly from a compaction notice and colour a
	// blocked hook.
	hookPhase    string
	hookTool     string
	hookDecision string // "info" | "blocked" | "modified"
}

// subagentLane is the flat, fleet-level projection of ONE Subagent child run, keyed by
// ChildID. It mirrors the per-Subagent-block subagent fields (subGoal/subTrace/…) but is
// collected ACROSS all Subagent cards into conversation.subagentFleet, so the footer
// segment can show aggregate running/done counts and the f6 Subagents tab can
// list one row per child regardless of where its inline card sits in scrollback. It
// carries the BOUNDED previews the subagent.* events forward (ADR 0079) — bounded,
// scrubbed, client-only (gauntlet #7 is about the conversation, not the client).
//
// current is the latest child tool NAME (the most-recent subagent.tool ToolName) —
// the per-row liveness signal (mirroring teamLane.current). done/stop/usage/durationMs
// are the resolved end stats (done gates them). isError marks the LAST child tool
// errored (a transient cue); the terminal disposition rides stop. background marks
// a detached-delivery child (subagent.start's Background field): its Subagent call
// already returned a started-result and the result body is collected by the AGENT
// via SubagentStatus — the events carry only background + done, never the
// registry's delivered state, so the lane renders what it honestly knows.
type subagentLane struct {
	childID         string
	goal            string
	background      bool
	routedCategory  string // opt-in model router's category label (ADR 0031); "" when unrouted
	routedModel     string // opt-in model router's chosen model id (ADR 0031); "" when unrouted
	routingReason   string // WHY the router did not classify (issue #397 / ADR 0083); "" on a routed hit
	routingDecision *client.RoutingDecision
	model           string // concrete model id the child ACTUALLY ran on (issue #112 / ADR 0035); == routedModel when routed
	current         string // latest child tool name, "" when none yet
	trace           []teamTrace
	toolCount       int
	usage           client.Usage
	isError         bool // the most-recent child tool errored (transient)
	done            bool
	stop            string
	// cause is the child's FAILURE DETAIL on an errored terminal (subagent.end's
	// Cause; empty otherwise) — the harness/provider error, not child-authored
	// output, so gauntlet #7 holds (issue #319). It is the ONLY place the fleet
	// surfaces WHY a child failed: a roster/focus row otherwise shows just
	// "stop:error", and a BACKGROUND child's failure never reaches an inline card
	// at all (its Subagent call already returned the started-result).
	cause      string
	durationMs int64
}

// conversation is the ordered scrollback. It owns block creation/mutation so the
// model never pokes blocks directly; render.go turns it into the viewport string.
//
// subagentFleet is the flat, insertion-ordered collection of Subagent child lanes keyed
// by ChildID (see subagentLane). It is fed alongside the inline-card routing by
// applySubagent/upsertSubagentLane, and read by the fleet footer segment and the
// f6 Subagents tab. It is part of the conversation so a /clear (which rebuilds
// the conversation) drops it too.
type conversation struct {
	blocks      []block
	nextBlockID uint64
	// filesChanged preserves first-seen order; filesSeen tracks membership. The
	// appendix receives its identity at the first distinct change, even though it
	// is not yet a physical rendered block.
	filesChanged           []string
	filesSeen              map[string]struct{}
	changedFilesAppendixID uint64
	// subagentFleet preserves first-seen order; fleetIndex maps ChildID → its slot so
	// repeated tool/end events for a child update the same lane in O(1).
	subagentFleet []subagentLane
	fleetIndex    map[string]int
	// parallelGroups preserves first-seen order; parallelIndex maps ParentCallID → its
	// slot so repeated branch/end events for a Parallel call update the same group in
	// O(1). A Parallel run is a GROUP (not a flat fleet), so each group holds its own
	// ordered branches. Part of the conversation, so /clear drops it too.
	parallelGroups []parallelGroup
	parallelIndex  map[string]int
}

// appendBlock assigns the next UI-local document identity before adding a block.
func (c *conversation) appendBlock(b block) {
	c.nextBlockID++
	b.id = c.nextBlockID
	c.blocks = append(c.blocks, b)
}

// recordFileChange records a first-seen mutated workspace path. The synthetic
// appendix gets one stable identity at its first distinct member.
func (c *conversation) recordFileChange(path string) {
	if path == "" {
		return
	}
	if c.filesSeen == nil {
		c.filesSeen = make(map[string]struct{})
	}
	if _, ok := c.filesSeen[path]; ok {
		return
	}
	c.filesSeen[path] = struct{}{}
	c.filesChanged = append(c.filesChanged, path)
	if c.changedFilesAppendixID == 0 {
		c.nextBlockID++
		c.changedFilesAppendixID = c.nextBlockID
	}
}

// isEmpty reports whether the conversation has no blocks yet — the first-run
// state, before any prompt is sent. The zero-state welcome card renders in the
// empty viewport while this holds (and vanishes the instant the first block,
// e.g. the user prompt, is appended).
func (c *conversation) isEmpty() bool { return len(c.blocks) == 0 }

// addUser appends a text-only user-prompt block.
func (c *conversation) addUser(text string) {
	c.appendBlock(block{kind: blockUser, raw: text})
}

// addUserWithMedia appends a user-prompt block carrying media-part placeholders.
// media is one human-readable descriptor per non-text part (e.g. "image/png
// (inline)" / "audio/wav (url)"); the renderer shows each as a "📎 …" line below
// the text so a multimodal prompt is never silently rendered as text-only. With
// no media it is equivalent to addUser.
func (c *conversation) addUserWithMedia(text string, media []string) {
	c.appendBlock(block{kind: blockUser, raw: text, media: media})
}

// startAssistant opens a fresh, empty assistant block to accumulate deltas into.
// Called on turn.start so each turn is its own markdown block.
func (c *conversation) startAssistant() {
	c.appendBlock(block{kind: blockAssistant})
}

// appendAssistant appends streamed text to the current assistant block, opening
// one if the last block isn't an (unfinished) assistant block — defensive against
// a delta arriving before turn.start. The first answer text of a turn ends the
// "reasoning…" live affordance (the reasoning summary, if any, freezes into its
// static collapsed header).
func (c *conversation) appendAssistant(text string) {
	if b := c.currentAssistant(); b != nil {
		b.raw += text
		b.reasoningStreaming = false
		return
	}
	c.appendBlock(block{kind: blockAssistant, raw: text})
}

// reviseAssistant REPLACES the current assistant block's raw content with text
// (rather than growing it like appendAssistant). It opens a fresh assistant block
// if the last block is not one, mirroring appendAssistant's defensive shape.
//
// It exists for the streaming-floor benchmark (scrollback_bench_test.go): calling
// it with a same-LENGTH but byte-DIFFERENT string each op makes markdownAt's
// src-keyed cache MISS every op (render.go markdownAt keys on (src, width); the
// rev bump that currentAssistant() performs misses blockCache; a fresh per-block
// render bumps blockRenders, so the join cache also misses — the worst-case
// streaming floor where the whole scrollback re-joins each op) WITHOUT growing
// b.raw. Because the live block stays a fixed size, the per-op work is constant
// and allocs/op is INDEPENDENT of b.N — the join still all-misses (the worst-case
// streaming floor) but the live block does not grow. The mutation rides the
// currentAssistant() gateway (the codebase's sole rev-bump path for assistant
// blocks) so it never pokes block.rev directly.
func (c *conversation) reviseAssistant(text string) {
	if b := c.currentAssistant(); b != nil {
		b.raw = text
		b.reasoningStreaming = false
		return
	}
	c.appendBlock(block{kind: blockAssistant, raw: text})
}

// appendReasoning accumulates streamed reasoning-summary text into the current
// turn's assistant block. Reasoning is an attribute of that block (not a
// reordered sibling) precisely because reasoning and answer deltas can interleave
// within one turn — folding it in means a single reasoning region always renders,
// above the merged answer, with a truthful line count. A reasoning delta arriving
// before any assistant block (e.g. a delta racing turn.start) opens one rather
// than dropping the text.
func (c *conversation) appendReasoning(text string) {
	b := c.currentAssistant()
	if b == nil {
		c.appendBlock(block{kind: blockAssistant})
		b = &c.blocks[len(c.blocks)-1]
	}
	b.reasoning += text
	// Reasoning is still "live" only while the answer text has not started.
	if b.raw == "" {
		b.reasoningStreaming = true
	}
}

// endReasoningStream clears the live "reasoning…" affordance on the current
// assistant block (called on turn.end), so a turn that streamed reasoning but no
// answer text settles into the static collapsed header.
func (c *conversation) endReasoningStream() {
	if b := c.currentAssistant(); b != nil {
		b.reasoningStreaming = false
	}
}

// currentAssistant returns the trailing assistant block (the one being streamed
// into this turn) or nil when the last block is not an assistant block.
//
// It bumps the block's render revision (rev) before returning non-nil: it is the
// sole gateway for the assistant mutators (appendAssistant / appendReasoning /
// endReasoningStream), so bumping here keeps every mutation path invalidating the
// render cache. A read-only future caller pays only a spurious re-render of one
// block — never a stale frame.
func (c *conversation) currentAssistant() *block {
	if n := len(c.blocks); n > 0 && c.blocks[n-1].kind == blockAssistant {
		c.blocks[n-1].rev++
		return &c.blocks[n-1]
	}
	return nil
}

// addTurnStat appends a muted per-turn usage/elapsed stat line.
func (c *conversation) addTurnStat(text string) {
	c.appendBlock(block{kind: blockTurnStat, raw: text})
}

// addTool appends a running tool-call block.
func (c *conversation) addTool(id, name, args string) {
	c.appendBlock(block{
		kind:     blockTool,
		toolID:   id,
		toolName: name,
		toolArgs: args,
	})
}

// resolveTool marks the tool block matching callID (its toolID) as resolved with
// its result. Matching is by id only — never by tool name — mirroring the
// tool_call.id ⇄ tool_result.call_id contract. Returns false if no match (the
// caller can then render an orphan result notice). blocks carries the typed content
// blocks relayed from the server (resource links, images, …) so the renderer can
// surface user-audience artifacts distinctly; omitted/nil leaves the existing text
// path byte-unchanged. The variadic shape keeps the common text-only call sites
// (no blocks relayed) unchanged.
func (c *conversation) resolveTool(callID, body string, isErr bool, blocks ...client.ContentBlock) bool {
	for i := len(c.blocks) - 1; i >= 0; i-- {
		b := &c.blocks[i]
		if b.kind == blockTool && b.toolID == callID && !b.resolved {
			b.rev++ // render-visible mutation (a possibly NON-tail block): invalidate its cache entry
			b.resolved = true
			b.resultBody = body
			b.resultError = isErr
			if len(blocks) > 0 {
				b.resultBlocks = blocks
			}
			return true
		}
	}
	return false
}

// subagentBlock returns the unresolved Subagent tool block whose toolID matches
// parentCallID, or nil if none. Matching is by id only — the SAME contract as
// resolveTool — so a subagent.* event is attributed to its originating Subagent card
// even with several Subagent cards interleaved. It scans from the end so the most
// recent matching call wins.
//
// It bumps the block's render revision (rev) before returning non-nil: it is the
// gateway for the three subagent mutators (setSubagentStart / addSubagentTool /
// setSubagentEnd), all of which mutate render-visible subagent fields — possibly
// on a NON-tail block (a background subagent.end lands after later blocks were
// appended), so the renderer's per-block cache must be invalidated here.
func (c *conversation) subagentBlock(parentCallID string) *block {
	for i := len(c.blocks) - 1; i >= 0; i-- {
		b := &c.blocks[i]
		if b.kind == blockTool && b.toolID == parentCallID {
			b.rev++
			return b
		}
	}
	return nil
}

// setSubagentStart marks the Subagent block matching parentCallID as a subagent and
// records its goal title and routed-category metadata. Returns false when no
// matching block exists.
func (c *conversation) setSubagentStart(parentCallID, goal, routedCategory, routedModel, routingReason, model string) bool {
	b := c.subagentBlock(parentCallID)
	if b == nil {
		return false
	}
	b.subagent = true
	b.subGoal = goal
	b.subRoutedCategory = routedCategory
	b.subRoutedModel = routedModel
	b.subRoutingReason = routingReason
	b.subModel = model
	return true
}

// cloneRoutingDecision takes ownership of optional scalar presence as well as the
// value itself. UI state must not retain pointers owned by a transient client event.
func cloneRoutingDecision(in *client.RoutingDecision) *client.RoutingDecision {
	if in == nil {
		return nil
	}
	out := *in
	if in.Confidence != nil {
		v := *in.Confidence
		out.Confidence = &v
	}
	if in.MinimumConfidence != nil {
		v := *in.MinimumConfidence
		out.MinimumConfidence = &v
	}
	return &out
}

func (c *conversation) setSubagentRoutingDecision(parentCallID, childID string, decision *client.RoutingDecision) {
	if b := c.subagentBlock(parentCallID); b != nil {
		b.subRoutingDecision = cloneRoutingDecision(decision)
	}
	if childID != "" {
		c.fleetLane(childID).routingDecision = cloneRoutingDecision(decision)
	}
}

// addSubagentTool routes one subagent.tool projection into the matching Subagent
// block's trace per the event's InnerKind: a tool.call sets the live current tool
// and appends a pending chip (with its bounded arg preview); a tool.result
// finalises the chip (error state + result preview); a message.delta extends a
// capped message line. A turn.end carries the child's cumulative usage, which the
// live status line (subagentLiveLine) renders mid-run; the trace is capped at
// maxTraceEntries (oldest entries dropped); the count is the authoritative running
// total carried by the event, not len(trace). Returns false when no match.
func (c *conversation) addSubagentTool(msg client.SubagentMsg) bool {
	b := c.subagentBlock(msg.ParentCallID)
	if b == nil {
		return false
	}
	b.subagent = true
	// ToolCount and Usage are cumulative totals stamped on every projection (see
	// SubagentPayload docs), so they are assigned unconditionally — always current,
	// never 0-after-positive. A turn.end projection advances the live usage mid-run.
	b.subToolCount = msg.ToolCount
	b.subUsage = msg.Usage
	b.subTrace, b.subCurrent = routeTraceEvent(b.subTrace, b.subCurrent, msg.InnerKind, msg.ToolName, msg.Detail, msg.Text, msg.IsError)
	return true
}

// routeTraceEvent is the shared InnerKind→trace projection every delegation family
// routes its tool/message events through (subagent inline + fleet, parallel branch,
// team member), so the semantics (coalesce message deltas, resolve result onto the
// pending chip) can never drift between surfaces. It returns the updated trace and
// the lane's live current-tool name (cleared by nothing but the end accumulator).
func routeTraceEvent(trace []teamTrace, current, innerKind, toolName, detail, text string, isError bool) ([]teamTrace, string) {
	switch innerKind {
	case "message.delta", "result":
		trace = traceAppendMessage(trace, text)
	case "tool.result":
		trace = traceMarkToolResult(trace, toolName, detail, isError)
		if toolName != "" {
			current = toolName
		}
	default: // "tool.call" — or an older server's kind-less subagent.tool/branch_tool
		if toolName != "" {
			current = toolName
			trace = traceAppendTool(trace, toolName, detail, isError)
		}
	}
	return trace, current
}

// setSubagentEnd records the resolved end stats (usage, final tool count, stop,
// duration) on the matching Subagent block. Returns false when no match.
func (c *conversation) setSubagentEnd(parentCallID string, usage client.Usage, toolCount int, stop string, durationMs int64) bool {
	b := c.subagentBlock(parentCallID)
	if b == nil {
		return false
	}
	b.subagent = true
	b.subDone = true
	b.subUsage = usage
	b.subToolCount = toolCount
	b.subStop = stop
	b.subDurationMs = durationMs
	return true
}

// fleetLane returns the existing subagentLane for childID (creating one in first-seen
// order if absent), so the start/tool/end accumulators all converge on one lane per
// child. childID is the stable per-child discriminator carried on every subagent.*
// event — unlike the inline card (keyed by ParentCallID), the fleet keys by ChildID
// so two children of the SAME Subagent call are still distinct rows.
func (c *conversation) fleetLane(childID string) *subagentLane {
	if c.fleetIndex == nil {
		c.fleetIndex = make(map[string]int)
	}
	if i, ok := c.fleetIndex[childID]; ok {
		return &c.subagentFleet[i]
	}
	c.fleetIndex[childID] = len(c.subagentFleet)
	c.subagentFleet = append(c.subagentFleet, subagentLane{childID: childID})
	return &c.subagentFleet[len(c.subagentFleet)-1]
}

// fleetStart records a child's goal label, background mode, and routed-category
// metadata on its fleet lane (creating the lane). A missing childID is dropped:
// the fleet keys on ChildID, so without one there is no stable row — the inline
// card (keyed by ParentCallID) still renders regardless.
func (c *conversation) fleetStart(childID, goal, routedCategory, routedModel, routingReason, model string, background bool) {
	if childID == "" {
		return
	}
	ln := c.fleetLane(childID)
	ln.goal = goal
	ln.background = background
	ln.routedCategory = routedCategory
	ln.routedModel = routedModel
	ln.routingReason = routingReason
	ln.model = model
}

// fleetTool routes one subagent.tool projection into the fleet lane: the latest
// tool name (the per-row liveness signal), the running count (authoritative from
// the event), the last-error cue, and the shared trace (chips with bounded
// previews + capped message lines), mirroring addSubagentTool.
func (c *conversation) fleetTool(msg client.SubagentMsg) {
	if msg.ChildID == "" {
		return
	}
	ln := c.fleetLane(msg.ChildID)
	ln.isError = msg.IsError
	// ToolCount and Usage are cumulative totals stamped on every projection (see
	// SubagentPayload docs): assigned unconditionally, always current, never
	// 0-after-positive. A turn.end projection advances the live ↑↓ mid-run; end
	// still overwrites with the authoritative terminal figure via fleetEnd.
	ln.toolCount = msg.ToolCount
	ln.usage = msg.Usage
	ln.trace, ln.current = routeTraceEvent(ln.trace, ln.current, msg.InnerKind, msg.ToolName, msg.Detail, msg.Text, msg.IsError)
}

// fleetEnd records the resolved end stats on the fleet lane (done gates them), so the
// footer count and the Subagents-tab glyph flip to terminal.
func (c *conversation) fleetEnd(childID string, usage client.Usage, toolCount int, stop, cause string, durationMs int64) {
	if childID == "" {
		return
	}
	ln := c.fleetLane(childID)
	ln.done = true
	ln.usage = usage
	ln.toolCount = toolCount
	ln.stop = stop
	ln.cause = cause
	ln.durationMs = durationMs
}

// countDone classifies a slice into (running, done) by a per-element done predicate. It
// is the single loop behind the four agent-roster count helpers (subagentFleetCounts /
// fleetCounts / parallelGroupCounts / parallelCounts) — Rule of Three is met, so the loop
// body lives once here and each helper supplies only its element type + done accessor.
func countDone[T any](xs []T, done func(T) bool) (running, doneN int) {
	for i := range xs {
		if done(xs[i]) {
			doneN++
		} else {
			running++
		}
	}
	return running, doneN
}

// subagentFleetCounts classifies the fleet into (running, done). A child is done once
// its subagent.end arrived (lane.done); the rest are running. It is the footer
// segment's aggregate and the Subagents-tab header count.
func (c *conversation) subagentFleetCounts() (running, done int) {
	return fleetCounts(c.subagentFleet)
}

// hasSubagents reports whether ≥1 subagent has started this session — the gate for
// showing the fleet footer segment and enabling the f6 Subagents tab. The
// context-sensitive default tab (preferredAgentsTab) keys off this plus liveTeamBlock:
// it prefers Subagents whenever ANY subagent ran (running OR done, so a finished fleet
// is still reviewable, mirroring how the Teams tab reviews a finished team), unless a
// team is live.
func (c *conversation) hasSubagents() bool { return len(c.subagentFleet) > 0 }

// parallelBranch is the per-branch projection of ONE Parallel branch, keyed by its 0-based
// BranchIndex WITHIN a group. It mirrors subagentLane (a current tool, a capped trace,
// terminal stats) but is GROUPED under a parallelGroup — a Parallel run is a fan-out group,
// not a flat fleet. It carries the BOUNDED previews the parallel.* events forward
// (ADR 0079) — bounded, scrubbed, client-only (gauntlet #7 is about the conversation).
// workspace is the branch's fork-root path (a handle, not content).
type parallelBranch struct {
	index int
	// childID is the branch's child SESSION id ("parallel-<callID>-<i>") — the
	// CancelChild handle, arriving on branch_start/branch_end (D16). Empty from an
	// older server (the x cancel key then no-ops for the lane).
	childID string
	label   string
	goal    string
	// routedCategory/routedModel are the opt-in model router's bare metadata for this
	// branch (a category label + a model id), set on branch_start only when the router
	// classified it (ADR 0031 / ADR 0034); "" when unrouted. BARE metadata — never
	// branch content — so gauntlet #7 holds.
	routedCategory string
	routedModel    string
	// routingReason names WHY the router did not classify this branch (issue #397 /
	// ADR 0083); "" on a routed hit. BARE metadata — never branch content.
	routingReason   string
	routingDecision *client.RoutingDecision
	// model is the concrete model id this branch ACTUALLY ran on (issue #112 /
	// ADR 0035), regardless of how it was chosen; == routedModel when routed. BARE
	// metadata — never branch content — so gauntlet #7 holds.
	model      string
	current    string // latest branch tool name, "" when none yet
	trace      []teamTrace
	toolCount  int
	usage      client.Usage
	isError    bool // the most-recent branch tool errored (transient)
	done       bool
	failed     bool
	stop       string
	durationMs int64
}

// parallelGroup is the fan-out GROUP projection of ONE Parallel call, keyed by
// ParentCallID. It holds the run-level facts that have no home on a flat per-branch row —
// the join strategy, the single winner index (-1 = none/all), and the run-level
// stop, plus the ordered list of its branches. It is the grouped
// analogue of the subagentFleet (which is flat). branches preserves first-seen index order
// via branchIndex (BranchIndex → slot).
const maxParallelJoinModeLen = 24

// parallelJoinMode bounds untrusted server metadata once at ingestion so every
// roster, focus, essential, and compact projection reads the same safe value.
func parallelJoinMode(join string) string {
	return truncate(strings.Join(strings.Fields(sanitizeTerminal(join)), " "), maxParallelJoinModeLen)
}

type parallelGroup struct {
	parentCallID string
	join         string
	branchCount  int
	winner       int // -1 until parallel.end resolves a winner (join=all stays -1)
	stop         string
	done         bool
	branches     []parallelBranch
	branchIndex  map[int]int // BranchIndex → slot in branches
}

// parallelGroups is the insertion-ordered collection of Parallel groups keyed by
// ParentCallID; parallelIndex maps ParentCallID → its slot so repeated branch/end events
// update the same group in O(1). Like subagentFleet it is part of the conversation, so a
// /clear (which rebuilds the conversation) drops it too.
//
// (These live on the conversation struct; declared here next to the helpers for locality.)

// parallelGroupFor returns the existing parallelGroup for parentCallID (creating one in
// first-seen order if absent). winner defaults to -1 (no winner yet / join=all).
func (c *conversation) parallelGroupFor(parentCallID string) *parallelGroup {
	if c.parallelIndex == nil {
		c.parallelIndex = make(map[string]int)
	}
	if i, ok := c.parallelIndex[parentCallID]; ok {
		return &c.parallelGroups[i]
	}
	c.parallelIndex[parentCallID] = len(c.parallelGroups)
	c.parallelGroups = append(c.parallelGroups, parallelGroup{
		parentCallID: parentCallID,
		winner:       -1,
		branchIndex:  map[int]int{},
	})
	return &c.parallelGroups[len(c.parallelGroups)-1]
}

// parallelBranchFor returns the branch slot for index within a group (creating one in
// first-seen order if absent), so the branch_start/tool/end accumulators converge on one
// branch per index.
func (g *parallelGroup) parallelBranchFor(index int) *parallelBranch {
	if g.branchIndex == nil {
		g.branchIndex = map[int]int{}
	}
	if i, ok := g.branchIndex[index]; ok {
		return &g.branches[i]
	}
	g.branchIndex[index] = len(g.branches)
	g.branches = append(g.branches, parallelBranch{index: index})
	return &g.branches[len(g.branches)-1]
}

// parallelStart records the run-level join + branch count on a group (creating it). A
// missing parentCallID is dropped (no stable group key).
func (c *conversation) parallelStart(parentCallID, join string, branchCount int) {
	if parentCallID == "" {
		return
	}
	g := c.parallelGroupFor(parentCallID)
	g.join = parallelJoinMode(join)
	g.branchCount = branchCount
}

// parallelBranchStart records a branch's label + goal + child id + routed-category
// metadata on its group branch (creating both). The child id (the CancelChild handle)
// is set only when non-empty, so a later event from an older server never erases a
// known id.
func (c *conversation) parallelBranchStart(parentCallID string, index int, childID, label, goal, routedCategory, routedModel, routingReason, model string) {
	if parentCallID == "" {
		return
	}
	br := c.parallelGroupFor(parentCallID).parallelBranchFor(index)
	if childID != "" {
		br.childID = childID
	}
	br.label = label
	br.goal = goal
	br.routedCategory = routedCategory
	br.routedModel = routedModel
	br.routingReason = routingReason
	br.model = model
}

func (c *conversation) setParallelRoutingDecision(parentCallID string, index int, decision *client.RoutingDecision) {
	if parentCallID == "" {
		return
	}
	c.parallelGroupFor(parentCallID).parallelBranchFor(index).routingDecision = cloneRoutingDecision(decision)
}

// parallelBranchTool routes one branch_tool projection into the branch lane: the
// latest tool name (liveness), the running count, the last-error cue, and the
// shared trace (chips with bounded previews + capped message lines), mirroring
// fleetTool.
func (c *conversation) parallelBranchTool(msg client.ParallelMsg) {
	if msg.ParentCallID == "" {
		return
	}
	br := c.parallelGroupFor(msg.ParentCallID).parallelBranchFor(msg.BranchIndex)
	br.isError = msg.IsError
	br.toolCount = msg.ToolCount
	br.trace, br.current = routeTraceEvent(br.trace, br.current, msg.InnerKind, msg.ToolName, msg.Detail, msg.Text, msg.IsError)
}

// parallelBranchEnd records a branch's resolved terminal stats (done gates them).
// The child id backfills defensively (branch_start can be missed), only when
// non-empty.
func (c *conversation) parallelBranchEnd(parentCallID string, index int, childID string, usage client.Usage, toolCount int, stop string, failed bool, durationMs int64) {
	if parentCallID == "" {
		return
	}
	br := c.parallelGroupFor(parentCallID).parallelBranchFor(index)
	if childID != "" {
		br.childID = childID
	}
	br.done = true
	br.usage = usage
	br.toolCount = toolCount
	br.stop = stop
	br.failed = failed
	br.durationMs = durationMs
}

// parallelEnd records the run-level terminal facts on a group.
func (c *conversation) parallelEnd(parentCallID, join string, branchCount, winner int, stop string) {
	if parentCallID == "" {
		return
	}
	g := c.parallelGroupFor(parentCallID)
	g.done = true
	g.join = parallelJoinMode(join)
	if branchCount > 0 {
		g.branchCount = branchCount
	}
	g.winner = winner
	g.stop = stop
}

// parallelGroupCounts classifies the Parallel groups into (running, done). A group is done
// once its parallel.end arrived. Footer segment aggregate + Parallel-tab header count.
func (c *conversation) parallelGroupCounts() (running, done int) {
	return parallelCounts(c.parallelGroups)
}

// hasParallel reports whether ≥1 Parallel run has started this session — the gate for the
// fleet footer segment and the f6 Parallel tab (mirroring hasSubagents).
func (c *conversation) hasParallel() bool { return len(c.parallelGroups) > 0 }

// liveParallel reports whether any Parallel group is still RUNNING (no parallel.end yet) —
// the "richest live surface" signal for the default-tab precedence (mirroring how
// liveTeamBlock gates the Teams default).
func (c *conversation) liveParallel() bool {
	for i := range c.parallelGroups {
		if !c.parallelGroups[i].done {
			return true
		}
	}
	return false
}

// teamBlock returns the Team tool block whose toolID matches parentCallID, or nil
// if none. Matching is by id only — the SAME contract as resolveTool/subagentBlock
// — so a team.* event is attributed to its originating Team card even with several
// tool cards interleaved. It scans from the end so the most recent match wins.
//
// It bumps the block's render revision (rev) before returning non-nil: it is the
// gateway for the five team mutators (setTeamStart / addTeamMember / setTeamEnd /
// setTeamTasks / setTeamFindings) — note setTeamTasks/setTeamFindings flip the
// render-visible b.team flag even though tasks/findings themselves render only in
// the f6 overlay, so they invalidate too. latestTeamBlock/liveTeamBlock (the
// overlay READ path) deliberately do NOT bump.
func (c *conversation) teamBlock(parentCallID string) *block {
	for i := len(c.blocks) - 1; i >= 0; i-- {
		b := &c.blocks[i]
		if b.kind == blockTool && b.toolID == parentCallID {
			b.rev++
			return b
		}
	}
	return nil
}

// setTeamStart marks the Team block matching parentCallID as a team, records its
// stable team id (for the live footer summary segment), and seeds its per-member
// lanes from the roster (in roster order). teamID is set only when non-empty so a
// later defensive set from a team.member/team.end event never erases a known id.
// Returns false when no matching block exists.
func (c *conversation) setTeamStart(parentCallID, teamID string, roster []client.TeamMemberSpec) bool {
	b := c.teamBlock(parentCallID)
	if b == nil {
		return false
	}
	b.team = true
	if teamID != "" {
		b.teamID = teamID
	}
	b.teamLanes = make([]teamLane, 0, len(roster))
	for _, m := range roster {
		b.teamLanes = append(b.teamLanes, teamLane{
			name:            m.Name,
			role:            m.Role,
			mutating:        m.Mutating,
			lead:            m.Lead,
			routedCategory:  m.RoutedCategory,
			routedModel:     m.RoutedModel,
			routingReason:   m.RoutingReason,
			routingDecision: cloneRoutingDecision(m.RoutingDecision),
			model:           m.Model,
		})
	}
	return true
}

// lane returns a pointer to the lane for member, creating one (appended in
// arrival order) if the roster did not list it — so a team.member event is never
// dropped just because team.start was missed or the roster was partial.
func (b *block) lane(member string) *teamLane {
	for i := range b.teamLanes {
		if b.teamLanes[i].name == member {
			return &b.teamLanes[i]
		}
	}
	b.teamLanes = append(b.teamLanes, teamLane{name: member})
	return &b.teamLanes[len(b.teamLanes)-1]
}

// addTeamMember routes one team.member projection to its member lane on the Team
// block matching parentCallID, accumulating per the inner kind: a message.delta
// appends/extends a message trace line; a tool.call sets the lane's current tool
// and appends a (pending) tool chip; a tool.result finalises the chip's error
// state; a turn.end/result carries usage and (for result) marks the lane IDLE
// (finished its round, not terminal); forward activity (delta / tool.call /
// turn.end) clears idle again. Team-terminal lives on block.teamDone, set only by
// setTeamEnd — never on a lane. The trace is capped at maxTeamTrace (oldest
// entries dropped). Returns false when no matching Team block exists.
func (c *conversation) addTeamMember(msg client.TeamMsg) bool {
	b := c.teamBlock(msg.ParentCallID)
	if b == nil {
		return false
	}
	b.team = true
	// Defensively backfill the team id: team.start can be missed (the same race the
	// lane() fallback guards against), so a team.member carrying the id seeds it.
	// Only overwrite when non-empty so a known id is never erased.
	if msg.TeamID != "" {
		b.teamID = msg.TeamID
	}
	ln := b.lane(msg.Member)
	// Backfill the member's session id (the CancelChild handle) from any member event
	// carrying it; only overwrite when non-empty so a known id is never erased.
	if msg.MemberSessionID != "" {
		ln.sessionID = msg.MemberSessionID
	}
	switch msg.InnerKind {
	case "message.delta":
		ln.idle = false
		ln.appendMessage(msg.Text)
	case "tool.call":
		ln.idle = false
		ln.current = msg.ToolName
		ln.toolCount++
		ln.appendTool(msg.ToolName, msg.Detail, false)
	case "tool.result":
		ln.markToolResult(msg.ToolName, msg.Detail, msg.IsError)
	case "turn.end":
		ln.idle = false
		ln.usage = sumUsage(ln.usage, msg.Usage)
		// The context meter tracks CURRENT occupancy, not cumulative cost: assign the
		// most recent turn's input tokens (matching the main meter's
		// m.contextTokens = msg.Usage.InputTokens), and keep the window sticky so a
		// later turn.end that omits it (0) does not erase a known denominator.
		ln.ctxUsed = msg.Usage.InputTokens
		if msg.ContextWindow > 0 {
			ln.ctxWindow = msg.ContextWindow
		}
	case "result":
		ln.idle = true
		ln.current = ""
		if msg.Usage != (client.Usage{}) {
			ln.usage = sumUsage(ln.usage, msg.Usage)
		}
		if msg.Text != "" {
			ln.appendMessage(msg.Text)
		}
		if msg.Cause != "" {
			ln.cause = msg.Cause // last failed round's cause wins; rendered only when benched (stopped/error)
		}
	}
	return true
}

// appendMessage adds a member message line to the lane trace via the shared
// traceAppendMessage projection (coalescing streamed deltas).
func (ln *teamLane) appendMessage(text string) {
	ln.trace = traceAppendMessage(ln.trace, text)
}

// appendTool adds a tool chip (pending; error + result detail resolved later by
// markToolResult). detail here is the call's bounded arg preview.
func (ln *teamLane) appendTool(name, detail string, isError bool) {
	ln.trace = traceAppendTool(ln.trace, name, detail, isError)
}

// markToolResult finalises the most recent matching pending tool chip via the
// shared traceMarkToolResult projection (result preview supersedes the arg preview).
func (ln *teamLane) markToolResult(name, detail string, isError bool) {
	ln.trace = traceMarkToolResult(ln.trace, name, detail, isError)
}

// setTeamEnd records the resolved end stats (rounds, stop, summed usage) on the
// Team block matching parentCallID, defensively backfilling the team id (only when
// non-empty) in case team.start was missed. It also applies the per-member terminal
// disposition snapshot onto the matching lanes (by name), so a stopped member renders
// "✗ stopped — <reason>" instead of the blanket "✓ done" once the team has ended.
// Returns false when no match.
func (c *conversation) setTeamEnd(parentCallID, teamID string, rounds int, stop string, usage client.Usage, dispositions []client.TeamMemberDisposition) bool {
	b := c.teamBlock(parentCallID)
	if b == nil {
		return false
	}
	b.team = true
	if teamID != "" {
		b.teamID = teamID
	}
	b.teamDone = true
	b.teamRounds = rounds
	b.teamStop = stop
	b.teamUsage = usage
	for _, d := range dispositions {
		ln := b.lane(d.Name)
		ln.stopped = d.Stopped
		ln.stopReason = d.Reason
		ln.errorRounds = d.ErrorRounds
	}
	return true
}

// setTeamTasks replaces the shared task-list snapshot on the Team block matching
// parentCallID. The server emits a fresh full snapshot on every task transition
// (de-duped on change), so a replace is correct — the latest snapshot is the whole
// truth. Returns false when no match. Member content is never touched.
func (c *conversation) setTeamTasks(parentCallID string, tasks []client.TeamTask) bool {
	b := c.teamBlock(parentCallID)
	if b == nil {
		return false
	}
	b.team = true
	out := make([]teamTask, 0, len(tasks))
	for _, tk := range tasks {
		deps := make([]string, len(tk.Deps))
		copy(deps, tk.Deps)
		out = append(out, teamTask{
			id:       tk.ID,
			desc:     tk.Description,
			state:    tk.State,
			assignee: tk.Assignee,
			deps:     deps,
		})
	}
	b.teamTasks = out
	return true
}

// setTeamFindings replaces the cached findings-ledger snapshot on the Team block
// identified by parentCallID. Like setTeamTasks, the server emits a fresh full
// snapshot on every change (de-duped), so a replace is correct. Returns false on no
// match. Findings carry only the recording member + a bounded body preview.
func (c *conversation) setTeamFindings(parentCallID string, findings []client.TeamFinding) {
	b := c.teamBlock(parentCallID)
	if b == nil {
		return
	}
	b.team = true
	out := make([]teamFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, teamFinding{member: f.Member, body: f.Body})
	}
	b.teamFindings = out
}

// latestTeamBlock returns the most-recent tool block that carries team lanes (a
// Team card with at least one member lane), or nil if no team has been seen this
// session. It scans from the end so a fresh team supersedes an earlier one — the
// f6 overlay always reflects the latest team. The block is returned by
// pointer so the overlay reads the live, accumulating lane state (it never
// mutates it). A team card with no lanes yet (team.start not seen, or empty
// roster) is skipped so the overlay never opens onto an empty roster.
func (c *conversation) latestTeamBlock() *block {
	for i := len(c.blocks) - 1; i >= 0; i-- {
		b := &c.blocks[i]
		if b.kind == blockTool && b.team && len(b.teamLanes) > 0 {
			return b
		}
	}
	return nil
}

// liveTeamBlock returns the latest team block that is still RUNNING (not
// teamDone) — the footer's live-activity signal. Distinct from latestTeamBlock,
// which returns the most-recent team done-or-not so the f6 overlay can still
// review a finished roster.
func (c *conversation) liveTeamBlock() *block {
	b := c.latestTeamBlock()
	if b == nil || b.teamDone {
		return nil
	}
	return b
}

// addNotice appends a muted info block (compaction / permission verb).
func (c *conversation) addNotice(text string) {
	c.appendBlock(block{kind: blockNotice, raw: text})
}

// addRecoverNotice appends a WARNING-styled recover-notice block (a session that
// failed on a PERMANENT provider error was recovered for re-entry). Unlike a
// compaction notice, this is ACTIONABLE (start a new session / change the
// request), so it renders as a durable ⚠ warning block rather than a muted
// bullet — and durable rather than a transient statusMsg so the run's first
// event does not overwrite it before the user reads it.
func (c *conversation) addRecoverNotice(text string) {
	c.appendBlock(block{kind: blockNotice, raw: text, recover: true})
}

// addDelivery appends a fire-result delivery note block: a scheduled-task
// affordance + the schedule name + the outcome body, visually distinct from a
// user prompt, the model's text, and a notice. scheduleName + fireID label the
// card; text is the full recorded note (the same fenced-untrusted content the
// engine recorded) — the renderer strips the fence markers + redundant
// provenance header for display (they are machine markers, not content).
func (c *conversation) addDelivery(scheduleName, fireID, text string) {
	c.appendBlock(block{
		kind:           blockDelivery,
		toolName:       scheduleName, // reused for the schedule-name label
		deliveryFireID: fireID,
		raw:            text,
	})
}

// addHook appends a structured hook-notice block carrying the lifecycle phase,
// the related tool (per-tool phases), and the decision. The renderer styles it
// distinctly from a plain notice — a hook glyph + phase, with the outcome
// coloured (blocked stands out from a benign info/modified notice).
func (c *conversation) addHook(text, phase, tool, decision string) {
	c.appendBlock(block{
		kind:         blockHook,
		raw:          text,
		hookPhase:    phase,
		hookTool:     tool,
		hookDecision: decision,
	})
}

// addError appends an error block.
func (c *conversation) addError(text string) {
	c.appendBlock(block{kind: blockError, raw: text})
}

// addPermanentError appends a permanent-error block: the error is a server-classified
// PERMANENT provider rejection and retrying cannot help. The renderer shows a one-line
// human summary; the raw error payload is available on expand (ctrl+t).
func (c *conversation) addPermanentError(text string) {
	c.appendBlock(block{kind: blockError, raw: text, permanent: true})
}
