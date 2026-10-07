package mcpbroker

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func (s *SessionAPI) BeginEnrollment(ctx context.Context, ref c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	if len(s.process.construction.protectedBackends) != 0 || len(s.process.construction.anonymous) == 0 {
		return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
	}
	ctx, release, err := s.operation(ctx, ref, apiControl{})
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	if st.record.Withdrawing {
		return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
	}
	if st.record.Connected {
		if st.catalogue == nil || st.record.Catalogue != st.catalogue.Ref() {
			return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
		}
		return c.BeginEnrollmentOutcome{Kind: c.EnrollmentAlreadyConnected}, nil
	}

	raw, _, err := s.process.AttachSession(ctx, session.SessionID(ref))
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	attachment, ok := raw.(*Attachment)
	if !ok {
		_, _ = raw.Close(ctx)
		return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
	}
	keepAttachment := false
	defer func() {
		if !keepAttachment {
			_, _ = attachment.Close(context.Background())
		}
	}()
	if err := attachment.ResetWorkspaceEnrollment(ctx); err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	routes, err := discoverCompleteAnonymous(ctx, s.process.construction.anonymous, s.process.reservedToolNames, s.process.diag)
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	if !s.validOperation(ctx) {
		return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
	}
	connection := c.ConnectionRef(apiRef())
	enrollment := c.WorkspaceEnrollmentRef{
		ID: session.WorkspaceEnrollmentID(apiRef()), RequiredServices: uint32(len(s.process.construction.anonymous)), ExpiresAt: st.record.ExpiresAt,
	}
	completed := &completedWorkspaceEnrollment{ref: enrollment, routes: routes}
	workspace, err := attachment.installCompletedEnrollment(completed)
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	catalogue, err := c.NewCatalogue(c.CatalogueRef(apiRef()), connection, workspace.Tools())
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	next := st.record
	next.Connected = true
	next.Withdrawing = false
	next.Connection = string(connection)
	next.Catalogue = catalogue.Ref()
	next.Anonymous = make([]apiDescriptor, 0, len(routes))
	for _, route := range routes {
		next.Anonymous = append(next.Anonymous, apiDescriptor{Backend: route.backend, Spec: copySpec(route.spec), ReadOnly: route.readOnly})
	}
	if err := s.saveRecord(ctx, st, next); err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	st.attachment = attachment
	st.catalogue = catalogue
	attachment.logical.mu.Lock()
	attachment.logical.completedEnrollment = completed
	attachment.logical.mu.Unlock()
	keepAttachment = true
	return c.BeginEnrollmentOutcome{Kind: c.EnrollmentCompletedKind, Catalogue: catalogue}, nil
}

func (s *SessionAPI) DisconnectTools(ctx context.Context, ref c.SessionRef, connection c.ConnectionRef) (c.DisconnectResult, error) {
	if !validAPIRef(string(connection)) {
		return 0, c.ErrStateUnavailable
	}
	ctx, release, err := s.operation(ctx, ref, apiControl{connection: connection})
	if err != nil {
		return 0, err
	}
	defer release()
	st, err := s.metadataState(ctx, ref)
	if err != nil {
		return 0, err
	}
	if st.record.Connection != string(connection) {
		return c.ConnectionChanged, nil
	}
	if !st.record.Connected && !st.record.Withdrawing {
		s.rearm(ctx, st)
		return c.AlreadyDisconnected, nil
	}
	if err := s.controlGeneration(ctx, st); err != nil {
		return 0, err
	}
	if !st.record.Withdrawing {
		next := st.record
		next.Withdrawing = true
		if err := s.saveRecord(ctx, st, next); err != nil {
			return 0, err
		}
	}
	if st.attachment == nil {
		return 0, c.ErrStateUnavailable
	}
	if _, err := st.attachment.Close(ctx); err != nil {
		return 0, err
	}
	if _, err := s.process.DeleteSession(ctx, session.SessionID(ref)); err != nil {
		return 0, err
	}
	next := st.record
	next.Connected = false
	next.Withdrawing = false
	next.Anonymous = nil
	next.Catalogue = c.CatalogueRef(apiRef())
	catalogue, err := c.NewCatalogue(next.Catalogue, c.ConnectionRef(next.Connection), nil)
	if err != nil {
		return 0, c.ErrStateUnavailable
	}
	if err := s.saveRecord(ctx, st, next); err != nil {
		return 0, err
	}
	st.completeDisconnect()
	st.catalogue = catalogue
	s.rearm(ctx, st)
	return c.Disconnected, nil
}
