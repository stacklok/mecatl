package server

import (
	"fmt"
	"math"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcp/source"
)

// contentFromProto maps the proto Content parts of a multimodal prompt into the
// domain []session.Content. It is the wire→domain choke point: each part is
// constructed through session.NewContent, which enforces the structural
// invariants (kind set, mime consistent with kind, exactly-one-of(data,url)) AND
// validates a URL source as an absolute https URL to a non-internal host (the
// SSRF backstop, CWE-918). The whole slice is then size-capped via
// session.ValidateMediaParts (CWE-770). Any violation is returned as an error the
// wire adapter maps to InvalidArgument — never silently dropped. An empty/nil
// input yields nil parts.
func contentFromProto(parts []*mecatlv1.Content) ([]session.Content, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]session.Content, 0, len(parts))
	for i, p := range parts {
		if p == nil {
			return nil, fmt.Errorf("prompt parts[%d]: nil part", i)
		}
		var kind session.MediaKind
		switch p.GetKind() {
		case mecatlv1.Content_KIND_IMAGE:
			kind = session.MediaImage
		case mecatlv1.Content_KIND_AUDIO:
			kind = session.MediaAudio
		case mecatlv1.Content_KIND_UNSPECIFIED:
			return nil, fmt.Errorf("prompt parts[%d]: kind is required (KIND_UNSPECIFIED)", i)
		default:
			return nil, fmt.Errorf("prompt parts[%d]: unknown kind %v", i, p.GetKind())
		}
		c, err := session.NewContent(kind, p.GetMimeType(), p.GetData(), p.GetUrl())
		if err != nil {
			return nil, fmt.Errorf("prompt parts[%d]: %w", i, err)
		}
		out = append(out, c)
	}
	if err := session.ValidateMediaParts(out); err != nil {
		return nil, err
	}
	return out, nil
}

// contentToProto maps domain []session.Content back to proto Content parts (the
// inverse of contentFromProto), for any surface that projects a recorded
// multimodal user message back to a client. A nil/empty input yields nil.
func contentToProto(parts []session.Content) []*mecatlv1.Content {
	if len(parts) == 0 {
		return nil
	}
	out := make([]*mecatlv1.Content, 0, len(parts))
	for _, p := range parts {
		kind := mecatlv1.Content_KIND_UNSPECIFIED
		switch p.Kind {
		case session.MediaImage:
			kind = mecatlv1.Content_KIND_IMAGE
		case session.MediaAudio:
			kind = mecatlv1.Content_KIND_AUDIO
		}
		out = append(out, &mecatlv1.Content{
			Kind:     kind,
			MimeType: p.MIMEType,
			Data:     p.Data,
			Url:      p.URL,
		})
	}
	return out
}

// ClampInt32 narrows a Go int (counter/index/count) to the proto int32 wire
// type, saturating at the int32 bounds rather than wrapping. These values
// (turn indices, counters, model counts) never realistically approach the
// limit; the clamp exists only so the conversion is provably overflow-safe.
// Exported so internal/app (e.g. the ProviderStatus model_count clamp) can
// reuse it instead of hand-rolling the same gosec G115 dance.
//
// Written as the if-assign idiom, not `min(v, math.MaxInt32)`, because
// gosec's G115 range analysis tracks this bound but does NOT propagate it
// through the `min` builtin (the min form trips G115 on the int32 cast
// below).
func ClampInt32(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}

// toProto translates a domain session.Event into its wire-level proto Event.
// It is pure (no I/O, no shared state) so it can be unit-tested exhaustively
// across every EventType and submessage. The string type field mirrors
// session.EventType verbatim; the structured submessages are populated only
// when the corresponding domain pointer is set.
func toProto(ev session.Event) *mecatlv1.Event {
	out := &mecatlv1.Event{
		Type: string(ev.Type),
		Seq:  ev.Seq,
		Turn: ClampInt32(ev.Turn),
		Text: ev.Text,
	}
	if ev.ToolCall != nil {
		out.ToolCall = toProtoToolCall(*ev.ToolCall)
	}
	if ev.ToolResult != nil {
		out.ToolResult = toProtoToolResult(*ev.ToolResult)
	}
	if ev.Ask != nil {
		out.Ask = toProtoAsk(*ev.Ask)
	}
	if ev.Result != nil {
		out.Result = toProtoResult(*ev.Result)
	}
	if ev.TurnEnd != nil {
		out.TurnEnd = toProtoTurnEnd(*ev.TurnEnd)
	}
	if ev.Hook != nil {
		out.Hook = toProtoHook(*ev.Hook)
	}
	if ev.Usage != nil {
		out.Usage = toProtoUsage(*ev.Usage)
	}
	if ev.Subagent != nil {
		out.Subagent = toProtoSubagent(*ev.Subagent)
	}
	if ev.Team != nil {
		out.Team = toProtoTeam(*ev.Team)
	}
	if ev.Parallel != nil {
		out.Parallel = toProtoParallel(*ev.Parallel)
	}
	if ev.Schedule != nil {
		out.Schedule = toProtoSchedule(*ev.Schedule)
	}
	if ev.Approval != nil {
		out.Approval = toProtoApproval(*ev.Approval)
	}
	if ev.UserPrompt != nil {
		out.UserPrompt = toProtoUserPrompt(*ev.UserPrompt)
	}
	if ev.CompactionArchive != nil {
		out.CompactionArchive = toProtoCompactionArchive(*ev.CompactionArchive)
	}
	return out
}

// toProtoParallel maps a session.ParallelPayload to its proto Parallel form: the
// bounded-preview projection of a Parallel fork-join run. Usage is always emitted
// (zero on the start/branch_start/branch_tool kinds); the per-kind field population
// mirrors the domain payload's documented contract. It copies only the already-
// redacted scalars and the already-clamped previews (Text / Detail / InnerKind —
// inner_kind a STRING passthrough, mirroring toProtoTeam) — never unbounded branch
// content — preserving gauntlet #7.
func toProtoParallel(p session.ParallelPayload) *mecatlv1.Parallel {
	return &mecatlv1.Parallel{
		ParentCallId:    p.ParentCallID,
		Kind:            string(p.Kind),
		Join:            p.Join,
		BranchCount:     ClampInt32(p.BranchCount),
		BranchIndex:     ClampInt32(p.BranchIndex),
		ChildId:         p.ChildID,
		BranchLabel:     p.BranchLabel,
		Goal:            p.Goal,
		RoutedCategory:  p.RoutedCategory,
		RoutedModel:     p.RoutedModel,
		Model:           p.Model,
		ToolName:        p.ToolName,
		IsError:         p.IsError,
		ToolCount:       ClampInt32(p.ToolCount),
		InnerKind:       string(p.InnerKind),
		Text:            p.Text,
		Detail:          p.Detail,
		Failed:          p.Failed,
		Workspace:       p.Workspace,
		Stop:            string(p.Stop),
		Usage:           toProtoUsage(p.Usage),
		DurationMs:      p.DurationMs,
		Winner:          ClampInt32(p.Winner),
		WinnerWorkspace: p.WinnerWorkspace,
	}
}

// toProtoSchedule maps a session.SchedulePayload to its proto SchedulePayload
// form: the scheduler lifecycle projection emitted from composition (not the
// loop). It copies the scalar fields verbatim — kind/stop/err are STRING
// passthroughs (no enum), mirroring the domain payload's documented contract.
// The payload carries no child content (the scheduler emits it at fire time,
// not the agent loop), so there is no redaction surface here.
func toProtoSchedule(p session.SchedulePayload) *mecatlv1.SchedulePayload {
	return &mecatlv1.SchedulePayload{
		ScheduleName: p.ScheduleName,
		FireId:       p.FireID,
		SessionId:    string(p.SessionID),
		Kind:         p.Kind,
		Stop:         string(p.Stop),
		Err:          p.Err,
	}
}

// toProtoApproval maps a session.ApprovalPayload (EvApproval) to its proto Approval
// form: the verdict half of a permission ask. It copies the metadata-only scalars
// (tool NAME + verdict string + askID + the opaque gated-call id + the allow-always
// flag) — gauntlet #7: NEVER raw args. The live Converse relay SKIPS this event
// (log-only); this mapper exists so the StreamSessionEvents replay surfaces it.
func toProtoApproval(p session.ApprovalPayload) *mecatlv1.Approval {
	return &mecatlv1.Approval{
		AskId:       p.AskID,
		Verdict:     p.Verdict,
		Tool:        p.Tool,
		CallId:      string(p.Call),
		AllowAlways: p.AllowAlways,
	}
}

// toProtoUserPrompt maps a session.UserPromptPayload (EvUserPrompt) to its proto
// UserPrompt form: the recorded user message (Text + non-text media Parts). It
// reuses contentToProto (the inverse of contentFromProto) for the media Parts.
// The live Converse relay SKIPS this event (log-only); this mapper exists so the
// StreamSessionEvents replay surfaces what the user asked.
func toProtoUserPrompt(p session.UserPromptPayload) *mecatlv1.UserPrompt {
	return &mecatlv1.UserPrompt{
		Text:  p.Text,
		Parts: contentToProto(p.Parts),
	}
}

// toProtoCompactionArchive maps a session.CompactionArchivePayload
// (EvCompactionArchive) to its proto CompactionArchive form: the pre-compaction
// conversation that ReplaceHistory replaced. It carries the full pre-compaction
// Messages slice via toProtoConversationMessage (the parent's OWN history —
// gauntlet #7, no child content). The live Converse relay SKIPS this event
// (log-only); this mapper exists so the StreamSessionEvents replay recovers the
// dropped turns.
func toProtoCompactionArchive(p session.CompactionArchivePayload) *mecatlv1.CompactionArchive {
	if len(p.Replaced) == 0 {
		return &mecatlv1.CompactionArchive{}
	}
	out := make([]*mecatlv1.ConversationMessage, 0, len(p.Replaced))
	for _, m := range p.Replaced {
		out = append(out, toProtoConversationMessage(m))
	}
	return &mecatlv1.CompactionArchive{Replaced: out}
}

// toProtoConversationMessage maps one session.Message (an immutable conversation
// history entry) to its proto ConversationMessage form. It mirrors the Message
// value object: Role + Text + the assistant's ToolCalls + an optional tool-role
// ToolResult + the opaque provider replay blobs (Reasoning / ProviderPhase) + the
// user-role media Parts. It reuses the existing toProtoToolCall / toProtoToolResult
// / contentToProto mappers so there is no second projection path. This type exists
// for the CompactionArchive replay surface — the live Converse wire streams events,
// not history, so it has no message-slice mapper of its own.
func toProtoConversationMessage(m session.Message) *mecatlv1.ConversationMessage {
	out := &mecatlv1.ConversationMessage{
		Role:          string(m.Role),
		Text:          m.Text,
		Reasoning:     m.Reasoning,
		ProviderPhase: m.ProviderPhase,
		Parts:         contentToProto(m.Parts),
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]*mecatlv1.ToolCall, 0, len(m.ToolCalls))
		for _, c := range m.ToolCalls {
			calls = append(calls, toProtoToolCall(c))
		}
		out.ToolCalls = calls
	}
	if m.ToolResult != nil {
		out.ToolResult = toProtoToolResult(*m.ToolResult)
	}
	return out
}

// toProtoTeam maps a session.TeamPayload to its proto Team form: the bounded
// observability projection of a Team tool run. Usage is always emitted (zero on the
// start kind); the per-kind field population mirrors the domain payload's documented
// contract. The previews are already capped in the domain (projectTeamEvent /
// clampPreview); this mapper copies them verbatim — it adds no further redaction.
func toProtoTeam(p session.TeamPayload) *mecatlv1.Team {
	roster := make([]*mecatlv1.TeamMemberSpec, 0, len(p.Roster))
	for _, m := range p.Roster {
		roster = append(roster, &mecatlv1.TeamMemberSpec{
			Name:           m.Name,
			Role:           m.Role,
			Mutating:       m.Mutating,
			Lead:           m.Lead,
			RoutedCategory: m.RoutedCategory,
			RoutedModel:    m.RoutedModel,
			Model:          m.Model,
		})
	}
	tasks := make([]*mecatlv1.TeamTask, 0, len(p.Tasks))
	for _, tk := range p.Tasks {
		tasks = append(tasks, toProtoTeamTaskSnapshot(tk))
	}
	findings := make([]*mecatlv1.TeamFinding, 0, len(p.Findings))
	for _, f := range p.Findings {
		findings = append(findings, &mecatlv1.TeamFinding{Member: f.Member, Body: f.Body})
	}
	dispositions := make([]*mecatlv1.TeamMemberDisposition, 0, len(p.Dispositions))
	for _, d := range p.Dispositions {
		dispositions = append(dispositions, &mecatlv1.TeamMemberDisposition{
			Name:        d.Name,
			Stopped:     d.Disposition == "stopped",
			Reason:      toProtoTeamMemberStopReason(d.Reason),
			ErrorRounds: ClampInt32(d.ErrorRounds),
		})
	}
	return &mecatlv1.Team{
		ParentCallId:    p.ParentCallID,
		TeamId:          p.TeamID,
		Roster:          roster,
		Member:          p.Member,
		MemberSessionId: p.MemberSessionID,
		InnerKind:       string(p.InnerKind),
		Text:            p.Text,
		ToolName:        p.ToolName,
		Detail:          p.Detail,
		IsError:         p.IsError,
		Rounds:          ClampInt32(p.Rounds),
		Stop:            string(p.Stop),
		Usage:           toProtoUsage(p.Usage),
		ContextUsed:     p.ContextUsed,
		ContextWindow:   p.ContextWindow,
		Tasks:           tasks,
		Findings:        findings,
		Dispositions:    dispositions,
	}
}

// toProtoTeamOutcome maps the supervisor's terminal agent.TeamOutcome to its proto
// form — the payload of the single terminal TeamEvent frame both RunTeam wire
// handlers emit (issue #36). Stop is the string passthrough of agent.TeamStop (the
// one quiescent/budget/round-cap rule); dispositions reuse the closed-enum mapping
// toProtoTeam applies to the event-stream projection; finding bodies arrive
// already capped on the outcome.
func toProtoTeamOutcome(o agent.TeamOutcome) *mecatlv1.TeamOutcome {
	dispositions := make([]*mecatlv1.TeamMemberDisposition, 0, len(o.Members))
	for _, m := range o.Members {
		dispositions = append(dispositions, &mecatlv1.TeamMemberDisposition{
			Name:        m.Name,
			Stopped:     m.Stopped,
			Reason:      toProtoTeamMemberStopReason(string(m.Reason)),
			ErrorRounds: ClampInt32(m.ErrorRounds),
		})
	}
	findings := make([]*mecatlv1.TeamFinding, 0, len(o.Findings))
	for _, f := range o.Findings {
		findings = append(findings, &mecatlv1.TeamFinding{Member: f.Member, Body: f.Body})
	}
	return &mecatlv1.TeamOutcome{
		Rounds:          ClampInt32(o.Rounds),
		Quiescent:       o.Quiescent,
		BudgetExhausted: o.BudgetExhausted,
		Stop:            string(agent.TeamStop(o)),
		Usage:           toProtoUsage(o.Usage),
		Dispositions:    dispositions,
		Findings:        findings,
	}
}

// toProtoTeamMemberStopReason maps the closed disposition-reason string (the
// supervisor's MemberStopReason value, projected through session.TeamMemberDisposition)
// to its proto enum. An unknown or empty reason maps to UNSPECIFIED, so a future reason
// is never silently mis-classified as an existing one.
func toProtoTeamMemberStopReason(r string) mecatlv1.TeamMemberStopReason {
	switch r {
	case "error":
		return mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_ERROR
	case "cancelled":
		return mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_CANCELLED
	case "budget":
		return mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_BUDGET
	default:
		return mecatlv1.TeamMemberStopReason_TEAM_MEMBER_STOP_REASON_UNSPECIFIED
	}
}

// toProtoTeamTaskSnapshot maps a session.TeamTaskSnapshot (the task-list projection
// carried on the team event stream) to its proto TeamTask form. It is distinct from
// toProtoTeamTask, which takes a team.Task off the ListTeam RPC path: a TeamPayload
// carries the already-projected snapshot (session must not import engine/team), so
// the snapshot's Deps are already []string and copied verbatim here.
func toProtoTeamTaskSnapshot(t session.TeamTaskSnapshot) *mecatlv1.TeamTask {
	deps := make([]string, len(t.Deps))
	copy(deps, t.Deps)
	return &mecatlv1.TeamTask{
		Id:          t.ID,
		Description: t.Description,
		State:       t.State,
		Assignee:    t.Assignee,
		Deps:        deps,
	}
}

// toProtoSubagent maps a session.SubagentPayload to its proto Subagent form: the
// bounded-preview projection of a Subagent child run. Usage is always emitted (zero
// on the start/tool kinds); the per-kind field population mirrors the domain
// payload's documented contract. The bounded previews (Text / Detail / InnerKind —
// inner_kind a STRING passthrough, mirroring toProtoTeam) are copied verbatim:
// already clamped by the single redaction chokepoint upstream in engine/agent.
func toProtoSubagent(p session.SubagentPayload) *mecatlv1.Subagent {
	return &mecatlv1.Subagent{
		ParentCallId:   p.ParentCallID,
		ChildId:        p.ChildID,
		Goal:           p.Goal,
		Background:     p.Background,
		RoutedCategory: p.RoutedCategory,
		RoutedModel:    p.RoutedModel,
		Model:          p.Model,
		ToolName:       p.ToolName,
		IsError:        p.IsError,
		ToolCount:      ClampInt32(p.ToolCount),
		InnerKind:      string(p.InnerKind),
		Text:           p.Text,
		Detail:         p.Detail,
		Usage:          toProtoUsage(p.Usage),
		Stop:           string(p.Stop),
		Cause:          p.Cause,
		DurationMs:     p.DurationMs,
	}
}

// toProtoToolCall maps a session.ToolCall to its proto form.
func toProtoToolCall(c session.ToolCall) *mecatlv1.ToolCall {
	return &mecatlv1.ToolCall{
		Id:   string(c.ID),
		Name: c.Name,
		Args: string(c.Args),
	}
}

// toProtoToolResult maps a session.ToolResult to its proto form. The typed
// tool-result blocks on r.Parts (text/image/audio/resource-link/embedded-resource/
// structured-content) map 1:1 to the proto ToolResult.blocks field via
// blocksToProto; the legacy Content string is carried verbatim alongside (both may
// be present — consumers prefer blocks when non-empty, falling back to content).
// structured_content is DERIVED from a BlockStructuredContent block's Text if one
// is present (T3 stores structured content as such a block in Parts), so the
// dedicated proto field stays populated even for consumers that read only it.
// A nil/empty Parts yields nil blocks — a legacy string-only result is
// byte-identical to the pre-Parts shape.
func toProtoToolResult(r session.ToolResult) *mecatlv1.ToolResult {
	out := &mecatlv1.ToolResult{
		CallId:  string(r.CallID),
		Content: r.Content,
		IsError: r.IsError,
		Blocks:  blocksToProto(r.Parts),
	}
	if sc := structuredContentText(r.Parts); sc != "" {
		out.StructuredContent = sc
	}
	return out
}

// structuredContentText returns the Text of the FIRST BlockStructuredContent part
// in parts, or "" if none. It derives the proto ToolResult.structured_content field
// from the structured block T3 stores in Parts (there is no separate field on
// session.ToolResult for it).
func structuredContentText(parts []session.Content) string {
	for _, p := range parts {
		if p.BlockKind == session.BlockStructuredContent {
			return p.Text
		}
	}
	return ""
}

// blocksToProto maps domain []session.Content tool-result blocks to proto
// ContentBlock. Each block kind maps to its ContentBlock_Kind enum; all fields
// (MIMEType/Data/URL/Text/Name/Title/Description/Size/Audience/Priority/
// LastModified) are copied verbatim. A nil/empty input yields nil.
func blocksToProto(parts []session.Content) []*mecatlv1.ContentBlock {
	if len(parts) == 0 {
		return nil
	}
	out := make([]*mecatlv1.ContentBlock, 0, len(parts))
	for _, p := range parts {
		out = append(out, &mecatlv1.ContentBlock{
			Kind:         blockKindToProto(p.BlockKind),
			MimeType:     p.MIMEType,
			Data:         p.Data,
			Url:          p.URL,
			Text:         p.Text,
			Name:         p.Name,
			Title:        p.Title,
			Description:  p.Description,
			Size:         p.Size,
			Audience:     p.Audience,
			Priority:     p.Priority,
			LastModified: p.LastModified,
		})
	}
	return out
}

// blocksFromProto is the symmetric inverse of blocksToProto, mapping proto
// ContentBlock back to domain []session.Content. Tool results flow server→client
// only (the loop emits, the relay sends), so there is no inbound tool-result
// path today; the reverse mapper exists for symmetry and is exercised by the
// round-trip test. A nil/empty input yields nil.
func blocksFromProto(pb []*mecatlv1.ContentBlock) []session.Content {
	if len(pb) == 0 {
		return nil
	}
	out := make([]session.Content, 0, len(pb))
	for _, b := range pb {
		if b == nil {
			continue
		}
		out = append(out, session.Content{
			BlockKind:    blockKindFromProto(b.GetKind()),
			Kind:         mediaKindFromBlock(b.GetKind()),
			MIMEType:     b.GetMimeType(),
			Data:         b.GetData(),
			URL:          b.GetUrl(),
			Text:         b.GetText(),
			Name:         b.GetName(),
			Title:        b.GetTitle(),
			Description:  b.GetDescription(),
			Size:         b.GetSize(),
			Audience:     b.GetAudience(),
			Priority:     b.GetPriority(),
			LastModified: b.GetLastModified(),
		})
	}
	return out
}

// blockKindToProto maps a session.BlockKind to its proto ContentBlock_Kind enum.
// An empty/unknown kind maps to KIND_UNSPECIFIED (a legacy media part on a tool
// result is unexpected but harmless; it round-trips without loss).
func blockKindToProto(k session.BlockKind) mecatlv1.ContentBlock_Kind {
	switch k {
	case session.BlockText:
		return mecatlv1.ContentBlock_KIND_TEXT
	case session.BlockImage:
		return mecatlv1.ContentBlock_KIND_IMAGE
	case session.BlockAudio:
		return mecatlv1.ContentBlock_KIND_AUDIO
	case session.BlockResourceLink:
		return mecatlv1.ContentBlock_KIND_RESOURCE_LINK
	case session.BlockEmbeddedResource:
		return mecatlv1.ContentBlock_KIND_EMBEDDED_RESOURCE
	case session.BlockStructuredContent:
		return mecatlv1.ContentBlock_KIND_STRUCTURED_CONTENT
	default:
		return mecatlv1.ContentBlock_KIND_UNSPECIFIED
	}
}

// blockKindFromProto is the inverse of blockKindToProto.
func blockKindFromProto(k mecatlv1.ContentBlock_Kind) session.BlockKind {
	switch k {
	case mecatlv1.ContentBlock_KIND_TEXT:
		return session.BlockText
	case mecatlv1.ContentBlock_KIND_IMAGE:
		return session.BlockImage
	case mecatlv1.ContentBlock_KIND_AUDIO:
		return session.BlockAudio
	case mecatlv1.ContentBlock_KIND_RESOURCE_LINK:
		return session.BlockResourceLink
	case mecatlv1.ContentBlock_KIND_EMBEDDED_RESOURCE:
		return session.BlockEmbeddedResource
	case mecatlv1.ContentBlock_KIND_STRUCTURED_CONTENT:
		return session.BlockStructuredContent
	default:
		return ""
	}
}

// mediaKindFromBlock derives the session.MediaKind for an image/audio block kind;
// returns "" for non-media kinds (the legacy media-part discriminator stays empty
// for typed tool-result blocks that are not image/audio).
func mediaKindFromBlock(k mecatlv1.ContentBlock_Kind) session.MediaKind {
	switch k {
	case mecatlv1.ContentBlock_KIND_IMAGE:
		return session.MediaImage
	case mecatlv1.ContentBlock_KIND_AUDIO:
		return session.MediaAudio
	default:
		return ""
	}
}

// toProtoAsk maps a session.PendingAsk to its proto PermissionAsk form.
func toProtoAsk(a session.PendingAsk) *mecatlv1.PermissionAsk {
	return &mecatlv1.PermissionAsk{
		AskId:  a.AskID,
		Tool:   a.Tool,
		Args:   string(a.Args),
		Reason: a.Reason,
	}
}

// toProtoResult maps a session.ResultPayload to its proto Result form.
func toProtoResult(p session.ResultPayload) *mecatlv1.Result {
	return &mecatlv1.Result{
		Stop:  string(p.Stop),
		Text:  p.Text,
		Usage: toProtoUsage(p.Usage),
		Error: p.Error,
	}
}

// toProtoTurnEnd maps a session.TurnEndPayload to its proto TurnEnd form.
func toProtoTurnEnd(p session.TurnEndPayload) *mecatlv1.TurnEnd {
	return &mecatlv1.TurnEnd{
		Usage:      toProtoUsage(p.Usage),
		DurationMs: p.DurationMs,
	}
}

// toProtoHook maps a session.HookPayload to its proto Hook form.
func toProtoHook(h session.HookPayload) *mecatlv1.Hook {
	return &mecatlv1.Hook{
		Phase:    h.Phase,
		Tool:     h.Tool,
		Decision: hookDecisionToProto(h.Decision),
		CallId:   string(h.CallID),
	}
}

// hookDecisionToProto maps a session.HookDecision to its proto enum, defaulting
// an empty/unknown decision to INFO (the benign baseline).
func hookDecisionToProto(d session.HookDecision) mecatlv1.HookDecision {
	switch d {
	case session.HookBlocked:
		return mecatlv1.HookDecision_HOOK_DECISION_BLOCKED
	case session.HookModified:
		return mecatlv1.HookDecision_HOOK_DECISION_MODIFIED
	case session.HookAdvisory:
		return mecatlv1.HookDecision_HOOK_DECISION_ADVISORY
	case session.HookInfo:
		return mecatlv1.HookDecision_HOOK_DECISION_INFO
	default:
		return mecatlv1.HookDecision_HOOK_DECISION_INFO
	}
}

// toProtoUsage maps a session.Usage to its proto form.
func toProtoUsage(u session.Usage) *mecatlv1.Usage {
	return &mecatlv1.Usage{
		InputTokens:      int64(u.InputTokens),
		OutputTokens:     int64(u.OutputTokens),
		CacheReadTokens:  int64(u.CacheReadTokens),
		CacheWriteTokens: int64(u.CacheWriteTokens),
		ReasoningTokens:  int64(u.ReasoningTokens),
	}
}

// toProtoSession maps a session.Session aggregate to its proto snapshot.
func toProtoSession(s *session.Session, rm ResolvedModel, caps *mecatlv1.ServerCapabilities) *mecatlv1.Session {
	return &mecatlv1.Session{
		SessionId:     string(s.ID),
		State:         string(s.State),
		Mode:          modeToProto(s.Mode),
		Workspace:     s.Workspace,
		Limits:        limitsToProto(s.Limits),
		Turns:         ClampInt32(s.Counters.Turns),
		ToolCalls:     ClampInt32(s.Counters.ToolCalls),
		CreatedAtUnix: s.CreatedAt.Unix(),
		ResolvedModel: resolvedModelToProto(rm),
		Title:         s.Title,
		Capabilities:  caps,
	}
}

// resolvedModelToProto maps the server-side ResolvedModel value to its proto form.
// A zero value (empty ids, no provider) yields nil so an older-server-equivalent
// (no resolution) round-trips to "absent", letting the client fall back to today's
// behavior. The value originates from the composition single source (see
// Service.ResolvedModel) — this mapper never recomputes a resolution.
func resolvedModelToProto(rm ResolvedModel) *mecatlv1.ResolvedModel {
	if rm.ProviderID == "" && rm.ModelID == "" && rm.ContextWindow == 0 && rm.ReasoningEffort == "" {
		return nil
	}
	return &mecatlv1.ResolvedModel{
		ProviderId:      rm.ProviderID,
		ModelId:         rm.ModelID,
		ContextWindow:   rm.ContextWindow,
		ReasoningEffort: rm.ReasoningEffort,
	}
}

// limitsToProto maps session.Limits to the proto Limits message.
func limitsToProto(l session.Limits) *mecatlv1.Limits {
	return &mecatlv1.Limits{
		MaxTurns:               ClampInt32(l.MaxTurns),
		MaxToolCalls:           ClampInt32(l.MaxToolCalls),
		MaxConsecutiveFailures: ClampInt32(l.MaxConsecutiveFailures),
	}
}

// limitsFromProto maps the proto Limits message to session.Limits, treating a
// nil message as the zero (all-disabled) value.
func limitsFromProto(l *mecatlv1.Limits) session.Limits {
	if l == nil {
		return session.Limits{}
	}
	return session.Limits{
		MaxTurns:               int(l.GetMaxTurns()),
		MaxToolCalls:           int(l.GetMaxToolCalls()),
		MaxConsecutiveFailures: int(l.GetMaxConsecutiveFailures()),
	}
}

// modeToProto maps a session.PermissionMode to its proto enum.
func modeToProto(m session.PermissionMode) mecatlv1.PermissionMode {
	switch m {
	case session.ModePlan:
		return mecatlv1.PermissionMode_PERMISSION_MODE_PLAN
	case session.ModeAccept:
		return mecatlv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS
	case session.ModeDefault:
		return mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT
	default:
		return mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT
	}
}

// --- MCP inspection mappers --------------------------------------------------

// toProtoMcpResource maps an mcp.Resource value object to its proto form.
func toProtoMcpResource(r mcp.Resource) *mecatlv1.McpResource {
	return &mecatlv1.McpResource{
		Server:      r.Server,
		Uri:         r.URI,
		Name:        r.Name,
		Title:       r.Title,
		Description: r.Description,
		MimeType:    r.MIMEType,
		Size:        r.Size,
		ReadOnly:    r.ReadOnly,
	}
}

// toProtoMcpResources maps a slice of mcp.Resource to proto.
func toProtoMcpResources(rs []mcp.Resource) []*mecatlv1.McpResource {
	out := make([]*mecatlv1.McpResource, 0, len(rs))
	for _, r := range rs {
		out = append(out, toProtoMcpResource(r))
	}
	return out
}

// toProtoMcpResourceContents maps an mcp.ResourceContents chunk to its proto
// form. Binary Blob passes through unchanged as proto bytes.
func toProtoMcpResourceContents(c mcp.ResourceContents) *mecatlv1.McpResourceContents {
	return &mecatlv1.McpResourceContents{
		Uri:      c.URI,
		MimeType: c.MIMEType,
		Text:     c.Text,
		Blob:     c.Blob,
	}
}

// toProtoMcpPromptArgument maps an mcp.PromptArgument to proto.
func toProtoMcpPromptArgument(a mcp.PromptArgument) *mecatlv1.McpPromptArgument {
	return &mecatlv1.McpPromptArgument{
		Name:        a.Name,
		Title:       a.Title,
		Description: a.Description,
		Required:    a.Required,
	}
}

// toProtoMcpPrompt maps an mcp.Prompt value object to its proto form.
func toProtoMcpPrompt(p mcp.Prompt) *mecatlv1.McpPrompt {
	args := make([]*mecatlv1.McpPromptArgument, 0, len(p.Arguments))
	for _, a := range p.Arguments {
		args = append(args, toProtoMcpPromptArgument(a))
	}
	return &mecatlv1.McpPrompt{
		Server:      p.Server,
		Name:        p.Name,
		Title:       p.Title,
		Description: p.Description,
		Arguments:   args,
	}
}

// toProtoMcpPrompts maps a slice of mcp.Prompt to proto.
func toProtoMcpPrompts(ps []mcp.Prompt) []*mecatlv1.McpPrompt {
	out := make([]*mecatlv1.McpPrompt, 0, len(ps))
	for _, p := range ps {
		out = append(out, toProtoMcpPrompt(p))
	}
	return out
}

// toProtoMcpPromptMessage maps an mcp.PromptMessage to proto.
func toProtoMcpPromptMessage(m mcp.PromptMessage) *mecatlv1.McpPromptMessage {
	return &mecatlv1.McpPromptMessage{Role: m.Role, Text: m.Text}
}

// toProtoMcpSource maps a source.SourceInfo snapshot to its proto form.
func toProtoMcpSource(s source.SourceInfo) *mecatlv1.McpSource {
	servers := make([]*mecatlv1.McpServerInfo, 0, len(s.Servers))
	for _, sv := range s.Servers {
		servers = append(servers, &mecatlv1.McpServerInfo{
			Name:      sv.Name,
			Url:       sv.URL,
			Transport: sv.Transport,
			Group:     sv.Group,
		})
	}
	diags := make([]string, len(s.Diagnostics))
	copy(diags, s.Diagnostics)
	return &mecatlv1.McpSource{
		Name:        s.Name,
		Kind:        s.Kind,
		Enabled:     s.Enabled,
		Group:       s.Group,
		Servers:     servers,
		Diagnostics: diags,
	}
}

// toProtoCommands maps a slice of Service Commands to their proto form.
func toProtoCommands(cs []Command) []*mecatlv1.Command {
	out := make([]*mecatlv1.Command, 0, len(cs))
	for _, c := range cs {
		out = append(out, &mecatlv1.Command{Name: c.Name, Description: c.Description})
	}
	return out
}

// toProtoWorktree maps a Service Worktree to its proto form (issue #102).
func toProtoWorktree(w Worktree) *mecatlv1.Worktree {
	return &mecatlv1.Worktree{
		Path:   w.Path,
		Branch: w.Branch,
		Head:   w.Head,
		Bare:   w.Bare,
	}
}

// toProtoWorktrees maps a slice of Service Worktrees to their proto form.
func toProtoWorktrees(wts []Worktree) []*mecatlv1.Worktree {
	out := make([]*mecatlv1.Worktree, 0, len(wts))
	for _, w := range wts {
		out = append(out, toProtoWorktree(w))
	}
	return out
}

// toProtoSessionSummary maps a Service SessionSummary (the surface-agnostic
// picker row, issue #245 Phase 1) to its proto form. It projects ONLY the
// picker metadata — no conversation content.
func toProtoSessionSummary(s SessionSummary) *mecatlv1.SessionSummary {
	return &mecatlv1.SessionSummary{
		SessionId:      s.SessionID,
		ModifiedAtUnix: s.ModifiedAtUnix,
		State:          s.State,
		Turns:          ClampInt32(s.Turns),
		ModelId:        s.ModelID,
		CreatedAtUnix:  s.CreatedAtUnix,
		Title:          s.Title,
	}
}

// toProtoSessionSummaries maps a slice of Service SessionSummary rows to their
// proto form (mirrors toProtoWorktrees/toProtoCommands).
func toProtoSessionSummaries(rows []SessionSummary) []*mecatlv1.SessionSummary {
	out := make([]*mecatlv1.SessionSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProtoSessionSummary(r))
	}
	return out
}

// --- team mappers ------------------------------------------------------------

// toProtoTeamMember maps a team.Member roster entry to its proto form.
func toProtoTeamMember(m team.Member) *mecatlv1.TeamMember {
	return &mecatlv1.TeamMember{
		Name:      m.Name,
		AgentType: m.AgentType,
		State:     string(m.State),
		SessionId: string(m.Session),
	}
}

// toProtoTeamMembers maps a slice of team.Member to proto.
func toProtoTeamMembers(ms []team.Member) []*mecatlv1.TeamMember {
	out := make([]*mecatlv1.TeamMember, 0, len(ms))
	for _, m := range ms {
		out = append(out, toProtoTeamMember(m))
	}
	return out
}

// toProtoTeamTask maps a team.Task to its proto form.
func toProtoTeamTask(t team.Task) *mecatlv1.TeamTask {
	deps := make([]string, 0, len(t.Deps))
	for _, d := range t.Deps {
		deps = append(deps, string(d))
	}
	return &mecatlv1.TeamTask{
		Id:          string(t.ID),
		Description: t.Description,
		State:       string(t.State),
		Assignee:    t.Assignee,
		Deps:        deps,
	}
}

// toProtoTeamTasks maps a slice of team.Task to proto.
func toProtoTeamTasks(ts []team.Task) []*mecatlv1.TeamTask {
	out := make([]*mecatlv1.TeamTask, 0, len(ts))
	for _, t := range ts {
		out = append(out, toProtoTeamTask(t))
	}
	return out
}

// modeFromProto maps a proto enum to a session.PermissionMode, defaulting an
// unspecified value to ModeDefault.
func modeFromProto(m mecatlv1.PermissionMode) session.PermissionMode {
	switch m {
	case mecatlv1.PermissionMode_PERMISSION_MODE_PLAN:
		return session.ModePlan
	case mecatlv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS:
		return session.ModeAccept
	case mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED:
		return session.ModeDefault
	default:
		return session.ModeDefault
	}
}

// verdictFromResumeApproval derives the session.ApprovalVerdict from a
// ResumeApproval frame, preferring the explicit `verdict` enum and falling back
// to the legacy `allow` bool for clients that predate it (BACK-COMPAT). The
// mapping is fail-safe: an UNSPECIFIED verdict with allow=false, and any
// unrecognized value, resolve to VerdictDeny.
func verdictFromResumeApproval(verdict mecatlv1.ApprovalVerdict, allow bool) session.ApprovalVerdict {
	switch verdict {
	case mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS:
		return session.VerdictAllowAlways
	case mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE:
		return session.VerdictAllowOnce
	case mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY:
		return session.VerdictDeny
	case mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_UNSPECIFIED:
		// Legacy clients send only the bool. true -> allow once; false -> deny.
		if allow {
			return session.VerdictAllowOnce
		}
		return session.VerdictDeny
	default:
		return session.VerdictDeny
	}
}
