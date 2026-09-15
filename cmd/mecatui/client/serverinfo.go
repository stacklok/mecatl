package client

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// ServerInfo is the safe, content-free identity and sanitized diagnostic display
// snapshot returned by a server. Its endpoint fields are never connection instructions.
type ServerInfo struct {
	BuildID              string
	ServerImplementation string
	// DisplayServerEndpoint is the sanitized local diagnostic projection of the
	// configured remote connection target. It is not a reconnect target and is
	// empty when no safe URL representation exists.
	DisplayServerEndpoint string
	// LLMProviderDisplayEndpoint is the sanitized server diagnostic projection for
	// the selected provider. It is not connection configuration and is empty when
	// unavailable.
	LLMProviderDisplayEndpoint string
}

// GetServerInfo reads the server build identity and sanitized diagnostic display
// endpoint for the caller's already-known effective provider over the existing
// authenticated connection. The returned endpoints are not connection instructions.
func (c *Client) GetServerInfo(ctx context.Context, providerID string) (ServerInfo, error) {
	resp, err := c.svc.GetServerInfo(ctx, &mecatlv1.GetServerInfoRequest{ProviderId: providerID})
	if err != nil {
		return ServerInfo{}, err
	}
	implementation := strings.TrimSpace(resp.GetServerImplementation())
	if implementation == "" {
		implementation = string(SessionKindUnknown)
	}
	return ServerInfo{
		BuildID:                    strings.TrimSpace(resp.GetBuildId()),
		ServerImplementation:       implementation,
		DisplayServerEndpoint:      sanitizeDiagnosticEndpoint(c.displayServerEndpoint),
		LLMProviderDisplayEndpoint: sanitizeDiagnosticEndpoint(resp.GetLlmProviderDisplayEndpoint()),
	}, nil
}

// SafeInfoFailure maps remote failures to the only values suitable for a bug report.
func SafeInfoFailure(err error) string {
	if status.Code(err) == codes.Unimplemented {
		return "not-supported"
	}
	if status.Code(err) == codes.Unavailable {
		return "unreachable"
	}
	return "invalid-response"
}
