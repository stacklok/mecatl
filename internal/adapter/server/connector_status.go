package server

import (
	"context"
	"errors"
	"fmt"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

var (
	errConnectorUnavailable     = fmt.Errorf("%w: broker connector inspection unavailable", ErrFailedPrecondition)
	errConnectorUnauthenticated = errors.New("verified principal required")
)

// ListSessionMcpConnectors reads broker-local publication under the exact saved
// binding. In particular, it never opens an attachment or observes enrollment.
func (s *Service) ListSessionMcpConnectors(ctx context.Context, id session.SessionID) (brokercontract.ConnectorInventory, error) {
	if !s.cfg.OwnershipEnforced {
		return brokercontract.ConnectorInventory{}, errConnectorUnavailable
	}
	if session.PrincipalFromContext(ctx) == nil {
		return brokercontract.ConnectorInventory{}, errConnectorUnauthenticated
	}
	if !sessionaffinity.ValidValue(string(id)) {
		return brokercontract.ConnectorInventory{}, ErrInvalidArgument
	}
	sess, err := s.cfg.Store.Load(ctx, id)
	if errors.Is(err, port.ErrSessionNotFound) {
		return brokercontract.ConnectorInventory{}, ErrNotFound
	}
	if err != nil {
		return brokercontract.ConnectorInventory{}, ErrInternal
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return brokercontract.ConnectorInventory{}, ErrNotFound
	}
	if s.cfg.MCPConnectorInspector == nil || sess.ExternalBinding == "" {
		return brokercontract.ConnectorInventory{}, errConnectorUnavailable
	}
	unlockBroker := s.brokerMu.lock(id)
	defer unlockBroker()
	result, err := s.cfg.MCPConnectorInspector.InspectConnectors(ctx, id, sess.ExternalBinding)
	if err != nil {
		return brokercontract.ConnectorInventory{}, ErrInternal
	}
	return result, nil
}

// ListSessionMcpConnectors implements the owner-scoped broker inventory RPC.
func (h *HarnessServer) ListSessionMcpConnectors(ctx context.Context, req *mecatlv1.ListSessionMcpConnectorsRequest) (*mecatlv1.ListSessionMcpConnectorsResponse, error) {
	if err := validateGRPCSessionAffinity(ctx, req.GetSessionId()); err != nil {
		return nil, err
	}
	result, err := h.svc.ListSessionMcpConnectors(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoConnectorInventory(result), nil
}

func toProtoConnectorInventory(in brokercontract.ConnectorInventory) *mecatlv1.ListSessionMcpConnectorsResponse {
	out := &mecatlv1.ListSessionMcpConnectorsResponse{Availability: valid(in.Availability), EnrollmentState: valid(in.EnrollmentState), TotalConnectors: in.TotalConnectors, Truncated: in.Truncated, Connectors: make([]*mecatlv1.McpConnectorStatus, len(in.Connectors))}
	for i, row := range in.Connectors {
		out.Connectors[i] = &mecatlv1.McpConnectorStatus{Name: valid(row.Name), CatalogueState: valid(row.CatalogueState), ToolCount: row.ToolCount}
	}
	return out
}

// Request-dependent capability truth stays separate from context-free engine facts.
func (s *Service) capabilitiesFor(ctx context.Context) *mecatlv1.ServerCapabilities {
	out := s.capabilities()
	out.McpConnectorStatus = s.cfg.MCPConnectorInspector != nil && s.cfg.OwnershipEnforced && session.PrincipalFromContext(ctx) != nil
	return out
}
