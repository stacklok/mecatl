// Package client is the gRPC-facing layer of mecatui: it dials mecated, creates
// sessions, opens the bidi Converse stream, and translates proto Events into the
// plain Go tea.Msg structs the ui consumes. It is the ONLY mecatui package that
// imports contracts/gen + grpc; the ui never sees a proto type. This boundary is
// deliberate: it keeps the Elm model rendering "pure data" and makes the whole
// event pipeline testable from a scripted fake (see Recver / fakeStream in
// tests) with no network.
package client

import (
	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The msg taxonomy: one struct per proto Event type, plus transport/lifecycle
// msgs that don't originate from the stream. These are plain data — no proto,
// no grpc — so ui can switch over them freely.

// SessionInitMsg marks the run stream as live (proto type "session.init").
type SessionInitMsg struct{ Seq int64 }

// TurnStartMsg opens a new assistant turn; the spinner starts here.
type TurnStartMsg struct{ Turn int32 }

// AssistantDeltaMsg is a streamed chunk of assistant markdown to append+rerender.
type AssistantDeltaMsg struct {
	Turn int32
	Text string
}

// ReasoningDeltaMsg is a streamed chunk of the model's human-readable reasoning
// summary for a turn. Display-only and clearly subordinate to the assistant
// text; the ui renders it collapsed by default.
type ReasoningDeltaMsg struct {
	Turn int32
	Text string
}

// TurnEndMsg closes a turn's model exchange, carrying that turn's token usage
// and the elapsed model-call time. DurationMs is 0 when the server had no clock.
type TurnEndMsg struct {
	Turn       int32
	Usage      Usage
	DurationMs int64
}

// ToolCallMsg announces a tool invocation (status: running until its result).
type ToolCallMsg struct {
	ID   string
	Name string
	Args string // raw JSON
}

// ToolResultMsg resolves the matching ToolCallMsg by CallID. Content is the legacy
// model-facing string result body; Blocks carries the typed content blocks (when the
// server relayed them — resource links, images, etc.) so the ui can render user-
// audience artifacts distinctly from/below the model-facing text. Both may be set:
// the blocks are rendered IN ADDITION to Content (the model-facing text body), not as
// a replacement. StructuredContent is the JSON-stringified structured payload mirror.
type ToolResultMsg struct {
	CallID            string
	Content           string
	IsError           bool
	Blocks            []ContentBlock
	StructuredContent string
}

// ContentBlock is the plain-data mirror of mecatlv1.ContentBlock, duplicated here so
// the ui stays proto-free (the same discipline as Usage). Only the fields the ui
// renders are carried: the kind discriminant plus the per-kind payload (mime/data/url
// for media, text for text/embedded/structured, and the resource-link name/uri).
type ContentBlock struct {
	// Kind is the block kind (text/image/audio/resource-link/embedded-resource/
	// structured-content). UNSPECIFIED is treated as absent by the renderer.
	Kind ContentBlockKind
	// MimeType is the IANA media type of inline bytes (image/audio/blob).
	MimeType string
	// Data is the inline content bytes (image/audio/blob).
	Data []byte
	// URL is the remote reference (resource-link URI, or URL-sourced media).
	URL string
	// Text is the text payload (text, embedded-resource text, or structured-content JSON).
	Text string
	// Name is the resource-link name.
	Name string
	// Title is the resource-link title.
	Title string
	// Description is the resource-link description.
	Description string
}

// ContentBlockKind discriminates a ContentBlock, mirroring the proto ContentBlock.Kind
// enum as a plain string so the ui keys off it without importing proto.
type ContentBlockKind string

const (
	// ContentBlockUnspecified is the zero value; consumers treat it as absent.
	ContentBlockUnspecified ContentBlockKind = ""
	// ContentBlockText is a text block (already represented in the model-facing body).
	ContentBlockText ContentBlockKind = "text"
	// ContentBlockImage is an inline image block.
	ContentBlockImage ContentBlockKind = "image"
	// ContentBlockAudio is an inline audio block.
	ContentBlockAudio ContentBlockKind = "audio"
	// ContentBlockResourceLink is a reference to an MCP resource by URI.
	ContentBlockResourceLink ContentBlockKind = "resource_link"
	// ContentBlockEmbeddedResource is an embedded MCP resource (text or blob).
	ContentBlockEmbeddedResource ContentBlockKind = "embedded_resource"
	// ContentBlockStructuredContent is a JSON structured-content block.
	ContentBlockStructuredContent ContentBlockKind = "structured_content"
)

// ToolProgressMsg is a transient, human-readable progress line from a
// long-running tool (proto type "tool.progress"). It carries no call id and no
// content — only advisory Text. The ui shows it as a transient status line while
// a tool runs and clears it on the next tool.result (or turn boundary); it is
// never persisted and never enters the conversation transcript.
type ToolProgressMsg struct {
	Text string
}

// PermissionAskMsg opens the approval modal; AskID is the exact correlation key
// echoed back in ResumeApproval — never inferred from the tool name.
type PermissionAskMsg struct {
	AskID  string
	Tool   string
	Args   string // raw JSON
	Reason string
}

// PermissionRetractMsg withdraws a previously surfaced permission ask: the
// owning subagent was cancelled while parked on it, so there is nothing left to
// approve. The ui keeps a FIFO queue of surfaced asks behind the visible modal
// (concurrent subagents can surface asks concurrently): a retract matching the
// VISIBLE ask dismisses the modal and advances the queue; a retract matching a
// QUEUED ask removes it in place; an unknown/stale id is ignored (idempotent).
type PermissionRetractMsg struct {
	AskID string
}

// HookDecision is the outcome a hook fire produced, as plain data the ui colours
// and ranks without touching proto. Mirrors mecatlv1.HookDecision.
type HookDecision string

const (
	// HookInfo is a benign, informational hook notice (the default).
	HookInfo HookDecision = "info"
	// HookBlocked means the hook vetoed the action — the most severe notice.
	HookBlocked HookDecision = "blocked"
	// HookModified means the hook rewrote the action's payload without blocking.
	HookModified HookDecision = "modified"
	// HookAdvisory means the hook flagged content as a finding but did NOT alter
	// the call/result (advisory guardrail). Client-visible warning, model-invisible.
	HookAdvisory HookDecision = "advisory"
)

// HookMsg is an inline hook notice. Beyond the human-readable Text it carries the
// structured Phase (lifecycle point, e.g. "PreToolUse"), the related Tool (for
// per-tool phases), and the Decision (info/blocked/modified/advisory) so the ui can
// render it distinctly from a compaction notice and colour a blocked or advisory hook.
type HookMsg struct {
	Text     string
	Phase    string
	Tool     string
	Decision HookDecision
}

// SubagentKind discriminates the three subagent.* event kinds carried by a
// SubagentMsg, so the ui switches on a plain value rather than re-deriving it.
type SubagentKind string

const (
	// SubagentStart marks a Subagent tool run beginning (Goal set).
	SubagentStart SubagentKind = "start"
	// SubagentTool marks a child tool call resolving (ToolName/IsError/ToolCount set).
	SubagentTool SubagentKind = "tool"
	// SubagentEnd marks a Subagent tool run finishing (ToolCount/Usage/Stop/DurationMs set).
	SubagentEnd SubagentKind = "end"
)

// SubagentMsg is the BOUNDED projection of a Subagent tool's child run. It carries
// ids, a goal label, child tool names/counts, usage, stop, and duration — plus the
// BOUNDED content previews the server clamp-scrubs per ADR 0079 (InnerKind/Text/
// Detail on a subagent.tool event), so the ui renders a subagent's activity under
// its Subagent card while the previews stay bounded, scrubbed, and client-only
// (never entering the parent conversation — gauntlet #7). ParentCallID attributes
// the msg to the originating Subagent tool block.
// Background marks a detached-delivery (background: true) child; the server sets
// it on subagent.start only, and an older server yields false (no marker).
// RoutedCategory/RoutedModel are the OPT-IN semantic model router's bare
// metadata (a category label + a model id) on subagent.start, empty when no
// router classified the delegation (ADR 0031) — never child content, so
// gauntlet #7 holds.
type SubagentMsg struct {
	Kind           SubagentKind
	ParentCallID   string
	ChildID        string
	Goal           string
	Background     bool
	RoutedCategory string
	RoutedModel    string
	// Model is the concrete model id the child ACTUALLY ran on (subagent.start only),
	// regardless of how it was chosen — inherited default, agent-def pin, per-call
	// override, or the opt-in router (issue #112 / ADR 0035). When routed, Model ==
	// RoutedModel. Bare metadata, never child content, so gauntlet #7 holds.
	Model    string
	ToolName string
	IsError  bool
	// InnerKind / Text / Detail are the ADR-0079 bounded previews, set on a
	// subagent.tool event per the child's forwarded inner event kind:
	// InnerKind is "tool.call" (Detail = the bounded args preview), "tool.result"
	// (Detail = the bounded result preview), or "message.delta" (Text = the capped
	// message text); empty on start/end and from an older server (the ui then renders
	// a bare chip, exactly the pre-ADR-0079 shape). The server clamp-scrubs every
	// preview (control bytes out, ≤200 runes); the ui caps again on render. ToolCount
	// (the running total) rides every subagent.tool event regardless of InnerKind.
	InnerKind  string
	Text       string
	Detail     string
	ToolCount  int
	Usage      Usage
	Stop       string
	DurationMs int64
	// Cause is the child run's FAILURE DETAIL on subagent.end when Stop is "error"
	// (empty otherwise): the harness/provider error the server recorded on the
	// terminal result (issue #319). It is METADATA about how the delegation failed —
	// a transport/loop error string, never child-authored output — so gauntlet #7
	// holds. Server-clamped; an older server yields "".
	Cause string
}

// TeamKind discriminates the three team.* event kinds carried by a TeamMsg, so
// the ui switches on a plain value rather than re-deriving it from the proto.
type TeamKind string

const (
	// TeamStart marks a Team run beginning (Roster set).
	TeamStart TeamKind = "start"
	// TeamMember marks one forwarded member-session event (Member/InnerKind set,
	// plus the subset of Text/ToolName/Detail/IsError/Usage relevant to InnerKind).
	TeamMember TeamKind = "member"
	// TeamEnd marks a Team run finishing (Rounds/Stop/Usage set).
	TeamEnd TeamKind = "end"
	// TeamTasks marks a snapshot of the team's shared task list (Tasks set). It maps
	// 1:1 from the first-class team.tasks proto Event.Type — a team-wide event with no
	// Member — so the ui switches on a clean discriminant.
	TeamTasks TeamKind = "tasks"
	// TeamFindings marks a snapshot of the team's shared findings ledger (Findings
	// set). It maps 1:1 from the first-class team.findings proto Event.Type — a
	// team-wide event with no Member — mirroring TeamTasks.
	TeamFindings TeamKind = "findings"
)

// TeamTask is one entry in the team's shared task list, as plain data the ctrl+a
// agents task sub-view renders. Mirrors mecatlv1.TeamTask; carries only task
// metadata, never member content. Deps are the task ids this task waits on.
type TeamTask struct {
	ID          string
	Description string
	State       string
	Assignee    string
	Deps        []string
}

// TeamFinding is one entry in the team's shared findings ledger, as plain data the
// ctrl+a agents findings view renders. Mirrors mecatlv1.TeamFinding; carries only
// the recording member's name and a bounded body preview, never the raw finding.
type TeamFinding struct {
	Member string
	Body   string
}

// TeamMemberDisposition is one member's TERMINAL disposition, set on a TeamEnd msg.
// Mirrors mecatlv1.TeamMemberDisposition; closed-enum supervisor verdicts only, never
// member content. Stopped distinguishes a non-resumable/budget-exhausted member from a
// clean one; Reason refines a stop ("error"/"cancelled"/"budget"; empty when not
// stopped). ErrorRounds counts the member's run-level failures, which a bounded retry
// can leave behind on a member that FINISHED (issue #318) — so it is the only signal
// that a not-stopped member's run was not clean.
type TeamMemberDisposition struct {
	Name        string
	Stopped     bool
	Reason      string
	ErrorRounds int
}

// TeamMemberSpec is one roster entry forwarded on team.start, as plain data.
// Mirrors mecatlv1.TeamMemberSpec; carries only member metadata, never content.
type TeamMemberSpec struct {
	Name     string
	Role     string
	Mutating bool
	Lead     bool
	// RoutedCategory/RoutedModel are the OPT-IN semantic model router's bare metadata
	// (a category label + a model id) for a routed member, empty when no router
	// classified the member (ADR 0031 / ADR 0034) — never member content, so gauntlet
	// #7 holds.
	RoutedCategory string
	RoutedModel    string
	// Model is the concrete model id the member's engine ACTUALLY runs on (team.start
	// roster only), regardless of how it was chosen (issue #112 / ADR 0035). When routed,
	// Model == RoutedModel. Bare metadata, never member content, so gauntlet #7 holds.
	Model string
}

// TeamMsg is the BOUNDED projection of an in-process team's run, as plain data
// the ui renders on the Team tool card. Unlike the metadata-only SubagentMsg, a
// team.member event carries BOUNDED member CONTENT (Text / a capped Detail
// preview) — the team is meant to be watched. It is still bounded and redacted
// server-side, and never enters the parent conversation. ParentCallID attributes
// the msg to the originating Team tool block.
type TeamMsg struct {
	Kind         TeamKind
	ParentCallID string
	TeamID       string
	// Roster is set on TeamStart.
	Roster []TeamMemberSpec
	// Member / InnerKind and the per-event content are set on TeamMember.
	Member string
	// MemberSessionID is the member's child SESSION id ("team-<teamID>-<member>") —
	// the CancelChild handle, set on TeamMember msgs. Carried explicitly so the ui
	// never derives the (server-internal) id grammar; empty from an older server.
	MemberSessionID string
	InnerKind       string
	Text            string
	ToolName        string
	Detail          string
	IsError         bool
	// Rounds / Stop are set on TeamEnd.
	Rounds int
	Stop   string
	// Usage is a member's per-event usage (TeamMember turn.end/result) or, on
	// TeamEnd, the summed team total.
	Usage Usage
	// ContextUsed / ContextWindow are the per-member context-meter numerator
	// (current context occupancy — the most recent turn's input tokens) and
	// denominator (the member engine's context window), set on TeamMember turn.end;
	// 0 when unknown. They drive the band bar on each member lane in the ctrl+a
	// agents overlay.
	ContextUsed   int64
	ContextWindow int64
	// Tasks is the team's shared task-list snapshot, set on a TeamTasks msg (the
	// first-class team.tasks event) and on TeamEnd. It feeds the ctrl+a agents task
	// sub-view.
	Tasks []TeamTask
	// Findings is the team's shared findings-ledger snapshot, set on a TeamFindings
	// msg (the first-class team.findings event) and on TeamEnd. It feeds the ctrl+a
	// agents findings view.
	Findings []TeamFinding
	// Dispositions is the per-member terminal disposition snapshot, set on a TeamEnd
	// msg. It lets the overlay render a stopped member distinctly from a clean "done".
	Dispositions []TeamMemberDisposition
}

// ParallelKind discriminates the parallel.* event kinds carried by a ParallelMsg, so
// the ui switches on a plain value rather than re-deriving it from the proto. The
// run-level start/end are their own kinds; the per-branch events carry the branch_*
// kinds (from the proto Parallel.kind discriminant).
type ParallelKind string

const (
	// ParallelStart marks a Parallel fork-join run beginning (Join/BranchCount set).
	ParallelStart ParallelKind = "start"
	// ParallelBranchStart marks one branch beginning (BranchIndex/BranchLabel/Goal set).
	ParallelBranchStart ParallelKind = "branch_start"
	// ParallelBranchTool marks one branch's child tool resolving (ToolName/IsError/ToolCount set).
	ParallelBranchTool ParallelKind = "branch_tool"
	// ParallelBranchEnd marks one branch finishing (Stop/Usage/DurationMs/Failed/Workspace set).
	ParallelBranchEnd ParallelKind = "branch_end"
	// ParallelEnd marks a Parallel run finishing (Join/Winner/WinnerWorkspace/Usage/Stop set).
	ParallelEnd ParallelKind = "end"
)

// ParallelMsg is the BOUNDED projection of a Parallel fork-join run, as plain data
// the ui renders in the ctrl+a Parallel tab. Unlike the FLAT SubagentMsg, a
// Parallel run is a GROUP: N branches of ONE call (keyed by ParentCallID) sharing a
// join strategy + a single winner + preserved per-branch fork paths. It carries ids,
// a goal label, child tool names/counts, usage, stop, duration, the join strategy,
// the winner index, the fork-root PATHS (handles already in the result text, not
// branch content) — plus the BOUNDED content previews the server clamp-scrubs per
// ADR 0079 (InnerKind/Text/Detail on a branch_tool event), bounded, scrubbed, and
// client-only (never entering the parent conversation — gauntlet #7).
// ParentCallID is the group key.
type ParallelMsg struct {
	Kind         ParallelKind
	ParentCallID string
	// Join / BranchCount are set on ParallelStart and ParallelEnd (run-level).
	Join        string
	BranchCount int
	// BranchIndex is the stable per-branch key, set on every branch_* kind.
	BranchIndex int
	// ChildID is the branch's child SESSION id ("parallel-<callID>-<i>") — the
	// CancelChild handle, set on branch_start/branch_end. Carried explicitly so the
	// ui never derives the (server-internal) id grammar; empty from an older server.
	ChildID string
	// BranchLabel / Goal are set on ParallelBranchStart.
	BranchLabel string
	Goal        string
	// RoutedCategory/RoutedModel are the OPT-IN semantic model router's bare metadata
	// (a category label + a model id) for a routed branch, set on branch_start only,
	// empty when no router classified the branch (ADR 0031 / ADR 0034) — never branch
	// content, so gauntlet #7 holds.
	RoutedCategory string
	RoutedModel    string
	// Model is the concrete model id the branch ACTUALLY ran on (branch_start only),
	// regardless of how it was chosen (issue #112 / ADR 0035). When routed, Model ==
	// RoutedModel. Bare metadata, never branch content, so gauntlet #7 holds.
	Model string
	// ToolName / IsError / ToolCount carry per-branch tool activity (branch_tool;
	// ToolCount is also final on branch_end).
	ToolName string
	IsError  bool
	// InnerKind / Text / Detail are the ADR-0079 bounded previews, set on a
	// branch_tool event per the branch's forwarded inner event kind (tool.call /
	// tool.result / message.delta), exactly as on SubagentMsg; empty from an older
	// server. The server clamp-scrubs every preview; the ui caps again on render.
	InnerKind string
	Text      string
	Detail    string
	ToolCount int
	// Failed / Workspace are set on ParallelBranchEnd (Workspace is the branch's fork root).
	Failed    bool
	Workspace string
	// Stop is the branch terminal (branch_end) or the run-level stop (end).
	Stop string
	// Usage is the branch's cumulative usage (branch_end) or the run total (end).
	Usage Usage
	// DurationMs is the branch's wall-clock duration (branch_end).
	DurationMs int64
	// Winner / WinnerWorkspace are set on ParallelEnd: the real winning branch index
	// (-1 for join=all / none-succeeded) and its preserved fork root.
	Winner          int
	WinnerWorkspace string
}

// CompactionMsg is a muted "history compacted" notice.
type CompactionMsg struct{ Text string }

// NoProgressMsg is a muted advisory notice emitted when a completed turn produced
// no tool call and no meaningful text and the loop is nudging the model to continue
// (or giving up after the budget). It is rendered as a transient status line, like
// CompactionMsg; it carries only the harness-authored reason Text (no model content).
type NoProgressMsg struct{ Text string }

// ResultMsg is the terminal event: stop reason, final text, error, usage.
type ResultMsg struct {
	Stop  string
	Text  string
	Error string
	Usage Usage
	// Transient marks a stop=error whose Error text classifies as a transient
	// (retryable) failure — see TransientResultError. The ui auto-resumes a paused
	// queue on a transient error; a hard error still pauses. Always false for a
	// non-error stop.
	Transient bool
}

// Usage is the token accounting carried by ResultMsg (and usage-bearing events).
// Duplicated as a plain struct so ui stays proto-free.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

// Transport / lifecycle msgs (NOT from the proto stream).

// SessionReadyMsg carries the session id AND the server's capabilities from the
// async CreateSession. Capabilities drives the ui's honest discoverability
// affordances; an older server yields the all-false zero value.
type SessionReadyMsg struct {
	SessionID    string
	Capabilities Capabilities
	// ResolvedModel is the EFFECTIVE provider+model the session resolved to (server-
	// owned, echoed verbatim). The ui shows it in the header from turn zero; an older
	// server yields the zero value (no model segment).
	ResolvedModel ResolvedModel
	// Mode is the server-confirmed permission posture; empty means older create path /
	// default.
	Mode string
}

// ConnectErrMsg reports a dial/CreateSession failure.
type ConnectErrMsg struct{ Err error }

// StreamErrMsg reports a non-EOF Recv error on the Converse stream.
type StreamErrMsg struct {
	Err error
	// Transient marks a stream error that classifies as a transient (retryable)
	// transport/backend failure — see TransientStreamErr. The ui auto-resumes a
	// paused queue on a transient stream error; a hard error still pauses.
	Transient bool
}

// StreamClosedMsg reports a clean stream close (io.EOF) without a result event
// (e.g. server closed early). Normal completion arrives as ResultMsg first.
type StreamClosedMsg struct{}

// The log-only replay msgs. These three kinds (approval/user_prompt/
// compaction.archive) are LOG-ONLY on the live Converse wire (the relay skips
// them) and are relayed ONLY by the StreamSessionEvents replay. They are the
// transcript-viewer's audit/history surface (cloud-native Phase 3a read-back).

// ApprovalMsg is the verdict half of a permission ask (EvApproval), relayed
// only by the replay (log-only on the live wire). Metadata-only (gauntlet #7):
// tool NAME + verdict string + askID + callID + the allow-always flag. NEVER raw
// args.
type ApprovalMsg struct {
	AskID       string
	Verdict     string
	Tool        string
	CallID      string
	AllowAlways bool
}

// UserPromptMsg is the recorded user message (EvUserPrompt), relayed only by the
// replay. Text carries the flattened prompt body (or a harness-authored
// continuation/notice); Parts carries any non-text media (image/audio) that rode
// alongside it, projected to the plain ContentBlock type (image/audio only).
// A delivery-patterned user_prompt (the fire-result delivery channel, ADR 0075)
// maps to DeliveryNoteMsg instead — see deliverNoteFrom.
type UserPromptMsg struct {
	Text  string
	Parts []ContentBlock
}

// DeliveryNoteMsg is a fire-result delivery note (ADR 0075 Scenario 5): the
// fenced-untrusted harness note the scheduler delivered into the origin session.
// It is projected from an EvUserPrompt event whose text starts with the
// "[scheduled task <name> (fire <id>) ..." provenance header renderFireDelivery
// emits. ScheduleName + FireID are structured fields the ui renders as a
// distinct delivery card with a scheduled-task affordance; Text carries the
// full note body verbatim (the same fenced-untrusted content the engine
// recorded). Parts carries any non-text media that rode alongside the note
// (normally nil — delivery notes are text-only). The live subscription and
// the replay (StreamSessionEvents) path share this type via EventToMsg.
type DeliveryNoteMsg struct {
	ScheduleName string
	FireID       string
	Text         string
	Parts        []ContentBlock
}

// deliveryNotePrefix is the literal provenance header renderFireDelivery emits
// as the FIRST CONTENT LINE of every delivery note, INSIDE the fenced-untrusted
// block (the line after the "<<<UNTRUSTED\n" opener). It is the single detection
// pattern EventToMsg keys off to route a user_prompt to DeliveryNoteMsg; it must
// match the SAME literal renderFireDelivery produces (internal/app/scheduler_delivery.go).
const deliveryNotePrefix = "[scheduled task "

// deliveryNoteFenceOpener is the leading fence marker renderFireDelivery wraps
// EVERY delivery note in (agent.FenceUntrusted writes "<<<UNTRUSTED\n" then the
// body). Detection keys off the fence opener FOLLOWED by the header prefix so a
// non-delivery user prompt (never fenced) cannot match, and a user who literally
// typed "[scheduled task …" (un-fenced) is NOT mis-detected. This mirrors the
// server relay's isDeliveryNoteText (internal/adapter/server/grpc.go) — the two
// share the SAME detection contract.
const deliveryNoteFenceOpener = "<<<UNTRUSTED\n"

// deliverNoteFrom extracts the structured fields from a user_prompt whose text
// is a fenced fire-result delivery note (the renderFireDelivery output: a
// "<<<UNTRUSTED\n" opener followed by the "[scheduled task …" provenance
// header). On a match it returns the DeliveryNoteMsg; on a non-match it returns
// nil (the caller falls back to UserPromptMsg).
func deliverNoteFrom(text string, parts []ContentBlock) *DeliveryNoteMsg {
	// The note is fenced: the untrusted-fence opener precedes the header. Strip
	// it before detecting + parsing so extractDeliveryFields reads the header at
	// offset 0.
	header := text
	if hasDeliveryFenceOpener(text) {
		header = text[len(deliveryNoteFenceOpener):]
	}
	if len(header) < len(deliveryNotePrefix) || header[:len(deliveryNotePrefix)] != deliveryNotePrefix {
		return nil
	}
	// A non-fenced text that merely starts with the header prefix is NOT a
	// delivery note — renderFireDelivery ALWAYS fences, so an un-fenced match is
	// a user who literally typed the prefix. Reject it so it renders as an
	// ordinary UserPromptMsg (mirrors the live-wire relay's fenced discriminator).
	if !hasDeliveryFenceOpener(text) {
		return nil
	}
	// Extract the schedule name: everything between "[scheduled task " and
	// " (fire ". Handle the model-authored schedule name which may contain
	// arbitrary characters (but is neutralised by renderFireDelivery, so it
	// cannot contain the closing ") (fire " substring).
	schedName, fireID := extractDeliveryFields(header)
	if schedName == "" {
		return nil // malformed — no schedule name extractable
	}
	return &DeliveryNoteMsg{
		ScheduleName: schedName,
		FireID:       fireID,
		Text:         text,
		Parts:        parts,
	}
}

// hasDeliveryFenceOpener reports whether text begins with the untrusted-fence
// opener renderFireDelivery wraps every delivery note in.
func hasDeliveryFenceOpener(text string) bool {
	return len(text) >= len(deliveryNoteFenceOpener) && text[:len(deliveryNoteFenceOpener)] == deliveryNoteFenceOpener
}

// extractDeliveryFields pulls the schedule name and fire id from a delivery
// note's provenance header: "[scheduled task <name> (fire <id>) <rest>]".
// The header is produced by renderFireDelivery (internal/app/scheduler_delivery.go):
//
//	fmt.Sprintf("[scheduled task %%s (fire %%s) completed with stop reason: %%s]", ...)
//
// Returns ("", "") on any parse failure (the caller falls back to UserPromptMsg).
func extractDeliveryFields(text string) (string, string) {
	rest := text[len(deliveryNotePrefix):]
	// The schedule name is everything before " (fire ".
	fireTag := " (fire "
	fireIdx := indexSubstring(rest, fireTag)
	if fireIdx < 0 {
		return "", ""
	}
	name := rest[:fireIdx]
	rest = rest[fireIdx+len(fireTag):]
	// The fire id is everything before the next ")".
	closeParen := indexByteIn(rest, ')')
	if closeParen < 0 {
		return "", ""
	}
	fireID := rest[:closeParen]
	if name == "" || fireID == "" {
		return "", ""
	}
	return name, fireID
}

// indexSubstring returns the byte index of s within b, or -1.
func indexSubstring(b, s string) int {
	if len(s) == 0 {
		return 0
	}
	for i := 0; i <= len(b)-len(s); i++ {
		if b[i:i+len(s)] == s {
			return i
		}
	}
	return -1
}

// indexByteIn returns the byte index of c within s, or -1.
func indexByteIn(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// CompactionArchiveMsg is the pre-compaction conversation (EvCompactionArchive),
// relayed only by the replay, so a transcript recovers the dropped turns. It is
// the parent's OWN conversation (gauntlet #7 — no child content).
type CompactionArchiveMsg struct {
	Replaced []ConversationMessage
}

// ConversationMessage is the proto-free mirror of mecatlv1.ConversationMessage:
// one immutable entry in the model-visible conversation history. It mirrors the
// session.Message value object — Role + Text + the assistant's ToolCalls + an
// optional tool-role ToolResult + the opaque provider replay blobs (Reasoning /
// ProviderPhase) + the user-role media Parts. The ToolCalls/ToolResult fields
// use the dedicated ConvToolCall/ConvToolResult structs below (NOT the event-msg
// types ToolCallMsg/ToolResultMsg — those are EVENTS, not message PARTS: a
// tool.call event is a transient status line, a ConvToolCall is the persisted
// assistant message part; overloading them would conflate the two lifecycles).
// If the Phase-3 ui only renders Role+Text, the extra fields are unused-but-cheap.
type ConversationMessage struct {
	Role          string
	Text          string
	ToolCalls     []ConvToolCall
	ToolResult    *ConvToolResult
	Reasoning     string
	ProviderPhase string
	Parts         []ContentBlock
}

// ConvToolCall is the proto-free mirror of one assistant-message tool invocation
// (mecatlv1.ToolCall as carried by a ConversationMessage). Distinct from
// ToolCallMsg (an event) — see the ConversationMessage doc.
type ConvToolCall struct {
	ID   string
	Name string
	Args string // raw JSON
}

// ConvToolResult is the proto-free mirror of a tool-role message's result
// (mecatlv1.ToolResult as carried by a ConversationMessage). Distinct from
// ToolResultMsg (an event) — see the ConversationMessage doc.
type ConvToolResult struct {
	CallID            string
	Content           string
	IsError           bool
	Blocks            []ContentBlock
	StructuredContent string
}

// hookDecisionFrom converts a proto HookDecision enum to the plain HookDecision
// string the ui keys off. An unspecified/unknown value (including a nil Hook,
// since GetDecision is nil-safe) maps to HookInfo, the benign baseline.
func hookDecisionFrom(d mecatlv1.HookDecision) HookDecision {
	switch d {
	case mecatlv1.HookDecision_HOOK_DECISION_BLOCKED:
		return HookBlocked
	case mecatlv1.HookDecision_HOOK_DECISION_MODIFIED:
		return HookModified
	case mecatlv1.HookDecision_HOOK_DECISION_ADVISORY:
		return HookAdvisory
	default:
		return HookInfo
	}
}

// subagentMsg builds a SubagentMsg of the given kind from a proto Subagent
// payload (nil-safe via the generated getters). It is the single translation
// point for the three subagent.* event kinds.
func subagentMsg(kind SubagentKind, s *mecatlv1.Subagent) SubagentMsg {
	return SubagentMsg{
		Kind:           kind,
		ParentCallID:   s.GetParentCallId(),
		ChildID:        s.GetChildId(),
		Goal:           s.GetGoal(),
		Background:     s.GetBackground(),
		RoutedCategory: s.GetRoutedCategory(),
		RoutedModel:    s.GetRoutedModel(),
		Model:          s.GetModel(),
		ToolName:       s.GetToolName(),
		IsError:        s.GetIsError(),
		InnerKind:      s.GetInnerKind(),
		Text:           s.GetText(),
		Detail:         s.GetDetail(),
		ToolCount:      int(s.GetToolCount()),
		Usage:          usageFrom(s.GetUsage()),
		Stop:           s.GetStop(),
		Cause:          s.GetCause(),
		DurationMs:     s.GetDurationMs(),
	}
}

// parallelMsg builds a ParallelMsg from a proto Parallel payload (nil-safe via the
// generated getters). The kind is derived from the event type (start/end) or, for a
// parallel.branch event, the proto kind discriminant (branch_start/tool/end). It is the
// single translation point for the parallel.* event family.
func parallelMsg(kind ParallelKind, p *mecatlv1.Parallel) ParallelMsg {
	return ParallelMsg{
		Kind:            kind,
		ParentCallID:    p.GetParentCallId(),
		Join:            p.GetJoin(),
		BranchCount:     int(p.GetBranchCount()),
		BranchIndex:     int(p.GetBranchIndex()),
		ChildID:         p.GetChildId(),
		BranchLabel:     p.GetBranchLabel(),
		Goal:            p.GetGoal(),
		RoutedCategory:  p.GetRoutedCategory(),
		RoutedModel:     p.GetRoutedModel(),
		Model:           p.GetModel(),
		ToolName:        p.GetToolName(),
		IsError:         p.GetIsError(),
		InnerKind:       p.GetInnerKind(),
		Text:            p.GetText(),
		Detail:          p.GetDetail(),
		ToolCount:       int(p.GetToolCount()),
		Failed:          p.GetFailed(),
		Workspace:       p.GetWorkspace(),
		Stop:            p.GetStop(),
		Usage:           usageFrom(p.GetUsage()),
		DurationMs:      p.GetDurationMs(),
		Winner:          int(p.GetWinner()),
		WinnerWorkspace: p.GetWinnerWorkspace(),
	}
}

// parallelBranchKind maps the proto parallel.branch kind discriminant to its
// ParallelKind. An unknown/empty kind falls back to ParallelBranchTool (the benign
// metadata-only kind) so a future kind never crashes the reader.
func parallelBranchKind(protoKind string) ParallelKind {
	switch protoKind {
	case "branch_start":
		return ParallelBranchStart
	case "branch_end":
		return ParallelBranchEnd
	default:
		return ParallelBranchTool
	}
}

// teamMsg builds a TeamMsg of the given kind from a proto Team payload (nil-safe
// via the generated getters). It is the single translation point for the team.*
// event kinds; the roster is converted to plain TeamMemberSpec values and the task
// snapshot to plain TeamTask values.
func teamMsg(kind TeamKind, t *mecatlv1.Team) TeamMsg {
	msg := TeamMsg{
		Kind:            kind,
		ParentCallID:    t.GetParentCallId(),
		TeamID:          t.GetTeamId(),
		Member:          t.GetMember(),
		MemberSessionID: t.GetMemberSessionId(),
		InnerKind:       t.GetInnerKind(),
		Text:            t.GetText(),
		ToolName:        t.GetToolName(),
		Detail:          t.GetDetail(),
		IsError:         t.GetIsError(),
		Rounds:          int(t.GetRounds()),
		Stop:            t.GetStop(),
		Usage:           usageFrom(t.GetUsage()),
		ContextUsed:     t.GetContextUsed(),
		ContextWindow:   t.GetContextWindow(),
	}
	for _, r := range t.GetRoster() {
		msg.Roster = append(msg.Roster, TeamMemberSpec{
			Name:           r.GetName(),
			Role:           r.GetRole(),
			Mutating:       r.GetMutating(),
			Lead:           r.GetLead(),
			RoutedCategory: r.GetRoutedCategory(),
			RoutedModel:    r.GetRoutedModel(),
			Model:          r.GetModel(),
		})
	}
	for _, tk := range t.GetTasks() {
		msg.Tasks = append(msg.Tasks, TeamTask{
			ID:          tk.GetId(),
			Description: tk.GetDescription(),
			State:       tk.GetState(),
			Assignee:    tk.GetAssignee(),
			Deps:        tk.GetDeps(),
		})
	}
	for _, f := range t.GetFindings() {
		msg.Findings = append(msg.Findings, TeamFinding{Member: f.GetMember(), Body: f.GetBody()})
	}
	for _, d := range t.GetDispositions() {
		msg.Dispositions = append(msg.Dispositions, TeamMemberDisposition{
			Name:        d.GetName(),
			Stopped:     d.GetStopped(),
			Reason:      reasonString(d.GetReason()),
			ErrorRounds: int(d.GetErrorRounds()),
		})
	}
	return msg
}

// reasonString maps a proto TeamMemberStopReason enum to the plain reason string the
// ui consumes ("error"/"cancelled"/"budget"; "" for UNSPECIFIED/done). Keeping the
// enum→string switch here keeps the ui layer proto-free.
func reasonString(r mecatlv1.TeamMemberStopReason) string {
	switch r {
	case mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_ERROR:
		return "error"
	case mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED:
		return "cancelled"
	case mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_BUDGET:
		return "budget"
	default:
		return ""
	}
}

// usageFrom converts a proto Usage (nil-safe) to the plain struct.
func usageFrom(u *mecatlv1.Usage) Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		InputTokens:      u.GetInputTokens(),
		OutputTokens:     u.GetOutputTokens(),
		CacheReadTokens:  u.GetCacheReadTokens(),
		CacheWriteTokens: u.GetCacheWriteTokens(),
		ReasoningTokens:  u.GetReasoningTokens(),
	}
}

// EventToMsg maps a single proto Event onto its tea.Msg. It is a total function
// over the documented type strings; an unknown/empty type returns nil (the
// reader skips nil so unknown future event kinds are ignored, not fatal). This
// is the single translation point between the proto schema and the ui model and
// is unit-tested over every type.
func EventToMsg(ev *mecatlv1.Event) tea.Msg {
	if ev == nil {
		return nil
	}
	switch ev.GetType() {
	case "session.init":
		return SessionInitMsg{Seq: ev.GetSeq()}
	case "turn.start":
		return TurnStartMsg{Turn: ev.GetTurn()}
	case "turn.end":
		te := ev.GetTurnEnd()
		return TurnEndMsg{Turn: ev.GetTurn(), Usage: usageFrom(te.GetUsage()), DurationMs: te.GetDurationMs()}
	case "message.delta":
		return AssistantDeltaMsg{Turn: ev.GetTurn(), Text: ev.GetText()}
	case "reasoning.delta":
		return ReasoningDeltaMsg{Turn: ev.GetTurn(), Text: ev.GetText()}
	case "tool.call":
		tc := ev.GetToolCall()
		return ToolCallMsg{ID: tc.GetId(), Name: tc.GetName(), Args: tc.GetArgs()}
	case "tool.result":
		tr := ev.GetToolResult()
		return ToolResultMsg{
			CallID:            tr.GetCallId(),
			Content:           tr.GetContent(),
			IsError:           tr.GetIsError(),
			Blocks:            contentBlocksFromProto(tr.GetBlocks()),
			StructuredContent: tr.GetStructuredContent(),
		}
	case "tool.progress":
		return ToolProgressMsg{Text: ev.GetText()}
	case "permission.ask":
		a := ev.GetAsk()
		return PermissionAskMsg{AskID: a.GetAskId(), Tool: a.GetTool(), Args: a.GetArgs(), Reason: a.GetReason()}
	case "permission.retract":
		// The retraction payload rides the same ask field, carrying the AskID only
		// (server-authored; no tool/args/reason).
		return PermissionRetractMsg{AskID: ev.GetAsk().GetAskId()}
	case "hook":
		h := ev.GetHook()
		return HookMsg{
			Text:     ev.GetText(),
			Phase:    h.GetPhase(),
			Tool:     h.GetTool(),
			Decision: hookDecisionFrom(h.GetDecision()),
		}
	case "compaction":
		return CompactionMsg{Text: ev.GetText()}
	case "no_progress":
		return NoProgressMsg{Text: ev.GetText()}
	case "result":
		r := ev.GetResult()
		return ResultMsg{
			Stop:      r.GetStop(),
			Text:      r.GetText(),
			Error:     r.GetError(),
			Usage:     usageFrom(r.GetUsage()),
			Transient: TransientResultError(r.GetError()),
		}
	case "approval":
		return approvalMsg(ev.GetApproval())
	case "user_prompt":
		up := ev.GetUserPrompt()
		text := up.GetText()
		parts := contentPartsFromProto(up.GetParts())
		// Route fire-result delivery notes (ADR 0075 Scenario 5) to a
		// distinct DeliveryNoteMsg so the ui renders them as a delivery
		// card with a scheduled-task affordance, not as a user-typed
		// prompt. The pattern is the renderFireDelivery provenance header.
		if dn := deliverNoteFrom(text, parts); dn != nil {
			return *dn
		}
		return UserPromptMsg{Text: text, Parts: parts}
	case "compaction.archive":
		return compactionArchiveMsg(ev.GetCompactionArchive())
	default:
		// The subagent.* / team.* delegation projections are mapped by
		// delegationEventToMsg (a second switch) to keep this dispatcher under the
		// cyclomatic-complexity bound. The two switches are total over the documented
		// type strings ONLY together: a new delegation case must be added there, not
		// here. An unknown/empty type returns nil so future event kinds are ignored,
		// not fatal.
		return delegationEventToMsg(ev)
	}
}

// delegationEventToMsg maps the subagent.* and team.* delegation-projection event
// types to their tea.Msg. It is the second half of EventToMsg, split out only so
// neither dispatcher grows past the cyclomatic-complexity bound; together they
// remain a total function over the documented type strings. An unmatched type
// returns nil (skipped by the reader).
func delegationEventToMsg(ev *mecatlv1.Event) tea.Msg {
	switch ev.GetType() {
	case "subagent.start":
		return subagentMsg(SubagentStart, ev.GetSubagent())
	case "subagent.tool":
		return subagentMsg(SubagentTool, ev.GetSubagent())
	case "subagent.end":
		return subagentMsg(SubagentEnd, ev.GetSubagent())
	case "team.start":
		return teamMsg(TeamStart, ev.GetTeam())
	case "team.member":
		return teamMsg(TeamMember, ev.GetTeam())
	case "team.tasks":
		return teamMsg(TeamTasks, ev.GetTeam())
	case "team.findings":
		return teamMsg(TeamFindings, ev.GetTeam())
	case "team.end":
		return teamMsg(TeamEnd, ev.GetTeam())
	case "parallel.start":
		return parallelMsg(ParallelStart, ev.GetParallel())
	case "parallel.branch":
		return parallelMsg(parallelBranchKind(ev.GetParallel().GetKind()), ev.GetParallel())
	case "parallel.end":
		return parallelMsg(ParallelEnd, ev.GetParallel())
	default:
		return nil
	}
}

// contentBlocksFromProto converts the proto ContentBlock slice to plain-data
// ContentBlock values (nil-safe). It is the single translation point for the typed
// tool-result blocks, mirroring usageFrom — keeping the ui proto-free. A nil/empty
// slice returns nil so a text-only result (the common case, no blocks relayed) takes
// the unchanged existing render path.
func contentBlocksFromProto(pb []*mecatlv1.ContentBlock) []ContentBlock {
	if len(pb) == 0 {
		return nil
	}
	out := make([]ContentBlock, 0, len(pb))
	for _, b := range pb {
		out = append(out, ContentBlock{
			Kind:        contentBlockKindFromProto(b.GetKind()),
			MimeType:    b.GetMimeType(),
			Data:        b.GetData(),
			URL:         b.GetUrl(),
			Text:        b.GetText(),
			Name:        b.GetName(),
			Title:       b.GetTitle(),
			Description: b.GetDescription(),
		})
	}
	return out
}

// contentBlockKindFromProto maps a proto ContentBlock_Kind to the plain
// ContentBlockKind string the ui keys off. UNSPECIFIED (and any unknown future
// value) maps to the empty string so the renderer treats it as absent — never a
// crash on a forward-compatible kind.
func contentBlockKindFromProto(k mecatlv1.ContentBlock_Kind) ContentBlockKind {
	switch k {
	case mecatlv1.ContentBlock_KIND_TEXT:
		return ContentBlockText
	case mecatlv1.ContentBlock_KIND_IMAGE:
		return ContentBlockImage
	case mecatlv1.ContentBlock_KIND_AUDIO:
		return ContentBlockAudio
	case mecatlv1.ContentBlock_KIND_RESOURCE_LINK:
		return ContentBlockResourceLink
	case mecatlv1.ContentBlock_KIND_EMBEDDED_RESOURCE:
		return ContentBlockEmbeddedResource
	case mecatlv1.ContentBlock_KIND_STRUCTURED_CONTENT:
		return ContentBlockStructuredContent
	default:
		return ContentBlockUnspecified
	}
}

// contentKindFromProto maps a proto Content_Kind (the USER-message media-part
// kind: image/audio) to the plain ContentBlockKind the ui renders. It reuses
// the same ContentBlock plain type (image/audio are the only kinds a user-part
// Content carries), so a transcript viewer renders user-prompt media with the
// same code path as tool-result image blocks. UNSPECIFIED (and any unknown
// value) maps to the empty string (absent).
func contentKindFromProto(k mecatlv1.Content_Kind) ContentBlockKind {
	switch k {
	case mecatlv1.Content_KIND_IMAGE:
		return ContentBlockImage
	case mecatlv1.Content_KIND_AUDIO:
		return ContentBlockAudio
	default:
		return ContentBlockUnspecified
	}
}

// contentPartsFromProto maps the proto user-message media Parts ([]*Content,
// the image/audio prompt-part type DISTINCT from []*ContentBlock) to the plain
// ContentBlock values the ui renders. It is the single translation point for a
// UserPrompt's Parts, mirroring contentBlocksFromProto — keeping the ui
// proto-free. A nil/empty slice returns nil (text-only prompt, the common case).
func contentPartsFromProto(in []*mecatlv1.Content) []ContentBlock {
	if len(in) == 0 {
		return nil
	}
	out := make([]ContentBlock, 0, len(in))
	for _, p := range in {
		out = append(out, ContentBlock{
			Kind:     contentKindFromProto(p.GetKind()),
			MimeType: p.GetMimeType(),
			Data:     p.GetData(),
			URL:      p.GetUrl(),
		})
	}
	return out
}

// approvalMsg builds an ApprovalMsg from a proto Approval payload (nil-safe via
// the generated getters). It is the single translation point for the
// log-only "approval" event kind.
func approvalMsg(a *mecatlv1.Approval) ApprovalMsg {
	return ApprovalMsg{
		AskID:       a.GetAskId(),
		Verdict:     a.GetVerdict(),
		Tool:        a.GetTool(),
		CallID:      a.GetCallId(),
		AllowAlways: a.GetAllowAlways(),
	}
}

// compactionArchiveMsg builds a CompactionArchiveMsg from a proto
// CompactionArchive payload (nil-safe via the generated getters). It is the
// single translation point for the log-only "compaction.archive" event kind.
func compactionArchiveMsg(c *mecatlv1.CompactionArchive) CompactionArchiveMsg {
	return CompactionArchiveMsg{
		Replaced: conversationMessagesFromProto(c.GetReplaced()),
	}
}

// conversationMessagesFromProto maps a proto ConversationMessage slice (the
// pre-compaction history carried by a CompactionArchive) to the plain
// ConversationMessage values the ui renders. Nil-safe via the generated
// getters; a nil/empty slice returns nil (no archived turns).
func conversationMessagesFromProto(in []*mecatlv1.ConversationMessage) []ConversationMessage {
	if len(in) == 0 {
		return nil
	}
	out := make([]ConversationMessage, 0, len(in))
	for _, m := range in {
		msg := ConversationMessage{
			Role:          m.GetRole(),
			Text:          m.GetText(),
			Reasoning:     m.GetReasoning(),
			ProviderPhase: m.GetProviderPhase(),
			Parts:         contentPartsFromProto(m.GetParts()),
		}
		for _, tc := range m.GetToolCalls() {
			msg.ToolCalls = append(msg.ToolCalls, ConvToolCall{
				ID:   tc.GetId(),
				Name: tc.GetName(),
				Args: tc.GetArgs(),
			})
		}
		if tr := m.GetToolResult(); tr != nil {
			msg.ToolResult = &ConvToolResult{
				CallID:            tr.GetCallId(),
				Content:           tr.GetContent(),
				IsError:           tr.GetIsError(),
				Blocks:            contentBlocksFromProto(tr.GetBlocks()),
				StructuredContent: tr.GetStructuredContent(),
			}
		}
		out = append(out, msg)
	}
	return out
}
