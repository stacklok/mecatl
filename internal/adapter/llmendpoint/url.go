package llmendpoint

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

var errInvalidGatewayURL = errors.New("gateway URL is invalid")

// CanonicalGatewayURL validates and canonicalizes one configured gateway origin
// and base path. It performs no credential or network access.
func CanonicalGatewayURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errInvalidGatewayURL
	}
	port := u.Port()
	if port == "443" {
		port = ""
	}
	hostname := strings.ToLower(u.Hostname())
	if strings.Contains(hostname, ":") {
		if port == "" {
			u.Host = "[" + hostname + "]"
		} else {
			u.Host = net.JoinHostPort(hostname, port)
		}
	} else if port == "" {
		u.Host = hostname
	} else {
		u.Host = net.JoinHostPort(hostname, port)
	}
	escaped, decoded, err := canonicalSegments(u.EscapedPath(), false)
	if err != nil {
		return "", err
	}
	u.Scheme = "https"
	u.Path = decoded
	u.RawPath = escaped
	return u.String(), nil
}

// JoinGatewayURL appends a relative adapter request path beneath a canonical
// gateway base. Query parameters are deliberately rejected here and may be set
// on the returned URL only after this confinement check succeeds.
func JoinGatewayURL(canonicalBase, relative string) (*url.URL, error) {
	validatedBase, err := CanonicalGatewayURL(canonicalBase)
	if err != nil || validatedBase != canonicalBase || strings.HasPrefix(relative, "/") {
		return nil, errInvalidGatewayURL
	}
	base, err := url.Parse(canonicalBase)
	if err != nil {
		return nil, errInvalidGatewayURL
	}
	r, err := parseRelativeURL(relative)
	if err != nil {
		return nil, err
	}
	escaped, decoded, err := canonicalSegments(r.EscapedPath(), true)
	if err != nil {
		return nil, err
	}
	baseEscaped := base.EscapedPath()
	if baseEscaped == "/" {
		baseEscaped = ""
	}
	baseDecoded := base.Path
	if baseDecoded == "/" {
		baseDecoded = ""
	}
	joinedEscaped := baseEscaped + "/" + strings.TrimPrefix(escaped, "/")
	joinedDecoded := baseDecoded + "/" + strings.TrimPrefix(decoded, "/")
	joined := *base
	joined.Path = joinedDecoded
	joined.RawPath = joinedEscaped

	if joined.Scheme != base.Scheme || joined.Host != base.Host ||
		(baseEscaped != "" && joined.EscapedPath() != baseEscaped && !strings.HasPrefix(joined.EscapedPath(), baseEscaped+"/")) {
		return nil, errInvalidGatewayURL
	}
	return &joined, nil
}

func parseRelativeURL(relative string) (*url.URL, error) {
	r, err := url.Parse(relative)
	if err != nil || r.IsAbs() || r.Host != "" || r.User != nil || r.Opaque != "" || r.RawQuery != "" || r.ForceQuery || r.Fragment != "" {
		return nil, errInvalidGatewayURL
	}
	return r, nil
}

func canonicalSegments(rawPath string, requireNonEmpty bool) (string, string, error) {
	if strings.Contains(rawPath, "\\") {
		return "", "", errInvalidGatewayURL
	}
	parts := strings.Split(rawPath, "/")
	escaped := make([]string, 0, len(parts))
	decoded := make([]string, 0, len(parts))
	for _, raw := range parts {
		if raw == "" {
			continue
		}
		segment, err := url.PathUnescape(raw)
		if err != nil || segment == "." || segment == ".." || strings.ContainsAny(segment, "/\\") || hasControl(segment) {
			return "", "", errInvalidGatewayURL
		}
		escaped = append(escaped, url.PathEscape(segment))
		decoded = append(decoded, segment)
	}
	if requireNonEmpty && len(escaped) == 0 {
		return "", "", errInvalidGatewayURL
	}
	if len(escaped) == 0 {
		return "/", "/", nil
	}
	return "/" + strings.Join(escaped, "/"), "/" + strings.Join(decoded, "/"), nil
}

func hasControl(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}
