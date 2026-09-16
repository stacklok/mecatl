package server

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

// LocalSessionContextServer implements ADR 0296's privileged local-client
// workspace-root projection. Registration is deliberately left to composition,
// which must attest the listener is local-client trusted.
type LocalSessionContextServer struct {
	mecatlv1.UnimplementedLocalSessionContextServiceServer
	svc *Service
}

var _ mecatlv1.LocalSessionContextServiceServer = (*LocalSessionContextServer)(nil)

// NewLocalSessionContextServer constructs the separately registered privileged
// service over svc. It does not register the service on any listener.
func NewLocalSessionContextServer(svc *Service) *LocalSessionContextServer {
	return &LocalSessionContextServer{svc: svc}
}

// GetLocalSessionContext returns only the exact reattached local workspace root.
func (s *LocalSessionContextServer) GetLocalSessionContext(ctx context.Context, req *mecatlv1.GetLocalSessionContextRequest) (*mecatlv1.GetLocalSessionContextResponse, error) {
	if s == nil || s.svc == nil || req.GetSessionId() == "" {
		return nil, localSessionContextStatus(ErrFailedPrecondition)
	}
	root, err := s.svc.localSessionContextRoot(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, localSessionContextStatus(err)
	}
	return &mecatlv1.GetLocalSessionContextResponse{WorkspacePath: root}, nil
}

func (s *Service) localSessionContextRoot(ctx context.Context, id session.SessionID) (string, error) {
	persisted, err := s.GetSession(ctx, id)
	if err != nil {
		return "", err
	}
	if persisted.EnvironmentRef.Kind != session.EnvKindLocal {
		return "", ErrFailedPrecondition
	}
	binding, release, err := s.borrowSessionPlacement(ctx, persisted)
	if err != nil {
		return "", err
	}
	defer release()
	if binding.Ref != persisted.EnvironmentRef || binding.Environment.Ref() != persisted.EnvironmentRef || binding.Environment.Workspace() == nil {
		return "", ErrFailedPrecondition
	}
	root := binding.Environment.Workspace().Root()
	if root == "" {
		return "", ErrFailedPrecondition
	}
	return root, nil
}

func localSessionContextStatus(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, "local session context not found")
	case errors.Is(err, ErrPlacementUnavailable):
		return status.Error(codes.Unavailable, "local session context is temporarily unavailable")
	default:
		return status.Error(codes.FailedPrecondition, "local session context is unavailable for this session")
	}
}
