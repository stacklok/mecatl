package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// EvidenceProtocol identifies the coordinate and digest semantics of evidence.
type EvidenceProtocol string

const (
	// ReflectionEvidenceLegacyV0 preserves ADR-0109 input-local ordinal semantics.
	ReflectionEvidenceLegacyV0 EvidenceProtocol = "reflection-evidence/legacy-v0"
	// ReflectionEvidenceV1 identifies manifest-backed durable source coordinates.
	ReflectionEvidenceV1 EvidenceProtocol = "reflection-evidence/v1"
	// ReflectionEvidenceSourceV1 identifies the session source-boundary domain.
	ReflectionEvidenceSourceV1 = "mecatl/reflection-evidence/source/v1"
)

// SourceIdentity is the exact storage-neutral namespace from which evidence came.
type SourceIdentity struct {
	Domain string            `json:"domain"`
	Value  session.SessionID `json:"value"`
}

// ManifestEntry binds one selected-local handle to durable source coordinates.
type ManifestEntry struct {
	Handle          string               `json:"handle"`
	Locator         EvidenceLocator      `json:"locator"`
	OriginalMessage *int                 `json:"original_message,omitempty"`
	EventSequence   *int64               `json:"event_sequence,omitempty"`
	Digest          string               `json:"digest"`
	Component       string               `json:"component,omitempty"`
	ToolCallIDs     []session.ToolCallID `json:"tool_call_ids,omitempty"`
}

// MaterializationManifest is the immutable aggregate identity of selected evidence.
type MaterializationManifest struct {
	Protocol EvidenceProtocol `json:"protocol"`
	Source   SourceIdentity   `json:"source"`
	Entries  []ManifestEntry  `json:"entries"`
	Digest   string           `json:"digest"`
}

// MaterializationLimits bound selected evidence, never the retained source.
type MaterializationLimits struct {
	MaxMessages int `json:"max_messages,omitempty"`
	MaxEvents   int `json:"max_events,omitempty"`
	MaxBytes    int `json:"max_bytes,omitempty"`
}

// MaterializationRequest supplies a source boundary and deterministic selection context.
type MaterializationRequest struct {
	Trajectory Trajectory            `json:"trajectory"`
	Events     []session.Event       `json:"events,omitempty"`
	Signals    []Signal              `json:"signals,omitempty"`
	Mandatory  MessageSpan           `json:"mandatory,omitempty"`
	Limits     MaterializationLimits `json:"limits,omitempty"`
	Explicit   bool                  `json:"explicit,omitempty"`
}

// MaterializationDisposition is the closed selected/abstained/skipped result vocabulary.
type MaterializationDisposition string

// Materialization dispositions distinguish usable evidence from explicit and automatic no-work.
const (
	MaterializationSelected  MaterializationDisposition = "selected"
	MaterializationAbstained MaterializationDisposition = "abstained"
	MaterializationSkipped   MaterializationDisposition = "skipped"
)

// MaterializationReason is a closed, content-free materialization outcome reason.
type MaterializationReason string

// Materialization reasons never contain source-derived content.
const (
	MaterializationReasonSelected             MaterializationReason = "selected"
	MaterializationNoEligibleEvidence         MaterializationReason = "no_eligible_evidence"
	MaterializationMandatorySpanExceedsBounds MaterializationReason = "mandatory_span_exceeds_bounds"
)

func (r MaterializationReason) String() string { return string(r) }

// Materialization is one bounded selected input and its immutable provenance.
type Materialization struct {
	Disposition MaterializationDisposition `json:"disposition"`
	Reason      MaterializationReason      `json:"reason"`
	Input       Input                      `json:"input"`
	Manifest    MaterializationManifest    `json:"manifest"`
	Canonical   []byte                     `json:"canonical,omitempty"`
}

type materialUnit struct {
	messages  []int
	event     int
	tier      int
	order     int
	bytes     int
	mandatory bool
	component string
	calls     []session.ToolCallID
}

// MaterializeEvidence selects whole source components without constructing a full
// source projection. Each field is projected into its existing bounded canonical
// form only while its bounded candidate unit is considered. The scan observes ctx
// between source records and within each bounded tool component.
//
//nolint:gocyclo // selection keeps the closed ranking and all bounds visible in one pass
func MaterializeEvidence(ctx context.Context, req MaterializationRequest) (Materialization, error) {
	if err := ctx.Err(); err != nil {
		return Materialization{}, err
	}
	if req.Trajectory.SessionID == "" {
		return Materialization{}, fmt.Errorf("%w: trajectory session id is required", ErrInvalidInput)
	}
	limits := req.Limits
	if limits.MaxMessages <= 0 {
		limits.MaxMessages = MaxInputMessages
	}
	if limits.MaxEvents <= 0 && req.Limits.MaxEvents == 0 {
		limits.MaxEvents = MaxInputEvents
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 256 << 10
	}

	if limits.MaxMessages > MaxInputMessages {
		limits.MaxMessages = MaxInputMessages
	}
	if limits.MaxEvents > MaxInputEvents {
		limits.MaxEvents = MaxInputEvents
	}

	units, mandatoryUncovered, err := boundedUnits(ctx, req, limits)
	if err != nil {
		return Materialization{}, err
	}

	sort.SliceStable(units, func(i, j int) bool { return unitRanksBefore(units[i], units[j]) })
	selected := make([]materialUnit, 0, min(len(units), limits.MaxMessages+limits.MaxEvents))
	messages, events, used := 0, 0, 0
	mandatoryFailed := mandatoryUncovered
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return Materialization{}, err
		}
		if unit.tier == 99 {
			if unit.mandatory {
				mandatoryFailed = true
			}
			continue
		}
		messageCount := len(unit.messages)
		eventCount := 0
		if unit.event >= 0 && len(unit.messages) == 0 {
			eventCount = 1
		}
		fits := messages+messageCount <= limits.MaxMessages && events+eventCount <= limits.MaxEvents && used+unit.bytes <= limits.MaxBytes
		if !fits {
			if unit.mandatory {
				mandatoryFailed = true
			}
			continue
		}
		selected = append(selected, unit)
		messages += messageCount
		events += eventCount
		used += unit.bytes
	}
	if mandatoryFailed {
		return emptyMaterialization(req, MaterializationMandatorySpanExceedsBounds), nil
	}
	if len(selected) == 0 {
		return emptyMaterialization(req, MaterializationNoEligibleEvidence), nil
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].order < selected[j].order })
	return buildMaterialization(req, selected), nil
}

func emptyMaterialization(req MaterializationRequest, reason MaterializationReason) Materialization {
	disposition := MaterializationSkipped
	if req.Explicit {
		disposition = MaterializationAbstained
	}
	return Materialization{Disposition: disposition, Reason: reason}
}

//nolint:gocyclo // one bounded scan keeps source classification before copying explicit
func boundedUnits(ctx context.Context, req MaterializationRequest, limits MaterializationLimits) ([]materialUnit, bool, error) {
	capUnits := limits.MaxMessages + limits.MaxEvents
	units := make([]materialUnit, 0, capUnits)
	mandatoryCovered := 0
	mandatoryFailed := req.Mandatory.Valid(len(req.Trajectory.Messages)) && req.Mandatory.End-req.Mandatory.Start > limits.MaxMessages
	messages := req.Trajectory.Messages
	for i, message := range messages {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		unit, ok, err := sourceUnitAt(ctx, messages, i)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			if req.Mandatory.Valid(len(messages)) && req.Mandatory.Contains(i) && message.Role != session.RoleTool {
				mandatoryFailed = true
			}
			continue
		}
		classifyUnit(&unit, req)
		projected := make([]MessageProjection, 0, len(unit.messages))
		eligible := true
		for _, index := range unit.messages {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if req.Mandatory.Valid(len(messages)) && req.Mandatory.Contains(index) {
				mandatoryCovered++
			}
			candidate := messages[index]
			if !sourceMessageClassifiable(candidate) {
				eligible = false
				break
			}
			p := projectMessage(candidate)
			if !messageProjectionEligible(p) {
				eligible = false
				break
			}
			projected = append(projected, p)
		}
		raw, _ := json.Marshal(projected)
		unit.bytes = len(raw)
		if !eligible || unit.bytes > limits.MaxBytes || len(unit.messages) > limits.MaxMessages {
			if unit.mandatory {
				mandatoryFailed = true
			}
			continue
		}
		units = retainRankedUnit(units, unit, capUnits)
	}
	for i := range req.Events {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !eligibleEvent(req.Events[i]) {
			continue
		}
		projected := projectEvent(materializerEvent(req.Events[i]))
		raw, _ := json.Marshal(projected)
		if len(raw) > limits.MaxBytes {
			continue
		}
		units = retainRankedUnit(units, materialUnit{event: i, tier: 4, order: len(messages) + i, bytes: len(raw)}, capUnits)
	}
	if req.Mandatory.Valid(len(messages)) && mandatoryCovered < req.Mandatory.End-req.Mandatory.Start {
		mandatoryFailed = true
	}
	return units, mandatoryFailed, nil
}

func sourceUnitAt(ctx context.Context, messages []session.Message, i int) (materialUnit, bool, error) {
	message := messages[i]
	if message.Role == session.RoleTool {
		return materialUnit{}, false, nil
	}
	if message.Role != session.RoleAssistant || len(message.ToolCalls) == 0 {
		return materialUnit{messages: []int{i}, event: -1, tier: 3, order: i, component: fmt.Sprintf("message:%d", i)}, true, nil
	}
	if len(message.ToolCalls) > MaxCandidateEvidence {
		return materialUnit{}, false, nil
	}
	indices := []int{i}
	calls := make([]session.ToolCallID, 0, len(message.ToolCalls))
	pending := make(map[session.ToolCallID]struct{}, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		if _, duplicate := pending[call.ID]; duplicate {
			return materialUnit{}, false, nil
		}
		calls = append(calls, call.ID)
		pending[call.ID] = struct{}{}
	}
	for j := i + 1; j < len(messages) && len(pending) > 0; j++ {
		if err := ctx.Err(); err != nil {
			return materialUnit{}, false, err
		}
		candidate := messages[j]
		if candidate.Role != session.RoleTool || candidate.ToolResult == nil {
			break
		}
		if _, ok := pending[candidate.ToolResult.CallID]; !ok {
			return materialUnit{}, false, nil
		}
		indices = append(indices, j)
		delete(pending, candidate.ToolResult.CallID)
	}
	if len(pending) != 0 {
		return materialUnit{}, false, nil
	}
	return materialUnit{messages: indices, event: -1, tier: 3, order: i, component: fmt.Sprintf("tool:%d", i), calls: calls}, true, nil
}

func retainRankedUnit(units []materialUnit, unit materialUnit, limit int) []materialUnit {
	if limit == 0 {
		return units
	}
	units = append(units, unit)
	sort.SliceStable(units, func(i, j int) bool { return unitRanksBefore(units[i], units[j]) })
	if len(units) > limit {
		units = units[:limit]
	}
	return units
}

func unitRanksBefore(a, b materialUnit) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if a.tier == 3 {
		return a.order > b.order
	}
	return a.order < b.order
}

//nolint:gocyclo // the closed tier table is clearer as one visibly ordered classifier
func classifyUnit(unit *materialUnit, req MaterializationRequest) {
	for _, index := range unit.messages {
		if req.Mandatory.Valid(len(req.Trajectory.Messages)) && req.Mandatory.Contains(index) {
			unit.tier = 0
			unit.mandatory = true
		}
	}
	if unit.tier == 0 {
		return
	}
	for _, index := range unit.messages {
		m := req.Trajectory.Messages[index]
		lower := strings.ToLower(strings.TrimSpace(m.Text))
		if m.Role == session.RoleUser && (strings.HasPrefix(lower, "remember ") || strings.HasPrefix(lower, "learn ") || strings.HasPrefix(lower, "please remember ") || strings.HasPrefix(lower, "save this as a skill")) {
			unit.tier = 1
			return
		}
	}
	for _, signal := range req.Signals {
		if signal.Kind != SignalRepeatedCorrection && signal.Kind != SignalFailureRecovery && signal.Kind != SignalRepeatedToolSequence {
			continue
		}
		for _, ref := range signal.Evidence {
			if ref.Locator != EvidenceMessage {
				continue
			}
			for _, index := range unit.messages {
				if index == ref.Ordinal {
					unit.tier = 2
					return
				}
			}
		}
		for _, index := range unit.messages {
			text := strings.ToLower(strings.TrimSpace(req.Trajectory.Messages[index].Text))
			if strings.HasPrefix(text, "no,") || strings.HasPrefix(text, "actually,") || strings.Contains(text, "recover") || len(unit.calls) > 0 {
				unit.tier = 2
				return
			}
		}
	}
}

func sourceMessageClassifiable(message session.Message) bool {
	if len(message.ToolCalls) > MaxCandidateEvidence || len(message.Parts) > MaxCandidateEvidence {
		return false
	}
	return message.ToolResult == nil || len(message.ToolResult.Parts) <= MaxCandidateEvidence
}

func messageProjectionEligible(p MessageProjection) bool {
	if p.Role != session.RoleUser && p.Role != session.RoleAssistant && p.Role != session.RoleTool {
		return false
	}
	if strings.TrimSpace(p.Text) != "" || len(p.ToolCalls) > 0 || p.ToolResult != nil {
		return true
	}
	for _, part := range p.Parts {
		if part.Text != "" || part.Name != "" || part.Title != "" || part.Description != "" || part.MIMEType != "" && !part.Binary {
			return true
		}
	}
	return false
}

func eligibleEvent(event session.Event) bool {
	kind := string(event.Type)
	if strings.HasPrefix(kind, "subagent.") || strings.HasPrefix(kind, "parallel.") || strings.HasPrefix(kind, "team.") {
		return false
	}
	switch event.Type {
	case session.EvReasoningDelta, session.EvPermissionAsk, session.EvPermissionRetract, session.EvApproval:
		return false
	}
	return event.Type != "" && (event.Result != nil || event.ToolCall != nil || event.ToolResult != nil || event.Text == "")
}

func materializerEvent(event session.Event) EvidenceEventData {
	projected := projectSessionEvents([]session.Event{event})[0]
	projected.Text = ""
	return projected
}

func buildMaterialization(req MaterializationRequest, selected []materialUnit) Materialization {
	var selectedMessages []session.Message
	var selectedEvents []EvidenceEventData
	manifest := MaterializationManifest{Protocol: ReflectionEvidenceV1, Source: SourceIdentity{Domain: ReflectionEvidenceSourceV1, Value: req.Trajectory.SessionID}}
	messageLocal, eventLocal := 0, 0
	for _, unit := range selected {
		if len(unit.messages) > 0 {
			for _, original := range unit.messages {
				projected := projectMessage(req.Trajectory.Messages[original])
				selectedMessages = append(selectedMessages, canonicalMessage(projected))
				o := original
				manifest.Entries = append(manifest.Entries, ManifestEntry{Handle: fmt.Sprintf("m:%d", messageLocal), Locator: EvidenceMessage, OriginalMessage: &o, Digest: digestCanonical(projected), Component: unit.component, ToolCallIDs: append([]session.ToolCallID(nil), unit.calls...)})
				messageLocal++
			}
		} else {
			event := materializerEvent(req.Events[unit.event])
			selectedEvents = append(selectedEvents, event)
			seq := event.Seq
			manifest.Entries = append(manifest.Entries, ManifestEntry{Handle: fmt.Sprintf("e:%d", eventLocal), Locator: EvidenceEvent, EventSequence: &seq, Digest: digestCanonical(projectEvent(event))})
			eventLocal++
		}
	}
	trajectory := NewTrajectory(req.Trajectory.SessionID, "", req.Trajectory.Stop, req.Trajectory.Usage, selectedMessages)
	input := Input{Trajectory: trajectory, Events: selectedEvents}
	digest, canonical := materializationDigest(manifest, selectedMessages, selectedEvents)
	manifest.Digest = digest
	input.Manifest = &manifest
	return Materialization{Disposition: MaterializationSelected, Reason: MaterializationReasonSelected, Input: input, Manifest: manifest, Canonical: canonical}
}

func materializationDigest(manifest MaterializationManifest, messages []session.Message, events []EvidenceEventData) (string, []byte) {
	identity := struct {
		Protocol EvidenceProtocol    `json:"protocol"`
		Source   SourceIdentity      `json:"source"`
		Entries  []ManifestEntry     `json:"entries"`
		Messages []MessageProjection `json:"messages"`
		Events   []EventProjection   `json:"events,omitempty"`
	}{Protocol: manifest.Protocol, Source: manifest.Source, Entries: manifest.Entries}
	for _, message := range messages {
		identity.Messages = append(identity.Messages, projectMessage(message))
	}
	for _, event := range events {
		identity.Events = append(identity.Events, projectEvent(event))
	}
	raw, _ := json.Marshal(identity)
	canonical := []byte(governance.FenceUntrusted(string(raw)))
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), canonical
}

//nolint:gocyclo // protocol, source, entries, bindings, and aggregate are one validation boundary
func validateMaterializationManifest(in Input) error {
	manifest := in.Manifest
	if manifest == nil {
		return nil
	}
	if manifest.Protocol != ReflectionEvidenceV1 || manifest.Source.Domain != ReflectionEvidenceSourceV1 || manifest.Source.Value != in.Trajectory.SessionID || !ValidSHA256(manifest.Digest) || len(manifest.Entries) != len(in.Trajectory.Messages)+len(in.Events) {
		return fmt.Errorf("%w: invalid materialization manifest identity", ErrInvalidInput)
	}
	if err := session.ValidateToolPairing(in.Trajectory.Messages); err != nil {
		return fmt.Errorf("%w: materialization tool component is incomplete", ErrInvalidInput)
	}
	messageEntries := manifest.Entries[:len(in.Trajectory.Messages)]
	for i, entry := range messageEntries {
		if entry.Handle != fmt.Sprintf("m:%d", i) || entry.Locator != EvidenceMessage || entry.OriginalMessage == nil || entry.EventSequence != nil || entry.Digest != digestCanonical(projectMessage(in.Trajectory.Messages[i])) {
			return fmt.Errorf("%w: materialization message entry %d mismatch", ErrInvalidInput, i)
		}
		if i > 0 && *entry.OriginalMessage <= *messageEntries[i-1].OriginalMessage {
			return fmt.Errorf("%w: materialization message coordinates are not ordered", ErrInvalidInput)
		}
		component, calls, ok := selectedMessageBinding(in.Trajectory.Messages, messageEntries, i)
		if !ok || entry.Component != component || !slices.Equal(entry.ToolCallIDs, calls) {
			return fmt.Errorf("%w: materialization tool component binding mismatch", ErrInvalidInput)
		}
	}
	lastSequence := int64(-1)
	for i, event := range in.Events {
		entry := manifest.Entries[len(in.Trajectory.Messages)+i]
		if entry.Handle != fmt.Sprintf("e:%d", i) || entry.Locator != EvidenceEvent || entry.OriginalMessage != nil || entry.EventSequence == nil || *entry.EventSequence != event.Seq || entry.EventSequence != nil && *entry.EventSequence <= lastSequence || entry.Component != "" || len(entry.ToolCallIDs) != 0 || entry.Digest != digestCanonical(projectEvent(event)) {
			return fmt.Errorf("%w: materialization event entry %d mismatch", ErrInvalidInput, i)
		}
		lastSequence = *entry.EventSequence
	}
	digest, _ := materializationDigest(*manifest, in.Trajectory.Messages, in.Events)
	if digest != manifest.Digest {
		return fmt.Errorf("%w: materialization aggregate digest mismatch", ErrInvalidInput)
	}
	return nil
}

func selectedMessageBinding(messages []session.Message, entries []ManifestEntry, index int) (string, []session.ToolCallID, bool) {
	message := messages[index]
	original := *entries[index].OriginalMessage
	if message.Role == session.RoleAssistant && len(message.ToolCalls) > 0 {
		calls := make([]session.ToolCallID, len(message.ToolCalls))
		for i, call := range message.ToolCalls {
			calls[i] = call.ID
		}
		return fmt.Sprintf("tool:%d", original), calls, true
	}
	if message.Role == session.RoleTool && message.ToolResult != nil {
		for i, candidate := range messages {
			if candidate.Role != session.RoleAssistant || len(candidate.ToolCalls) == 0 {
				continue
			}
			for _, call := range candidate.ToolCalls {
				if call.ID == message.ToolResult.CallID {
					component, calls, ok := selectedMessageBinding(messages, entries, i)
					return component, calls, ok
				}
			}
		}
		return "", nil, false
	}
	return fmt.Sprintf("message:%d", original), nil, true
}

func canonicalMessage(projected MessageProjection) session.Message {
	m := session.Message{Role: projected.Role, Text: projected.Text, Parts: canonicalParts(projected.Parts)}
	for _, call := range projected.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, session.ToolCall{ID: call.ID, Name: call.Name})
	}
	if projected.ToolResult != nil {
		m.ToolResult = &session.ToolResult{CallID: projected.ToolResult.CallID, Content: projected.ToolResult.Content, IsError: projected.ToolResult.IsError, Parts: canonicalParts(projected.ToolResult.Parts)}
	}
	return m
}

// ValidSHA256 reports whether value is one lower-case SHA-256 hex digest.
func ValidSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
