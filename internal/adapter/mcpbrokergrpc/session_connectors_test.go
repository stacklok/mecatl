package mcpbrokergrpc

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type connectorWireFixture struct {
	p.UnimplementedSessionServiceServer
	out *p.InspectConnectorsResponse
}

func (f *connectorWireFixture) InspectConnectors(context.Context, *p.InspectConnectorsRequest) (*p.InspectConnectorsResponse, error) {
	return f.out, nil
}

type connectorServiceFixture struct {
	c.SessionService
	out c.ConnectorInventory
}

func (f connectorServiceFixture) InspectConnectors(context.Context, c.SessionRef, c.CatalogueRef) (c.ConnectorInventory, error) {
	return f.out, nil
}

func TestSessionConnectorWireValidation(t *testing.T) {
	ref := strings.Repeat("A", 43)
	for name, mutate := range map[string]func(*p.InspectConnectorsResponse){
		"valid":          func(*p.InspectConnectorsResponse) {},
		"availability":   func(r *p.InspectConnectorsResponse) { r.Availability = "healthy" },
		"enrollment":     func(r *p.InspectConnectorsResponse) { r.EnrollmentState = "ready" },
		"state":          func(r *p.InspectConnectorsResponse) { r.Connectors[0].CatalogueState = "empty" },
		"control":        func(r *p.InspectConnectorsResponse) { r.Connectors[0].Name = "bad\n" },
		"format":         func(r *p.InspectConnectorsResponse) { r.Connectors[0].Name = "bad\u202e" },
		"long":           func(r *p.InspectConnectorsResponse) { r.Connectors[0].Name = strings.Repeat("é", 129) },
		"count":          func(r *p.InspectConnectorsResponse) { r.TotalConnectors = 2 },
		"truncated":      func(r *p.InspectConnectorsResponse) { r.Truncated = true },
		"unknown fields": func(r *p.InspectConnectorsResponse) { r.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01}) },
	} {
		t.Run(name, func(t *testing.T) {
			out := &p.InspectConnectorsResponse{Availability: "unavailable", EnrollmentState: "unknown", TotalConnectors: 1, Connectors: []*p.ConnectorStatus{{Name: "public", CatalogueState: "unknown"}}}
			mutate(out)
			server := grpc.NewServer()
			p.RegisterSessionServiceServer(server, &connectorWireFixture{out: out})
			listener := bufconn.Listen(1 << 20)
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()
			defer listener.Close()
			client, err := NewSessionClient("passthrough:///fixture", time.Second, time.Second, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.InspectConnectors(t.Context(), c.SessionRef(ref), c.CatalogueRef(ref))
			if (err == nil) != (name == "valid") {
				t.Fatalf("client: %v", err)
			}
			inventory := c.ConnectorInventory{Availability: c.Availability(out.Availability), EnrollmentState: c.EnrollmentState(out.EnrollmentState), TotalConnectors: out.TotalConnectors, Truncated: out.Truncated}
			for _, row := range out.Connectors {
				inventory.Connectors = append(inventory.Connectors, c.ConnectorStatus{Name: row.Name, CatalogueState: c.CatalogueState(row.CatalogueState), ToolCount: row.ToolCount})
			}
			rpc, _ := NewSessionRPC(connectorServiceFixture{out: inventory})
			_, err = rpc.InspectConnectors(t.Context(), &p.InspectConnectorsRequest{SessionRef: ref, CatalogueRef: ref})
			if name != "unknown fields" && (err == nil) != (name == "valid") {
				t.Fatalf("server: %v", err)
			}
			if _, err = rpc.InspectConnectors(t.Context(), &p.InspectConnectorsRequest{SessionRef: "bad", CatalogueRef: ref}); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("framing: %v", err)
			}
		})
	}
	rows := make([]c.ConnectorStatus, 256)
	for i := range rows {
		rows[i] = c.ConnectorStatus{Name: "public", CatalogueState: c.CatalogueUnknown}
	}
	if !validConnectorInventory(c.ConnectorInventory{Availability: c.AvailabilityUnavailable, EnrollmentState: c.EnrollmentUnknown, Connectors: rows, TotalConnectors: 257, Truncated: true}) {
		t.Fatal("valid truncated inventory rejected")
	}
}
