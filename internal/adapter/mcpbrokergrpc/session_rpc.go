package mcpbrokergrpc

import (
	"context"
	"encoding/base64"
	"errors"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// authorizationChecker is a private adapter preflight capability, not a
// SessionService operation or replay authority.
type authorizationChecker interface {
	CheckAuthorization(context.Context, c.SessionRef, c.CatalogueRef, *c.Call, c.AuthorizationRef) (c.AuthorizationCheck, error)
}

// SessionRPC exposes broker-owned sessions behind verified workload middleware.
type SessionRPC struct {
	p.UnimplementedSessionServiceServer
	service c.SessionService
}

func NewSessionRPC(service c.SessionService) (*SessionRPC, error) {
	if service == nil {
		return nil, c.ErrStateUnavailable
	}
	return &SessionRPC{service: service}, nil
}
func sessionRef(v string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(v)
	return e == nil && len(v) == 43 && len(b) == 32
}
func sessionError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline")
	case errors.Is(err, c.ErrCapacity):
		return status.Error(codes.ResourceExhausted, "session capacity")
	case errors.Is(err, c.ErrAuthorizationNotFound):
		return status.Error(codes.NotFound, "flow unavailable")
	default:
		return status.Error(codes.FailedPrecondition, "session operation unavailable")
	}
}
func sessionCatalogue(cat c.Catalogue) (*p.Catalogue, error) {
	if cat == nil || !cat.Valid() {
		return nil, status.Error(codes.Internal, "invalid catalogue")
	}
	d, _, err := descriptors(cat.Tools())
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid descriptors")
	}
	return &p.Catalogue{Ref: string(cat.Ref()), Tools: d}, nil
}
func sessionPrompt(prompt c.BrowserPrompt) (*p.BrowserPrompt, error) {
	if !prompt.Valid() {
		return nil, status.Error(codes.Internal, "invalid prompt")
	}
	return &p.BrowserPrompt{Url: prompt.URL, ExpiresAt: timestamppb.New(prompt.ExpiresAt)}, nil
}
func sessionInvocation(out c.InvocationOutcome, err error) (*p.InvocationOutcome, error) {
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid invocation outcome")
	}
	switch out.Kind {
	case c.InvocationCompleted:
		r, e := resultToWire(*out.Result)
		if e != nil {
			return nil, status.Error(codes.Internal, "invalid result")
		}
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_Completed{Completed: r}}, nil
	case c.InvocationAuthorizationRequired:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: string(out.Authorization)}}, nil
	case c.InvocationNotDispatched:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_NotDispatched{NotDispatched: &p.NonDispatch{Reason: p.FailureReason(out.Reason)}}}, nil
	default:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_OutcomeUnknown{OutcomeUnknown: &emptypb.Empty{}}}, nil
	}
}
func sessionFlow(out c.FlowStatus, err error) (*p.FlowStatus, error) {
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid flow outcome")
	}
	switch out.Kind {
	case c.FlowCompleted:
		cat, e := sessionCatalogue(out.Catalogue)
		if e != nil {
			return nil, e
		}
		return &p.FlowStatus{Status: &p.FlowStatus_Completed{Completed: cat}}, nil
	case c.FlowPending:
		return &p.FlowStatus{Status: &p.FlowStatus_Pending{Pending: &emptypb.Empty{}}}, nil
	case c.FlowCancelled:
		return &p.FlowStatus{Status: &p.FlowStatus_Cancelled{Cancelled: &emptypb.Empty{}}}, nil
	case c.FlowExpired:
		return &p.FlowStatus{Status: &p.FlowStatus_Expired{Expired: &emptypb.Empty{}}}, nil
	default:
		return &p.FlowStatus{Status: &p.FlowStatus_Failed{Failed: p.FailureReason(out.Reason)}}, nil
	}
}
func sessionCancel(out c.CancelResult, err error) (*p.CancelOutcome, error) {
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid cancel outcome")
	}
	return &p.CancelOutcome{Outcome: p.CancelOutcome_Value(out)}, nil
}
func (s *SessionRPC) OpenSession(ctx context.Context, r *p.OpenSessionRequest) (*p.SessionSnapshot, error) {
	if r == nil {
		return nil, invalid("request required")
	}
	var saved *c.SessionRef
	if r.SavedRef != nil {
		if !sessionRef(*r.SavedRef) {
			return nil, invalid("invalid saved reference")
		}
		ref := c.SessionRef(*r.SavedRef)
		saved = &ref
	}
	out, err := s.service.OpenSession(ctx, saved)
	if err != nil {
		return nil, sessionError(err)
	}
	cat, err := sessionCatalogue(out.Catalogue)
	if err != nil {
		return nil, err
	}
	return &p.SessionSnapshot{Ref: string(out.Ref), ExpiresAt: timestamppb.New(out.ExpiresAt), Catalogue: cat}, nil
}
func (s *SessionRPC) CheckAuthorization(ctx context.Context, r *p.CheckAuthorizationRequest) (*p.CheckAuthorizationResponse, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.CatalogueRef) {
		return nil, invalid("invalid check reference")
	}
	var call *c.Call
	var auth c.AuthorizationRef
	switch target := r.Target.(type) {
	case *p.CheckAuthorizationRequest_Call:
		if target.Call == nil {
			return nil, invalid("missing call")
		}
		native, err := callFrom(target.Call.Name, target.Call.Id, target.Call.Arguments, "")
		if err != nil {
			return nil, invalid("invalid call")
		}
		call = &c.Call{ID: native.ID, Name: native.Name, Arguments: native.Args}
	case *p.CheckAuthorizationRequest_AuthorizationRef:
		if !sessionRef(target.AuthorizationRef) {
			return nil, invalid("invalid authorization reference")
		}
		auth = c.AuthorizationRef(target.AuthorizationRef)
	default:
		return nil, invalid("missing check target")
	}
	checker, ok := s.service.(authorizationChecker)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "authorization preflight unavailable")
	}
	out, err := checker.CheckAuthorization(ctx, c.SessionRef(r.SessionRef), c.CatalogueRef(r.CatalogueRef), call, auth)
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid check outcome")
	}
	if out.Ready {
		return &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_Ready{Ready: &emptypb.Empty{}}}, nil
	}
	if out.Authorization != "" {
		return &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_AuthorizationRequired{AuthorizationRequired: &p.FlowRef{Ref: string(out.Authorization), ExpiresAt: timestamppb.New(out.ExpiresAt)}}}, nil
	}
	return &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_NotDispatched{NotDispatched: &p.NonDispatch{Reason: p.FailureReason(out.Reason)}}}, nil
}

func (s *SessionRPC) InvokeTool(ctx context.Context, r *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.CatalogueRef) || r.Call == nil {
		return nil, invalid("invalid invocation")
	}
	call, err := callFrom(r.Call.Name, r.Call.Id, r.Call.Arguments, "")
	if err != nil {
		return nil, invalid("invalid call")
	}
	return sessionInvocation(s.service.InvokeTool(ctx, c.SessionRef(r.SessionRef), c.CatalogueRef(r.CatalogueRef), c.Call{ID: call.ID, Name: call.Name, Arguments: call.Args}))
}
func (s *SessionRPC) BeginAuthorization(ctx context.Context, r *p.BeginAuthorizationRequest) (*p.BrowserPrompt, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.AuthorizationRef) {
		return nil, invalid("invalid flow reference")
	}
	out, err := s.service.BeginAuthorization(ctx, c.SessionRef(r.SessionRef), c.AuthorizationRef(r.AuthorizationRef))
	if err != nil {
		return nil, sessionError(err)
	}
	return sessionPrompt(out)
}
func (s *SessionRPC) ObserveAuthorization(ctx context.Context, r *p.ObserveAuthorizationRequest) (*p.FlowStatus, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.AuthorizationRef) {
		return nil, invalid("invalid flow reference")
	}
	return sessionFlow(s.service.ObserveAuthorization(ctx, c.SessionRef(r.SessionRef), c.AuthorizationRef(r.AuthorizationRef)))
}
func (s *SessionRPC) CancelAuthorization(ctx context.Context, r *p.CancelAuthorizationRequest) (*p.CancelOutcome, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.AuthorizationRef) {
		return nil, invalid("invalid flow reference")
	}
	return sessionCancel(s.service.CancelAuthorization(ctx, c.SessionRef(r.SessionRef), c.AuthorizationRef(r.AuthorizationRef)))
}
func (s *SessionRPC) ResumeTool(ctx context.Context, r *p.ResumeToolRequest) (*p.InvocationOutcome, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.AuthorizationRef) || !sessionRef(r.AdoptedCatalogue) {
		return nil, invalid("invalid resume reference")
	}
	return sessionInvocation(s.service.ResumeTool(ctx, c.SessionRef(r.SessionRef), c.AuthorizationRef(r.AuthorizationRef), c.CatalogueRef(r.AdoptedCatalogue)))
}
func (s *SessionRPC) BeginEnrollment(ctx context.Context, r *p.BeginEnrollmentRequest) (*p.BeginEnrollmentResponse, error) {
	if r == nil || !sessionRef(r.SessionRef) {
		return nil, invalid("invalid session reference")
	}
	out, err := s.service.BeginEnrollment(ctx, c.SessionRef(r.SessionRef))
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid enrollment outcome")
	}
	switch out.Kind {
	case c.EnrollmentStartedKind:
		prompt, e := sessionPrompt(out.Started.Prompt)
		if e != nil {
			return nil, e
		}
		return &p.BeginEnrollmentResponse{Outcome: &p.BeginEnrollmentResponse_Started{Started: &p.EnrollmentStarted{Ref: string(out.Started.Ref), Prompt: prompt}}}, nil
	case c.EnrollmentCompletedKind:
		cat, e := sessionCatalogue(out.Catalogue)
		if e != nil {
			return nil, e
		}
		return &p.BeginEnrollmentResponse{Outcome: &p.BeginEnrollmentResponse_Completed{Completed: cat}}, nil
	default:
		return &p.BeginEnrollmentResponse{Outcome: &p.BeginEnrollmentResponse_AlreadyConnected{AlreadyConnected: &emptypb.Empty{}}}, nil
	}
}
func (s *SessionRPC) ObserveEnrollment(ctx context.Context, r *p.ObserveEnrollmentRequest) (*p.FlowStatus, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.EnrollmentRef) {
		return nil, invalid("invalid enrollment reference")
	}
	return sessionFlow(s.service.ObserveEnrollment(ctx, c.SessionRef(r.SessionRef), c.EnrollmentRef(r.EnrollmentRef)))
}
func (s *SessionRPC) CancelEnrollment(ctx context.Context, r *p.CancelEnrollmentRequest) (*p.CancelOutcome, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.EnrollmentRef) {
		return nil, invalid("invalid enrollment reference")
	}
	return sessionCancel(s.service.CancelEnrollment(ctx, c.SessionRef(r.SessionRef), c.EnrollmentRef(r.EnrollmentRef)))
}
func (s *SessionRPC) DisconnectTools(ctx context.Context, r *p.DisconnectToolsRequest) (*p.DisconnectOutcome, error) {
	if r == nil || !sessionRef(r.SessionRef) || !sessionRef(r.ExpectedCatalogue) {
		return nil, invalid("invalid catalogue reference")
	}
	out, err := s.service.DisconnectTools(ctx, c.SessionRef(r.SessionRef), c.CatalogueRef(r.ExpectedCatalogue))
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid disconnect outcome")
	}
	return &p.DisconnectOutcome{Outcome: p.DisconnectOutcome_Value(out)}, nil
}
func (s *SessionRPC) DeleteSession(ctx context.Context, r *p.DeleteSessionRequest) (*p.DeleteOutcome, error) {
	if r == nil || !sessionRef(r.SessionRef) {
		return nil, invalid("invalid session reference")
	}
	out, err := s.service.DeleteSession(ctx, c.SessionRef(r.SessionRef))
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid delete outcome")
	}
	return &p.DeleteOutcome{Outcome: p.DeleteOutcome_Value(out)}, nil
}
