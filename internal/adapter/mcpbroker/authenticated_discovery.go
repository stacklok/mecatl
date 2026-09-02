package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	toolhiveauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// ErrAuthenticatedDiscovery reports a provider-scoped capability discovery
// failure without exposing a provider key, auth-session identifier, or credential.
var ErrAuthenticatedDiscovery = errors.New("mcpbroker: authenticated capability discovery failed")

// ToolHiveAuthSessionID is the process-local ToolHive token-session handle.
// It is only accepted by this concrete adapter and is never projected into a
// catalogue, tool specification, error, or log record.
type ToolHiveAuthSessionID string

// AuthenticatedCapabilities is the neutral, copied result of discovering one
// protected backend. It deliberately contains no ToolHive capability value or
// authentication material.
type AuthenticatedCapabilities struct {
	Backend string
	Tools   []ToolDefinition
}

type upstreamCredentialReader interface {
	GetValidTokens(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error)
}

// capabilityQuerier intentionally exposes no aggregate query operation.
type capabilityQuerier interface {
	QueryCapabilities(context.Context, vmcp.Backend) (*aggregator.BackendCapabilities, error)
}

type backendLookup interface {
	Get(context.Context, string) *vmcp.Backend
}

type authenticatedDiscovery struct {
	capabilities capabilityQuerier
	backends     backendLookup
	tokens       upstreamCredentialReader
	providers    map[string]string
}

// QueryAuthenticatedCapabilities obtains one credential for one configured
// protected backend, then makes one provider-scoped ToolHive query. Discovery
// is intentionally separate from catalogue construction: no discovered result
// is admitted, frozen, or retained by the Process.
func (p *Process) QueryAuthenticatedCapabilities(ctx context.Context, authSession ToolHiveAuthSessionID, backend string) (AuthenticatedCapabilities, error) {
	if p == nil || p.discovery == nil || authSession == "" || backend == "" {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	queryCtx, release, ok := p.discoveryContext(ctx)
	if !ok {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	defer release()
	discovery := p.discovery
	provider, configured, ok := discovery.protectedBackend(queryCtx, backend)
	if !ok || queryCtx.Err() != nil {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}

	credential, err := discovery.tokens.GetValidTokens(queryCtx, string(authSession), provider)
	if err != nil || credential == nil || credential.AccessToken == "" || queryCtx.Err() != nil {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	queryCtx = toolhiveauth.WithIdentity(queryCtx, &toolhiveauth.Identity{
		PrincipalInfo:  toolhiveauth.PrincipalInfo{Subject: "mecatl-authenticated-discovery"},
		TokenType:      "Bearer",
		UpstreamTokens: map[string]string{provider: credential.AccessToken},
	})
	capabilities, err := discovery.capabilities.QueryCapabilities(queryCtx, *configured)
	if err != nil || capabilities == nil || capabilities.BackendID != backend || queryCtx.Err() != nil {
		return AuthenticatedCapabilities{}, ErrAuthenticatedDiscovery
	}
	return neutralCapabilities(backend, capabilities, []string{provider, string(authSession), credential.AccessToken, credential.IDToken})
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

func (d *authenticatedDiscovery) protectedBackend(ctx context.Context, backend string) (string, *vmcp.Backend, bool) {
	if d == nil || d.capabilities == nil || d.backends == nil || d.tokens == nil {
		return "", nil, false
	}
	provider, protected := d.providers[backend]
	configured := d.backends.Get(ctx, backend)
	if !protected || provider == "" || configured == nil || configured.AuthConfig == nil || configured.AuthConfig.UpstreamInject == nil || configured.AuthConfig.UpstreamInject.ProviderName != provider {
		return "", nil, false
	}
	return provider, configured, true
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
