package mcpbroker

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ValidateProtectedURL admits an operator-selected HTTPS endpoint without
// making assumptions about whether its host is public or private. The caller
// owns the trust boundary for private infrastructure; transport code must
// still pin and verify it.
func ValidateProtectedURL(raw, label string) error {
	u, err := url.Parse(raw)
	if err != nil || invalidProtectedURL(u) {
		return fmt.Errorf("%s must be an exact HTTPS URL", label)
	}
	if strings.ContainsAny(u.Host, "\r\n") || !validProtectedHostname(u.Hostname()) {
		return fmt.Errorf("%s must have a valid hostname", label)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s has an invalid port", label)
		}
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && strings.Contains(u.Hostname(), ":") {
		host := "[" + ip.String() + "]"
		if port := u.Port(); port != "" {
			host = net.JoinHostPort(ip.String(), port)
		}
		if u.Host != host {
			return fmt.Errorf("%s must use a canonical IPv6 hostname", label)
		}
	}
	return nil
}

func invalidProtectedURL(u *url.URL) bool {
	return u.Scheme != "https" || u.Opaque != "" || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" ||
		(u.RawPath != "" && u.RawPath != u.Path)
}

// validProtectedHostname matches the existing MCP endpoint hostname grammar.
func validProtectedHostname(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}
