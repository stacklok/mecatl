package server

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ownsResource is the sole persisted-resource decision. In the compatibility
// path no verified caller exists and ownership remains inert. With OIDC wired,
// ownerless records and a missing or mismatched caller are deliberately
// indistinguishable from absence.
func (s *Service) ownsResource(ctx context.Context, owner *session.Principal) bool {
	if !s.cfg.OwnershipEnforced {
		return true
	}
	return owner != nil && owner.SameIdentity(session.PrincipalFromContext(ctx))
}

func (s *Service) authorizeSession(ctx context.Context, sess *session.Session) error {
	if sess == nil || !s.ownsResource(ctx, sess.Owner) {
		return fmt.Errorf("%w", ErrNotFound)
	}
	return nil
}

func (s *Service) authorizeSchedule(ctx context.Context, owner *session.Principal) error {
	if !s.ownsResource(ctx, owner) {
		return fmt.Errorf("%w", port.ErrScheduleNotFound)
	}
	return nil
}
