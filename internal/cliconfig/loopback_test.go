package cliconfig

import "testing"

// TestIsLoopbackAddr pins the fail-closed loopback gate shared by the four mains
// (ADR 0018 decision 6): loopback hosts pass; non-loopback, wildcard, and empty
// hosts do NOT, so a misconfigured bind of the unauthenticated admin surface is
// refused.
func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		// Loopback accepts.
		{"127.0.0.1:9090", true},
		{"127.0.0.2:9090", true}, // 127.0.0.0/8 is all loopback
		{"127.1.2.3:9090", true},
		{"[::1]:9090", true},
		{"localhost:9090", true},
		// A bare host with no port resolves through the same gate.
		{"127.0.0.1", true},
		{"localhost", true},
		{"::1", true},

		// Rejections (fail closed).
		{"0.0.0.0:9090", false}, // wildcard — routable
		{"[::]:9090", false},    // IPv6 wildcard — routable
		{"192.168.1.10:9090", false},
		{"10.0.0.1:9090", false},
		{"example.com:9090", false},
		{"", false},      // empty host — fail safe
		{":9090", false}, // empty host with port — fail safe
	}
	for _, tt := range tests {
		if got := IsLoopbackAddr(tt.addr); got != tt.want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
