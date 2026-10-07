package mcpbrokergrpc

import (
	"context"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func attemptFromWire(a *p.Attempt) c.BrokerAttempt {
	return c.BrokerAttempt{Slot: a.GetSlot(), Sequence: a.GetSequence()}
}
func attemptToWire(a c.BrokerAttempt) *p.Attempt {
	return &p.Attempt{Slot: a.Slot, Sequence: a.Sequence}
}
func wireAttemptValid(a *p.Attempt) bool { return cleanSessionWire(a) && attemptFromWire(a).Valid() }
func encodeAttemptStatus(out c.AttemptStatus, expected c.BrokerAttempt, err error) (*p.AttemptStatus, error) {
	if err != nil {
		return nil, sessionError(err)
	}
	if out.Attempt != expected || !out.Valid() {
		return nil, status.Error(codes.Internal, "invalid attempt status")
	}
	wire := &p.AttemptStatus{Attempt: attemptToWire(out.Attempt), Phase: out.Phase, Disposition: string(out.Disposition)}
	if out.Outcome != nil {
		wire.Outcome, err = sessionInvocation(*out.Outcome, nil)
	}
	return wire, err
}
func decodeAttemptStatus(out *p.AttemptStatus, expected c.BrokerAttempt) (c.AttemptStatus, error) {
	if !cleanSessionWire(out) || !wireAttemptValid(out.Attempt) || attemptFromWire(out.Attempt) != expected {
		return c.AttemptStatus{}, errSessionWire
	}
	decoded := c.AttemptStatus{Attempt: expected, Phase: out.Phase, Disposition: session.BrokerAttemptDisposition(out.Disposition)}
	if out.Outcome != nil {
		outcome, err := decodeSessionInvocation(out.Outcome, "")
		if err != nil {
			return c.AttemptStatus{}, err
		}
		decoded.Outcome = &outcome
	}
	if !decoded.Valid() {
		return c.AttemptStatus{}, errSessionWire
	}
	return decoded, nil
}
func (s *SessionRPC) InspectAttempt(ctx context.Context, r *p.InspectAttemptRequest) (*p.AttemptStatus, error) {
	if r == nil || !sessionRef(r.SessionRef) || !wireAttemptValid(r.Attempt) {
		return nil, invalid("invalid attempt reference")
	}
	a := attemptFromWire(r.Attempt)
	out, err := s.service.InspectAttempt(ctx, c.SessionRef(r.SessionRef), a)
	return encodeAttemptStatus(out, a, err)
}
func (s *SessionRPC) AcknowledgeAttempt(ctx context.Context, r *p.AcknowledgeAttemptRequest) (*p.AttemptStatus, error) {
	if r == nil || !sessionRef(r.SessionRef) || !wireAttemptValid(r.Attempt) {
		return nil, invalid("invalid attempt reference")
	}
	a := attemptFromWire(r.Attempt)
	out, err := s.service.AcknowledgeAttempt(ctx, c.SessionRef(r.SessionRef), a)
	return encodeAttemptStatus(out, a, err)
}
func (s *SessionClient) InspectAttempt(ctx context.Context, ref c.SessionRef, a c.BrokerAttempt) (c.AttemptStatus, error) {
	if !sessionRef(string(ref)) || !a.Valid() {
		return c.AttemptStatus{}, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.InspectAttempt(ctx, &p.InspectAttemptRequest{SessionRef: string(ref), Attempt: attemptToWire(a)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.AttemptStatus{}, err
	}
	return decodeAttemptStatus(out, a)
}
func (s *SessionClient) AcknowledgeAttempt(ctx context.Context, ref c.SessionRef, a c.BrokerAttempt) (c.AttemptStatus, error) {
	if !sessionRef(string(ref)) || !a.Valid() {
		return c.AttemptStatus{}, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	out, err := s.rpc.AcknowledgeAttempt(ctx, &p.AcknowledgeAttemptRequest{SessionRef: string(ref), Attempt: attemptToWire(a)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.AttemptStatus{}, err
	}
	return decodeAttemptStatus(out, a)
}
