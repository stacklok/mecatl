package mcpbrokergrpc

import (
	"context"
	"encoding/base64"
	"errors"
	"time"
	"unicode/utf8"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SessionRPC exposes broker-owned session lifecycle behind verified workload middleware.
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

func validSessionRef(ref string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(ref)
	return err == nil && len(ref) == 43 && len(decoded) == 32
}

func invalidRequest(message string) error { return status.Error(codes.InvalidArgument, message) }

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
	default:
		return status.Error(codes.FailedPrecondition, "session operation unavailable")
	}
}

func sessionCatalogue(cat c.Catalogue) (*p.Catalogue, error) {
	if cat == nil || !cat.Valid() || !validSessionRef(string(cat.Ref())) {
		return nil, status.Error(codes.Internal, "invalid catalogue")
	}
	out := &p.Catalogue{Ref: string(cat.Ref())}
	if connection := cat.Connection(); connection != "" {
		if !validSessionRef(string(connection)) {
			return nil, status.Error(codes.Internal, "invalid catalogue connection")
		}
		value := string(connection)
		out.ConnectionRef = &value
	}
	for _, candidate := range cat.Tools() {
		if candidate == nil {
			return nil, status.Error(codes.Internal, "invalid catalogue descriptor")
		}
		spec := candidate.Spec()
		if advertised, ok := candidate.(tool.Disclosable); ok {
			spec = advertised.Advertised()
		}
		if !utf8.ValidString(spec.Name) || !utf8.ValidString(spec.Description) || !utf8.Valid(spec.Schema) {
			return nil, status.Error(codes.Internal, "invalid catalogue descriptor")
		}
		_, serial := candidate.(tool.DispatchSerial)
		_, authorization := candidate.(tool.AuthorizationRequester)
		out.Tools = append(out.Tools, &p.ToolDescriptor{
			Name: spec.Name, Description: spec.Description, Schema: append([]byte(nil), spec.Schema...),
			ReadOnly: candidate.ReadOnly(), DispatchSerial: serial, AuthorizationCapable: authorization,
		})
	}
	return out, nil
}

func (s *SessionRPC) OpenSession(ctx context.Context, request *p.OpenSessionRequest) (*p.SessionSnapshot, error) {
	if request == nil {
		return nil, invalidRequest("request required")
	}
	var saved *c.SessionRef
	if request.SavedRef != nil {
		if !validSessionRef(*request.SavedRef) {
			return nil, invalidRequest("invalid saved reference")
		}
		ref := c.SessionRef(*request.SavedRef)
		saved = &ref
	}
	snapshot, err := s.service.OpenSession(ctx, saved)
	if err != nil {
		return nil, sessionError(err)
	}
	if !validSessionRef(string(snapshot.Ref)) || snapshot.ExpiresAt.IsZero() || !snapshot.ExpiresAt.After(time.Now()) || (saved != nil && snapshot.Ref != *saved) {
		return nil, status.Error(codes.Internal, "invalid session snapshot")
	}
	catalogue, err := sessionCatalogue(snapshot.Catalogue)
	if err != nil {
		return nil, err
	}
	return &p.SessionSnapshot{Ref: string(snapshot.Ref), ExpiresAt: timestamppb.New(snapshot.ExpiresAt), Catalogue: catalogue}, nil
}

func (s *SessionRPC) InvokeTool(ctx context.Context, request *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.CatalogueRef) || request.Call == nil || !wireAttemptValid(request.Attempt) {
		return nil, invalidRequest("invalid invocation")
	}
	call, err := callFrom(request.Call.Name, request.Call.Id, request.Call.Arguments, "")
	if err != nil {
		return nil, invalidRequest("invalid call")
	}
	out, err := s.service.InvokeTool(ctx, c.SessionRef(request.SessionRef), c.CatalogueRef(request.CatalogueRef), c.Call{ID: call.ID, Name: call.Name, Arguments: call.Args}, attemptFromWire(request.Attempt))
	if err != nil {
		return nil, sessionError(err)
	}
	if !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid invocation outcome")
	}
	switch out.Kind {
	case c.InvocationCompleted:
		if out.Result.CallID != session.ToolCallID(call.ID) {
			return nil, status.Error(codes.Internal, "invalid result call ID")
		}
		result, err := resultToWire(*out.Result)
		if err != nil {
			return nil, status.Error(codes.Internal, "invalid result")
		}
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_Completed{Completed: result}}, nil
	case c.InvocationAuthorizationRequired:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: string(out.Authorization)}}, nil
	case c.InvocationNotDispatched:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_NotDispatched{NotDispatched: &p.NonDispatch{Reason: p.FailureReason(out.Reason)}}}, nil
	default:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_OutcomeUnknown{OutcomeUnknown: &emptypb.Empty{}}}, nil
	}
}

func (s *SessionRPC) CheckAuthorization(ctx context.Context, request *p.CheckAuthorizationRequest) (*p.CheckAuthorizationResponse, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.CatalogueRef) || !wireAttemptValid(request.Attempt) {
		return nil, invalidRequest("invalid authorization check")
	}
	var call *c.Call
	var authorization c.AuthorizationRef
	switch target := request.Target.(type) {
	case *p.CheckAuthorizationRequest_Call:
		if target.Call == nil {
			return nil, invalidRequest("invalid authorization call")
		}
		decoded, err := callFrom(target.Call.Name, target.Call.Id, target.Call.Arguments, "")
		if err != nil {
			return nil, invalidRequest("invalid authorization call")
		}
		call = &c.Call{ID: decoded.ID, Name: decoded.Name, Arguments: decoded.Args}
	case *p.CheckAuthorizationRequest_AuthorizationRef:
		if !validSessionRef(target.AuthorizationRef) {
			return nil, invalidRequest("invalid authorization reference")
		}
		authorization = c.AuthorizationRef(target.AuthorizationRef)
	default:
		return nil, invalidRequest("authorization target required")
	}
	check, err := s.service.CheckAuthorization(ctx, c.SessionRef(request.SessionRef), c.CatalogueRef(request.CatalogueRef), call, authorization, attemptFromWire(request.Attempt))
	if err != nil {
		return nil, sessionError(err)
	}
	if !check.Valid() {
		return nil, status.Error(codes.Internal, "invalid authorization check")
	}
	switch {
	case check.Ready:
		return &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_Ready{Ready: &emptypb.Empty{}}}, nil
	case check.Authorization != "":
		return &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_AuthorizationRequired{AuthorizationRequired: &p.FlowRef{Ref: string(check.Authorization), ExpiresAt: timestamppb.New(check.ExpiresAt)}}}, nil
	default:
		return &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_NotDispatched{NotDispatched: &p.NonDispatch{Reason: p.FailureReason(check.Reason)}}}, nil
	}
}

func (s *SessionRPC) BeginAuthorization(ctx context.Context, request *p.BeginAuthorizationRequest) (*p.BrowserPrompt, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.AuthorizationRef) {
		return nil, invalidRequest("invalid authorization reference")
	}
	prompt, err := s.service.BeginAuthorization(ctx, c.SessionRef(request.SessionRef), c.AuthorizationRef(request.AuthorizationRef))
	if err != nil {
		return nil, sessionError(err)
	}
	if !prompt.Valid() || !prompt.ExpiresAt.After(time.Now()) {
		return nil, status.Error(codes.Internal, "invalid browser prompt")
	}
	return &p.BrowserPrompt{Url: prompt.URL, ExpiresAt: timestamppb.New(prompt.ExpiresAt)}, nil
}

func flowStatusToWire(flow c.FlowStatus) (*p.FlowStatus, error) {
	if !flow.Valid() {
		return nil, status.Error(codes.Internal, "invalid authorization flow")
	}
	switch flow.Kind {
	case c.FlowPending:
		return &p.FlowStatus{Status: &p.FlowStatus_Pending{Pending: &emptypb.Empty{}}}, nil
	case c.FlowCompleted:
		catalogue, err := sessionCatalogue(flow.Catalogue)
		if err != nil {
			return nil, err
		}
		return &p.FlowStatus{Status: &p.FlowStatus_Completed{Completed: catalogue}}, nil
	case c.FlowCancelled:
		return &p.FlowStatus{Status: &p.FlowStatus_Cancelled{Cancelled: &emptypb.Empty{}}}, nil
	case c.FlowExpired:
		return &p.FlowStatus{Status: &p.FlowStatus_Expired{Expired: &emptypb.Empty{}}}, nil
	case c.FlowFailed:
		return &p.FlowStatus{Status: &p.FlowStatus_Failed{Failed: p.FailureReason(flow.Reason)}}, nil
	default:
		return nil, status.Error(codes.Internal, "invalid authorization flow")
	}
}

func (s *SessionRPC) ObserveAuthorization(ctx context.Context, request *p.ObserveAuthorizationRequest) (*p.FlowStatus, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.AuthorizationRef) {
		return nil, invalidRequest("invalid authorization reference")
	}
	flow, err := s.service.ObserveAuthorization(ctx, c.SessionRef(request.SessionRef), c.AuthorizationRef(request.AuthorizationRef))
	if err != nil {
		return nil, sessionError(err)
	}
	return flowStatusToWire(flow)
}

func (s *SessionRPC) CancelAuthorization(ctx context.Context, request *p.CancelAuthorizationRequest) (*p.CancelOutcome, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.AuthorizationRef) || !wireAttemptValid(request.Attempt) {
		return nil, invalidRequest("invalid authorization cancellation")
	}
	outcome, err := s.service.CancelAuthorization(ctx, c.SessionRef(request.SessionRef), c.AuthorizationRef(request.AuthorizationRef), attemptFromWire(request.Attempt))
	if err != nil {
		return nil, sessionError(err)
	}
	if !outcome.Valid() {
		return nil, status.Error(codes.Internal, "invalid authorization cancellation")
	}
	value := p.CancelOutcome_ALREADY_RESOLVED
	if outcome == c.Cancelled {
		value = p.CancelOutcome_CANCELLED
	}
	return &p.CancelOutcome{Outcome: value}, nil
}

func (s *SessionRPC) ResumeTool(ctx context.Context, request *p.ResumeToolRequest) (*p.InvocationOutcome, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.AuthorizationRef) || !validSessionRef(request.AdoptedCatalogue) || !wireAttemptValid(request.Attempt) {
		return nil, invalidRequest("invalid authorization continuation")
	}
	outcome, err := s.service.ResumeTool(ctx, c.SessionRef(request.SessionRef), c.AuthorizationRef(request.AuthorizationRef), c.CatalogueRef(request.AdoptedCatalogue), attemptFromWire(request.Attempt))
	if err != nil {
		return nil, sessionError(err)
	}
	if !outcome.Valid() {
		return nil, status.Error(codes.Internal, "invalid invocation outcome")
	}
	switch outcome.Kind {
	case c.InvocationCompleted:
		result, err := resultToWire(*outcome.Result)
		if err != nil {
			return nil, status.Error(codes.Internal, "invalid result")
		}
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_Completed{Completed: result}}, nil
	case c.InvocationAuthorizationRequired:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: string(outcome.Authorization)}}, nil
	case c.InvocationNotDispatched:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_NotDispatched{NotDispatched: &p.NonDispatch{Reason: p.FailureReason(outcome.Reason)}}}, nil
	default:
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_OutcomeUnknown{OutcomeUnknown: &emptypb.Empty{}}}, nil
	}
}

func (s *SessionRPC) BeginEnrollment(ctx context.Context, request *p.BeginEnrollmentRequest) (*p.BeginEnrollmentResponse, error) {
	if request == nil || !validSessionRef(request.SessionRef) {
		return nil, invalidRequest("invalid session reference")
	}
	outcome, err := s.service.BeginEnrollment(ctx, c.SessionRef(request.SessionRef))
	if err != nil {
		return nil, sessionError(err)
	}
	if !outcome.Valid() {
		return nil, status.Error(codes.Internal, "invalid enrollment outcome")
	}
	switch outcome.Kind {
	case c.EnrollmentCompletedKind:
		catalogue, err := sessionCatalogue(outcome.Catalogue)
		if err != nil {
			return nil, err
		}
		return &p.BeginEnrollmentResponse{Outcome: &p.BeginEnrollmentResponse_Completed{Completed: catalogue}}, nil
	case c.EnrollmentAlreadyConnected:
		return &p.BeginEnrollmentResponse{Outcome: &p.BeginEnrollmentResponse_AlreadyConnected{AlreadyConnected: &emptypb.Empty{}}}, nil
	default:
		return nil, status.Error(codes.Internal, "enrollment outcome unavailable")
	}
}

func (s *SessionRPC) DisconnectTools(ctx context.Context, request *p.DisconnectToolsRequest) (*p.DisconnectOutcome, error) {
	if request == nil || !validSessionRef(request.SessionRef) || !validSessionRef(request.ExpectedConnection) {
		return nil, invalidRequest("invalid connection reference")
	}
	outcome, err := s.service.DisconnectTools(ctx, c.SessionRef(request.SessionRef), c.ConnectionRef(request.ExpectedConnection))
	if err != nil {
		return nil, sessionError(err)
	}
	if !outcome.Valid() {
		return nil, status.Error(codes.Internal, "invalid disconnect outcome")
	}
	return &p.DisconnectOutcome{Outcome: p.DisconnectOutcome_Value(outcome)}, nil
}

func (s *SessionRPC) DeleteSession(ctx context.Context, request *p.DeleteSessionRequest) (*p.DeleteOutcome, error) {
	if request == nil || !validSessionRef(request.SessionRef) {
		return nil, invalidRequest("invalid session reference")
	}
	outcome, err := s.service.DeleteSession(ctx, c.SessionRef(request.SessionRef))
	if err != nil {
		return nil, sessionError(err)
	}
	if !outcome.Valid() {
		return nil, status.Error(codes.Internal, "invalid delete outcome")
	}
	return &p.DeleteOutcome{Outcome: p.DeleteOutcome_Value(outcome)}, nil
}
