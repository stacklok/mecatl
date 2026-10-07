package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

type staticBrokerAuthorityLoader struct{ authority *mcpauthority.Result }

func (l staticBrokerAuthorityLoader) LoadAuthority(*permconfig.MCPSection, mcpauthority.Mode, bool) (*mcpauthority.Result, error) {
	return l.authority, nil
}

func TestBuildRejectsProgrammaticMCPServersWithBrokerAuthority(t *testing.T) {
	for _, cfg := range []Config{{MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{})}, {MCPAuthorityLoader: staticBrokerAuthorityLoader{mcpauthority.NewBroker(mcpauthority.BrokerConfig{})}}} {
		cfg.Workspace = t.TempDir()
		cfg.UseMock = true
		cfg.NoSoul = true
		cfg.MCPServers = []mcp.ServerConfig{{Name: "global", URL: "http://127.0.0.1:1/mcp"}}
		_, err := buildIsolated(t, t.Context(), cfg)
		if err == nil || err.Error() != "broker MCP authority cannot be combined with programmatic MCPServers" {
			t.Fatalf("mixed authority: %v", err)
		}
	}
}

func TestHostBrokerRetirementRequiresRemoteAndNeverFallsBack(t *testing.T) {
	for _, failure := range []string{"missing", "error", "incomplete"} {
		t.Run(failure, func(t *testing.T) {
			cfg := Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{})}
			calls, closes := 0, 0
			if failure != "missing" {
				cfg.SessionBrokerFactory = func(context.Context) (mcpbrokergrpc.SessionHostClient, func() error, error) {
					calls++
					if failure == "error" {
						return nil, func() error { closes++; return nil }, errors.New("unavailable")
					}
					return nil, nil, nil
				}
			}
			_, err := buildIsolated(t, t.Context(), cfg)
			if err == nil {
				t.Fatal("retired fallback accepted")
			}
			if failure == "missing" && !strings.Contains(err.Error(), "bundled broker has been retired") {
				t.Fatal(err)
			}
			if failure != "missing" && calls != 1 {
				t.Fatal("factory not called exactly once")
			}
			if failure == "error" && closes != 1 {
				t.Fatal("failed factory leaked returned client")
			}
		})
	}
}
