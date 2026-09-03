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
	if !safe(raw) {
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
		if port == 443 {
			u.Host = strings.ToLower(u.Hostname())
		} else {
			u.Host = net.JoinHostPort(strings.ToLower(u.Hostname()), strconv.FormatUint(port, 10))
		}
	} else {
		u.Host = strings.ToLower(u.Hostname())
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

func safe(value string) bool {
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
