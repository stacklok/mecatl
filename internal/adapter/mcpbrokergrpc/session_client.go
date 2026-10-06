package mcpbrokergrpc

import (
	"context"
	"errors"
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

// SessionClient owns its connection. The host supplies TLS and verified workload
// credentials through dial options. Policy retries are disabled; no operation is
// retried by this adapter and catalogues are never automatically adopted.
type SessionClient struct {
	conn                         *grpc.ClientConn
	rpc                          p.SessionServiceClient
	rpcDeadline, executeDeadline time.Duration
}

var _ c.SessionService = (*SessionClient)(nil)

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

func sessionRefs(refs ...string) error {
	for _, ref := range refs {
		if !sessionRef(ref) {
			return errSessionWire
		}
	}
	return nil
}
func wireCall(call c.Call) (*p.Call, error) {
	if _, err := callFrom(call.Name, string(call.ID), call.Arguments, ""); err != nil {
		return nil, err
	}
	return &p.Call{Id: string(call.ID), Name: call.Name, Arguments: append([]byte(nil), call.Arguments...)}, nil
}
func (s *SessionClient) catalogue(ref c.SessionRef, wire *p.Catalogue) (c.Catalogue, error) {
	if !cleanSessionWire(wire) || !sessionRef(wire.Ref) || len(wire.Tools) > 1024 {
		return nil, errSessionWire
	}
	tools := make([]tool.Tool, 0, len(wire.Tools))
	seen := map[string]bool{}
	for _, d := range wire.Tools {
		if d == nil || seen[d.Name] || !utf8.Valid(d.Schema) {
			return nil, errSessionWire
		}
		seen[d.Name] = true
		base := &sessionRemoteTool{client: s, ref: ref, catalogue: c.CatalogueRef(wire.Ref), spec: tool.ToolSpec{Name: d.Name, Description: d.Description, Schema: append([]byte(nil), d.Schema...)}, readOnly: d.ReadOnly}
		tools = append(tools, sessionToolMarkers(base, d.AuthorizationCapable, d.DispatchSerial))
	}
	return c.NewCatalogue(c.CatalogueRef(wire.Ref), tools)
}
func (s *SessionClient) OpenSession(ctx context.Context, saved *c.SessionRef) (c.SessionSnapshot, error) {
	r := &p.OpenSessionRequest{}
	if saved != nil {
		if err := sessionRefs(string(*saved)); err != nil {
			return c.SessionSnapshot{}, err
		}
		v := string(*saved)
		r.SavedRef = &v
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.OpenSession(ctx, r, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	if !cleanSessionWire(out) || !sessionRef(out.Ref) || out.ExpiresAt == nil || !out.ExpiresAt.IsValid() || !out.ExpiresAt.AsTime().After(time.Now()) || (saved != nil && out.Ref != string(*saved)) {
		return c.SessionSnapshot{}, errSessionWire
	}
	cat, err := s.catalogue(c.SessionRef(out.Ref), out.Catalogue)
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	return c.SessionSnapshot{Ref: c.SessionRef(out.Ref), ExpiresAt: out.ExpiresAt.AsTime(), Catalogue: cat}, nil
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
		r, err := resultFromWire(arm.Completed)
		if err != nil || (id != "" && r.CallID != id) {
			return unknownInvocation(errSessionWire)
		}
		decoded = c.InvocationOutcome{Kind: c.InvocationCompleted, Result: &r}
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
func (s *SessionClient) InvokeTool(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call c.Call) (c.InvocationOutcome, error) {
	if err := sessionRefs(string(ref), string(cat)); err != nil {
		return unknownInvocation(err)
	}
	wire, err := wireCall(call)
	if err != nil {
		return unknownInvocation(err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.executeDeadline)
	defer cancel()
	out, err := s.rpc.InvokeTool(ctx, &p.InvokeToolRequest{SessionRef: string(ref), CatalogueRef: string(cat), Call: wire}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return unknownInvocation(err)
	}
	return decodeSessionInvocation(out, call.ID)
}
func (s *SessionClient) ResumeTool(ctx context.Context, ref c.SessionRef, auth c.AuthorizationRef, cat c.CatalogueRef) (c.InvocationOutcome, error) {
	if err := sessionRefs(string(ref), string(auth), string(cat)); err != nil {
		return unknownInvocation(err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.executeDeadline)
	defer cancel()
	out, err := s.rpc.ResumeTool(ctx, &p.ResumeToolRequest{SessionRef: string(ref), AuthorizationRef: string(auth), AdoptedCatalogue: string(cat)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return unknownInvocation(err)
	}
	return decodeSessionInvocation(out, "")
}
func (s *SessionClient) CheckAuthorization(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call *c.Call, auth c.AuthorizationRef) (c.AuthorizationCheck, error) {
	if err := sessionRefs(string(ref), string(cat)); err != nil {
		return c.AuthorizationCheck{}, err
	}
	r := &p.CheckAuthorizationRequest{SessionRef: string(ref), CatalogueRef: string(cat)}
	if (call == nil) == (auth == "") {
		return c.AuthorizationCheck{}, errSessionWire
	}
	if call != nil {
		wire, err := wireCall(*call)
		if err != nil {
			return c.AuthorizationCheck{}, err
		}
		r.Target = &p.CheckAuthorizationRequest_Call{Call: wire}
	} else {
		if !sessionRef(string(auth)) {
			return c.AuthorizationCheck{}, errSessionWire
		}
		r.Target = &p.CheckAuthorizationRequest_AuthorizationRef{AuthorizationRef: string(auth)}
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.CheckAuthorization(ctx, r, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.AuthorizationCheck{}, err
	}
	if !cleanSessionWire(out) {
		return c.AuthorizationCheck{}, errSessionWire
	}
	var check c.AuthorizationCheck
	switch arm := out.Outcome.(type) {
	case *p.CheckAuthorizationResponse_Ready:
		if arm.Ready == nil {
			return check, errSessionWire
		}
		check.Ready = true
	case *p.CheckAuthorizationResponse_AuthorizationRequired:
		a := arm.AuthorizationRequired
		if a == nil || a.ExpiresAt == nil || !a.ExpiresAt.IsValid() || !a.ExpiresAt.AsTime().After(time.Now()) || (auth != "" && a.Ref != string(auth)) {
			return check, errSessionWire
		}
		check.Authorization = c.AuthorizationRef(a.Ref)
		check.ExpiresAt = a.ExpiresAt.AsTime()
	case *p.CheckAuthorizationResponse_NotDispatched:
		if arm.NotDispatched == nil || arm.NotDispatched.Reason < 1 || arm.NotDispatched.Reason > 7 {
			return check, errSessionWire
		}
		check.Reason = c.FailureReason(arm.NotDispatched.Reason)
	default:
		return check, errSessionWire
	}
	if !check.Valid() {
		return c.AuthorizationCheck{}, errSessionWire
	}
	return check, nil
}
func decodeSessionPrompt(out *p.BrowserPrompt) (c.BrowserPrompt, error) {
	if !cleanSessionWire(out) || out.ExpiresAt == nil || !out.ExpiresAt.IsValid() {
		return c.BrowserPrompt{}, errSessionWire
	}
	prompt := c.BrowserPrompt{URL: out.Url, ExpiresAt: out.ExpiresAt.AsTime()}
	if !prompt.Valid() || !prompt.ExpiresAt.After(time.Now()) {
		return c.BrowserPrompt{}, errSessionWire
	}
	return prompt, nil
}
func (s *SessionClient) BeginAuthorization(ctx context.Context, ref c.SessionRef, auth c.AuthorizationRef) (c.BrowserPrompt, error) {
	if err := sessionRefs(string(ref), string(auth)); err != nil {
		return c.BrowserPrompt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.BeginAuthorization(ctx, &p.BeginAuthorizationRequest{SessionRef: string(ref), AuthorizationRef: string(auth)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.BrowserPrompt{}, err
	}
	return decodeSessionPrompt(out)
}
func (s *SessionClient) flow(ref c.SessionRef, out *p.FlowStatus) (c.FlowStatus, error) {
	if !cleanSessionWire(out) {
		return c.FlowStatus{}, errSessionWire
	}
	var decoded c.FlowStatus
	switch arm := out.Status.(type) {
	case *p.FlowStatus_Pending:
		if arm.Pending == nil {
			return decoded, errSessionWire
		}
		decoded.Kind = c.FlowPending
	case *p.FlowStatus_Cancelled:
		if arm.Cancelled == nil {
			return decoded, errSessionWire
		}
		decoded.Kind = c.FlowCancelled
	case *p.FlowStatus_Expired:
		if arm.Expired == nil {
			return decoded, errSessionWire
		}
		decoded.Kind = c.FlowExpired
	case *p.FlowStatus_Failed:
		if arm.Failed < 1 || arm.Failed > 7 {
			return decoded, errSessionWire
		}
		decoded.Kind = c.FlowFailed
		decoded.Reason = c.FailureReason(arm.Failed)
	case *p.FlowStatus_Completed:
		cat, err := s.catalogue(ref, arm.Completed)
		if err != nil {
			return decoded, err
		}
		decoded.Kind = c.FlowCompleted
		decoded.Catalogue = cat
	default:
		return decoded, errSessionWire
	}
	return c.NewFlowStatus(decoded.Kind, decoded.Catalogue, decoded.Reason)
}
func (s *SessionClient) ObserveAuthorization(ctx context.Context, ref c.SessionRef, auth c.AuthorizationRef) (c.FlowStatus, error) {
	if err := sessionRefs(string(ref), string(auth)); err != nil {
		return c.FlowStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.ObserveAuthorization(ctx, &p.ObserveAuthorizationRequest{SessionRef: string(ref), AuthorizationRef: string(auth)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.FlowStatus{}, err
	}
	return s.flow(ref, out)
}
func decodeSessionCancel(out *p.CancelOutcome, err error) (c.CancelResult, error) {
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(out) || out.Outcome < 1 || out.Outcome > 2 {
		return 0, errSessionWire
	}
	return c.CancelResult(out.Outcome), nil
}
func (s *SessionClient) CancelAuthorization(ctx context.Context, ref c.SessionRef, auth c.AuthorizationRef) (c.CancelResult, error) {
	if err := sessionRefs(string(ref), string(auth)); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	return decodeSessionCancel(s.rpc.CancelAuthorization(ctx, &p.CancelAuthorizationRequest{SessionRef: string(ref), AuthorizationRef: string(auth)}, grpc.MaxRetryRPCBufferSize(0)))
}
func (s *SessionClient) BeginEnrollment(ctx context.Context, ref c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	if err := sessionRefs(string(ref)); err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.BeginEnrollment(ctx, &p.BeginEnrollmentRequest{SessionRef: string(ref)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.BeginEnrollmentOutcome{}, err
	}
	if !cleanSessionWire(out) {
		return c.BeginEnrollmentOutcome{}, errSessionWire
	}
	var decoded c.BeginEnrollmentOutcome
	switch arm := out.Outcome.(type) {
	case *p.BeginEnrollmentResponse_AlreadyConnected:
		if arm.AlreadyConnected == nil {
			return decoded, errSessionWire
		}
		decoded.Kind = c.EnrollmentAlreadyConnected
	case *p.BeginEnrollmentResponse_Completed:
		cat, err := s.catalogue(ref, arm.Completed)
		if err != nil {
			return decoded, err
		}
		decoded.Kind = c.EnrollmentCompletedKind
		decoded.Catalogue = cat
	case *p.BeginEnrollmentResponse_Started:
		if arm.Started == nil {
			return decoded, errSessionWire
		}
		prompt, err := decodeSessionPrompt(arm.Started.Prompt)
		if err != nil {
			return decoded, err
		}
		decoded.Kind = c.EnrollmentStartedKind
		decoded.Started = &c.EnrollmentStarted{Ref: c.EnrollmentRef(arm.Started.Ref), Prompt: prompt}
	default:
		return decoded, errSessionWire
	}
	if !decoded.Valid() {
		return c.BeginEnrollmentOutcome{}, errSessionWire
	}
	return decoded, nil
}
func (s *SessionClient) ObserveEnrollment(ctx context.Context, ref c.SessionRef, e c.EnrollmentRef) (c.FlowStatus, error) {
	if err := sessionRefs(string(ref), string(e)); err != nil {
		return c.FlowStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.ObserveEnrollment(ctx, &p.ObserveEnrollmentRequest{SessionRef: string(ref), EnrollmentRef: string(e)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.FlowStatus{}, err
	}
	return s.flow(ref, out)
}
func (s *SessionClient) CancelEnrollment(ctx context.Context, ref c.SessionRef, e c.EnrollmentRef) (c.CancelResult, error) {
	if err := sessionRefs(string(ref), string(e)); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	return decodeSessionCancel(s.rpc.CancelEnrollment(ctx, &p.CancelEnrollmentRequest{SessionRef: string(ref), EnrollmentRef: string(e)}, grpc.MaxRetryRPCBufferSize(0)))
}
func (s *SessionClient) DisconnectTools(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef) (c.DisconnectResult, error) {
	if err := sessionRefs(string(ref), string(cat)); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.DisconnectTools(ctx, &p.DisconnectToolsRequest{SessionRef: string(ref), ExpectedCatalogue: string(cat)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(out) || out.Outcome < 1 || out.Outcome > 3 {
		return 0, errSessionWire
	}
	return c.DisconnectResult(out.Outcome), nil
}
func (s *SessionClient) DeleteSession(ctx context.Context, ref c.SessionRef) (c.DeleteResult, error) {
	if err := sessionRefs(string(ref)); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.DeleteSession(ctx, &p.DeleteSessionRequest{SessionRef: string(ref)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(out) || out.Outcome < 1 || out.Outcome > 2 {
		return 0, errSessionWire
	}
	return c.DeleteResult(out.Outcome), nil
}
