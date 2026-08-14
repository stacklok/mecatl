package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ActivityReplayStatus describes the optional EventLog activity plane. EventLog
// has no completeness attestation, so Complete and Authoritative remain false.
type ActivityReplayStatus struct {
	Available     bool
	Complete      bool
	Authoritative bool
}

// SessionTranscript is the coherent, human-displayable projection of one loaded
// session aggregate. A successful load is complete even when Messages is empty.
type SessionTranscript struct {
	SessionID    session.SessionID
	Messages     []session.Message
	Complete     bool
	Activity     ActivityReplayStatus
	Kind         session.SessionKind
	Relationship session.SessionRelationship
}

// GetTranscript performs exactly one ownership-checked SessionStore load and
// returns its current Conversation. It deliberately bypasses run entry: no
// environment resolution, engine rehydration, lease, or persistence occurs.
func (s *Service) GetTranscript(ctx context.Context, id session.SessionID) (*SessionTranscript, error) {
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session transcript load failed", "session", id, "error", err)
		return nil, fmt.Errorf("%w: transcript storage unavailable", ErrInternal)
	}
	if sess == nil {
		return nil, fmt.Errorf("%w: load transcript returned nil session", ErrInternal)
	}
	if !validSessionIdentityMetadata(sess.ID, sess.Relationship) {
		return nil, fmt.Errorf("%w: transcript contains invalid session identity metadata", ErrInternal)
	}
	if err := s.authorizeSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	return &SessionTranscript{
		SessionID:    sess.ID,
		Messages:     session.CloneMessages(sess.Conversation.Messages),
		Complete:     true,
		Kind:         sess.Kind,
		Relationship: sess.Relationship,
		Activity: ActivityReplayStatus{
			Available: s.cfg.EventLog != nil,
		},
	}, nil
}
