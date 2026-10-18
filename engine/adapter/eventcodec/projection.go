package eventcodec

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// LegacyNamespace identifies the source namespace for legacy event-log records.
const LegacyNamespace = "eventlog/legacy-v1"

// Projection contains a safe event stream and private reconstruction facts.
// PendingHold is opaque legacy continuation data, never public or executable.
type Projection struct {
	Events            []session.Event
	PendingHold       []byte
	OpenAuthorization bool
}

type occurrence struct {
	index                     int
	call                      legacyCall
	id                        session.ToolCallID
	unsafe, answered, pending bool
}

// ProjectRecord prepares one reader/cursor record for public mapping without
// trying to pair it against another source or a partial cursor page. Unproven
// legacy records become historical content rather than canonical activity links.
// Normal canonical payloads remain normal. Archives are independent closed sources.
func ProjectRecord(e session.Event, inc session.IncarnationID, ordinal int) (session.Event, error) {
	p := e.LegacyIdentity()
	if p == nil {
		return session.CloneEventIdentity(e), nil
	}
	if p.Incarnation != "" {
		inc = p.Incarnation
	} else {
		p.Incarnation = inc
	}
	if !inc.Valid() {
		return session.Event{}, fmt.Errorf("eventcodec: missing stable source incarnation")
	}
	if p.Namespace == "" {
		p.Namespace = LegacyNamespace
	}
	// A retained-page index is not an original evidence ordinal.
	if p.Coordinate == "" {
		if e.RunID != "" {
			p.Coordinate = fmt.Sprintf("run:%s/seq:%d", e.RunID, e.Seq)
		} else if p.Ordinal >= 0 {
			p.Coordinate = fmt.Sprintf("event:%d", p.Ordinal)
		}
	}
	coordinate := p.Coordinate
	if coordinate == "" {
		if ordinal < 0 {
			return session.Event{}, fmt.Errorf("eventcodec: missing original source position")
		}
		coordinate = fmt.Sprintf("event:%d", ordinal)
	}
	e = e.WithLegacyIdentity(*p)
	h, err := session.HistoricalEvent(e, inc, ordinal)
	if err != nil {
		return session.Event{}, err
	}
	e.Historical = h
	if e.Type == session.EvCompactionArchive && e.CompactionArchive == nil {
		old, err := decodeLegacy(p.Payload)
		if err != nil {
			return session.Event{}, err
		}
		if len(old.CompactionArchive) != 0 && string(old.CompactionArchive) != "null" {
			messages, err := sessnap.DecodeLegacyArchive(old.CompactionArchive, inc, p.Namespace+"/archive/"+coordinate)
			if err != nil {
				return session.Event{}, err
			}
			e.CompactionArchive = &session.CompactionArchivePayload{Replaced: messages}
		}
	}
	return e, nil
}

// Project proves pairs only within this complete source. It preserves event order
// and count; no synthetic calls, snapshot crosswalk, or provider-key public aliases.
// The caller must supply the stable source incarnation and the FULL event-only
// EventLog.Read sequence, never a cursor page (use ProjectRecord for those).
func Project(events []session.Event, inc session.IncarnationID) (Projection, error) {
	if !inc.Valid() {
		return Projection{}, fmt.Errorf("eventcodec: missing stable source incarnation")
	}
	index := projectionIndex{
		olds:       make([]*legacyEvent, len(events)),
		identities: make([]*session.LegacyEventIdentity, len(events)),
		calls:      make(map[int]*occurrence), results: make(map[int]*occurrence), asks: make(map[int]*occurrence),
		open: make(map[string][]*occurrence), auth: make(map[string]string), pendingIndex: -1,
	}
	out := Projection{Events: slices.Clone(events)}
	if err := index.collect(&out, events, inc); err != nil {
		return Projection{}, err
	}
	if err := index.provePending(&out); err != nil {
		return Projection{}, err
	}
	out.OpenAuthorization = len(index.auth) != 0
	index.projectEvents(&out)
	return out, nil
}

type projectionIndex struct {
	olds                 []*legacyEvent
	identities           []*session.LegacyEventIdentity
	calls, results, asks map[int]*occurrence
	open                 map[string][]*occurrence
	auth                 map[string]string
	pendingIndex         int
}

func keyFor(p *session.LegacyEventIdentity, id string) string {
	b, _ := json.Marshal([]string{string(p.Incarnation), p.Namespace, id})
	return string(b)
}

func (x *projectionIndex) collect(out *Projection, events []session.Event, inc session.IncarnationID) error {
	for i, e := range events {
		p := e.LegacyIdentity()
		if p == nil {
			switch e.Type {
			case session.EvPermissionAsk, session.EvApproval, session.EvPermissionRetract, session.EvResult:
				x.pendingIndex = -1
			}
			continue
		}
		anchored := p.Coordinate != "" || p.Ordinal >= 0 || e.RunID != "" || e.ToolCall != nil
		if err := x.indexRecord(out, i, e, p, inc); err != nil {
			return err
		}
		x.indexCalls(i, x.olds[i], p)
		if occ := x.calls[i]; occ != nil && !anchored {
			occ.unsafe = true
		}
		if e.ToolCall != nil {
			if occ := x.calls[i]; occ != nil && e.ToolCall.ID != "" {
				occ.id = e.ToolCall.ID // A persisted canonical binding wins over re-derivation.
			}
		}
		x.indexLifecycle(i, x.olds[i], p)
	}
	return nil
}

func (x *projectionIndex) indexRecord(out *Projection, i int, e session.Event, p *session.LegacyEventIdentity, inc session.IncarnationID) error {
	if p.Incarnation == "" {
		p.Incarnation = inc
	}
	if p.Namespace == "" {
		p.Namespace = LegacyNamespace
	}
	if p.Coordinate == "" {
		if e.RunID != "" {
			p.Coordinate = fmt.Sprintf("run:%s/seq:%d", e.RunID, e.Seq)
		} else if p.Ordinal >= 0 {
			p.Coordinate = fmt.Sprintf("event:%d", p.Ordinal)
		}
	}
	coordinate := p.Coordinate
	if coordinate == "" {
		coordinate = fmt.Sprintf("event:%d", i)
	}
	old, err := decodeLegacy(p.Payload)
	if err != nil {
		return fmt.Errorf("eventcodec: legacy record %d: %w", i, err)
	}
	x.olds[i], x.identities[i] = &old, p
	base, err := Unmarshal(p.Payload)
	if err != nil {
		return err
	}
	out.Events[i] = session.CloneEventIdentity(base.WithLegacyIdentity(*p))
	if old.Type == session.EvCompactionArchive && len(old.CompactionArchive) != 0 && string(old.CompactionArchive) != "null" {
		namespace := p.Namespace + "/archive/" + coordinate
		messages, err := sessnap.DecodeLegacyArchive(old.CompactionArchive, p.Incarnation, namespace)
		if err != nil {
			return err
		}
		out.Events[i].CompactionArchive = &session.CompactionArchivePayload{Replaced: messages}
	}
	if h := out.Events[i].Historical; h != nil {
		h.RecordID = session.HistoricalRecordID(p.Incarnation, p.Namespace, coordinate)
	}
	return nil
}

func (x *projectionIndex) indexCalls(i int, old *legacyEvent, p *session.LegacyEventIdentity) {
	if old.Type == session.EvToolCall && old.ToolCall != nil {
		call := *old.ToolCall
		id := session.ToolCallID("call_" + session.HistoricalRecordID(p.Incarnation, p.Namespace, p.Coordinate))
		// Keep the original-ordinal binding when it is the only durable coordinate.
		if p.Ordinal >= 0 && p.Coordinate == fmt.Sprintf("event:%d", p.Ordinal) {
			id = session.HistoricalInvocationID(p.Incarnation, p.Namespace, p.Ordinal)
		}
		occ := &occurrence{index: i, call: call, id: id, unsafe: call.ID == ""}
		key := keyFor(p, call.ID)
		for _, prior := range x.open[key] {
			prior.unsafe = true
			occ.unsafe = true
		}
		x.open[key] = append(x.open[key], occ)
		x.calls[i] = occ
	}
	if old.Type == session.EvToolResult && old.ToolResult != nil {
		key := keyFor(p, old.ToolResult.CallID)
		if list := x.open[key]; old.ToolResult.CallID != "" && len(list) == 1 && !list[0].unsafe {
			x.results[i] = list[0]
			list[0].answered = true
			delete(x.open, key)
		}
	}
}

func (x *projectionIndex) indexLifecycle(i int, old *legacyEvent, p *session.LegacyEventIdentity) {
	switch old.Type {
	case session.EvPermissionAsk:
		x.pendingIndex = i
		if old.Ask != nil {
			if list := x.open[keyFor(p, old.Ask.Call)]; old.Ask.Call != "" && len(list) == 1 && !list[0].unsafe && list[0].call.Name == old.Ask.Tool {
				x.asks[i] = list[0]
			}
		}
	case session.EvApproval, session.EvPermissionRetract, session.EvResult:
		x.pendingIndex = -1
	case session.EvAuthorizationRequired:
		if old.Authorization != nil {
			x.auth[keyFor(p, old.Authorization.AuthorizationID)] = old.Authorization.Call
		}
	case session.EvAuthorizationResolved:
		if old.Authorization != nil && old.Authorization.Status != session.AuthorizationPending {
			key := keyFor(p, old.Authorization.AuthorizationID)
			if call, ok := x.auth[key]; ok && call == old.Authorization.Call {
				delete(x.auth, key)
			}
		}
	}
}

func (x *projectionIndex) provePending(out *Projection) error {
	pendingIndex := x.pendingIndex
	if pendingIndex >= 0 {
		if occ := x.asks[pendingIndex]; occ != nil && !occ.unsafe && !occ.answered {
			// An awaiting suffix must be a unique outstanding occurrence, not a guessed
			// call selected from an incomplete parallel batch.
			total := 0
			for _, list := range x.open {
				total += len(list)
			}
			if total == 1 {
				occ.pending = true
			} else {
				delete(x.asks, pendingIndex)
			}
		} else {
			delete(x.asks, pendingIndex)
		}
		if x.asks[pendingIndex] == nil {
			pending, err := json.Marshal(x.olds[pendingIndex].Ask)
			if err != nil {
				return err
			}
			out.PendingHold, err = json.Marshal(struct {
				Version int           `json:"version"`
				State   session.State `json:"state"`
				Pending []byte        `json:"pending"`
				Event   []byte        `json:"event"`
			}{1, session.StateAwaiting, pending, x.identities[pendingIndex].Payload})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (x *projectionIndex) projectEvents(out *Projection) {
	for i, old := range x.olds {
		if old == nil {
			continue
		}
		e := out.Events[i]
		if occ := x.calls[i]; occ != nil && !occ.unsafe && (occ.answered || occ.pending) {
			e.Type, e.Historical = session.EvToolCall, nil
			call := session.NewToolCall(occ.id, occ.call.Name, slices.Clone(occ.call.Args))
			e.ToolCall = &call
		}
		if occ := x.results[i]; occ != nil && !occ.unsafe && occ.answered {
			e.Type, e.Historical = session.EvToolResult, nil
			e.ToolResult = &session.ToolResult{CallID: occ.id, Content: old.ToolResult.Content, IsError: old.ToolResult.IsError, Parts: old.ToolResult.Parts}
		}
		if occ := x.asks[i]; occ != nil && !occ.unsafe && (occ.answered || occ.pending) {
			e.Type, e.Historical = session.EvPermissionAsk, nil
			a := old.Ask
			e.Ask = &session.PendingAsk{AskID: a.AskID, Tool: a.Tool, Args: slices.Clone(a.Args), Reason: a.Reason, Call: occ.id, Guardrail: a.Guardrail, Origin: a.Origin}
		}
		out.Events[i] = e
	}
}
