package identitybroker

import (
	"errors"
	"fmt"
	"slices"
)

// Gateway is the MCP gateway the broker authorizes calls through.
type Gateway struct {
	// Servers are the backend names the gateway routes. A call naming any
	// other server is denied.
	Servers []string
}

// WithGateway has AuthorizeCall authorize calls through g. Without it every
// call that reaches the server check is denied. New refuses a second
// WithGateway, a gateway with no servers, or with a server name that is not
// printable text without spaces or that appears twice, and a gateway without
// WithRecorder.
//
// Shortcut: one gateway, the embedded one; removed by #2538 (operator-declared gateway entries, each with its own AS-2).
func WithGateway(g Gateway) Option {
	return func(b *Broker) {
		// Each broker gets its own copy, so the caller's server list cannot
		// change the broker's.
		gc := g
		gc.Servers = slices.Clone(g.Servers)
		b.gateways++
		b.gateway = &gc
	}
}

// configureGateway validates the broker's gateway and the collaborators it
// requires.
func (b *Broker) configureGateway() error {
	g := b.gateway
	if g == nil {
		return nil
	}
	if b.gateways > 1 {
		return errors.New("only one gateway can be configured")
	}
	if err := g.validate(); err != nil {
		return err
	}
	if b.recorder == nil {
		return errors.New("a gateway requires a recorder")
	}
	return nil
}

func (g *Gateway) validate() error {
	if len(g.Servers) == 0 {
		return errors.New("the gateway routes no servers")
	}
	seen := make(map[string]bool, len(g.Servers))
	for _, s := range g.Servers {
		if !isToken(s) {
			return fmt.Errorf("gateway server %q must be non-empty printable text without spaces", s)
		}
		if seen[s] {
			return fmt.Errorf("gateway server %q is listed twice", s)
		}
		seen[s] = true
	}
	return nil
}

// routes reports whether g routes server. A nil g routes nothing.
func (g *Gateway) routes(server string) bool {
	return g != nil && slices.Contains(g.Servers, server)
}
