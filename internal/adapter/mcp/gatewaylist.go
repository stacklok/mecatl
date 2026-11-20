package mcp

import (
	"context"
	"errors"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// ErrGatewayList reports a failed gateway tools/list. It never carries the
// gateway's answer, which could echo the token.
var ErrGatewayList = errors.New("mcp: gateway tools/list failed")

// ListGatewayTools lists every tool the gateway at endpoint advertises, every
// page, over a session of its own. token is only ever set in the
// Authorization header; client's Transport and Timeout are used, and
// redirects are never followed. An error is ErrGatewayList or ctx's error.
func ListGatewayTools(ctx context.Context, endpoint string, client *http.Client, token identitybroker.Token) ([]*mcpsdk.Tool, error) {
	if endpoint == "" || client == nil || token.IsZero() {
		return nil, ErrGatewayList
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{
			Transport:     bearerTransport{base: base, authorization: "Bearer " + token.Value()},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       client.Timeout,
		},
		DisableStandaloneSSE: true,
	}
	sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: clientName, Version: clientVersion}, nil)
	cs, err := sdkClient.Connect(ctx, transport, &mcpsdk.ClientSessionOptions{ProtocolVersion: gatewayProtocolVersion})
	if err != nil {
		return nil, listError(ctx)
	}
	defer func() { _ = cs.Close() }()
	var tools []*mcpsdk.Tool
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil || tool == nil {
			return nil, listError(ctx)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func listError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrGatewayList
}

// bearerTransport sets the Authorization header of every request.
type bearerTransport struct {
	base          http.RoundTripper
	authorization string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", t.authorization)
	return t.base.RoundTrip(out)
}
