package server

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/syscaller"
)

func staleReconcileAuthorized(ctx context.Context) bool {
	principal := session.PrincipalFromContext(ctx)
	return principal != nil && principal.Issuer == syscaller.Issuer &&
		principal.Subject == string(syscaller.RootStaleSessionReconcile) &&
		principal.GrantType == session.GrantTypeSystem
}

func staleMaintenanceCandidate(meta port.SessionDiscoveryMeta) bool {
	return meta.State == session.StateRunning && meta.Kind != session.SessionKindScheduled &&
		!strings.HasPrefix(string(meta.ID), scheduleFireSessionPrefix) &&
		session.ValidateSessionMetadata(meta.Kind, meta.Relationship) == nil
}

func staleMaintenanceSessionCandidate(sess *session.Session) bool {
	return sess.State == session.StateRunning && sess.Kind != session.SessionKindScheduled &&
		!strings.HasPrefix(string(sess.ID), scheduleFireSessionPrefix) &&
		session.ValidateSessionMetadata(sess.Kind, sess.Relationship) == nil
}

// StaleRunningCandidates returns only the metadata needed by the dedicated
// stale-session reconciler. It is store-wide infrastructure enumeration, not a
// caller-owned list operation, and accepts only that worker's system root.
func (s *Service) StaleRunningCandidates(ctx context.Context) ([]port.SessionMeta, error) {
	if !staleReconcileAuthorized(ctx) {
		return nil, ErrManagementUnauthorized
	}
	pager, ok := s.cfg.Store.(port.SessionMetadataPager)
	if !ok {
		return nil, nil
	}
	var (
		out    []port.SessionMeta
		cursor *port.SessionMetadataCursor
	)
	for {
		page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 256, Cursor: cursor})
		if errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, row := range page.Sessions {
			if !staleMaintenanceCandidate(row) {
				continue
			}
			if s.cfg.OwnershipEnforced && row.Owner == nil {
				continue
			}
			out = append(out, port.SessionMeta{ID: row.ID, ModifiedAt: row.ModifiedAt, State: row.State})
		}
		if page.NextCursor == nil {
			return out, nil
		}
		// No-progress is a cursor that did not ADVANCE; an empty page is not itself
		// a stall, since concurrent deletion can empty a page the walk has already
		// passed. Cursor KEY comparison, not struct equality: SessionMetadataCursor
		// embeds a time.Time, and == on it compares the monotonic reading and the
		// location pointer, so the guard would never fire.
		if cursor != nil &&
			page.NextCursor.ModifiedAt.Equal(cursor.ModifiedAt) && page.NextCursor.ID == cursor.ID {
			return nil, port.ErrSessionMetadataCursorRestart
		}
		next := *page.NextCursor
		cursor = &next
	}
}
