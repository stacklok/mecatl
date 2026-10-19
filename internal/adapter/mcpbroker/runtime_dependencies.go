package mcpbroker

import (
	"context"

	"golang.org/x/oauth2"
)

// authenticatedDiscoverer owns infrastructure admission/cancellation and returns
// copied, credential-free metadata for one configured backend. It never publishes
// a catalogue or changes broker session state.
type authenticatedDiscoverer interface {
	QueryAuthenticatedCapabilities(context.Context, oauth2.TokenSource, string) (AuthenticatedCapabilities, error)
}

// WhileOpen serializes a short, in-memory commit against shutdown. Lock order is
// attachment (when needed), gate, runtime, then logical session. The callback
// must not perform I/O, log, or acquire an attachment lock. Unavailable admission
// returns ErrAuthenticatedDiscovery without invoking the callback; callback
// errors pass through unchanged.
type publicationGate interface {
	WhileOpen(commit func() error) error
}

type configuredConnector struct {
	id, name  string
	protected bool
}

type enrollmentConfig struct {
	backends   []string
	occupied   []string
	connectors []configuredConnector
	target     *oauthRoute
}

// configureEnrollment runs only during construction, before attachments exist.
// Bundle routes and the copied configuration share one canonical OAuth target.
func (r *Runtime) configureEnrollment(config enrollmentConfig, discovery authenticatedDiscoverer, publication publicationGate) {
	originalTarget := config.target
	config.backends = append([]string(nil), config.backends...)
	config.occupied = append([]string(nil), config.occupied...)
	config.connectors = append([]configuredConnector(nil), config.connectors...)
	if config.target != nil {
		target := *config.target
		target.scopes = append([]string(nil), target.scopes...)
		config.target = &target
		routes := append([]route(nil), r.catalogue.routes...)
		for i := range routes {
			if routes[i].oauth == originalTarget {
				routes[i].oauth = config.target
			}
		}
		r.catalogue = &Catalogue{routes: routes}
	}
	r.enrollment = &config
	r.discovery = discovery
	r.publication = publication
}
