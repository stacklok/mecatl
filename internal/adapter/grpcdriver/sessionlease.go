package grpcdriver

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// SessionLease is a port.SessionLease over a remote SessionLeaseService driver.
// Unlike the SessionStore/EventLog clients, the lease carries no opaque payload:
// its fields ARE the protocol, so this wrapper just marshals the Lease value
// to/from proto (expiry via timestamppb) and maps the status codes onto the port
// sentinels — FAILED_PRECONDITION → ErrLeaseHeld, UNIMPLEMENTED →
// ErrLeaseUnsupported (the sticky-disable signal).
type SessionLease struct {
	client driverv1.SessionLeaseServiceClient
}

// compile-time assertion that SessionLease satisfies the port.
var _ port.SessionLease = (*SessionLease)(nil)

// NewSessionLease wraps an established driver connection (see Dial) as a
// port.SessionLease.
func NewSessionLease(conn grpc.ClientConnInterface) *SessionLease {
	return &SessionLease{client: driverv1.NewSessionLeaseServiceClient(conn)}
}

// Acquire requests the lease for id from the driver. FAILED_PRECONDITION →
// ErrLeaseHeld, UNIMPLEMENTED → ErrLeaseUnsupported; any other non-OK status is
// an opaque infrastructure failure.
func (l *SessionLease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	resp, err := l.client.Acquire(ctx, &driverv1.AcquireRequest{
		SessionId: string(id),
		Owner:     owner,
	})
	if err != nil {
		return port.Lease{}, leaseStatusToErr(ctx, "acquire lease", err)
	}
	return leaseFromProto(resp.GetLease()), nil
}

// Renew extends the held lease, returning the refreshed value (new expiry, same
// token). A FAILED_PRECONDITION means the caller lost the lease → ErrLeaseHeld
// (the loss signal the renewer acts on).
func (l *SessionLease) Renew(ctx context.Context, in port.Lease) (port.Lease, error) {
	resp, err := l.client.Renew(ctx, &driverv1.RenewRequest{Lease: leaseToProto(in)})
	if err != nil {
		return port.Lease{}, leaseStatusToErr(ctx, "renew lease", err)
	}
	return leaseFromProto(resp.GetLease()), nil
}

// Release relinquishes the held lease; idempotent on the driver side.
func (l *SessionLease) Release(ctx context.Context, in port.Lease) error {
	if _, err := l.client.Release(ctx, &driverv1.ReleaseRequest{Lease: leaseToProto(in)}); err != nil {
		return leaseStatusToErr(ctx, "release lease", err)
	}
	return nil
}

// leaseToProto marshals a port.Lease to the wire form.
func leaseToProto(l port.Lease) *driverv1.Lease {
	return &driverv1.Lease{
		SessionId: string(l.SessionID),
		Owner:     l.Owner,
		Token:     l.Token,
		Expiry:    timestamppb.New(l.Expiry),
	}
}

// leaseFromProto unmarshals the wire form back into a port.Lease.
func leaseFromProto(p *driverv1.Lease) port.Lease {
	out := port.Lease{
		SessionID: session.SessionID(p.GetSessionId()),
		Owner:     p.GetOwner(),
		Token:     p.GetToken(),
	}
	if ts := p.GetExpiry(); ts != nil {
		out.Expiry = ts.AsTime()
	}
	return out
}

// leaseStatusToErr maps an RPC status onto the port.SessionLease sentinels.
// FAILED_PRECONDITION → ErrLeaseHeld (held by a live owner, or lost on Renew);
// UNIMPLEMENTED → ErrLeaseUnsupported (sticky-disable). A ctx-done caller wraps
// ctx.Err() (like rpcErr) so errors.Is(_, context.Canceled/DeadlineExceeded)
// holds harness-side; everything else is opaque infrastructure failure.
func leaseStatusToErr(ctx context.Context, op string, err error) error {
	switch status.Code(err) {
	case codes.FailedPrecondition:
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, port.ErrLeaseHeld, err)
	case codes.Unimplemented:
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, port.ErrLeaseUnsupported, err)
	default:
		return rpcErr(ctx, op, err)
	}
}

// sessionLeaseServer adapts a port.SessionLease to SessionLeaseServiceServer. It
// confines all proto/status translation; the wrapped backend speaks only the
// port. ErrLeaseHeld → FAILED_PRECONDITION, ErrLeaseUnsupported → UNIMPLEMENTED
// (via leaseStatus), blanks → INVALID_ARGUMENT.
type sessionLeaseServer struct {
	driverv1.UnimplementedSessionLeaseServiceServer
	lease port.SessionLease
}

// NewSessionLeaseServer wraps lease as a SessionLeaseService driver server.
func NewSessionLeaseServer(lease port.SessionLease) driverv1.SessionLeaseServiceServer {
	return &sessionLeaseServer{lease: lease}
}

// Acquire validates the request (blank session_id/owner → INVALID_ARGUMENT) and
// grants the lease, mapping ErrLeaseHeld/ErrLeaseUnsupported via leaseStatus.
func (s *sessionLeaseServer) Acquire(ctx context.Context, req *driverv1.AcquireRequest) (*driverv1.LeaseResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetOwner() == "" {
		return nil, status.Error(codes.InvalidArgument, "owner is required")
	}
	l, err := s.lease.Acquire(ctx, session.SessionID(req.GetSessionId()), req.GetOwner())
	if err != nil {
		return nil, leaseStatus(err)
	}
	return &driverv1.LeaseResponse{Lease: leaseToProto(l)}, nil
}

// Renew validates the carried lease (a present, non-blank lease) and refreshes
// it, mapping the loss signal (ErrLeaseHeld) via leaseStatus.
func (s *sessionLeaseServer) Renew(ctx context.Context, req *driverv1.RenewRequest) (*driverv1.LeaseResponse, error) {
	in, err := validateLease(req.GetLease())
	if err != nil {
		return nil, err
	}
	l, rerr := s.lease.Renew(ctx, in)
	if rerr != nil {
		return nil, leaseStatus(rerr)
	}
	return &driverv1.LeaseResponse{Lease: leaseToProto(l)}, nil
}

// Release validates the carried lease and relinquishes it (idempotent).
func (s *sessionLeaseServer) Release(ctx context.Context, req *driverv1.ReleaseRequest) (*driverv1.ReleaseResponse, error) {
	in, err := validateLease(req.GetLease())
	if err != nil {
		return nil, err
	}
	if rerr := s.lease.Release(ctx, in); rerr != nil {
		return nil, leaseStatus(rerr)
	}
	return &driverv1.ReleaseResponse{}, nil
}

// validateLease rejects a missing or blank-keyed lease (INVALID_ARGUMENT) and
// returns the decoded port.Lease.
func validateLease(p *driverv1.Lease) (port.Lease, error) {
	if p == nil {
		return port.Lease{}, status.Error(codes.InvalidArgument, "lease is required")
	}
	if p.GetSessionId() == "" {
		return port.Lease{}, status.Error(codes.InvalidArgument, "lease.session_id is required")
	}
	if p.GetOwner() == "" {
		return port.Lease{}, status.Error(codes.InvalidArgument, "lease.owner is required")
	}
	return leaseFromProto(p), nil
}
