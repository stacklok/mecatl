package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
)

func brokerConnectorWireProjection(t *testing.T) {
	profiles := make([]mcpbroker.ToolHiveProfile, 257)
	for i := range profiles {
		profiles[i] = mcpbroker.ToolHiveProfile{Name: fmt.Sprintf("connector-%03d", i), URL: "https://private.example/SECRET_ENDPOINT", Auth: "oauth", OAuth: &mcpbroker.ToolHiveOAuth{
			ClientID: "SECRET_CLIENT", Scopes: []string{"SECRET_SCOPE"}, AuthorizationEndpoint: "https://private.example/SECRET_AUTHORIZE", TokenEndpoint: "https://private.example/SECRET_TOKEN",
		}, Static: []mcpbroker.StaticTool{{Name: "SECRET_TOOL", Description: "SECRET_DESCRIPTION", Schema: json.RawMessage(`{"type":"object","description":"SECRET_SCHEMA"}`)}}}
	}
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), mcpbroker.ToolHiveConfig{CallbackURL: "https://private.example/SECRET_CALLBACK", Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	svc, _ := connectorService(t, connectorOwner())
	svc.cfg.MCPConnectorInspector = process.Runtime
	// No matching broker incarnation: configured names remain visible, but no
	// stale success or private binding may be projected from the stored session.
	ctx := session.WithPrincipal(t.Context(), connectorOwner())
	out, err := NewHarnessServer(svc).ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: "connector-session"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Availability != "unavailable" || out.EnrollmentState != "unknown" || out.TotalConnectors != 257 || !out.Truncated || len(out.Connectors) != 256 {
		t.Fatalf("bounds/unavailable: %+v", out)
	}
	for i, row := range out.Connectors {
		if row.Name != profiles[i].Name || row.CatalogueState != "unknown" || row.ToolCount != 0 {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	wire, err := proto.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/sessions/connector-session/mcp/connectors", nil))
	if rec.Code != 200 {
		t.Fatalf("HTTP %d", rec.Code)
	}
	for _, value := range []string{string(wire), rec.Body.String()} {
		for _, forbidden := range []string{"SECRET_", "private.example", "private-binding", "/private"} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("private field leaked: %s", forbidden)
			}
		}
	}
	var httpOut mecatlv1.ListSessionMcpConnectorsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &httpOut); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(out, &httpOut) {
		t.Fatal("wire projections differ")
	}
	// The mechanical proto boundary still repairs a custom producer's bytes.
	inventory, err := svc.ListSessionMcpConnectors(ctx, "connector-session")
	if err != nil {
		t.Fatal(err)
	}
	inventory.Connectors[0].Name = "bad\xffname"
	repaired := toProtoConnectorInventory(inventory)
	if !utf8.ValidString(repaired.Connectors[0].Name) {
		t.Fatal("invalid UTF-8 reached protobuf")
	}
	if _, err := proto.Marshal(repaired); err != nil {
		t.Fatal(err)
	}
}
