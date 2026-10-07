package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestSessionBrokerAuthoritySelection(t *testing.T) {
	factory := func(context.Context) (mcpbrokergrpc.SessionHostClient, func() error, error) {
		t.Fatal("validation called factory")
		return nil, nil, nil
	}
	for _, tc := range []struct {
		name      string
		authority *mcpauthority.Result
		valid     bool
	}{
		{name: "missing"},
		{name: "global", authority: mcpauthority.NewGlobal(nil, nil)},
		{name: "broker", authority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{}), valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMCPAuthority(Config{SessionBrokerFactory: factory, MCPAuthority: tc.authority})
			if (err == nil) != tc.valid {
				t.Fatalf("authority validation: %v", err)
			}
		})
	}
	if err := validateMCPAuthority(Config{MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{})}); err == nil {
		t.Fatal("explicit empty broker selection fell back without a remote factory")
	}
	for _, section := range []*permconfig.MCPSection{nil, {}} {
		if got := mcpAuthorityDefault(Config{MCPAuthorityDefault: mcpauthority.Broker}, section); got != mcpauthority.Global {
			t.Fatal("zero-MCP root did not stay disabled")
		}
	}
	if got := mcpAuthorityDefault(Config{MCPAuthorityDefault: mcpauthority.Broker}, &permconfig.MCPSection{Mode: "broker"}); got != mcpauthority.Broker {
		t.Fatal("explicit broker selection was reinterpreted")
	}
	if err := validateMCPAuthority(Config{}); err != nil {
		t.Fatalf("direct selection: %v", err)
	}
}
