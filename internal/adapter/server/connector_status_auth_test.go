package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestBrokerConnectorTransportAuthentication(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()
	if response, err := client.ListSessionMcpConnectors(t.Context(), &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: "private"}); status.Code(err) != codes.Unauthenticated || response != nil {
		t.Fatalf("unauthenticated inspection: %v %v", response, err)
	}
	if response, err := client.GetCompatibilityInfo(t.Context(), &mecatlv1.GetCompatibilityInfoRequest{}); status.Code(err) != codes.Unauthenticated || response != nil {
		t.Fatalf("unauthenticated capability: %v %v", response, err)
	}
	h := auth.Middleware(server.NewHTTPHandler(svc))
	for _, path := range []string{"/v1/sessions/private/mcp/connectors", "/v1/compatibility"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
