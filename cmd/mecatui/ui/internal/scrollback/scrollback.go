package scrollback

import (
	"reflect"
	"slices"
)

// BlockID is a non-zero identifier allocated by a Conversation for a card or its
// changed-files appendix. IDs are unique within one Conversation and are never
// reused; zero denotes no block.
type BlockID uint64

// Kind identifies the payload family stored in a card snapshot.
type Kind uint8

const (
	// KindUser identifies a UserCardSnapshot.
	KindUser Kind = iota
	// KindAssistant identifies an AssistantCardSnapshot.
	KindAssistant
	// KindTool identifies a ToolCardSnapshot.
	KindTool
	// KindSubagent identifies a SubagentCardSnapshot.
	KindSubagent
	// KindTeam identifies a TeamCardSnapshot.
	KindTeam
	// KindNotice identifies a NoticeCardSnapshot.
	KindNotice
	// KindTurnStat identifies a TurnStatCardSnapshot.
	KindTurnStat
	// KindError identifies an ErrorCardSnapshot.
	KindError
	// KindHook identifies a HookCardSnapshot.
	KindHook
	// KindDelivery identifies a DeliveryCardSnapshot.
	KindDelivery
)

// PayloadSnapshot is the immutable-from-the-conversation perspective payload of
// a BlockSnapshot. The unexported method seals the payload-family boundary:
// consumers can inspect the package-defined concrete types but cannot introduce
// another card family. Payload values returned by Conversation snapshots are
// detached and may be changed without changing the Conversation.
type PayloadSnapshot interface {
	Kind() Kind
	payloadSnapshot()
}

// BlockSnapshot is a detached view of one conversation card. ID remains stable
// for the card's lifetime. Revision starts at zero and advances when the card's
// payload changes, including confirmation of a provisional tool result.
type BlockSnapshot struct {
	ID       BlockID
	Revision uint64
	Payload  PayloadSnapshot
}

// BlockMetadata identifies a card for renderer cache lookup without cloning its
// payload. It deliberately exposes only logical identity, revision, and kind.
type BlockMetadata struct {
	ID       BlockID
	Revision uint64
	Kind     Kind
}

type card struct {
	id       BlockID
	revision uint64
	payload  PayloadSnapshot
}

// Conversation is an ordered, mutable logical conversation document. It owns its
// cards, tool-call index, and changed-files appendix; use its component facades
// to append or transition cards, and snapshots to observe them.
type Conversation struct {
	cards    []card
	nextID   BlockID
	calls    map[string]int
	appendix *AppendixSnapshot
	seen     map[string]struct{}
}

// ToolCallMetadata is the compact tool-card projection used by inventories. It
// intentionally excludes results and artifacts, whose byte payloads remain owned by
// the conversation until a selected detail requests a detached snapshot.
type ToolCallMetadata struct {
	ID                      BlockID
	Revision                uint64
	CallID, Name, Arguments string
	// ResultReceived reports payload.Resolved; Provisional reports an available
	// result awaiting canonical confirmation. Terminal is lifecycle completion
	// without implying a canonical result.
	ResultReceived, Provisional, Terminal bool
	ResultError, LifecycleFailed          bool
	Stop                                  string
}

// ToolCallMetadataAt returns the compact top-level tool projection at index i
// without detaching its payload. It returns false for non-tool cards.
func (c *Conversation) ToolCallMetadataAt(i int) (ToolCallMetadata, bool) {
	card := c.cards[i]
	return toolCallMetadata(card.id, card.revision, card.payload)
}

// ToolCallMetadataOf returns the same compact projection as ToolCallMetadataAt
// for an already detached snapshot, so a renderer that loaded a snapshot after a
// cache miss does not reinterpret tool lifecycle fields itself.
func ToolCallMetadataOf(s BlockSnapshot) (ToolCallMetadata, bool) {
	return toolCallMetadata(s.ID, s.Revision, s.Payload)
}

func toolCallMetadata(id BlockID, revision uint64, payload PayloadSnapshot) (ToolCallMetadata, bool) {
	var call ToolCall
	var resultReceived, provisional, terminal, resultError, lifecycleFailed bool
	var stop string
	switch payload := payload.(type) {
	case ToolCardSnapshot:
		call = payload.Call
		resultReceived, provisional, terminal = payload.Resolved, payload.available, payload.Finished
		resultError, lifecycleFailed = payload.Result.IsError, payload.Failed
	case SubagentCardSnapshot:
		call = payload.Call
		resultReceived, provisional, terminal = payload.Resolved, payload.available, payload.Update.Done
		resultError = payload.Result.IsError
		if terminal {
			stop = payload.Update.Stop
		}
	case TeamCardSnapshot:
		call = payload.Call
		resultReceived, provisional, terminal = payload.Resolved, payload.available, payload.Update.Done
		resultError = payload.Result.IsError
		if terminal {
			stop = payload.Update.Stop
		}
	default:
		return ToolCallMetadata{}, false
	}
	return ToolCallMetadata{
		ID: id, Revision: revision, CallID: call.ID, Name: call.Name, Arguments: call.Arguments,
		ResultReceived: resultReceived, Provisional: provisional, Terminal: terminal,
		ResultError: resultError, LifecycleFailed: lifecycleFailed, Stop: stop,
	}, true
}

// Len returns the number of ordinary cards in the conversation. It excludes the
// separately rendered changed-files appendix.
func (c *Conversation) Len() int { return len(c.cards) }

// SnapshotAt returns a detached snapshot of the card at index i. It panics when
// i is outside [0, Len()). Mutating the returned payload or data reachable from
// it cannot mutate the Conversation.
func (c *Conversation) SnapshotAt(i int) BlockSnapshot {
	card := c.cards[i]
	return BlockSnapshot{ID: card.id, Revision: card.revision, Payload: clonePayload(card.payload)}
}

// MetadataAt returns the cache identity for a card without allocating or
// detaching its payload. A renderer can reuse a cached rendering while ID and
// Revision match; it should call SnapshotAt only after a cache miss.
func (c *Conversation) MetadataAt(i int) BlockMetadata {
	card := c.cards[i]
	return BlockMetadata{ID: card.id, Revision: card.revision, Kind: card.payload.Kind()}
}

// SnapshotForCall returns the current detached snapshot for the tool card indexed
// by callID. It returns false when no non-empty call ID was recorded for a tool
// card. Call IDs are indexed when their tool card is appended.
func (c *Conversation) SnapshotForCall(callID string) (BlockSnapshot, bool) {
	i, ok := c.call(callID)
	if !ok {
		return BlockSnapshot{}, false
	}
	return c.SnapshotAt(i), true
}

func (c *Conversation) append(payload PayloadSnapshot) BlockID {
	c.nextID++
	c.cards = append(c.cards, card{id: c.nextID, payload: clonePayload(payload)})
	return c.nextID
}

func (c *Conversation) index(id BlockID) int {
	return slices.IndexFunc(c.cards, func(card card) bool { return card.id == id })
}

func (c *Conversation) replace(i int, payload PayloadSnapshot) bool {
	payload = clonePayload(payload)
	if reflect.DeepEqual(c.cards[i].payload, payload) {
		return true
	}
	c.cards[i].payload = payload
	c.cards[i].revision++
	return true
}

func (c *Conversation) removeCard(i int) {
	c.cards = append(c.cards[:i:i], c.cards[i+1:]...)
	for callID, index := range c.calls {
		switch {
		case index == i:
			delete(c.calls, callID)
		case index > i:
			c.calls[callID] = index - 1
		}
	}
}

func (c *Conversation) call(id string) (int, bool) {
	i, ok := c.calls[id]
	return i, ok
}
