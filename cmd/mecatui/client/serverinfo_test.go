package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakeServerInfoClient struct {
	mecatlv1.HarnessServiceClient
	resp *mecatlv1.GetServerInfoResponse
	err  error
	req  *mecatlv1.GetServerInfoRequest
}

func (f *fakeServerInfoClient) GetServerInfo(_ context.Context, req *mecatlv1.GetServerInfoRequest, _ ...grpc.CallOption) (*mecatlv1.GetServerInfoResponse, error) {
	f.req = req
	return f.resp, f.err
}

func TestGetServerInfoMapsImplementationWithOlderServerFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp *mecatlv1.GetServerInfoResponse
		want string
	}{
		{name: "current", resp: &mecatlv1.GetServerInfoResponse{BuildId: "build", ServerImplementation: "future-server"}, want: "future-server"},
		{name: "older server", resp: &mecatlv1.GetServerInfoResponse{BuildId: "build"}, want: "unknown"},
		{name: "whitespace only", resp: &mecatlv1.GetServerInfoResponse{BuildId: "build", ServerImplementation: " \t"}, want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := (&Client{svc: &fakeServerInfoClient{resp: tc.resp}}).GetServerInfo(context.Background(), "openrouter")
			if err != nil {
				t.Fatalf("GetServerInfo: %v", err)
			}
			if info.ServerImplementation != tc.want {
				t.Errorf("ServerImplementation = %q, want %q", info.ServerImplementation, tc.want)
			}
		})
	}
}

func TestGetServerInfoSendsActiveProviderSelector(t *testing.T) {
	remote := &fakeServerInfoClient{resp: &mecatlv1.GetServerInfoResponse{}}
	if _, err := (&Client{svc: remote}).GetServerInfo(context.Background(), "openrouter"); err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	if got := remote.req.GetProviderId(); got != "openrouter" {
		t.Fatalf("provider_id = %q, want active provider", got)
	}
}

func TestGetServerInfoSanitizesBothEndpointBoundaries(t *testing.T) {
	info, err := (&Client{
		displayServerEndpoint: "https://user:secret@server.example:8443/a/../rpc?token=secret#fragment",
		svc: &fakeServerInfoClient{resp: &mecatlv1.GetServerInfoResponse{
			LlmProviderDisplayEndpoint: "HTTPS://user:secret@provider.example:9443/a/../v%201?token=secret#fragment",
		}},
	}).GetServerInfo(context.Background(), "openrouter")
	if err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	if info.DisplayServerEndpoint != "https://server.example:8443/rpc" {
		t.Errorf("display server endpoint = %q", info.DisplayServerEndpoint)
	}
	if info.LLMProviderDisplayEndpoint != "https://provider.example:9443/v%201" {
		t.Errorf("LLM provider display endpoint = %q", info.LLMProviderDisplayEndpoint)
	}
}

func TestDiagnosticEndpointRejectsUnsafeInput(t *testing.T) {
	if got := displayServerEndpoint(DialConfig{Server: "server.example:8443", UseTLS: true}); got != "https://server.example:8443/" {
		t.Fatalf("display server endpoint = %q", got)
	}
	for _, raw := range []string{"https://provider.example/v1\nsecret", "not a URL", "https://provider.example/" + strings.Repeat("x", maxDiagnosticEndpointBytes)} {
		if got := sanitizeDiagnosticEndpoint(raw); got != "" {
			t.Errorf("sanitizeDiagnosticEndpoint(%q) = %q, want unavailable", raw, got)
		}
	}
}

func TestSafeInfoFailureNeverReturnsServerText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{status.Error(codes.Unimplemented, "https://host/token"), "not-supported"},
		{status.Error(codes.Unavailable, "TLS credentials rejected"), "unreachable"},
		{errors.New("authorization: Bearer secret"), "invalid-response"},
	}
	for _, tc := range cases {
		if got := SafeInfoFailure(tc.err); got != tc.want {
			t.Errorf("SafeInfoFailure(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
