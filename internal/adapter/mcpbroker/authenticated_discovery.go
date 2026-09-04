package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	toolhiveauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"golang.org/x/oauth2"
)

// ErrAuthenticatedDiscovery reports a provider-scoped capability discovery
// failure without exposing a provider key, broker credential, or upstream state.
var ErrAuthenticatedDiscovery = errors.New("mcpbroker: authenticated capability discovery failed")

// AuthenticatedCapabilities is the neutral, copied result of discovering one
// protected backend. It deliberately contains no ToolHive capability value or
// authentication material.
type AuthenticatedCapabilities struct {
	Backend string
	Tools   []ToolDefinition
}

// capabilityQuerier intentionally exposes no aggregate query operation. The
// ToolHive aggregate operation is fail-soft, so catalogue admission proves each
// configured protected backend independently.
type capabilityQuerier interface {
	QueryCapabilities(context.Context, vmcp.Backend) (*aggregator.BackendCapabilities, error)
}

type backendLookup interface {
	Get(context.Context, string) *vmcp.Backend
}

type authenticatedDiscovery struct {
	capabilities capabilityQuerier
	backends     backendLookup
	incoming     func(http.Handler) http.Handler
}

type privateResponseWriter struct {
	header http.Header
	status int
}

func (w *privateResponseWriter) Header() http.Header { return w.header }

func (w *privateResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(body), nil
}

func (w *privateResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// QueryAuthenticatedCapabilities sends the opaque outer broker credential
// through ToolHive's incoming identity middleware before making one
// backend-scoped capability query. Mecatl never reads ToolHive's token-session
// claim or retrieves an upstream provider credential.
func (p *Process) QueryAuthenticatedCapabilities(ctx context.Context, credential oauth2.TokenSource, backend string) (AuthenticatedCapabilities, error) {
	if p != nil && p.queryAuthenticated != nil {
		return p.queryAuthenticated(ctx, credential, backend)
	}
	if p == nil || p.discovery == nil || credential == nil || backend == "" {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	queryCtx, release, ok := p.discoveryContext(ctx)
	if !ok {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	defer release()

	configured, ok := p.discovery.protectedBackend(queryCtx, backend)
	if !ok {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	brokerToken, err := credential.Token()
	if err != nil || !validBearerToken(brokerToken) || queryCtx.Err() != nil {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}

	return p.discovery.query(queryCtx, brokerToken.AccessToken, backend, configured)
}

func (d *authenticatedDiscovery) query(ctx context.Context, brokerToken, backend string, configured *vmcp.Backend) (AuthenticatedCapabilities, error) {
	var result AuthenticatedCapabilities
	var queryErr error
	terminal := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if _, authenticated := toolhiveauth.IdentityFromContext(request.Context()); !authenticated {
			queryErr = ErrAuthenticatedDiscovery
			return
		}
		capabilities, err := d.capabilities.QueryCapabilities(request.Context(), *configured)
		if err != nil || capabilities == nil || capabilities.BackendID != backend || request.Context().Err() != nil {
			queryErr = ErrAuthenticatedDiscovery
			return
		}
		result, queryErr = neutralCapabilities(backend, capabilities, []string{brokerToken})
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mecatl.invalid/private/toolhive-discovery", nil)
	if err != nil {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	request.Header.Set("Authorization", "Bearer "+brokerToken)
	response := &privateResponseWriter{header: make(http.Header)}
	d.incoming(terminal).ServeHTTP(response, request)
	if queryErr != nil || response.status >= http.StatusBadRequest || result.Backend != backend {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	return result, nil
}

// discoveryContext admits a discovery request only while the process is live.
// The returned context retains the caller's cancellation semantics and is also
// cancelled when the process closes. context.AfterFunc avoids a forwarding
// goroutine per request.
func (p *Process) discoveryContext(ctx context.Context) (context.Context, func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	p.lifecycleMu.Lock()
	if p.closed || ctx.Err() != nil {
		p.lifecycleMu.Unlock()
		return nil, nil, false
	}
	processCtx := p.ctx
	if processCtx == nil {
		processCtx = context.Background()
	}
	if processCtx.Err() != nil {
		p.lifecycleMu.Unlock()
		return nil, nil, false
	}
	p.lifecycleMu.Unlock()

	combined, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(processCtx, func() { cancel(context.Cause(processCtx)) })
	if combined.Err() != nil {
		stop()
		cancel(nil)
		return nil, nil, false
	}
	return combined, func() {
		stop()
		cancel(nil)
	}, true
}

func (d *authenticatedDiscovery) protectedBackend(ctx context.Context, backend string) (*vmcp.Backend, bool) {
	if d == nil || d.capabilities == nil || d.backends == nil || d.incoming == nil {
		return nil, false
	}
	configured := d.backends.Get(ctx, backend)
	if configured == nil || configured.AuthConfig == nil || configured.AuthConfig.UpstreamInject == nil || configured.AuthConfig.UpstreamInject.ProviderName == "" {
		return nil, false
	}
	return configured, true
}

func neutralCapabilities(backend string, capabilities *aggregator.BackendCapabilities, private []string) (AuthenticatedCapabilities, error) {
	seen := make(map[string]struct{}, len(capabilities.Tools))
	result := AuthenticatedCapabilities{Backend: backend, Tools: make([]ToolDefinition, 0, len(capabilities.Tools))}
	for _, candidate := range capabilities.Tools {
		definition, err := neutralToolDefinition(backend, candidate, private)
		if err != nil {
			return AuthenticatedCapabilities{}, err
		}
		if _, duplicate := seen[candidate.Name]; duplicate {
			return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
		}
		seen[candidate.Name] = struct{}{}
		result.Tools = append(result.Tools, definition)
	}
	return result, nil
}

func neutralToolDefinition(backend string, candidate vmcp.Tool, private []string) (ToolDefinition, error) {
	if candidate.BackendID != backend || containsPrivateCapabilityMaterial(candidate, private) || !validDiscoveredToolName(candidate.Name) || !utf8.ValidString(candidate.Description) || len(candidate.Description) > 64<<10 {
		return ToolDefinition{}, ErrAuthenticatedDiscovery
	}
	schema, err := json.Marshal(candidate.InputSchema)
	if err != nil || len(schema) > 1<<20 || !json.Valid(schema) || containsPrivateString(string(schema), private) {
		return ToolDefinition{}, ErrAuthenticatedDiscovery
	}
	var schemaObject map[string]any
	if json.Unmarshal(schema, &schemaObject) != nil || schemaObject == nil {
		return ToolDefinition{}, ErrAuthenticatedDiscovery
	}
	readOnly := candidate.Annotations != nil && candidate.Annotations.ReadOnlyHint != nil && *candidate.Annotations.ReadOnlyHint
	return ToolDefinition{Backend: backend, Name: "mcp__" + backend + "__" + candidate.Name,
		Description: candidate.Description, Schema: append(json.RawMessage(nil), schema...), ReadOnly: readOnly}, nil
}

func validDiscoveredToolName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.'
		if !valid {
			return false
		}
	}
	return true
}

func containsPrivateCapabilityMaterial(candidate vmcp.Tool, private []string) bool {
	return containsPrivateString(candidate.Name, private) || containsPrivateString(candidate.Description, private) || containsPrivateValue(candidate.InputSchema, private)
}

func containsPrivateValue(value any, private []string) bool {
	switch typed := value.(type) {
	case string:
		return containsPrivateString(typed, private)
	case []any:
		for _, item := range typed {
			if containsPrivateValue(item, private) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			if containsPrivateString(key, private) || containsPrivateValue(item, private) {
				return true
			}
		}
	}
	return false
}

func containsPrivateString(value string, private []string) bool {
	for _, item := range private {
		if item != "" && strings.Contains(value, item) {
			return true
		}
	}
	return false
}
