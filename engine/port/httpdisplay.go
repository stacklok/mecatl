package port

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxHTTPDisplayIDBytes = 200

// AppendHTTPErrorDisplay appends the safe request target and one provider
// correlation ID to a structured HTTP rejection. It accepts only HTTP(S)
// targets and opaque ASCII identifiers; invalid inputs are omitted.
func AppendHTTPErrorDisplay(message string, request *http.Request, correlationID string) string {
	var target string
	if request != nil {
		target = httpDisplayTarget(request.URL)
	}
	id := httpDisplayID(correlationID)
	switch {
	case target != "" && id != "":
		return message + " (target: " + target + "; request ID: " + id + ")"
	case target != "":
		return message + " (target: " + target + ")"
	case id != "":
		return message + " (request ID: " + id + ")"
	default:
		return message
	}
}

// httpDisplayTarget returns a display-safe HTTP(S) request target: scheme,
// host, optional port, and a cleaned escaped path. It omits userinfo, query,
// and fragment, and rejects malformed targets.
func httpDisplayTarget(u *url.URL) string {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := u.Hostname()
	if !validHTTPDisplayHost(host) {
		return ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
		host += ":" + port
	}
	cleanPath := path.Clean(u.Path)
	if cleanPath == "." {
		cleanPath = "/"
	}
	return u.Scheme + "://" + host + (&url.URL{Path: cleanPath}).EscapedPath()
}

// httpDisplayID returns a bounded opaque provider correlation identifier fit
// for user display. It rejects whitespace, controls, and non-ASCII input.
func httpDisplayID(id string) string {
	if id == "" || len(id) > maxHTTPDisplayIDBytes || !utf8.ValidString(id) {
		return ""
	}
	for i := range len(id) {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return ""
		}
	}
	return id
}

func validHTTPDisplayHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
				(c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
