// Package sessiondebug provides the target-bound, read-only evidence tool used by
// dedicated debug sessions.
package sessiondebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	// ToolName is the sole tool advertised by a dedicated debug engine.
	ToolName             = "InspectSession"
	maxTranscriptRows    = 20
	maxActivityRows      = 100
	maxPerformanceRows   = 50
	maxPerformanceScan   = 10_000
	maxNetworkRows       = 50
	maxNetworkScan       = 10_000
	maxEvidenceBytes     = 64 << 10
	maxProjectedText     = 8 << 10
	maxProjectedPartText = 1 << 10
	maxProjectedItems    = 8
	maxRelatedSessions   = 500
	maxRelatedDepth      = 8
	maxHistoryRows       = 20
	maxManifestRows      = 50
	maxDelegationRows    = 100
	maxLineageEventScan  = 10_000
	errLogNotConfigured  = "event log is not configured"
	errLogReadFailed     = "event log read failed"
)

type inspectArgs struct {
	View          string `json:"view"`
	ScopeHandle   string `json:"scope_handle,omitempty"`
	HistoryHandle string `json:"history_handle,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type inspectTool struct {
	target              session.SessionID
	expectedFingerprint string
	expectedOwnerScope  [32]byte
	ownershipEnforced   bool
	store               port.SessionStore
	log                 port.EventLog
}

// New constructs an InspectSession tool from the target's current incarnation.
// Composition uses NewBound so authorization and construction share one read.
func New(target session.SessionID, store port.SessionStore, log port.EventLog) tool.Tool {
	current, err := store.Load(context.Background(), target)
	if err != nil || current == nil {
		return NewBound(target, "", nil, false, store, log)
	}
	return NewBound(target, session.DebugTargetFingerprint(current), current.Owner, false, store, log)
}

// NewBound constructs an InspectSession tool permanently bound to one authorized
// target incarnation and owner scope.
func NewBound(target session.SessionID, expectedFingerprint string, expectedOwner *session.Principal, ownershipEnforced bool, store port.SessionStore, log port.EventLog) tool.Tool {
	return &inspectTool{
		target: target, expectedFingerprint: expectedFingerprint,
		expectedOwnerScope: session.PrincipalScopeHash(expectedOwner),
		ownershipEnforced:  ownershipEnforced, store: store, log: log,
	}
}

func (t *inspectTool) loadTarget(ctx context.Context) (*session.Session, error) {
	target, err := t.store.Load(ctx, t.target)
	if err != nil || target == nil || target.ID != t.target ||
		session.DebugTargetFingerprint(target) != t.expectedFingerprint ||
		t.ownershipEnforced && session.PrincipalScopeHash(target.Owner) != t.expectedOwnerScope {
		return nil, errors.New("debug target is stale or inaccessible")
	}
	if t.ownershipEnforced {
		principal := session.PrincipalFromContext(ctx)
		if principal == nil || session.PrincipalScopeHash(principal) != t.expectedOwnerScope {
			return nil, errors.New("debug target is stale or inaccessible")
		}
	}
	return target, nil
}

func (t *inspectTool) revalidateScope(ctx context.Context, binding *lineageNode) error {
	root, err := t.loadTarget(ctx)
	if err != nil {
		return err
	}
	if binding == nil {
		return nil
	}
	candidate, err := t.store.Load(ctx, binding.ID)
	if err != nil || !t.validScopedSession(root, candidate, *binding) {
		return errors.New("scope handle is stale or inaccessible")
	}
	return nil
}

func (*inspectTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        ToolName,
		Description: "Inspect bounded read-only evidence rooted at the debug target. Start with status and related. Omit scope_handle for root/target views; only opaque scope handles returned by related evidence select retained authorized descendants. Use returned history handles to inspect archived history. Raw session IDs are never accepted.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"view":{"type":"string","enum":["status","transcript","activity","performance","network","related","delegation","history","manifest"]},"scope_handle":{"type":"string"},"history_handle":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["view"],"additionalProperties":false}`),
	}
}

func (*inspectTool) ReadOnly() bool { return true }

//nolint:gocyclo // The view dispatch preserves the target-bound evidence contract in one place.
func (t *inspectTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args inspectArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, msg), nil
	}
	if args.Offset < 0 || args.Limit < 0 {
		return session.NewToolError(call.ID, "offset must be non-negative and limit must be positive when set"), nil
	}
	if args.ScopeHandle == rootScope {
		args.ScopeHandle = ""
	}
	root, err := t.loadTarget(ctx)
	if err != nil {
		return session.NewToolError(call.ID, err.Error()), nil
	}
	var graph lineageGraph
	if args.ScopeHandle != "" || args.View == "related" || args.View == "delegation" {
		graph = t.scanLineage(ctx, root)
	}
	target, scope, scopeBinding, err := t.resolveScope(ctx, root, graph, args.ScopeHandle)
	if err != nil {
		return session.NewToolError(call.ID, err.Error()), nil
	}

	var value any
	switch args.View {
	case "status":
		value = t.statusView(ctx, target, scope)
	case "transcript":
		value = transcriptView(target, args.Offset, args.Limit)
	case "activity":
		value = t.activityView(ctx, target.ID, args.Offset, args.Limit)
	case "performance":
		value = t.performanceView(ctx, target.ID)
	case "network":
		value = t.networkView(ctx, target.ID, args.Offset, args.Limit)
	case "related":
		value = t.relatedView(graph, scope, args.Offset, args.Limit)
	case "delegation":
		value = t.delegationView(ctx, target, graph, args.Offset, args.Limit)
	case "history":
		value, err = t.historyView(ctx, target, scope, args.HistoryHandle, args.Offset, args.Limit)
	case "manifest":
		value = t.manifestView(ctx, target.ID, args.Offset, args.Limit)
	default:
		return session.NewToolError(call.ID, "view must be one of status, transcript, activity, performance, network, related, delegation, history, manifest"), nil
	}
	if err != nil {
		return session.NewToolError(call.ID, safeLine(err.Error())), nil
	}
	// Close the evidence-read TOCTOU window: deletion, replacement, owner, or
	// lineage-edge changes while a store/log projection was being read invalidate
	// the result. Root views need only the root reload; scoped views also revalidate
	// the exact descendant incarnation the opaque handle was minted for.
	if validateErr := t.revalidateScope(ctx, scopeBinding); validateErr != nil {
		return session.NewToolError(call.ID, validateErr.Error()), nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return session.ToolResult{}, fmt.Errorf("marshal session debug evidence: %w", err)
	}
	content := governance.FenceUntrusted(string(body))
	if len(content) > maxEvidenceBytes {
		return session.NewToolError(call.ID, "evidence exceeded the 64 KiB response bound"), nil
	}
	return session.NewToolResult(call.ID, content), nil
}

type statusEvidence struct {
	View                    string              `json:"view"`
	Scope                   string              `json:"scope"`
	Target                  session.SessionID   `json:"target_session_id,omitempty"`
	State                   session.State       `json:"state"`
	Kind                    session.SessionKind `json:"kind"`
	Profile                 string              `json:"profile,omitempty"`
	Provider                string              `json:"provider,omitempty"`
	Model                   string              `json:"model,omitempty"`
	Limits                  session.Limits      `json:"limits"`
	LatestRunCounters       session.Counters    `json:"latest_run_counters"`
	SnapshotCumulativeUsage session.Usage       `json:"snapshot_cumulative_usage"`
	Lifetime                lifetimeEvidence    `json:"lifetime_event_log"`
	CreatedAt               string              `json:"created_at"`
	Stop                    session.StopReason  `json:"stop,omitempty"`
	LastError               string              `json:"last_error,omitempty"`
	RepairedFields          []string            `json:"repaired_fields,omitempty"`
	PendingAsk              *pendingAskEvidence `json:"pending_ask,omitempty"`
}

type pendingAskEvidence struct {
	Tool   string             `json:"tool"`
	CallID session.ToolCallID `json:"call_id,omitempty"`
	Origin string             `json:"origin"`
}

func (t *inspectTool) statusView(ctx context.Context, s *session.Session, scope string) statusEvidence {
	out := statusEvidence{View: "status", Scope: scope, State: s.State, Kind: s.Kind, Limits: s.Limits, LatestRunCounters: s.Counters, SnapshotCumulativeUsage: s.Usage, Lifetime: t.lifetimeView(ctx, s.ID), CreatedAt: s.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	if scope == rootScope {
		out.Target = s.ID
	}
	set := func(name, value string, dst *string) {
		var repaired bool
		*dst, repaired = safeLineRepair(value)
		if repaired {
			out.RepairedFields = append(out.RepairedFields, name)
		}
	}
	set("profile", s.Profile, &out.Profile)
	set("provider", s.ProviderID, &out.Provider)
	set("model", s.ModelID, &out.Model)
	set("last_error", s.LastError(), &out.LastError)
	if stop, ok := s.StopReason(); ok {
		out.Stop = stop
	}
	if ask, ok := s.PendingAsk(); ok {
		toolName, toolRepaired := safeLineRepair(ask.Tool)
		out.PendingAsk = &pendingAskEvidence{Tool: toolName, CallID: ask.Call, Origin: askOrigin(ask.Origin())}
		if toolRepaired {
			out.RepairedFields = append(out.RepairedFields, "pending_ask.tool")
		}
	}
	return out
}

func askOrigin(origin session.AskOrigin) string {
	switch origin {
	case session.AskOriginHook:
		return "hook"
	case session.AskOriginPlan:
		return "plan"
	default:
		return "permission"
	}
}

type transcriptEvidence struct {
	View          string                  `json:"view"`
	Authoritative bool                    `json:"authoritative"`
	Offset        int                     `json:"offset"`
	Limit         int                     `json:"limit"`
	Total         int                     `json:"total"`
	Complete      bool                    `json:"complete"`
	ScanComplete  bool                    `json:"scan_complete"`
	Truncated     bool                    `json:"truncated"`
	Repaired      bool                    `json:"repaired,omitempty"`
	NextOffset    *int                    `json:"next_offset,omitempty"`
	OmittedRows   []transcriptRowOmission `json:"omitted_rows,omitempty"`
	Messages      []transcriptMessage     `json:"messages"`
}

type transcriptRowOmission struct {
	Index          int          `json:"index"`
	Role           session.Role `json:"role"`
	Reason         string       `json:"reason"`
	ProjectedBytes int          `json:"projected_bytes"`
}

type transcriptMessage struct {
	Index            int                   `json:"index"`
	Role             session.Role          `json:"role"`
	Text             string                `json:"text,omitempty"`
	TextTruncated    bool                  `json:"text_truncated,omitempty"`
	TextRepaired     bool                  `json:"text_repaired,omitempty"`
	TextBytes        int                   `json:"text_original_bytes,omitempty"`
	Parts            []transcriptPart      `json:"parts,omitempty"`
	PartsOmitted     int                   `json:"parts_omitted,omitempty"`
	ToolCalls        []transcriptToolCall  `json:"tool_calls,omitempty"`
	ToolCallsOmitted int                   `json:"tool_calls_omitted,omitempty"`
	ToolResult       *transcriptToolResult `json:"tool_result,omitempty"`
}

type transcriptToolCall struct {
	ID           string          `json:"id"`
	IDTruncated  bool            `json:"id_truncated,omitempty"`
	IDRepaired   bool            `json:"id_repaired,omitempty"`
	IDBytes      int             `json:"id_original_bytes,omitempty"`
	Name         string          `json:"name"`
	NameRepaired bool            `json:"name_repaired,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
	ArgsOmitted  bool            `json:"args_omitted,omitempty"`
	ArgsReason   string          `json:"args_omission_reason,omitempty"`
	ArgsBytes    int             `json:"args_original_bytes,omitempty"`
}

type transcriptToolResult struct {
	CallID                   string           `json:"call_id"`
	CallIDTruncated          bool             `json:"call_id_truncated,omitempty"`
	CallIDRepaired           bool             `json:"call_id_repaired,omitempty"`
	CallIDBytes              int              `json:"call_id_original_bytes,omitempty"`
	FallbackContent          string           `json:"fallback_content,omitempty"`
	FallbackContentTruncated bool             `json:"fallback_content_truncated,omitempty"`
	FallbackContentRepaired  bool             `json:"fallback_content_repaired,omitempty"`
	FallbackContentBytes     int              `json:"fallback_content_original_bytes,omitempty"`
	Parts                    []transcriptPart `json:"parts,omitempty"`
	PartsOmitted             int              `json:"parts_omitted,omitempty"`
	PartsSupersedeFallback   bool             `json:"parts_supersede_fallback"`
	IsError                  bool             `json:"is_error"`
}

type transcriptPart struct {
	BlockKind          string         `json:"block_kind,omitempty"`
	BlockKindTruncated bool           `json:"block_kind_truncated,omitempty"`
	BlockKindRepaired  bool           `json:"block_kind_repaired,omitempty"`
	BlockKindBytes     int            `json:"block_kind_original_bytes,omitempty"`
	Kind               string         `json:"kind,omitempty"`
	KindTruncated      bool           `json:"kind_truncated,omitempty"`
	KindRepaired       bool           `json:"kind_repaired,omitempty"`
	KindBytes          int            `json:"kind_original_bytes,omitempty"`
	MIMEType           string         `json:"mime_type,omitempty"`
	Text               string         `json:"text,omitempty"`
	TextTruncated      bool           `json:"text_truncated,omitempty"`
	TextRepaired       bool           `json:"text_repaired,omitempty"`
	TextBytes          int            `json:"text_original_bytes,omitempty"`
	URL                string         `json:"url,omitempty"`
	Name               string         `json:"name,omitempty"`
	Title              string         `json:"title,omitempty"`
	Description        string         `json:"description,omitempty"`
	Size               int64          `json:"size,omitempty"`
	Audience           []string       `json:"audience,omitempty"`
	AudienceOmitted    int            `json:"audience_omitted,omitempty"`
	Priority           float64        `json:"priority,omitempty"`
	LastModified       string         `json:"last_modified,omitempty"`
	TruncatedFields    map[string]int `json:"truncated_fields_original_bytes,omitempty"`
	RepairedFields     []string       `json:"repaired_fields,omitempty"`
	BinaryOmitted      bool           `json:"binary_omitted,omitempty"`
	BinaryBytes        int            `json:"binary_bytes,omitempty"`
}

func transcriptView(s *session.Session, offset, requested int) transcriptEvidence {
	limit := boundedLimit(requested, maxTranscriptRows)
	total := len(s.Conversation.Messages)
	if offset > total {
		offset = total
	}
	out := transcriptEvidence{View: "transcript", Authoritative: true, Offset: offset, Limit: limit, Total: total, Messages: []transcriptMessage{}}
	projectionComplete := true
	next := offset
	for next < total && next-offset < limit {
		row, complete := projectMessage(next, s.Conversation.Messages[next])
		out.Messages = append(out.Messages, row)
		if !fitsTranscriptPage(out) {
			out.Messages = out.Messages[:len(out.Messages)-1]
			out.OmittedRows = append(out.OmittedRows, transcriptRowOmission{
				Index: next, Role: row.Role, Reason: "projected row exceeds the response bound", ProjectedBytes: encodedSize(row),
			})
			projectionComplete = false
			next++
			break
		}
		projectionComplete = projectionComplete && complete
		out.Repaired = out.Repaired || messageRepaired(row)
		next++
	}
	out.ScanComplete = next == total
	out.Complete = out.ScanComplete && projectionComplete
	out.Truncated = !out.Complete
	if !out.ScanComplete {
		out.NextOffset = &next
	}
	return out
}

func messageRepaired(row transcriptMessage) bool {
	if row.TextRepaired || row.ToolResult != nil && (row.ToolResult.CallIDRepaired || row.ToolResult.FallbackContentRepaired) {
		return true
	}
	for _, call := range row.ToolCalls {
		if call.IDRepaired || call.NameRepaired {
			return true
		}
	}
	for _, part := range row.Parts {
		if part.BlockKindRepaired || part.KindRepaired || part.TextRepaired || len(part.RepairedFields) > 0 {
			return true
		}
	}
	if row.ToolResult != nil {
		for _, part := range row.ToolResult.Parts {
			if part.BlockKindRepaired || part.KindRepaired || part.TextRepaired || len(part.RepairedFields) > 0 {
				return true
			}
		}
	}
	return false
}

func projectMessage(index int, m session.Message) (transcriptMessage, bool) {
	text, textTruncated, textRepaired, textBytes := projectText(m.Text, maxProjectedText)
	row := transcriptMessage{Index: index, Role: m.Role, Text: text, TextTruncated: textTruncated, TextRepaired: textRepaired, TextBytes: textBytes}
	complete := !textTruncated && !textRepaired
	row.Parts, row.PartsOmitted, complete = projectParts(m.Parts, complete)
	for i, c := range m.ToolCalls {
		if i == maxProjectedItems {
			row.ToolCallsOmitted = len(m.ToolCalls) - i
			complete = false
			break
		}
		args, omitted, reason := projectArgs(c.Args)
		id, idTruncated, idRepaired, idBytes := projectText(string(c.ID), maxProjectedPartText)
		name, repaired := safeLineRepair(c.Name)
		row.ToolCalls = append(row.ToolCalls, transcriptToolCall{ID: id, IDTruncated: idTruncated, IDRepaired: idRepaired, IDBytes: idBytes, Name: name, NameRepaired: repaired, Args: args, ArgsOmitted: omitted, ArgsReason: reason, ArgsBytes: len(c.Args)})
		complete = complete && !omitted && !idTruncated && !idRepaired && !repaired
	}
	if m.ToolResult != nil {
		r := m.ToolResult
		callID, callIDTruncated, callIDRepaired, callIDBytes := projectText(string(r.CallID), maxProjectedPartText)
		content, truncated, repaired, contentBytes := projectText(r.Content, maxProjectedText)
		parts, omitted, partsComplete := projectParts(r.Parts, true)
		row.ToolResult = &transcriptToolResult{CallID: callID, CallIDTruncated: callIDTruncated, CallIDRepaired: callIDRepaired, CallIDBytes: callIDBytes, FallbackContent: content, FallbackContentTruncated: truncated, FallbackContentRepaired: repaired, FallbackContentBytes: contentBytes, Parts: parts, PartsOmitted: omitted, PartsSupersedeFallback: len(r.Parts) > 0, IsError: r.IsError}
		complete = complete && !callIDTruncated && !callIDRepaired && !truncated && !repaired && partsComplete
	}
	return row, complete
}

func projectArgs(raw json.RawMessage) (json.RawMessage, bool, string) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, true, "invalid JSON or UTF-8"
	}
	if len(raw) > maxProjectedText {
		return nil, true, "exceeds per-field evidence bound"
	}
	return append(json.RawMessage(nil), raw...), false, ""
}

func projectParts(parts []session.Content, complete bool) ([]transcriptPart, int, bool) {
	var out []transcriptPart
	for i, p := range parts {
		if i == maxProjectedItems {
			return out, len(parts) - i, false
		}
		text, truncated, repaired, textBytes := projectText(p.Text, maxProjectedPartText)
		blockKind, blockKindTruncated, blockKindRepaired, blockKindBytes := projectText(string(p.BlockKind), maxProjectedPartText)
		kind, kindTruncated, kindRepaired, kindBytes := projectText(string(p.Kind), maxProjectedPartText)
		part := transcriptPart{
			BlockKind: blockKind, BlockKindTruncated: blockKindTruncated, BlockKindRepaired: blockKindRepaired, BlockKindBytes: blockKindBytes,
			Kind: kind, KindTruncated: kindTruncated, KindRepaired: kindRepaired, KindBytes: kindBytes,
			Text: text, TextTruncated: truncated, TextRepaired: repaired, TextBytes: textBytes,
			Size: p.Size, Priority: p.Priority, BinaryOmitted: len(p.Data) > 0, BinaryBytes: len(p.Data),
		}
		part.MIMEType, complete = projectPartField(p.MIMEType, "mime_type", &part, complete)
		part.MIMEType = safeLine(part.MIMEType)
		part.URL, complete = projectPartField(p.URL, "url", &part, complete)
		part.Name, complete = projectPartField(p.Name, "name", &part, complete)
		part.Title, complete = projectPartField(p.Title, "title", &part, complete)
		part.Description, complete = projectPartField(p.Description, "description", &part, complete)
		part.LastModified, complete = projectPartField(p.LastModified, "last_modified", &part, complete)
		for j, audience := range p.Audience {
			if j == maxProjectedItems {
				part.AudienceOmitted = len(p.Audience) - j
				complete = false
				break
			}
			value, fieldComplete := projectPartField(audience, fmt.Sprintf("audience[%d]", j), &part, true)
			part.Audience = append(part.Audience, value)
			complete = complete && fieldComplete
		}
		out = append(out, part)
		complete = complete && !blockKindTruncated && !blockKindRepaired && !kindTruncated && !kindRepaired && !truncated && !repaired && len(p.Data) == 0
	}
	return out, 0, complete
}

func projectPartField(value, name string, part *transcriptPart, complete bool) (string, bool) {
	projected, truncated, repaired, originalBytes := projectText(value, maxProjectedPartText)
	if truncated {
		if part.TruncatedFields == nil {
			part.TruncatedFields = make(map[string]int)
		}
		part.TruncatedFields[name] = originalBytes
		complete = false
	}
	if repaired {
		part.RepairedFields = append(part.RepairedFields, name)
		complete = false
	}
	return projected, complete
}

func projectText(value string, limit int) (string, bool, bool, int) {
	original := len(value)
	projected := session.ToValidUTF8(value)
	repaired := projected != value
	if len(projected) <= limit {
		if repaired {
			return projected, false, true, original
		}
		return projected, false, false, 0
	}
	for limit > 0 && !utf8.RuneStart(projected[limit]) {
		limit--
	}
	return projected[:limit], true, repaired, original
}

type activityEvidence struct {
	View          string        `json:"view"`
	Available     bool          `json:"available"`
	Authoritative bool          `json:"authoritative"`
	Complete      bool          `json:"complete"`
	Offset        int           `json:"offset"`
	Limit         int           `json:"limit"`
	Truncated     bool          `json:"truncated"`
	Repaired      bool          `json:"repaired,omitempty"`
	NextOffset    *int          `json:"next_offset,omitempty"`
	Error         string        `json:"error,omitempty"`
	Rows          []activityRow `json:"rows"`
}

type activityRow struct {
	Index        int                `json:"index"`
	Type         session.EventType  `json:"type"`
	Seq          int64              `json:"seq"`
	Turn         int                `json:"turn"`
	Text         string             `json:"text,omitempty"`
	TextRepaired bool               `json:"text_repaired,omitempty"`
	Tool         string             `json:"tool,omitempty"`
	ToolRepaired bool               `json:"tool_repaired,omitempty"`
	ToolError    *bool              `json:"tool_error,omitempty"`
	Stop         session.StopReason `json:"stop,omitempty"`
	Usage        *session.Usage     `json:"usage,omitempty"`
	Duration     int64              `json:"duration_ms,omitempty"`
	TTFT         int64              `json:"ttft_ms,omitempty"`
}

func (t *inspectTool) activityView(ctx context.Context, target session.SessionID, offset, requested int) activityEvidence {
	limit := boundedLimit(requested, maxActivityRows)
	out := activityEvidence{View: "activity", Available: t.log != nil, Authoritative: false, Complete: false, Offset: offset, Limit: limit, Rows: []activityRow{}}
	if t.log == nil {
		out.Error = errLogNotConfigured
		return out
	}
	seen := 0
	for ev, err := range t.log.Read(ctx, target) {
		if err != nil {
			out.Error = safeLine(err.Error())
			return out
		}
		if highVolume(ev.Type) {
			continue
		}
		if seen < offset {
			seen++
			continue
		}
		row := normalizeActivity(seen, ev)
		seen++
		if len(out.Rows) == limit {
			out.Truncated = true
			break
		}
		out.Rows = append(out.Rows, row)
		if !fitsEvidence(out) {
			out.Rows = out.Rows[:len(out.Rows)-1]
			out.Truncated = true
			break
		}
		out.Repaired = out.Repaired || row.TextRepaired || row.ToolRepaired
	}
	if out.Truncated {
		next := offset + len(out.Rows)
		out.NextOffset = &next
	}
	return out
}

func highVolume(t session.EventType) bool {
	return t == session.EvMessageDelta || t == session.EvReasoningDelta || t == session.EvToolProgress
}

func normalizeActivity(index int, ev session.Event) activityRow {
	text := session.ToValidUTF8(ev.Text)
	row := activityRow{Index: index, Type: ev.Type, Seq: ev.Seq, Turn: ev.Turn, Text: text, TextRepaired: text != ev.Text}
	if ev.ToolCall != nil {
		row.Tool, row.ToolRepaired = safeLineRepair(ev.ToolCall.Name)
	}
	if ev.ToolResult != nil {
		failed := ev.ToolResult.IsError
		row.ToolError = &failed
	}
	if ev.Result != nil {
		row.Stop, row.Usage = ev.Result.Stop, &ev.Result.Usage
	}
	if ev.TurnEnd != nil {
		row.Usage, row.Duration, row.TTFT = &ev.TurnEnd.Usage, ev.TurnEnd.DurationMs, ev.TurnEnd.TTFTMs
	}
	return row
}

type performanceEvidence struct {
	View          string            `json:"view"`
	Available     bool              `json:"available"`
	Authoritative bool              `json:"authoritative"`
	Complete      bool              `json:"complete"`
	ScanComplete  bool              `json:"scan_complete"`
	Truncated     bool              `json:"truncated"`
	Error         string            `json:"error,omitempty"`
	Scanned       int               `json:"scanned_events"`
	Totals        performanceTotals `json:"totals"`
	Turns         []performanceTurn `json:"turns"`
}

type performanceTotals struct {
	Usage        session.Usage              `json:"usage"`
	DurationMs   int64                      `json:"duration_ms"`
	Retries      int                        `json:"retries"`
	ToolFailures int                        `json:"tool_failures"`
	Stops        int                        `json:"stops"`
	StopReasons  map[session.StopReason]int `json:"stop_reasons,omitempty"`
}

type performanceTurn struct {
	Turn             int           `json:"turn"`
	DurationMs       int64         `json:"duration_ms"`
	TTFTMs           int64         `json:"ttft_ms"`
	InterTokenMeanMs int64         `json:"inter_token_mean_ms"`
	InterTokenMaxMs  int64         `json:"inter_token_max_ms"`
	Usage            session.Usage `json:"usage"`
}

func (t *inspectTool) performanceView(ctx context.Context, target session.SessionID) performanceEvidence {
	out := performanceEvidence{View: "performance", Available: t.log != nil, Authoritative: false, Complete: false, Turns: []performanceTurn{}}
	if t.log == nil {
		out.Error = errLogNotConfigured
		return out
	}
	out.ScanComplete = true
	for ev, err := range t.log.Read(ctx, target) {
		if err != nil {
			out.Error = safeLine(err.Error())
			out.ScanComplete = false
			return out
		}
		if out.Scanned == maxPerformanceScan {
			out.Truncated = true
			out.ScanComplete = false
			break
		}
		out.Scanned++
		switch ev.Type {
		case session.EvTurnEnd:
			if ev.TurnEnd != nil {
				p := ev.TurnEnd
				out.Totals.Usage = out.Totals.Usage.Add(p.Usage)
				out.Totals.DurationMs += p.DurationMs
				if len(out.Turns) < maxPerformanceRows {
					out.Turns = append(out.Turns, performanceTurn{Turn: ev.Turn, DurationMs: p.DurationMs, TTFTMs: p.TTFTMs, InterTokenMeanMs: p.InterTokenMeanMs, InterTokenMaxMs: p.InterTokenMaxMs, Usage: p.Usage})
				} else {
					out.Truncated = true
				}
			}
		case session.EvModelRetry:
			out.Totals.Retries++
		case session.EvToolResult:
			if ev.ToolResult != nil && ev.ToolResult.IsError {
				out.Totals.ToolFailures++
			}
		case session.EvResult:
			out.Totals.Stops++
			if ev.Result != nil {
				if out.Totals.StopReasons == nil {
					out.Totals.StopReasons = make(map[session.StopReason]int)
				}
				out.Totals.StopReasons[ev.Result.Stop]++
			}
		}
	}
	return out
}

type networkEvidence struct {
	View                    string                   `json:"view"`
	Available               bool                     `json:"available"`
	Authoritative           bool                     `json:"authoritative"`
	Coverage                string                   `json:"coverage"`
	SuccessfulAttemptsTimed bool                     `json:"successful_attempts_timed"`
	Complete                bool                     `json:"complete"`
	ScanComplete            bool                     `json:"scan_complete"`
	Truncated               bool                     `json:"truncated"`
	Error                   string                   `json:"error,omitempty"`
	Offset                  int                      `json:"offset"`
	Limit                   int                      `json:"limit"`
	ScannedEvents           int                      `json:"scanned_events"`
	TotalMatched            int                      `json:"matched_attempts"`
	InvalidOmitted          int                      `json:"invalid_attempts_omitted,omitempty"`
	NextOffset              *int                     `json:"next_offset,omitempty"`
	Attempts                []networkAttemptEvidence `json:"attempts"`
}

type networkAttemptEvidence struct {
	RunSerial         int64  `json:"run_serial"`
	Turn              int    `json:"turn"`
	Attempt           int    `json:"attempt"`
	MaxAttempts       int    `json:"max_attempts"`
	ElapsedMs         int64  `json:"elapsed_ms"`
	RetryDisposition  string `json:"retry_disposition"`
	StreamProgress    string `json:"stream_progress"`
	Decision          string `json:"decision"`
	SuppressionReason string `json:"suppression_reason,omitempty"`
	BackoffMs         int64  `json:"backoff_ms"`
	FailureClass      string `json:"failure_class"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	InBandStatus      int    `json:"in_band_status,omitempty"`
	CorrelationKind   string `json:"correlation_kind,omitempty"`
	CorrelationDigest string `json:"correlation_digest,omitempty"`
}

func (t *inspectTool) networkView(ctx context.Context, target session.SessionID, offset, requested int) networkEvidence {
	limit := boundedLimit(requested, maxNetworkRows)
	out := networkEvidence{
		View: "network", Available: t.log != nil, Authoritative: false,
		Coverage:                "failed and policy-interesting attempts observed by the shared resilience wrapper; no request bodies, headers, URLs, raw errors, per-phase DNS/TCP/TLS timing, or successful-attempt timing",
		SuccessfulAttemptsTimed: false, Offset: offset, Limit: limit,
		Attempts: []networkAttemptEvidence{},
	}
	if t.log == nil {
		out.Error = errLogNotConfigured
		return out
	}
	out.ScanComplete = true
	matched := 0
	for ev, err := range t.log.Read(ctx, target) {
		if err != nil {
			out.Error = errLogReadFailed
			out.ScanComplete = false
			break
		}
		if out.ScannedEvents == maxNetworkScan {
			out.Truncated = true
			out.ScanComplete = false
			break
		}
		out.ScannedEvents++
		if ev.Type != session.EvNetworkAttempt || ev.NetworkAttempt == nil {
			continue
		}
		row := *ev.NetworkAttempt
		if !validNetworkAttempt(row, target) {
			out.InvalidOmitted++
			continue
		}
		if matched >= offset && len(out.Attempts) < limit {
			out.Attempts = append(out.Attempts, projectNetworkAttempt(row))
		}
		matched++
	}
	out.TotalMatched = matched
	if out.ScanComplete && offset+len(out.Attempts) < matched {
		next := offset + len(out.Attempts)
		out.NextOffset = &next
		out.Truncated = true
	}
	out.Complete = out.ScanComplete && out.NextOffset == nil && out.InvalidOmitted == 0
	return out
}

func projectNetworkAttempt(row session.NetworkAttemptPayload) networkAttemptEvidence {
	return networkAttemptEvidence{row.RunSerial, row.Turn, row.Attempt, row.MaxAttempts, row.ElapsedMs, row.RetryDisposition, row.StreamProgress, row.Decision, row.SuppressionReason, row.BackoffMs, row.FailureClass, row.HTTPStatus, row.InBandStatus, row.CorrelationKind, row.CorrelationDigest}
}

func validNetworkAttempt(row session.NetworkAttemptPayload, target session.SessionID) bool {
	if row.SessionID != target {
		return false
	}
	_, ok := session.CanonicalNetworkAttempt(row, target, row.RunSerial, row.Turn)
	return ok
}

func boundedLimit(requested, ceiling int) int {
	if requested <= 0 || requested > ceiling {
		return ceiling
	}
	return requested
}

func encodedSize(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}

func fitsEvidence(v any) bool {
	b, err := json.Marshal(v)
	return err == nil && len(governance.FenceUntrusted(string(b))) <= maxEvidenceBytes
}

func fitsTranscriptPage(v transcriptEvidence) bool {
	probe := v
	probe.OmittedRows = append(append([]transcriptRowOmission(nil), v.OmittedRows...), transcriptRowOmission{
		Index: int(^uint(0) >> 1), Role: session.RoleAssistant, Reason: "projected row exceeds the response bound", ProjectedBytes: int(^uint(0) >> 1),
	})
	return fitsEvidence(probe)
}

func safeLine(value string) string {
	projected, _ := safeLineRepair(value)
	return projected
}

func safeLineRepair(value string) (string, bool) {
	projected := session.ToValidUTF8(value)
	return strings.Join(strings.Fields(projected), " "), projected != value
}
