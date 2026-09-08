// Package mcpauthority carries the exclusive MCP construction selection between
// command-side profile loading and application composition.
package mcpauthority

import (
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// Mode identifies the sole selected MCP authority path. The zero value is invalid.
type Mode string

const (
	// Global selects the established process-global MCP manager.
	Global Mode = "global"
	// Broker selects session-scoped broker route declarations.
	Broker Mode = "broker"
)

// BrokerConfig is the neutral, runtime-independent broker declaration set.
type BrokerConfig struct {
	Routes      []permconfig.MCPServerProfile
	CallbackURL string
}

// Result is the canonical authority tagged union. Unexported payloads prevent a
// caller from constructing a value with both authority paths.
type Result struct {
	mode   Mode
	global *globalConfig
	broker *BrokerConfig
}

type globalConfig struct {
	servers   []mcp.ServerConfig
	lifecycle interface{ Close() error }
}

// NewGlobal takes a defensive copy and creates a global-only result.
func NewGlobal(servers []mcp.ServerConfig, lifecycle interface{ Close() error }) *Result {
	return &Result{mode: Global, global: &globalConfig{servers: cloneServers(servers), lifecycle: lifecycle}}
}

// NewBroker takes one deep defensive copy and creates a broker-only result.
func NewBroker(config BrokerConfig) *Result {
	return &Result{mode: Broker, broker: &BrokerConfig{Routes: cloneRoutes(config.Routes), CallbackURL: config.CallbackURL}}
}

// Mode returns the selected authority tag. Nil, zero, and malformed results fail
// closed to the invalid zero mode.
func (r *Result) Mode() Mode {
	if r == nil {
		return ""
	}
	switch r.mode {
	case Global:
		if r.global != nil && r.broker == nil {
			return Global
		}
	case Broker:
		if r.broker != nil && r.global == nil {
			return Broker
		}
	}
	return ""
}

// Global returns global server configs and their lifecycle only for a valid
// global selection. The returned slice and header maps do not alias the result.
func (r *Result) Global() ([]mcp.ServerConfig, interface{ Close() error }, bool) {
	if r.Mode() != Global {
		return nil, nil, false
	}
	return cloneServers(r.global.servers), r.global.lifecycle, true
}

// Broker returns broker declarations only for a valid broker selection. The
// returned declaration tree does not alias the result.
func (r *Result) Broker() (BrokerConfig, bool) {
	if r.Mode() != Broker {
		return BrokerConfig{}, false
	}
	return BrokerConfig{Routes: cloneRoutes(r.broker.Routes), CallbackURL: r.broker.CallbackURL}, true
}

func cloneServers(in []mcp.ServerConfig) []mcp.ServerConfig {
	out := append([]mcp.ServerConfig(nil), in...)
	for i := range out {
		if out[i].Headers != nil {
			out[i].Headers = make(map[string]string, len(in[i].Headers))
			for key, value := range in[i].Headers {
				out[i].Headers[key] = value
			}
		}
		if out[i].OAuth != nil {
			oauth := *out[i].OAuth
			oauth.AllowedScopes = append([]string(nil), oauth.AllowedScopes...)
			oauth.Network.AdditionalOrigins = append([]string(nil), oauth.Network.AdditionalOrigins...)
			oauth.Network.PrivateOrigins = append([]string(nil), oauth.Network.PrivateOrigins...)
			if oauth.Client.Preregistered != nil {
				credentials := *oauth.Client.Preregistered
				oauth.Client.Preregistered = &credentials
			}
			out[i].OAuth = &oauth
		}
	}
	return out
}

func cloneRoutes(in []permconfig.MCPServerProfile) []permconfig.MCPServerProfile {
	out := append([]permconfig.MCPServerProfile(nil), in...)
	for i := range out {
		auth := &out[i].Auth
		if auth.StaticBearer != nil {
			value := *auth.StaticBearer
			auth.StaticBearer = &value
		}
		if auth.OAuth == nil {
			continue
		}
		oauth := *auth.OAuth
		auth.OAuth = &oauth
		oauth.Scopes = append([]string(nil), oauth.Scopes...)
		oauth.Tools = append([]permconfig.MCPStaticToolProfile(nil), oauth.Tools...)
		for j := range oauth.Tools {
			oauth.Tools[j].InputSchema = append([]byte(nil), oauth.Tools[j].InputSchema...)
		}
		if oauth.Upstream != nil {
			upstream := *oauth.Upstream
			oauth.Upstream = &upstream
			if upstream.OAuth2 != nil {
				value := *upstream.OAuth2
				upstream.OAuth2 = &value
			}
		}
		if oauth.Network != nil {
			network := *oauth.Network
			network.AdditionalOrigins = append([]string(nil), network.AdditionalOrigins...)
			network.PrivateOrigins = append([]string(nil), network.PrivateOrigins...)
			oauth.Network = &network
		}
		if oauth.Client.Preregistered != nil {
			value := *oauth.Client.Preregistered
			oauth.Client.Preregistered = &value
		}
		if oauth.Client.CIMD != nil {
			value := *oauth.Client.CIMD
			oauth.Client.CIMD = &value
		}
		if oauth.Credentials.Local != nil {
			value := *oauth.Credentials.Local
			oauth.Credentials.Local = &value
		}
		if oauth.Credentials.Environment != nil {
			value := *oauth.Credentials.Environment
			oauth.Credentials.Environment = &value
		}
	}
	return out
}
