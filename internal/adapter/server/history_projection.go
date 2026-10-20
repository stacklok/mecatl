package server

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/session"
)

// eventSourceIncarnation uses persisted provenance, never a new session or a
// snapshot/log occurrence join. A snapshot supplies only its stable incarnation.
func (s *Service) eventSourceIncarnation(ctx context.Context, id session.SessionID, events []session.Event) (session.IncarnationID, error) {
	for _, ev := range events {
		if p := ev.LegacyIdentity(); p != nil && p.Incarnation.Valid() {
			return p.Incarnation, nil
		}
	}
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return "", err
	}
	return sess.Incarnation(), nil
}

// projectWatchEvent is conservative: a cursor page is not a complete source.
// It changes neither delivery position nor event count and never guesses a pair.
func (s *Service) projectWatchEvent(ctx context.Context, id session.SessionID, ev session.Event) (session.Event, error) {
	if ev.LegacyIdentity() == nil {
		return ev, nil
	}
	inc, err := s.eventSourceIncarnation(ctx, id, []session.Event{ev})
	if err != nil {
		return session.Event{}, err
	}
	projected, err := eventcodec.ProjectRecord(ev, inc, -1)
	if err != nil {
		return session.Event{}, fmt.Errorf("%w: historical activity unavailable", ErrInternal)
	}
	return projected, nil
}
