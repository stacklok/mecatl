// Package scrollback owns Mecatui's logical conversation cards. It deliberately
// contains no renderer, terminal, viewport, or client-event dependency.
//
//nolint:revive // This internal package deliberately exposes a typed package contract without public-module documentation.
package scrollback

import "reflect"

type BlockID uint64

type Kind uint8

const (
	KindUser Kind = iota
	KindAssistant
	KindTool
	KindSubagent
	KindTeam
	KindNotice
	KindTurnStat
	KindError
	KindHook
	KindDelivery
)

// PayloadSnapshot is closed: consumers can inspect package-defined snapshots but
// cannot create another card family or mutate model-owned payload state.
type PayloadSnapshot interface {
	Kind() Kind
	payloadSnapshot()
}

type BlockSnapshot struct {
	ID       BlockID
	Revision uint64
	Payload  PayloadSnapshot
}

type card struct {
	id       BlockID
	revision uint64
	payload  PayloadSnapshot
}

type Conversation struct {
	cards    []card
	nextID   BlockID
	calls    map[string]int
	appendix *AppendixSnapshot
	seen     map[string]struct{}
}

func (c *Conversation) Len() int { return len(c.cards) }

func (c *Conversation) SnapshotAt(i int) BlockSnapshot {
	card := c.cards[i]
	return BlockSnapshot{ID: card.id, Revision: card.revision, Payload: clonePayload(card.payload)}
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

func (c *Conversation) call(id string) (int, bool) {
	i, ok := c.calls[id]
	return i, ok
}
