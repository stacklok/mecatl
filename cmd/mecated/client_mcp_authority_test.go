package main

import "testing"

// TestSDKServerEnablers_Scenario9_ClientMCPPolicyFollowsListenerTopology pins the
// DEPLOYMENT-SCOPED reading of AC9.2/AC9.3 (issue #821, ADR 0237).
//
// Accepting an MCP endpoint plus its auth headers from an API caller lends the
// daemon's outbound network authority to a remote principal, so the decision is
// made ONCE from listener topology — exactly like workspaceAuthorityForListeners,
// and for the same reason 0237 gives: authority is a deployment policy, "not an
// inference made from a request or from the server package's socket state".
//
// The mixed case is the load-bearing row. One *Service backs BOTH listeners, so a
// daemon that serves a UNIX socket AND a TCP port refuses the field on both. That
// is deliberately conservative: a per-connection answer would contradict 0237 as
// written and would need its own ADR.
func TestSDKServerEnablers_Scenario9_ClientMCPPolicyFollowsListenerTopology(t *testing.T) {
	cases := []struct {
		name       string
		grpcAddr   string
		grpcSocket string
		httpAddr   string
		want       bool
	}{
		// The SDK-spawned daemon shape from Scenario 8: a UNIX socket with HTTP
		// disabled. No network surface at all, so it keeps the feature.
		{name: "uds with http disabled", grpcSocket: "/run/mecatl.sock", want: true},
		// A UNIX socket beside a loopback HTTP listener is still no wider than the
		// loopback bind that already grants client-selected workspace authority.
		{name: "uds with loopback http", grpcSocket: "/run/mecatl.sock", httpAddr: "127.0.0.1:8081", want: true},
		{name: "loopback tcp both", grpcAddr: "127.0.0.1:8080", httpAddr: "localhost:8081", want: true},
		{name: "ipv6 loopback", grpcAddr: "[::1]:8080", httpAddr: "[::1]:8081", want: true},
		{name: "http disabled, loopback grpc", grpcAddr: "127.0.0.1:8080", want: true},

		{name: "wildcard grpc", grpcAddr: "0.0.0.0:8080", httpAddr: "127.0.0.1:8081", want: false},
		{name: "public grpc", grpcAddr: "192.0.2.10:8080", httpAddr: "127.0.0.1:8081", want: false},
		{name: "wildcard http", grpcAddr: "127.0.0.1:8080", httpAddr: "[::]:8081", want: false},
		// The mixed shape: a local socket AND a network-facing HTTP listener. The
		// one Service answers for both, so the wider listener decides.
		{name: "uds plus wildcard http", grpcSocket: "/run/mecatl.sock", httpAddr: "0.0.0.0:8081", want: false},
		// An empty --grpc-addr is a WILDCARD bind, not a disabled listener (gRPC has
		// no disable path). Reading it as "no listener" would grant the feature on a
		// listener reachable from every interface.
		{name: "empty grpc addr is a wildcard", grpcAddr: "", httpAddr: "127.0.0.1:8081", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clientMCPOnCreateForListeners(config{
				grpcAddr:       tc.grpcAddr,
				grpcUnixSocket: tc.grpcSocket,
				httpAddr:       tc.httpAddr,
			})
			if got != tc.want {
				t.Fatalf("clientMCPOnCreateForListeners = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSDKServerEnablers_Scenario9_ClientMCPPolicyReachesTheService proves the
// derived policy is actually WIRED, not merely computed: appConfig must carry it
// into the composition Config the Service reads. Without this the derivation
// could be correct and the daemon still refuse (or accept) everything.
func TestSDKServerEnablers_Scenario9_ClientMCPPolicyReachesTheService(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config
		want bool
	}{
		{"local socket daemon", config{grpcUnixSocket: "/run/mecatl.sock"}, true},
		{"network daemon", config{grpcAddr: "0.0.0.0:8080", httpAddr: "127.0.0.1:8081"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := appConfig(tc.cfg, nil, nil, nil, nil, nil).ClientMCPOnCreate
			if got != tc.want {
				t.Fatalf("app.Config.ClientMCPOnCreate = %v, want %v", got, tc.want)
			}
		})
	}
}
