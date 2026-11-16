package mcpbroker

import (
	"context"
	"fmt"
	"net/http"

	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// defaultSendOnceLimit bounds the in-memory send-once record per root
// session.
const defaultSendOnceLimit = 4096

// Gateway is an MCP gateway reached only over HTTP. A tools/call on a route it
// serves is authorized by the identity broker and sent with a per-call token.
// Shortcut: tool listing for gateway routes still uses the MCP broker's OAuth login (workspace enrollment), a second sign-in after #2533; removed by #2729 (listing with a broker-issued token through Gateway).
type Gateway interface {
	// Endpoint is the gateway's MCP endpoint URL.
	Endpoint() string
	// HTTPClient carries every request of a call.
	HTTPClient() *http.Client
	// Route returns the gateway backend and the tool name the gateway routes
	// for the model-visible tool name on backend.
	Route(backend, modelVisibleName string) (server, tool string, err error)
}

// embeddedGateway is the bundled ToolHive process's vMCP, reached in process.
// Shortcut: only the embedded gateway implements Gateway; removed by #2812 (external gateway implementation of the Gateway interface).
type embeddedGateway struct {
	endpoint string
	client   *http.Client
}

func (g embeddedGateway) Endpoint() string         { return g.endpoint }
func (g embeddedGateway) HTTPClient() *http.Client { return g.client }

// Route returns backend and the name the vMCP advertises the tool under.
func (embeddedGateway) Route(backend, modelVisibleName string) (string, string, error) {
	tool, err := toolHiveAdvertisedToolName(backend, modelVisibleName)
	return backend, tool, err
}

// CallAuthorizer authorizes one gateway call; identitybroker.Service
// implements it.
type CallAuthorizer interface {
	AuthorizeCall(context.Context, identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error)
}

// AgentCredentials looks up the acting agent's credential; *agentcredential.Holder
// implements it. renew asks for a renewed credential after svid_expired.
type AgentCredentials interface {
	Credential(ctx context.Context, key agentcredential.Key, renew bool) (agentcredential.Credential, error)
}

// GatewayCalls is what the gateway path needs besides the gateway.
type GatewayCalls struct {
	Authorizer  CallAuthorizer
	Credentials AgentCredentials
	// Servers are the backends whose calls take the gateway path.
	Servers []string
	// SendOnce records claimed calls; nil means NewMemorySendOnceRecord.
	SendOnce SendOnceRecord
}

// WithGatewayCalls sends calls on calls.Servers' broker routes to g.
func WithGatewayCalls(g Gateway, calls GatewayCalls) Option {
	return func(r *Runtime) { r.setGateway(g, calls) }
}

func (r *Runtime) setGateway(g Gateway, calls GatewayCalls) {
	if calls.SendOnce == nil {
		calls.SendOnce = NewMemorySendOnceRecord(defaultSendOnceLimit)
	}
	servers := make(map[string]bool, len(calls.Servers))
	for _, server := range calls.Servers {
		servers[server] = true
	}
	r.gateway, r.gatewayCalls, r.gatewayServers = g, calls, servers
	if r.sendGateway == nil {
		r.sendGateway = mcpadapter.SendGatewayCall
	}
}

// checkGateway refuses an installed gateway that cannot authorize or send.
func (r *Runtime) checkGateway() error {
	if r.gateway == nil {
		return nil
	}
	if r.gatewayCalls.Authorizer == nil || r.gatewayCalls.Credentials == nil || r.gateway.Endpoint() == "" || r.gateway.HTTPClient() == nil {
		return fmt.Errorf("%w: gateway calls need an authorizer, agent credentials, an endpoint, and an HTTP client", ErrInvalidCatalogue)
	}
	return nil
}

// gatewayRoutes reports whether calls on backend take the gateway path.
func (r *Runtime) gatewayRoutes(backend string) bool { return r.gatewayServers[backend] }

// gatewayRoute reports whether t's calls are sent through the gateway.
func (t *sessionTool) gatewayRoute() bool {
	r := t.attachment.runtime
	return r.gateway != nil && t.route.broker && r.gatewayRoutes(t.route.backend)
}
