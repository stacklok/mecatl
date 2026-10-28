package eventcodec

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// decodeLegacyEvent keeps invocation-bearing fields in private legacy DTOs.
// Only display-safe fields enter the current Event before complete-source proof.
func decodeLegacyEvent(data []byte) (session.Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return session.Event{}, err
	}
	if fields == nil {
		return session.Event{}, fmt.Errorf("eventcodec: event must be an object")
	}
	// Remove all invocation-bearing payloads BEFORE unmarshalling the safe fields.
	// The untouched bytes remain private, including archive replay and child IDs.
	for key := range fields {
		switch strings.ToLower(key) {
		case "toolcall", "toolresult", "ask", "hook", "approval", "authorization", "subagent", "team", "parallel", "compactionarchive", "historical", "legacy_identity":
			delete(fields, key)
		}
	}
	safe, err := json.Marshal(fields)
	if err != nil {
		return session.Event{}, err
	}
	var e session.Event
	if err := json.Unmarshal(safe, &e); err != nil {
		return session.Event{}, err
	}
	old, err := decodeLegacy(data)
	if err != nil {
		return session.Event{}, err
	}
	e = e.WithLegacyIdentity(session.LegacyEventIdentity{Payload: data, Ordinal: -1})
	if old.hasAttachment() {
		e.Type = session.EvHistorical
		e.Historical = old.historical()
	}
	return e, nil
}

// LegacyCall contains original provider references, not canonical tool-call IDs.
type LegacyCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type legacyCall struct {
	ID     string
	Name   string
	Args   json.RawMessage
	ItemID string `json:"item_id"`
}

// LegacyResult contains the original result reference and content.
type LegacyResult struct {
	CallID  string
	Content string
	IsError bool
	Parts   []session.Content
}

// LegacyAsk contains original approval facts without granting continuation authority.
type LegacyAsk struct {
	AskID     string
	Tool      string
	Args      json.RawMessage
	Reason    string
	Call      string `json:"call"`
	Guardrail *session.GuardrailPendingScope
	Origin    session.ApprovalOrigin
}
type legacyEvent struct {
	Type       session.EventType
	Text       string
	ToolCall   *legacyCall
	ToolResult *LegacyResult
	Ask        *LegacyAsk
	Hook       *struct {
		Phase, Tool string
		Decision    session.HookDecision
		CallID      string
	}
	Approval *struct {
		AskID, Verdict, Tool, Call string
		AllowAlways                bool
		Origin                     session.ApprovalOrigin
	}
	Authorization *struct {
		AuthorizationID, Call, DisplayName string
		Status                             session.AuthorizationStatus
	}
	Subagent *struct{ Goal, ToolName, Text, Detail string }
	Team     *struct {
		Member, Text, ToolName, Detail string
		Findings                       []session.TeamFindingSnapshot
		Tasks                          []session.TeamTaskSnapshot
	}
	Parallel          *struct{ Goal, ToolName, Text, Detail string }
	CompactionArchive json.RawMessage
}

// LegacyRecord exposes only raw facts needed for complete-source reconstruction.
// Its string references remain noncanonical until the source proves their pairing.
// Display-only attachments stay private to the codec.
type LegacyRecord struct {
	Type          session.EventType
	ToolCall      *LegacyCall
	ToolResult    *LegacyResult
	Ask           *LegacyAsk
	Authorization *struct {
		AuthorizationID, Call string
		Status                session.AuthorizationStatus
	}
	CompactionArchive json.RawMessage
}

// DecodeLegacyRecord decodes legacy storage facts without reconstructing history.
func DecodeLegacyRecord(data []byte) (LegacyRecord, error) {
	old, err := decodeLegacy(data)
	if err != nil {
		return LegacyRecord{}, err
	}
	record := LegacyRecord{Type: old.Type, ToolResult: old.ToolResult, Ask: old.Ask, CompactionArchive: old.CompactionArchive}
	if old.ToolCall != nil {
		record.ToolCall = &LegacyCall{ID: old.ToolCall.ID, Name: old.ToolCall.Name, Args: old.ToolCall.Args}
	}
	if old.Authorization != nil {
		record.Authorization = &struct {
			AuthorizationID, Call string
			Status                session.AuthorizationStatus
		}{old.Authorization.AuthorizationID, old.Authorization.Call, old.Authorization.Status}
	}
	return record, nil
}

func decodeLegacy(data []byte) (legacyEvent, error) {
	var old legacyEvent
	err := json.Unmarshal(data, &old)
	return old, err
}
func (e legacyEvent) hasAttachment() bool {
	switch e.Type {
	case session.EvPermissionAsk, session.EvAuthorizationRequired, session.EvAuthorizationResolved:
		return true
	}
	return e.ToolCall != nil || e.ToolResult != nil || e.Ask != nil || e.Hook != nil || e.Approval != nil || e.Authorization != nil || e.Subagent != nil || e.Team != nil || e.Parallel != nil
}
func (e legacyEvent) historical() *session.HistoricalRecord {
	h := &session.HistoricalRecord{Kind: string(e.Type), Text: e.Text, AttachmentUnavailable: true}
	add := func(s string) {
		if s != "" {
			if h.Text != "" {
				h.Text += "\n"
			}
			h.Text += s
		}
	}
	if e.ToolCall != nil {
		add(e.ToolCall.Name)
		add(string(e.ToolCall.Args))
	}
	if e.ToolResult != nil {
		add(e.ToolResult.Content)
		h.Parts = e.ToolResult.Parts
	}
	if e.Ask != nil {
		add(e.Ask.Tool)
		add(string(e.Ask.Args))
		add(e.Ask.Reason)
	}
	if e.Hook != nil {
		add(e.Hook.Phase)
		add(e.Hook.Tool)
		add(string(e.Hook.Decision))
	}
	if e.Approval != nil {
		add(e.Approval.Tool)
		add(e.Approval.Verdict)
	}
	if e.Authorization != nil {
		add(e.Authorization.DisplayName)
		add(string(e.Authorization.Status))
	}
	// Only the pre-existing redacted display fields cross the child boundary.
	if e.Subagent != nil {
		add(e.Subagent.Goal)
		add(e.Subagent.ToolName)
		add(e.Subagent.Text)
		add(e.Subagent.Detail)
	}
	if e.Parallel != nil {
		add(e.Parallel.Goal)
		add(e.Parallel.ToolName)
		add(e.Parallel.Text)
		add(e.Parallel.Detail)
	}
	if e.Team != nil {
		add(e.Team.Member)
		add(e.Team.ToolName)
		add(e.Team.Text)
		add(e.Team.Detail)
		for _, finding := range e.Team.Findings {
			add(finding.Body)
		}
		for _, task := range e.Team.Tasks {
			add(task.Description)
		}
	}
	return h
}
