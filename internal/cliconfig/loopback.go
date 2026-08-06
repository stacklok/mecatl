package cliconfig

import (
	"net"
)

// IsLoopbackAddr reports whether addr is a loopback bind: the host is the literal
// "localhost" or a loopback IP (127.0.0.0/8, ::1). It is the single fail-closed
// gate (ADR 0018 decision 6) the four mains share for binding the UNAUTHENTICATED
// admin/metrics surface — the admin mux output is secret-shaped (pprof/expvar/
// metrics can embed prompt text, file paths, goroutine stacks), so a non-loopback
// bind is rejected at parse time.
//
// A malformed address (no port) is treated as the bare host; an empty or
// unparseable host is NOT loopback (fail safe).
//
// ACCEPTED assumption (security review Low): the literal string "localhost" is
// trusted as loopback without resolving it. A self-inflicted /etc/hosts override
// is contrived and single-user; the SDK's DNS-rebinding/Host validation remains
// the runtime backstop.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
