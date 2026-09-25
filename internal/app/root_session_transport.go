package app

import (
	"net/http"

	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/port"
)

const rootSessionIDHeader = "X-Mecatl-Root-Session-ID"

// withRootSessionCorrelation clones client and installs the final inference
// transport policy without changing the caller's shared client.
func withRootSessionCorrelation(client *http.Client) *http.Client {
	clone := &http.Client{}
	if client != nil {
		*clone = *client
	}
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = withRootSessionCorrelationTransport(base)
	return clone
}

func withRootSessionCorrelationTransport(base http.RoundTripper) http.RoundTripper {
	return sessionCorrelationRoundTripper{base: base}
}

// withCodexSessionCorrelationTransport runs inside the Codex request policy,
// after its trusted header reconstruction. It derives both correlation fields
// from trusted context without broadening the policy's incoming allowlist.
func withCodexSessionCorrelationTransport(base http.RoundTripper) http.RoundTripper {
	return sessionCorrelationRoundTripper{base: base, includeActive: true}
}

type sessionCorrelationRoundTripper struct {
	base          http.RoundTripper
	includeActive bool
}

func (t sessionCorrelationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	out.Header.Del(rootSessionIDHeader)
	if id, ok := port.RootSessionIDFromContext(req.Context()); ok && sessionaffinity.ValidValue(string(id)) {
		out.Header.Set(rootSessionIDHeader, string(id))
	}
	if t.includeActive {
		out.Header.Del(sessionaffinity.HeaderName)
		if id, ok := port.SessionIDFromContext(req.Context()); ok && sessionaffinity.ValidValue(string(id)) {
			out.Header.Set(sessionaffinity.HeaderName, string(id))
		}
	}
	return t.base.RoundTrip(out)
}
