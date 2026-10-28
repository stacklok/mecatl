package eventsource

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// Projection contains projected source records. Events preserves their count,
// order, and durable coordinates; it contains no synthetic records.
type Projection struct {
	Events []session.Event
}

// Project converts a complete source using the same incremental conversion as Fold.
// The caller supplies its stable incarnation and FULL event-only EventLog.Read
// sequence, never a cursor page (use eventcodec.ProjectRecord for those).
func Project(events []session.Event, inc session.IncarnationID) (Projection, error) {
	if !inc.Valid() {
		return Projection{}, fmt.Errorf("%w: missing stable source incarnation", ErrReconstruct)
	}
	c := legacyConverter{inc: inc}
	out := Projection{Events: make([]session.Event, 0, len(events))}
	for _, e := range events {
		converted, err := c.convert(e)
		if err != nil {
			return Projection{}, fmt.Errorf("%w: %w", ErrReconstruct, err)
		}
		out.Events = append(out.Events, converted)
	}
	if err := c.finish(); err != nil {
		return Projection{}, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	return out, nil
}

type legacyCall struct {
	id   session.ToolCallID
	tool string
}

type legacyConverter struct {
	inc     session.IncarnationID
	open    map[[3]string]legacyCall
	pending *[3]string
}

func keyFor(p *session.LegacyEventIdentity, id string) [3]string {
	return [3]string{string(p.Incarnation), p.Namespace, id}
}

func (c *legacyConverter) convert(e session.Event) (session.Event, error) {
	p := e.LegacyIdentity()
	if p == nil {
		return c.convertCurrent(e)
	}
	if p.Incarnation == "" {
		p.Incarnation = c.inc
	}
	if p.Namespace == "" {
		p.Namespace = eventcodec.LegacyNamespace
	}
	if p.Coordinate == "" {
		if e.RunID != "" {
			p.Coordinate = fmt.Sprintf("run:%s/seq:%d", e.RunID, e.Seq)
		} else if p.Ordinal >= 0 {
			p.Coordinate = fmt.Sprintf("event:%d", p.Ordinal)
		}
	}
	old, err := eventcodec.DecodeLegacyRecord(p.Payload)
	if err != nil {
		return session.Event{}, err
	}
	base, err := eventcodec.Unmarshal(p.Payload)
	if err != nil {
		return session.Event{}, err
	}
	base, err = c.convertLegacy(e, base.WithLegacyIdentity(*p), old, p)
	if err != nil {
		return session.Event{}, err
	}
	if base.Historical != nil && p.Coordinate != "" {
		base.Historical.RecordID = session.HistoricalRecordID(p.Incarnation, p.Namespace, p.Coordinate)
	}
	return base, nil
}

func (c *legacyConverter) convertCurrent(e session.Event) (session.Event, error) {
	switch e.Type {
	case session.EvUserPrompt, session.EvTurnStart, session.EvModelRetry, session.EvResult:
		if len(c.open) != 0 {
			return session.Event{}, fmt.Errorf("eventsource: current activity overlaps unresolved legacy calls")
		}
	case session.EvPermissionAsk, session.EvApproval, session.EvPermissionRetract:
		if c.pending != nil {
			return session.Event{}, fmt.Errorf("eventsource: current control overlaps legacy approval")
		}
	}
	return e, nil
}

func (c *legacyConverter) convertLegacy(e, base session.Event, old eventcodec.LegacyRecord, p *session.LegacyEventIdentity) (session.Event, error) {
	switch old.Type {
	case session.EvToolCall:
		return c.convertCall(e, base, old.ToolCall, p)
	case session.EvToolResult:
		return c.convertResult(e, base, old.ToolResult, p)
	case session.EvPermissionAsk:
		return c.convertAsk(e, base, old.Ask, p)
	case session.EvAuthorizationRequired, session.EvAuthorizationResolved:
		return session.Event{}, fmt.Errorf("eventsource: unsupported legacy authorization")
	case session.EvApproval, session.EvPermissionRetract:
		if c.pending != nil && ((*c.pending)[0] != string(p.Incarnation) || (*c.pending)[1] != p.Namespace) {
			return session.Event{}, fmt.Errorf("eventsource: legacy approval crosses sources")
		}
		c.pending = nil
	case session.EvUserPrompt, session.EvTurnStart, session.EvModelRetry, session.EvResult:
		if len(c.open) != 0 {
			return session.Event{}, fmt.Errorf("eventsource: activity overlaps unresolved legacy calls")
		}
		c.pending = nil
	case session.EvCompactionArchive:
		return convertArchive(base, old.CompactionArchive, p)
	}
	return base, nil
}

func (c *legacyConverter) convertCall(e, base session.Event, old *eventcodec.LegacyCall, p *session.LegacyEventIdentity) (session.Event, error) {
	if old == nil || old.ID == "" || (p.Coordinate == "" && (e.ToolCall == nil || e.ToolCall.ID == "")) {
		return session.Event{}, fmt.Errorf("eventsource: legacy call lacks a stable invocation reference")
	}
	key := keyFor(p, old.ID)
	if _, exists := c.open[key]; exists {
		return session.Event{}, fmt.Errorf("eventsource: duplicate outstanding legacy call")
	}
	id := session.ToolCallID("call_" + session.HistoricalRecordID(p.Incarnation, p.Namespace, p.Coordinate))
	if p.Ordinal >= 0 && p.Coordinate == fmt.Sprintf("event:%d", p.Ordinal) {
		id = session.HistoricalInvocationID(p.Incarnation, p.Namespace, p.Ordinal)
	}
	if e.ToolCall != nil && e.ToolCall.ID != "" {
		id = e.ToolCall.ID // Persisted canonical bindings win over re-derivation.
	}
	if c.open == nil {
		c.open = make(map[[3]string]legacyCall)
	}
	c.open[key] = legacyCall{id: id, tool: old.Name}
	call := session.NewToolCall(id, old.Name, old.Args)
	base.Type, base.Historical, base.ToolCall = session.EvToolCall, nil, &call
	return base, nil
}

func (c *legacyConverter) convertResult(e, base session.Event, old *eventcodec.LegacyResult, p *session.LegacyEventIdentity) (session.Event, error) {
	if old == nil || old.CallID == "" {
		return session.Event{}, fmt.Errorf("eventsource: legacy result lacks a call reference")
	}
	key := keyFor(p, old.CallID)
	call, ok := c.open[key]
	if !ok {
		return session.Event{}, fmt.Errorf("eventsource: orphan legacy result")
	}
	if e.ToolResult != nil && e.ToolResult.CallID != call.id {
		return session.Event{}, fmt.Errorf("eventsource: conflicting persisted legacy result binding")
	}
	delete(c.open, key)
	base.Type, base.Historical = session.EvToolResult, nil
	base.ToolResult = &session.ToolResult{CallID: call.id, Content: old.Content, IsError: old.IsError, Parts: old.Parts}
	return base, nil
}

func (c *legacyConverter) convertAsk(e, base session.Event, a *eventcodec.LegacyAsk, p *session.LegacyEventIdentity) (session.Event, error) {
	if a == nil || a.AskID == "" || a.Call == "" || c.pending != nil {
		return session.Event{}, fmt.Errorf("eventsource: unprovable legacy approval")
	}
	key := keyFor(p, a.Call)
	call, ok := c.open[key]
	if !ok || call.tool != a.Tool || (e.Ask != nil && e.Ask.Call != call.id) {
		return session.Event{}, fmt.Errorf("eventsource: unproven legacy approval reference")
	}
	c.pending = &key
	base.Type, base.Historical = session.EvPermissionAsk, nil
	base.Ask = &session.PendingAsk{AskID: a.AskID, Tool: a.Tool, Args: a.Args, Reason: a.Reason, Call: call.id, Guardrail: a.Guardrail, Origin: a.Origin}
	return base, nil
}

func convertArchive(base session.Event, archive []byte, p *session.LegacyEventIdentity) (session.Event, error) {
	if len(archive) != 0 && string(archive) != "null" {
		if p.Coordinate == "" {
			return session.Event{}, fmt.Errorf("eventsource: legacy archive lacks a stable coordinate")
		}
		messages, err := sessnap.DecodeLegacyArchive(archive, p.Incarnation, p.Namespace+"/archive/"+p.Coordinate)
		if err != nil {
			return session.Event{}, err
		}
		base.CompactionArchive = &session.CompactionArchivePayload{Replaced: messages}
	}
	return base, nil
}

func (c *legacyConverter) finish() error {
	if c.pending != nil {
		if _, ok := c.open[*c.pending]; !ok || len(c.open) != 1 {
			return fmt.Errorf("eventsource: unprovable pending legacy approval")
		}
	} else if len(c.open) != 0 {
		return fmt.Errorf("eventsource: unanswered legacy calls")
	}
	return nil
}
