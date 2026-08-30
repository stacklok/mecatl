package server

import (
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

const maxDiagnosticEndpointBytes = 2048

// sanitizeDiagnosticEndpoint retains only the safe, useful URL components for a
// diagnostics response. An unavailable or malformed endpoint is represented by
// the empty string so callers never relay the original value.
func sanitizeDiagnosticEndpoint(raw string) string {
	if raw == "" || len(raw) > maxDiagnosticEndpointBytes || !utf8.ValidString(raw) {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" {
		return ""
	}
	if strings.ContainsAny(u.Host, "\\/?#@") {
		return ""
	}
	escapedPath := u.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	} else {
		escapedPath = path.Clean(escapedPath)
		if !strings.HasPrefix(escapedPath, "/") {
			escapedPath = "/" + escapedPath
		}
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host + escapedPath
}

// serverInfoResponse is the single GetServerInfo projection shared by gRPC and
// HTTP. ProviderEndpoint is composition-owned and must be a side-effect-free
// lookup of the caller-selected provider. Its value is sanitized diagnostic display
// data, never connection configuration or an instruction.
func (s *Service) serverInfoResponse(providerID string) *mecatlv1.GetServerInfoResponse {
	endpoint := ""
	if providerID != "" && s.cfg.ProviderEndpoint != nil {
		endpoint = sanitizeDiagnosticEndpoint(s.cfg.ProviderEndpoint(providerID))
	}
	return &mecatlv1.GetServerInfoResponse{
		BuildId:                    s.cfg.BuildID,
		ServerImplementation:       s.cfg.ServerImplementation,
		LlmProviderDisplayEndpoint: endpoint,
	}
}
