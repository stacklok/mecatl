package main

import (
	"flag"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/app"
)

func TestSessionBrokerHostRejectsUnauthenticatedPublicControls(t *testing.T) {
	cfg := config{sessionBroker: sessionBrokerFlags{enabled: true}, httpAddr: "0.0.0.0:8081", grpcAddr: "127.0.0.1:8080"}
	if err := serveBuilt(t.Context(), cfg, &app.Built{}, nil, nil, nil); err == nil || err.Error() != "non-loopback broker-session host requires authenticated control APIs" {
		t.Fatalf("public host admission: %v", err)
	}
}

func TestSessionBrokerFlagsExplicitSelection(t *testing.T) {
	fs := flag.NewFlagSet("retired", flag.ContinueOnError)
	var retired sessionBrokerFlags
	registerSessionBrokerFlags(fs, &retired)
	if err := fs.Parse([]string{"--mcp-broker-session-api"}); err == nil {
		t.Fatal("retired selector accepted")
	}
	for _, tc := range []struct {
		name           string
		args           []string
		valid, enabled bool
	}{
		{name: "direct", valid: true},
		{name: "incomplete", args: []string{"--mcp-broker-address=broker.example.com:8443"}},
		{name: "session", args: []string{"--mcp-broker-address=broker.example.com:8443", "--mcp-broker-tls-ca=fixture.pem", "--mcp-broker-server-name=broker.example.com", "--mcp-broker-token-file=fixture.jwt"}, valid: true, enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			var cfg sessionBrokerFlags
			registerSessionBrokerFlags(fs, &cfg)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if err := cfg.validate(); (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
			if !tc.valid {
				return
			}
			if (cfg.factory() != nil) != tc.enabled {
				t.Fatal("unexpected client factory")
			}
			expected := mcpauthority.Global
			if tc.enabled {
				expected = mcpauthority.Broker
			}
			if cfg.authorityDefault() != expected {
				t.Fatal("unexpected authority mode")
			}
		})
	}
}
