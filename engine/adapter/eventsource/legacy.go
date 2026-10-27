package eventsource

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

const upgradeAbandonmentNotice = "The prior continuation was abandoned during upgrade. Prior tool effects and completion may be unknown."

// normalizeHistory abandons an unprovable legacy continuation at the end of one
// reconstruction segment, before any later canonical run is folded. The added
// messages are migration outcomes, not durable source records: they have no
// invented event coordinates or evidence attachments.
// Current-format authorization validation must run before this normalization.
func (p Projection) normalizeHistory(messages []session.Message, data sessnap.RestoreData) ([]session.Message, sessnap.RestoreData, error) {
	if !p.abandoned {
		return messages, data, nil
	}
	if data.Pending != nil {
		return nil, data, fmt.Errorf("eventsource: current approval overlaps abandoned legacy continuation")
	}
	for _, id := range p.interrupted {
		messages = append(messages, session.NewToolMessage(session.NewToolError(id, upgradeAbandonmentNotice)))
	}
	messages = append(messages, session.NewAssistantMessage(upgradeAbandonmentNotice, nil))
	if err := session.ValidateToolPairing(messages); err != nil {
		return nil, data, err
	}
	return messages, sessnap.RestoreData{State: session.StateIdle, Counters: data.Counters, TokenUsage: data.TokenUsage}, nil
}
