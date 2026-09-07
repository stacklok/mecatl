// Package resourceurl defines the canonical protected-resource URL identity shared
// by the server, enrollment registry, and discovery client.
package resourceurl

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const httpsScheme = "https"

// Canonical validates and canonicalizes an RFC 9728 HTTPS resource. Root slash,
// host case, and the default HTTPS port have one stable representation.
func Canonical(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !Safe(raw) {
		return "", errors.New("invalid resource URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != httpsScheme || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("invalid resource URL")
	}
	if u.Port() != "" {
		port, portErr := strconv.ParseUint(u.Port(), 10, 16)
		if portErr != nil || port == 0 || port > 65535 {
			return "", errors.New("invalid resource URL port")
		}
		host := canonicalHostname(u.Hostname())
		if port == 443 {
			u.Host = urlHost(host)
		} else {
			u.Host = net.JoinHostPort(host, strconv.FormatUint(port, 10))
		}
	} else {
		u.Host = urlHost(canonicalHostname(u.Hostname()))
	}
	if hasDotSegment(u.Path) {
		return "", errors.New("invalid resource URL path")
	}
	if u.Path == "/" {
		u.Path, u.RawPath = "", ""
	}
	return u.String(), nil
}

// MetadataURL returns the RFC 9728 metadata URL for a canonical resource.
func MetadataURL(raw string) string {
	resource, err := Canonical(raw)
	if err != nil {
		return ""
	}
	u, err := url.Parse(resource)
	if err != nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" || path == "/" {
		u.Path, u.RawPath = oauthproto.WellKnownOAuthResourcePath, ""
		return u.String()
	}
	u.Path = oauthproto.WellKnownOAuthResourcePath + u.Path
	u.RawPath = oauthproto.WellKnownOAuthResourcePath + path
	return u.String()
}

func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func canonicalHostname(host string) string {
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

func urlHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// Safe reports whether value is a non-empty, bounded string free of control
// and Unicode format characters. Shared by the server, enrollment registry,
// and discovery client so an identity-safety tightening cannot drift between
// them.
func Safe(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// ValidScopeToken reports whether scope is a single RFC 6749 §3.3 scope-token
// (VSCHAR minus DQUOTE/backslash, no comma — a comma is the CSV separator
// wherever scopes are joined for display or storage). Shared by the server's
// protected-resource profile, the OIDC CLI/Helm flag parser, and the
// discovery client so an untrusted metadata response cannot smuggle a
// separator character into a scope value.
func ValidScopeToken(scope string) bool {
	if scope == "" {
		return false
	}
	for _, r := range scope {
		if r < 0x21 || r > 0x7e || r == '"' || r == '\\' || r == ',' {
			return false
		}
	}
	return true
}
