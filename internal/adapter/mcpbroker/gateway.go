package mcpbroker

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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
	// Endpoint is the gateway's MCP endpoint URL, where requests are sent.
	Endpoint() string
	// Resource is the vMCP's OAuth protected-resource identifier, the
	// audience AS-2 issues the gateway token for. It is not Endpoint: a
	// gateway may be dialled at an address other than its resource.
	Resource() string
	// HTTPClient carries every request of a call.
	HTTPClient() *http.Client
	// Route returns the gateway server and the tool name the gateway routes
	// for the model-visible tool name on backend. The server must be backend,
	// the name operators and agent definitions use, and the tool must be
	// "<server>.<tool>", with the tool's name on the server: the identity
	// broker checks both against the acting agent's tool limit and refuses
	// any other form.
	Route(backend, modelVisibleName string) (server, tool string, err error)
	// ListTools lists the tools the gateway routes to server, each under its
	// name on that server, sent with token as the bearer. token is a value,
	// not a source, so the bearer sent is the one the caller holds and checks
	// listed tools for. The caller checks the token before passing it. The
	// gateway attributes each tool to its server. An error never carries the
	// token.
	ListTools(ctx context.Context, server string, token identitybroker.Token) ([]GatewayTool, error)
}

// GatewayTool is one tool a gateway lists for a server.
type GatewayTool struct {
	// Name is the tool's name on its server, without the gateway's routing.
	Name        string
	Description string
	InputSchema map[string]any
	// OutputSchema is the schema the tool's structured result declares; nil
	// when the tool declares none. It is not checked for the bearer, because
	// nothing uses it yet: whatever carries it into the catalogue must check
	// it as the input schema is checked.
	OutputSchema map[string]any
	ReadOnly     bool
}

// embeddedGateway is the bundled ToolHive process's vMCP, reached in process.
// Shortcut: only the embedded gateway implements Gateway; removed by #2812 (external gateway implementation of the Gateway interface).
type embeddedGateway struct {
	endpoint   string
	resource   string
	client     *http.Client
	listLimits mcpadapter.GatewayListLimits
}

func (g embeddedGateway) Endpoint() string         { return g.endpoint }
func (g embeddedGateway) Resource() string         { return g.resource }
func (g embeddedGateway) HTTPClient() *http.Client { return g.client }

// Route returns backend and the name the vMCP advertises the tool under.
func (embeddedGateway) Route(backend, modelVisibleName string) (string, string, error) {
	tool, err := toolHiveAdvertisedToolName(backend, modelVisibleName)
	return backend, tool, err
}

// ListTools lists the vMCP's tools routed to server.
func (g embeddedGateway) ListTools(ctx context.Context, server string, token identitybroker.Token) ([]GatewayTool, error) {
	if server == "" || token.IsZero() {
		return nil, mcpadapter.ErrGatewayList
	}
	listed, err := mcpadapter.ListGatewayTools(ctx, g.endpoint, g.client, token, g.listLimits)
	if err != nil {
		return nil, err
	}
	// Shortcut: the embedded gateway filters the vMCP's aggregate listing by its routed-name prefix; removed by #2729 (a listing V the vMCP answers with only that server's tools, toolhive#7062).
	prefix := server + "."
	var tools []GatewayTool
	for _, t := range listed {
		name, ok := strings.CutPrefix(t.Name, prefix)
		if !ok {
			continue
		}
		// The SDK decodes a listed schema as a JSON object; any other value is
		// left nil for the catalogue to refuse.
		schema, _ := t.InputSchema.(map[string]any)
		outputSchema, _ := t.OutputSchema.(map[string]any)
		tools = append(tools, GatewayTool{Name: name, Description: t.Description, InputSchema: schema,
			OutputSchema: outputSchema, ReadOnly: t.Annotations != nil && t.Annotations.ReadOnlyHint})
	}
	return tools, nil
}

// CallAuthorizer authorizes one gateway call; identitybroker.Service
// implements it.
type CallAuthorizer interface {
	AuthorizeCall(context.Context, identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error)
}

// AgentCredentials looks up the acting agent's credential and, for a helper,
// its current delegation; *agentcredential.Holder implements it. renew asks
// for a renewed credential after svid_expired.
type AgentCredentials interface {
	Credential(ctx context.Context, key agentcredential.Key, renew bool) (agentcredential.Credential, error)
	Delegation(key agentcredential.Key) (agentcredential.Delegation, bool)
}

// GatewayCalls is what the gateway path needs besides the gateway.
type GatewayCalls struct {
	Authorizer  CallAuthorizer
	Credentials AgentCredentials
	// Servers are the backends whose calls take the gateway path, by backend
	// name; the embedded gateway routes each backend under its own name.
	Servers []string
	// SendOnce records claimed calls; nil means NewMemorySendOnceRecord.
	SendOnce SendOnceRecord
	// ListLimits bound each listing of a server's tools through the gateway;
	// a zero field takes the mcp package's default. Production leaves it zero:
	// it lets tests lower a limit until operator gateway entries (#2538) need
	// their own.
	ListLimits mcpadapter.GatewayListLimits
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
	if r.gatewayCalls.Authorizer == nil || r.gatewayCalls.Credentials == nil || r.gateway.Endpoint() == "" || r.gateway.Resource() == "" || r.gateway.HTTPClient() == nil {
		return fmt.Errorf("%w: gateway calls need an authorizer, agent credentials, an endpoint, a resource, and an HTTP client", ErrInvalidCatalogue)
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
