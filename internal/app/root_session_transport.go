package app

import (
	"net/http"

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
	clone.Transport = rootSessionRoundTripper{base: base}
	return clone
}

type rootSessionRoundTripper struct {
	base http.RoundTripper
}

func (t rootSessionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	if out.Header == nil {
		out.Header = make(http.Header)
	}
	out.Header.Del(rootSessionIDHeader)
	if id, ok := port.RootSessionIDFromContext(req.Context()); ok && validRootSessionID(string(id)) {
		out.Header.Set(rootSessionIDHeader, string(id))
	}
	return t.base.RoundTrip(out)
}

func validRootSessionID(id string) bool {
	if len(id) == 0 || len(id) > 256 || id[0] == ' ' || id[len(id)-1] == ' ' {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] > 0x7e {
			return false
		}
	}
	return true
}
