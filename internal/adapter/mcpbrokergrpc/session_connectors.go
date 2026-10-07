package mcpbrokergrpc

import (
	"context"
	"unicode"
	"unicode/utf8"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validConnectorInventory(in c.ConnectorInventory) bool {
	if in.Availability != c.AvailabilityAvailable && in.Availability != c.AvailabilityUnavailable {
		return false
	}
	switch in.EnrollmentState {
	case c.EnrollmentNotRequired, c.EnrollmentNotStarted, c.EnrollmentPending, c.EnrollmentCompleted, c.EnrollmentUnknown:
	default:
		return false
	}
	if len(in.Connectors) != int(min(in.TotalConnectors, 256)) || in.Truncated != (in.TotalConnectors > 256) {
		return false
	}
	for _, row := range in.Connectors {
		if !utf8.ValidString(row.Name) || utf8.RuneCountInString(row.Name) > 128 {
			return false
		}
		for _, r := range row.Name {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return false
			}
		}
		switch row.CatalogueState {
		case c.CatalogueHidden, c.CatalogueDeclared, c.CatalogueDiscovered, c.CatalogueUnknown:
		default:
			return false
		}
	}
	return true
}

func (s *SessionRPC) InspectConnectors(ctx context.Context, req *p.InspectConnectorsRequest) (*p.InspectConnectorsResponse, error) {
	if !cleanSessionWire(req) || !sessionRef(req.GetSessionRef()) || !sessionRef(req.GetCatalogueRef()) {
		return nil, status.Error(codes.InvalidArgument, "invalid session references")
	}
	in, err := s.service.InspectConnectors(ctx, c.SessionRef(req.SessionRef), c.CatalogueRef(req.CatalogueRef))
	if err != nil {
		return nil, sessionError(err)
	}
	if !validConnectorInventory(in) {
		return nil, status.Error(codes.Internal, "invalid connector inventory")
	}
	out := &p.InspectConnectorsResponse{Availability: string(in.Availability), EnrollmentState: string(in.EnrollmentState), TotalConnectors: in.TotalConnectors, Truncated: in.Truncated}
	for _, row := range in.Connectors {
		out.Connectors = append(out.Connectors, &p.ConnectorStatus{Name: row.Name, CatalogueState: string(row.CatalogueState), ToolCount: row.ToolCount})
	}
	return out, nil
}

func (s *SessionClient) InspectConnectors(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef) (c.ConnectorInventory, error) {
	if err := sessionRefs(string(ref), string(cat)); err != nil {
		return c.ConnectorInventory{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	wire, err := s.rpc.InspectConnectors(ctx, &p.InspectConnectorsRequest{SessionRef: string(ref), CatalogueRef: string(cat)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.ConnectorInventory{}, err
	}
	if !cleanSessionWire(wire) {
		return c.ConnectorInventory{}, errSessionWire
	}
	out := c.ConnectorInventory{Availability: c.Availability(wire.Availability), EnrollmentState: c.EnrollmentState(wire.EnrollmentState), TotalConnectors: wire.TotalConnectors, Truncated: wire.Truncated}
	for _, row := range wire.Connectors {
		if row == nil {
			return c.ConnectorInventory{}, errSessionWire
		}
		out.Connectors = append(out.Connectors, c.ConnectorStatus{Name: row.Name, CatalogueState: c.CatalogueState(row.CatalogueState), ToolCount: row.ToolCount})
	}
	if !validConnectorInventory(out) {
		return c.ConnectorInventory{}, errSessionWire
	}
	return out, nil
}
