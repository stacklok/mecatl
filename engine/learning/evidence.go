package learning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	maxProjectionTextBytes  = 16 << 10
	maxEvidencePreviewBytes = 1024
)

// EvidenceEventData is the owned, bounded subset of a session event that can become
// reflection evidence. It has no actor, permission arguments, or delegation payload.
type EvidenceEventData struct {
	Type       session.EventType
	Seq        int64
	Turn       int
	Text       string
	ToolCall   *ToolProjection
	ToolResult *ResultProjection
	Stop       session.StopReason
}

// EvidenceProjection is model-facing content-addressed evidence metadata.
type EvidenceProjection struct {
	Handle     string             `json:"handle"`
	Digest     string             `json:"digest"`
	EventSeq   *int64             `json:"event_seq,omitempty"`
	ToolCallID session.ToolCallID `json:"tool_call_id,omitempty"`
}

// MessageProjection is the canonical, bounded, provider-neutral evidence view of a message.
type MessageProjection struct {
	Evidence   *EvidenceProjection `json:"evidence,omitempty"`
	Role       session.Role        `json:"role"`
	Text       string              `json:"text,omitempty"`
	ToolCalls  []ToolProjection    `json:"tool_calls,omitempty"`
	ToolResult *ResultProjection   `json:"tool_result,omitempty"`
	Parts      []PartProjection    `json:"parts,omitempty"`
}

// ToolProjection excludes raw arguments and provider item identifiers.
type ToolProjection struct {
	ID   session.ToolCallID `json:"id"`
	Name string             `json:"name"`
}

// ResultProjection excludes binary content and bounds all model/tool-authored text.
type ResultProjection struct {
	CallID  session.ToolCallID `json:"call_id"`
	Content string             `json:"content,omitempty"`
	IsError bool               `json:"is_error,omitempty"`
	Parts   []PartProjection   `json:"parts,omitempty"`
}

// PartProjection contains only bounded textual material and public media metadata.
type PartProjection struct {
	Kind        string `json:"kind,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Text        string `json:"text,omitempty"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Binary      bool   `json:"binary,omitempty"`
}

// EventProjection is the canonical bounded evidence view of one supplied event.
type EventProjection struct {
	Evidence   *EvidenceProjection `json:"evidence,omitempty"`
	Type       session.EventType   `json:"type"`
	Seq        int64               `json:"seq"`
	Turn       int                 `json:"turn"`
	Text       string              `json:"text,omitempty"`
	ToolCall   *ToolProjection     `json:"tool_call,omitempty"`
	ToolResult *ResultProjection   `json:"tool_result,omitempty"`
	Stop       session.StopReason  `json:"stop,omitempty"`
}

// SignalProjection is a model-facing admission hint whose evidence uses only
// supplied compact handles.
type SignalProjection struct {
	Kind     SignalKind `json:"kind"`
	Evidence []string   `json:"evidence,omitempty"`
}

// Projection is the canonical model-facing representation of Input. Existing
// facts are comparison-only and intentionally carry no evidence handles.
type Projection struct {
	SessionID session.SessionID   `json:"session_id"`
	Stop      session.StopReason  `json:"stop,omitempty"`
	Messages  []MessageProjection `json:"messages"`
	Events    []EventProjection   `json:"events,omitempty"`
	Signals   []SignalProjection  `json:"signals,omitempty"`
	Existing  []ExistingFact      `json:"existing,omitempty"`
}

// ProjectInput builds the canonical bounded projection used both for evidence
// digests and for the model request.
func ProjectInput(in Input) (Projection, error) {
	if err := ValidateInput(in); err != nil {
		return Projection{}, err
	}
	out := Projection{
		SessionID: session.SessionID(safeProjectionText("", string(in.Trajectory.SessionID))),
		Stop:      in.Trajectory.Stop,
		Messages:  make([]MessageProjection, len(in.Trajectory.Messages)),
		Events:    make([]EventProjection, len(in.Events)),
		Signals:   projectSignals(in.Signals),
		Existing:  projectExisting(in.Existing),
	}
	for i, message := range in.Trajectory.Messages {
		item := projectMessage(message)
		ref, err := MessageEvidenceRef(in, i, messageToolCallID(message))
		if err != nil {
			return Projection{}, err
		}
		item.Evidence = evidenceProjection(ref)
		out.Messages[i] = item
	}
	for i, event := range in.Events {
		item := projectEvent(event)
		ref, err := EventEvidenceRef(in, i, eventToolCallID(event))
		if err != nil {
			return Projection{}, err
		}
		item.Evidence = evidenceProjection(ref)
		out.Events[i] = item
	}
	return out, nil
}

// MessageEvidenceRef returns the canonical handle for a message ordinal.
func MessageEvidenceRef(in Input, ordinal int, callID session.ToolCallID) (EvidenceRef, error) {
	if ordinal < 0 || ordinal >= len(in.Trajectory.Messages) {
		return EvidenceRef{}, fmt.Errorf("%w: message ordinal %d is out of range", ErrInvalidEvidence, ordinal)
	}
	message := in.Trajectory.Messages[ordinal]
	if callID != "" && !messageHasCall(message, callID) {
		return EvidenceRef{}, fmt.Errorf("%w: message %d does not contain call %q", ErrInvalidEvidence, ordinal, callID)
	}
	ref := EvidenceRef{SessionID: in.Trajectory.SessionID, Locator: EvidenceMessage, Ordinal: ordinal, ToolCallID: callID}
	ref.Digest = digestCanonical(projectMessage(message))
	if in.Manifest != nil && in.Manifest.Protocol == ReflectionEvidenceV1 {
		entryIndex, entry, ok := manifestEntryForHandle(in.Manifest, "m:"+strconv.Itoa(ordinal))
		if !ok || entry.OriginalMessage == nil {
			return EvidenceRef{}, fmt.Errorf("%w: selected message %d is absent from manifest", ErrInvalidEvidence, ordinal)
		}
		ref.Protocol = ReflectionEvidenceV1
		ref.ManifestIndex = entryIndex
		original := *entry.OriginalMessage
		ref.OriginalMessage = &original
		ref.AggregateDigest = in.Manifest.Digest
		ref.Digest = entry.Digest
	}
	return ref, nil
}

// EventEvidenceRef returns the canonical handle for an event ordinal.
func EventEvidenceRef(in Input, ordinal int, callID session.ToolCallID) (EvidenceRef, error) {
	if ordinal < 0 || ordinal >= len(in.Events) {
		return EvidenceRef{}, fmt.Errorf("%w: event ordinal %d is out of range", ErrInvalidEvidence, ordinal)
	}
	event := in.Events[ordinal]
	if callID != "" && eventToolCallID(event) != callID {
		return EvidenceRef{}, fmt.Errorf("%w: event %d does not contain call %q", ErrInvalidEvidence, ordinal, callID)
	}
	seq := event.Seq
	ref := EvidenceRef{SessionID: in.Trajectory.SessionID, Locator: EvidenceEvent, Ordinal: ordinal, EventSeq: &seq, ToolCallID: callID}
	ref.Digest = digestCanonical(projectEvent(event))
	if in.Manifest != nil && in.Manifest.Protocol == ReflectionEvidenceV1 {
		entryIndex, entry, ok := manifestEntryForHandle(in.Manifest, "e:"+strconv.Itoa(ordinal))
		if !ok || entry.EventSequence == nil {
			return EvidenceRef{}, fmt.Errorf("%w: selected event %d is absent from manifest", ErrInvalidEvidence, ordinal)
		}
		ref.Protocol = ReflectionEvidenceV1
		ref.ManifestIndex = entryIndex
		ref.AggregateDigest = in.Manifest.Digest
		ref.Digest = entry.Digest
		sequence := *entry.EventSequence
		ref.EventSeq = &sequence
	}
	return ref, nil
}

// EvidenceHandle returns the compact model-facing handle for a reference.
func EvidenceHandle(ref EvidenceRef) string {
	ordinal := ref.Ordinal
	if ref.ResolvedProtocol() == ReflectionEvidenceV1 {
		// ManifestIndex addresses all entry kinds, while model handles are compact
		// independently within their message/event namespaces. Persisted refs keep
		// Ordinal as that selected-local namespace index for compatibility with Input.
		ordinal = ref.Ordinal
	}
	switch ref.Locator {
	case EvidenceMessage:
		return "m:" + strconv.Itoa(ordinal)
	case EvidenceEvent:
		return "e:" + strconv.Itoa(ordinal)
	default:
		return ""
	}
}

// ResolveEvidenceHandle resolves only a supplied m:<ordinal> or e:<ordinal>
// handle and returns its canonical digest-bound reference.
func ResolveEvidenceHandle(in Input, handle string) (EvidenceRef, error) {
	if len(handle) < 3 || handle[1] != ':' {
		return EvidenceRef{}, fmt.Errorf("%w: malformed handle %q", ErrInvalidEvidence, handle)
	}
	ordinal, err := strconv.Atoi(handle[2:])
	if err != nil || ordinal < 0 || strconv.Itoa(ordinal) != handle[2:] {
		return EvidenceRef{}, fmt.Errorf("%w: malformed handle %q", ErrInvalidEvidence, handle)
	}
	switch handle[0] {
	case 'm':
		return MessageEvidenceRef(in, ordinal, messageToolCallIDAt(in, ordinal))
	case 'e':
		return EventEvidenceRef(in, ordinal, eventToolCallIDAt(in, ordinal))
	default:
		return EvidenceRef{}, fmt.Errorf("%w: malformed handle %q", ErrInvalidEvidence, handle)
	}
}

// ResolveEvidence validates a content-addressed handle against the exact Input.
func ResolveEvidence(in Input, ref EvidenceRef) error {
	if ref.SessionID != in.Trajectory.SessionID || ref.Digest == "" {
		return fmt.Errorf("%w: source session or digest mismatch", ErrInvalidEvidence)
	}
	var expected EvidenceRef
	var err error
	switch ref.Locator {
	case EvidenceMessage:
		if ref.EventSeq != nil {
			return fmt.Errorf("%w: message reference carries event sequence", ErrInvalidEvidence)
		}
		expected, err = MessageEvidenceRef(in, ref.Ordinal, ref.ToolCallID)
	case EvidenceEvent:
		expected, err = EventEvidenceRef(in, ref.Ordinal, ref.ToolCallID)
	default:
		return fmt.Errorf("%w: unknown locator %q", ErrInvalidEvidence, ref.Locator)
	}
	if err != nil {
		return err
	}
	if expected.Digest != ref.Digest || !equalOptionalInt64(expected.EventSeq, ref.EventSeq) ||
		expected.ResolvedProtocol() != ref.ResolvedProtocol() || expected.AggregateDigest != ref.AggregateDigest ||
		expected.ManifestIndex != ref.ManifestIndex || !equalOptionalInt(expected.OriginalMessage, ref.OriginalMessage) {
		return fmt.Errorf("%w: digest, protocol, coordinate, or event sequence mismatch", ErrInvalidEvidence)
	}
	return nil
}

// EvidencePreview returns the bounded canonical projection of one digest-verified
// evidence item. It excludes reasoning, raw tool/permission arguments, and binary
// bytes and applies the same secret/control repair used for reflector input.
func EvidencePreview(in Input, ref EvidenceRef) (string, error) {
	if err := ResolveEvidence(in, ref); err != nil {
		return "", err
	}
	var value any
	switch ref.Locator {
	case EvidenceMessage:
		value = projectMessage(in.Trajectory.Messages[ref.Ordinal])
	case EvidenceEvent:
		value = projectEvent(in.Events[ref.Ordinal])
	default:
		return "", fmt.Errorf("%w: unknown locator %q", ErrInvalidEvidence, ref.Locator)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: preview encoding failed", ErrInvalidEvidence)
	}
	return truncateUTF8(string(raw), maxEvidencePreviewBytes), nil
}

func canonicalMessages(messages []session.Message) []session.Message {
	out := make([]session.Message, len(messages))
	for i, message := range messages {
		projected := projectMessage(message)
		canonical := session.Message{Role: projected.Role, Text: projected.Text}
		for _, call := range projected.ToolCalls {
			canonical.ToolCalls = append(canonical.ToolCalls, session.ToolCall{ID: call.ID, Name: call.Name})
		}
		canonical.Parts = canonicalParts(projected.Parts)
		if projected.ToolResult != nil {
			canonical.ToolResult = &session.ToolResult{
				CallID: projected.ToolResult.CallID, Content: projected.ToolResult.Content,
				IsError: projected.ToolResult.IsError, Parts: canonicalParts(projected.ToolResult.Parts),
			}
		}
		out[i] = canonical
	}
	return out
}

func canonicalParts(parts []PartProjection) []session.Content {
	out := make([]session.Content, len(parts))
	for i, part := range parts {
		out[i] = session.Content{
			BlockKind: session.BlockKind(part.Kind), MIMEType: part.MIMEType,
			Text: part.Text, Name: part.Name, Title: part.Title, Description: part.Description,
		}
		if part.Binary {
			out[i].Data = []byte{0}
		}
	}
	return out
}

func projectSessionEvents(events []session.Event) []EvidenceEventData {
	out := make([]EvidenceEventData, len(events))
	for i, event := range events {
		out[i] = EvidenceEventData{Type: event.Type, Seq: event.Seq, Turn: event.Turn, Text: projectEventText(event.Type, event.Text)}
		if event.ToolCall != nil {
			out[i].ToolCall = &ToolProjection{ID: session.ToolCallID(safeProjectionText("", string(event.ToolCall.ID))), Name: safeProjectionText("", event.ToolCall.Name)}
		}
		if event.ToolResult != nil {
			result := projectResult(*event.ToolResult)
			out[i].ToolResult = &result
		}
		if event.Result != nil {
			out[i].Stop = event.Result.Stop
		}
	}
	return out
}

func projectMessage(message session.Message) MessageProjection {
	out := MessageProjection{Role: message.Role, Text: safeProjectionText("", message.Text)}
	for _, call := range message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolProjection{ID: session.ToolCallID(safeProjectionText("", string(call.ID))), Name: safeProjectionText("", call.Name)})
	}
	if message.ToolResult != nil {
		result := projectResult(*message.ToolResult)
		out.ToolResult = &result
	}
	out.Parts = projectParts(message.Parts)
	return out
}

func projectEvent(event EvidenceEventData) EventProjection {
	out := EventProjection{Type: event.Type, Seq: event.Seq, Turn: event.Turn, Text: projectEventText(event.Type, event.Text), Stop: event.Stop}
	if event.ToolCall != nil {
		out.ToolCall = &ToolProjection{ID: session.ToolCallID(safeProjectionText("", string(event.ToolCall.ID))), Name: safeProjectionText("", event.ToolCall.Name)}
	}
	if event.ToolResult != nil {
		out.ToolResult = &ResultProjection{
			CallID: session.ToolCallID(safeProjectionText("", string(event.ToolResult.CallID))), Content: safeProjectionText("", event.ToolResult.Content),
			IsError: event.ToolResult.IsError, Parts: sanitizeProjectedParts(event.ToolResult.Parts),
		}
	}
	return out
}

func projectEventText(kind session.EventType, text string) string {
	switch kind {
	case session.EvReasoningDelta, session.EvPermissionAsk, session.EvPermissionRetract, session.EvApproval:
		return ""
	default:
		return safeProjectionText("", text)
	}
}

func sanitizeProjectedParts(parts []PartProjection) []PartProjection {
	out := make([]PartProjection, len(parts))
	for i, part := range parts {
		out[i] = PartProjection{
			Kind: safeProjectionText("", part.Kind), MIMEType: safeProjectionText("", part.MIMEType),
			Text: safeProjectionText("", part.Text), Name: safeProjectionText("", part.Name),
			Title: safeProjectionText("", part.Title), Description: safeProjectionText("", part.Description),
			Binary: part.Binary,
		}
	}
	return out
}

func projectResult(result session.ToolResult) ResultProjection {
	return ResultProjection{CallID: session.ToolCallID(safeProjectionText("", string(result.CallID))), Content: safeProjectionText("", result.Content), IsError: result.IsError, Parts: projectParts(result.Parts)}
}

func projectParts(parts []session.Content) []PartProjection {
	out := make([]PartProjection, 0, len(parts))
	for _, part := range parts {
		projection := PartProjection{
			Kind: string(part.BlockKind), MIMEType: safeProjectionText("", part.MIMEType),
			Text: safeProjectionText("", part.Text), Name: safeProjectionText("", part.Name),
			Title: safeProjectionText("", part.Title), Description: safeProjectionText("", part.Description),
			Binary: len(part.Data) > 0,
		}
		if projection.Kind == "" {
			projection.Kind = string(part.Kind)
		}
		out = append(out, projection)
	}
	return out
}

var projectionCredential = regexp.MustCompile(`(?i)(?:(?:sk-|ghp_|github_pat_|xoxb-|xoxp-)[a-z0-9_-]{20,}|(?:AKIA|ASIA)[A-Z0-9]{16}|[a-z0-9_-]{16,}\.[a-z0-9_-]{16,}\.[a-z0-9_-]{16,})`)

func safeProjectionText(key, value string) string {
	value = boundedCanonicalText(value, maxProjectionTextBytes)
	// Every credential alternative requires punctuation or ASCII A/a (AWS prefixes).
	// Keep this necessary condition aligned with projectionCredential.
	if strings.ContainsAny(value, "._-aA") {
		value = projectionCredential.ReplaceAllString(value, "[redacted-secret]")
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if tool.SecretShapedMemoryValue(key, line) {
			lines[i] = "[redacted-secret]"
		}
	}
	return governance.NeutraliseFraming(strings.Join(lines, "\n"))
}

func boundedCanonicalText(value string, limit int) string {
	var out strings.Builder
	out.Grow(min(len(value), limit))
	for _, r := range value {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		width := utf8.RuneLen(r)
		if width < 0 {
			width = len("�")
			r = '�'
		}
		if out.Len()+width > limit {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

func projectExisting(existing []ExistingFact) []ExistingFact {
	out := make([]ExistingFact, len(existing))
	for i, fact := range existing {
		out[i] = ExistingFact{
			Kind: fact.Kind, Key: safeProjectionText("", fact.Key),
			Value: safeProjectionText(fact.Key, fact.Value), Description: safeProjectionText("", fact.Description),
		}
	}
	return out
}

func projectSignals(signals []Signal) []SignalProjection {
	out := make([]SignalProjection, len(signals))
	for i, signal := range signals {
		out[i].Kind = signal.Kind
		for _, ref := range signal.Evidence {
			out[i].Evidence = append(out[i].Evidence, EvidenceHandle(ref))
		}
	}
	return out
}

func evidenceProjection(ref EvidenceRef) *EvidenceProjection {
	return &EvidenceProjection{
		Handle: EvidenceHandle(ref), Digest: ref.Digest, EventSeq: ref.EventSeq,
		ToolCallID: session.ToolCallID(safeProjectionText("", string(ref.ToolCallID))),
	}
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func digestCanonical(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic("learning canonical projection is not JSON-marshalable: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func messageHasCall(message session.Message, id session.ToolCallID) bool {
	for _, call := range message.ToolCalls {
		if call.ID == id {
			return true
		}
	}
	return message.ToolResult != nil && message.ToolResult.CallID == id
}

func messageToolCallID(message session.Message) session.ToolCallID {
	if len(message.ToolCalls) == 1 {
		return message.ToolCalls[0].ID
	}
	if message.ToolResult != nil {
		return message.ToolResult.CallID
	}
	return ""
}

func messageToolCallIDAt(in Input, ordinal int) session.ToolCallID {
	if ordinal < 0 || ordinal >= len(in.Trajectory.Messages) {
		return ""
	}
	return messageToolCallID(in.Trajectory.Messages[ordinal])
}

func eventToolCallID(event EvidenceEventData) session.ToolCallID {
	if event.ToolCall != nil {
		return event.ToolCall.ID
	}
	if event.ToolResult != nil {
		return event.ToolResult.CallID
	}
	return ""
}

func eventToolCallIDAt(in Input, ordinal int) session.ToolCallID {
	if ordinal < 0 || ordinal >= len(in.Events) {
		return ""
	}
	return eventToolCallID(in.Events[ordinal])
}

func equalOptionalInt(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func manifestEntryForHandle(manifest *MaterializationManifest, handle string) (int, ManifestEntry, bool) {
	if manifest == nil {
		return 0, ManifestEntry{}, false
	}
	for i, entry := range manifest.Entries {
		if entry.Handle == handle {
			return i, entry, true
		}
	}
	return 0, ManifestEntry{}, false
}

func equalOptionalInt64(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func evidenceIdentity(ref EvidenceRef) string {
	seq := ""
	if ref.EventSeq != nil {
		seq = strconv.FormatInt(*ref.EventSeq, 10)
	}
	return strings.Join([]string{string(ref.SessionID), string(ref.Locator), strconv.Itoa(ref.Ordinal), seq, string(ref.ToolCallID), ref.Digest}, "\x00")
}

func cloneSignals(signals []Signal) []Signal {
	out := append([]Signal(nil), signals...)
	for i := range out {
		out[i].Evidence = append([]EvidenceRef(nil), out[i].Evidence...)
	}
	return out
}
