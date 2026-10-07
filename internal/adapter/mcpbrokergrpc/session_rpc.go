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
