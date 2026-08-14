package client

import (
	"context"
	"fmt"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// ActivityReplayStatus describes the optional, non-authoritative activity replay
// plane independently from the snapshot-derived transcript.
type ActivityReplayStatus struct {
	Available     bool
	Complete      bool
	Authoritative bool
}

// SessionTranscript is the proto-free, human-displayable conversation projection.
type SessionTranscript struct {
	SessionID string
	Messages  []ConversationMessage
	Complete  bool
	Activity  ActivityReplayStatus
}

// GetSessionTranscript fetches the authoritative snapshot-derived transcript.
func (c *Client) GetSessionTranscript(ctx context.Context, id string) (SessionTranscript, error) {
	resp, err := c.svc.GetSessionTranscript(ctx, &mecatlv1.GetSessionTranscriptRequest{SessionId: id})
	if err != nil {
		return SessionTranscript{}, fmt.Errorf("get session transcript: %w", err)
	}
	activity := resp.GetActivity()
	return SessionTranscript{
		SessionID: resp.GetSessionId(),
		Messages:  transcriptMessagesFromProto(resp.GetMessages()),
		Complete:  resp.GetComplete(),
		Activity: ActivityReplayStatus{
			Available:     activity.GetAvailable(),
			Complete:      activity.GetComplete(),
			Authoritative: activity.GetAuthoritative(),
		},
	}, nil
}

func transcriptMessagesFromProto(in []*mecatlv1.ConversationMessage) []ConversationMessage {
	messages := conversationMessagesFromProto(in)
	for i := range messages {
		messages[i].Reasoning = ""
		messages[i].ProviderPhase = ""
		messages[i].ReasoningItemID = ""
	}
	return messages
}
