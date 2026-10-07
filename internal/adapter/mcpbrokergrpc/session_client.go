package mcpbrokergrpc

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var errSessionWire = errors.New("mcpbrokergrpc: invalid session response")
var ErrSessionOutcomeUnknown = errors.New("mcpbrokergrpc: invocation outcome unknown; do not retry")

// SessionClient owns its connection and applies a finite deadline to lifecycle RPCs.
type authorizationCall struct {
	session c.SessionRef
	callID  session.ToolCallID
}

type SessionClient struct {
	conn                         *grpc.ClientConn
	rpc                          p.SessionServiceClient
	rpcDeadline, executeDeadline time.Duration
	mu                           sync.Mutex
	authorizationCalls           map[c.AuthorizationRef]authorizationCall
}

func NewSessionClient(target string, rpcDeadline, executeDeadline time.Duration, options ...grpc.DialOption) (*SessionClient, error) {
	if target == "" || rpcDeadline <= 0 || executeDeadline <= 0 {
		return nil, c.ErrStateUnavailable
	}
	options = append(append([]grpc.DialOption(nil), options...), grpc.WithDisableRetry())
	conn, err := grpc.NewClient(target, options...)
	if err != nil {
		return nil, err
	}
	return &SessionClient{conn: conn, rpc: p.NewSessionServiceClient(conn), rpcDeadline: rpcDeadline, executeDeadline: executeDeadline}, nil
}

func (s *SessionClient) Close() error { return s.conn.Close() }

func cleanSessionWire(message proto.Message) bool {
	if message == nil {
		return false
	}
	m := message.ProtoReflect()
	if !m.IsValid() || len(m.GetUnknown()) != 0 {
		return false
	}
	clean := true
	m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind {
			return true
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if !cleanSessionWire(list.Get(i).Message().Interface()) {
					clean = false
					return false
				}
			}
		} else if !cleanSessionWire(value.Message().Interface()) {
			clean = false
		}
		return clean
	})
	return clean
}

func (s *SessionClient) catalogue(ref c.SessionRef, wire *p.Catalogue) (c.Catalogue, error) {
	if !cleanSessionWire(wire) || !validSessionRef(wire.Ref) || !validSessionRef(string(ref)) || (wire.ConnectionRef != nil && !validSessionRef(*wire.ConnectionRef)) || len(wire.Tools) > 1024 {
		return nil, errSessionWire
	}
	tools := make([]tool.Tool, 0, len(wire.Tools))
	seen := make(map[string]bool, len(wire.Tools))
	for _, descriptor := range wire.Tools {
		if descriptor == nil || seen[descriptor.Name] || !utf8.ValidString(descriptor.Name) || !utf8.ValidString(descriptor.Description) || !utf8.Valid(descriptor.Schema) {
			return nil, errSessionWire
		}
		seen[descriptor.Name] = true
		base := &sessionRemoteTool{client: s, ref: ref, catalogue: c.CatalogueRef(wire.Ref), spec: tool.ToolSpec{Name: descriptor.Name, Description: descriptor.Description, Schema: append([]byte(nil), descriptor.Schema...)}, readOnly: descriptor.ReadOnly}
		tools = append(tools, sessionToolMarkers(base, descriptor.AuthorizationCapable, descriptor.DispatchSerial))
	}
	catalogue, err := c.NewCatalogue(c.CatalogueRef(wire.Ref), c.ConnectionRef(wire.GetConnectionRef()), tools)
	if err != nil {
		return nil, errSessionWire
	}
	return catalogue, nil
}

func (s *SessionClient) OpenSession(ctx context.Context, saved *c.SessionRef) (c.SessionSnapshot, error) {
	request := &p.OpenSessionRequest{}
	if saved != nil {
		if !validSessionRef(string(*saved)) {
			return c.SessionSnapshot{}, errSessionWire
		}
		value := string(*saved)
		request.SavedRef = &value
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.OpenSession(ctx, request, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	if !cleanSessionWire(response) || !validSessionRef(response.Ref) || response.ExpiresAt == nil || !response.ExpiresAt.IsValid() || !response.ExpiresAt.AsTime().After(time.Now()) || (saved != nil && response.Ref != string(*saved)) {
		return c.SessionSnapshot{}, errSessionWire
	}
	catalogue, err := s.catalogue(c.SessionRef(response.Ref), response.Catalogue)
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	return c.SessionSnapshot{Ref: c.SessionRef(response.Ref), ExpiresAt: response.ExpiresAt.AsTime(), Catalogue: catalogue}, nil
}

func unknownInvocation(err error) (c.InvocationOutcome, error) {
	return c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}, err
}

func decodeSessionInvocation(out *p.InvocationOutcome, id session.ToolCallID) (c.InvocationOutcome, error) {
	if !cleanSessionWire(out) || proto.Size(out) > 256*1024 {
		return unknownInvocation(errSessionWire)
	}
	var decoded c.InvocationOutcome
	switch arm := out.Outcome.(type) {
	case *p.InvocationOutcome_Completed:
		result, err := resultFromWire(arm.Completed)
		if err != nil || result.CallID != id {
			return unknownInvocation(errSessionWire)
		}
		decoded = c.InvocationOutcome{Kind: c.InvocationCompleted, Result: &result}
	case *p.InvocationOutcome_AuthorizationRequired:
		decoded = c.InvocationOutcome{Kind: c.InvocationAuthorizationRequired, Authorization: c.AuthorizationRef(arm.AuthorizationRequired)}
	case *p.InvocationOutcome_NotDispatched:
		if arm.NotDispatched == nil || arm.NotDispatched.Reason < 1 || arm.NotDispatched.Reason > 7 {
			return unknownInvocation(errSessionWire)
		}
		decoded = c.InvocationOutcome{Kind: c.InvocationNotDispatched, Reason: c.FailureReason(arm.NotDispatched.Reason)}
	case *p.InvocationOutcome_OutcomeUnknown:
		if arm.OutcomeUnknown == nil {
			return unknownInvocation(errSessionWire)
		}
		decoded = c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}
	default:
		return unknownInvocation(errSessionWire)
	}
	frozen, err := c.NewInvocationOutcome(decoded.Kind, decoded.Result, decoded.Authorization, decoded.Reason)
	if err != nil {
		return unknownInvocation(errSessionWire)
	}
	return frozen, nil
}

func (s *SessionClient) InvokeTool(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call c.Call, attempt c.BrokerAttempt) (c.InvocationOutcome, error) {
	if !attempt.Valid() || !validSessionRef(string(ref)) || !validSessionRef(string(cat)) {
		return unknownInvocation(errSessionWire)
	}
	wire, err := wireCall(call)
	if err != nil {
		return unknownInvocation(err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.executeDeadline)
	defer cancel()
	out, err := s.rpc.InvokeTool(ctx, &p.InvokeToolRequest{SessionRef: string(ref), CatalogueRef: string(cat), Call: wire, Attempt: attemptToWire(attempt)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return unknownInvocation(err)
	}
	return decodeSessionInvocation(out, call.ID)
}

func (s *SessionClient) CheckAuthorization(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call *c.Call, authorization c.AuthorizationRef, attempt c.BrokerAttempt) (c.AuthorizationCheck, error) {
	if !attempt.Valid() || !validSessionRef(string(ref)) || !validSessionRef(string(cat)) || (call == nil) == (authorization == "") {
		return c.AuthorizationCheck{}, errSessionWire
	}
	request := &p.CheckAuthorizationRequest{SessionRef: string(ref), CatalogueRef: string(cat), Attempt: attemptToWire(attempt)}
	if call != nil {
		wire, err := wireCall(*call)
		if err != nil {
			return c.AuthorizationCheck{}, err
		}
		request.Target = &p.CheckAuthorizationRequest_Call{Call: wire}
	} else {
		if !validSessionRef(string(authorization)) {
			return c.AuthorizationCheck{}, errSessionWire
		}
		request.Target = &p.CheckAuthorizationRequest_AuthorizationRef{AuthorizationRef: string(authorization)}
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.CheckAuthorization(ctx, request, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	if !cleanSessionWire(response) {
		return c.AuthorizationCheck{}, errSessionWire
	}
	var check c.AuthorizationCheck
	switch arm := response.Outcome.(type) {
	case *p.CheckAuthorizationResponse_Ready:
		if arm.Ready == nil {
			return c.AuthorizationCheck{}, errSessionWire
		}
		check.Ready = true
	case *p.CheckAuthorizationResponse_AuthorizationRequired:
		flow := arm.AuthorizationRequired
		if flow == nil || !validSessionRef(flow.Ref) || flow.ExpiresAt == nil || !flow.ExpiresAt.IsValid() || !flow.ExpiresAt.AsTime().After(time.Now()) {
			return c.AuthorizationCheck{}, errSessionWire
		}
		check.Authorization, check.ExpiresAt = c.AuthorizationRef(flow.Ref), flow.ExpiresAt.AsTime()
		if call != nil {
			s.mu.Lock()
			if s.authorizationCalls == nil {
				s.authorizationCalls = make(map[c.AuthorizationRef]authorizationCall)
			}
			entry := authorizationCall{session: ref, callID: call.ID}
			if old, exists := s.authorizationCalls[check.Authorization]; exists && old != entry {
				s.mu.Unlock()
				return c.AuthorizationCheck{}, errSessionWire
			}
			s.authorizationCalls[check.Authorization] = entry
			s.mu.Unlock()
		}
	case *p.CheckAuthorizationResponse_NotDispatched:
		if arm.NotDispatched == nil || arm.NotDispatched.Reason < 1 || arm.NotDispatched.Reason > 7 {
			return c.AuthorizationCheck{}, errSessionWire
		}
		check.Reason = c.FailureReason(arm.NotDispatched.Reason)
	default:
		return c.AuthorizationCheck{}, errSessionWire
	}
	if !check.Valid() {
		return c.AuthorizationCheck{}, errSessionWire
	}
	if call != nil && (check.Ready || check.Reason.Valid()) {
		s.mu.Lock()
		for authRef, entry := range s.authorizationCalls {
			if entry.session == ref && entry.callID == call.ID {
				delete(s.authorizationCalls, authRef)
			}
		}
		s.mu.Unlock()
	}
	return check, nil
}

func (s *SessionClient) BeginAuthorization(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef) (c.BrowserPrompt, error) {
	if !validSessionRef(string(ref)) || !validSessionRef(string(authorization)) {
		return c.BrowserPrompt{}, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.BeginAuthorization(ctx, &p.BeginAuthorizationRequest{SessionRef: string(ref), AuthorizationRef: string(authorization)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.BrowserPrompt{}, err
	}
	if !cleanSessionWire(response) || response.ExpiresAt == nil || !response.ExpiresAt.IsValid() {
		return c.BrowserPrompt{}, errSessionWire
	}
	prompt := c.BrowserPrompt{URL: response.Url, ExpiresAt: response.ExpiresAt.AsTime()}
	if !prompt.Valid() || !prompt.ExpiresAt.After(time.Now()) {
		return c.BrowserPrompt{}, errSessionWire
	}
	return prompt, nil
}

func (s *SessionClient) ObserveAuthorization(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef) (c.FlowStatus, error) {
	if !validSessionRef(string(ref)) || !validSessionRef(string(authorization)) {
		return c.FlowStatus{}, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.ObserveAuthorization(ctx, &p.ObserveAuthorizationRequest{SessionRef: string(ref), AuthorizationRef: string(authorization)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.FlowStatus{}, err
	}
	if !cleanSessionWire(response) {
		return c.FlowStatus{}, errSessionWire
	}
	var flow c.FlowStatus
	switch arm := response.Status.(type) {
	case *p.FlowStatus_Pending:
		if arm.Pending == nil {
			return c.FlowStatus{}, errSessionWire
		}
		flow.Kind = c.FlowPending
	case *p.FlowStatus_Completed:
		catalogue, err := s.catalogue(ref, arm.Completed)
		if err != nil {
			return c.FlowStatus{}, err
		}
		flow.Kind, flow.Catalogue = c.FlowCompleted, catalogue
	case *p.FlowStatus_Cancelled:
		if arm.Cancelled == nil {
			return c.FlowStatus{}, errSessionWire
		}
		flow.Kind = c.FlowCancelled
	case *p.FlowStatus_Expired:
		if arm.Expired == nil {
			return c.FlowStatus{}, errSessionWire
		}
		flow.Kind = c.FlowExpired
	case *p.FlowStatus_Failed:
		if arm.Failed < 1 || arm.Failed > 7 {
			return c.FlowStatus{}, errSessionWire
		}
		flow.Kind, flow.Reason = c.FlowFailed, c.FailureReason(arm.Failed)
	default:
		return c.FlowStatus{}, errSessionWire
	}
	if !flow.Valid() {
		return c.FlowStatus{}, errSessionWire
	}
	return flow, nil
}

func (s *SessionClient) CancelAuthorization(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef, attempt c.BrokerAttempt) (c.CancelResult, error) {
	if !validSessionRef(string(ref)) || !validSessionRef(string(authorization)) || !attempt.Valid() {
		return 0, errSessionWire
	}
	s.mu.Lock()
	if entry, exists := s.authorizationCalls[authorization]; exists && entry.session == ref {
		delete(s.authorizationCalls, authorization)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.CancelAuthorization(ctx, &p.CancelAuthorizationRequest{SessionRef: string(ref), AuthorizationRef: string(authorization), Attempt: attemptToWire(attempt)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(response) {
		return 0, errSessionWire
	}
	switch response.Outcome {
	case p.CancelOutcome_CANCELLED:
		return c.Cancelled, nil
	case p.CancelOutcome_ALREADY_RESOLVED:
		return c.AlreadyResolved, nil
	default:
		return 0, errSessionWire
	}
}

func (s *SessionClient) ResumeTool(ctx context.Context, ref c.SessionRef, authorization c.AuthorizationRef, adopted c.CatalogueRef, attempt c.BrokerAttempt) (c.InvocationOutcome, error) {
	if !validSessionRef(string(ref)) || !validSessionRef(string(authorization)) || !validSessionRef(string(adopted)) || !attempt.Valid() {
		return unknownInvocation(errSessionWire)
	}
	s.mu.Lock()
	entry, found := s.authorizationCalls[authorization]
	delete(s.authorizationCalls, authorization)
	s.mu.Unlock()
	if !found || entry.session != ref {
		return unknownInvocation(errSessionWire)
	}
	ctx, cancel := context.WithTimeout(ctx, s.executeDeadline)
	defer cancel()
	response, err := s.rpc.ResumeTool(ctx, &p.ResumeToolRequest{SessionRef: string(ref), AuthorizationRef: string(authorization), AdoptedCatalogue: string(adopted), Attempt: attemptToWire(attempt)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return unknownInvocation(err)
	}
	return decodeSessionInvocation(response, entry.callID)
}

func (s *SessionClient) BeginEnrollment(ctx context.Context, ref c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	if !validSessionRef(string(ref)) {
		return c.BeginEnrollmentOutcome{}, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.BeginEnrollment(ctx, &p.BeginEnrollmentRequest{SessionRef: string(ref)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	if !cleanSessionWire(response) {
		return c.BeginEnrollmentOutcome{}, errSessionWire
	}
	var outcome c.BeginEnrollmentOutcome
	switch arm := response.Outcome.(type) {
	case *p.BeginEnrollmentResponse_AlreadyConnected:
		if arm.AlreadyConnected == nil {
			return outcome, errSessionWire
		}
		outcome.Kind = c.EnrollmentAlreadyConnected
	case *p.BeginEnrollmentResponse_Completed:
		catalogue, err := s.catalogue(ref, arm.Completed)
		if err != nil {
			return outcome, err
		}
		outcome.Kind, outcome.Catalogue = c.EnrollmentCompletedKind, catalogue
	default:
		return outcome, errSessionWire
	}
	if !outcome.Valid() {
		return c.BeginEnrollmentOutcome{}, errSessionWire
	}
	return outcome, nil
}

func (s *SessionClient) DisconnectTools(ctx context.Context, ref c.SessionRef, connection c.ConnectionRef) (c.DisconnectResult, error) {
	if !validSessionRef(string(ref)) || !validSessionRef(string(connection)) {
		return 0, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.DisconnectTools(ctx, &p.DisconnectToolsRequest{SessionRef: string(ref), ExpectedConnection: string(connection)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(response) || response.Outcome < p.DisconnectOutcome_DISCONNECTED || response.Outcome > p.DisconnectOutcome_CONNECTION_CHANGED {
		return 0, errSessionWire
	}
	return c.DisconnectResult(response.Outcome), nil
}

func (s *SessionClient) DeleteSession(ctx context.Context, ref c.SessionRef) (c.DeleteResult, error) {
	if !validSessionRef(string(ref)) {
		return 0, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.DeleteSession(ctx, &p.DeleteSessionRequest{SessionRef: string(ref)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(response) || response.Outcome < p.DeleteOutcome_DELETED || response.Outcome > p.DeleteOutcome_ALREADY_ABSENT {
		return 0, errSessionWire
	}
	return c.DeleteResult(response.Outcome), nil
}

var _ c.SessionService = (*SessionClient)(nil)
var _ tool.Tool = (*sessionRemoteTool)(nil)
var _ tool.AuthorizationRequester = (*sessionAuthorizationTool)(nil)
var _ tool.DispatchSerial = (*sessionSerialTool)(nil)
var _ tool.DispatchSerial = (*sessionAuthorizationSerialTool)(nil)
