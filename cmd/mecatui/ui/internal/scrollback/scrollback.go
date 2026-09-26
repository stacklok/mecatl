package scrollback

import "reflect"

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
// for the card's lifetime, while Revision starts at zero and advances only when
// that card's visible payload changes.
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
