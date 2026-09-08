package main

import "testing"

// TestSDKServerEnablers_Scenario9_ClientMCPPolicyFollowsListenerTopology pins the
// DEPLOYMENT-SCOPED reading of AC9.2/AC9.3 (issue #821, ADR 0237) and the
// UDS-ONLY threshold within it.
//
// Two independent decisions are under test here, and both have a failure mode:
//
//  1. WHERE the decision is made. Accepting an MCP endpoint plus its auth headers
//     from an API caller lends the daemon's outbound network authority to that
//     caller, so it is decided ONCE from listener topology, per 0237: authority is
//     a deployment policy, "not an inference made from a request or from the server
//     package's socket state".
//  2. WHAT counts as local enough. UNIX socket with HTTP disabled, and nothing
//     else. Loopback TCP does NOT qualify, which is where this derivation parts
//     company with workspaceAuthorityForListeners — the loopback rows below are the
//     ones that pin the difference. Loopback is reachable by every local process
//     and every local user on the host; a UNIX socket is guarded by filesystem
//     permissions on an owner-only directory. AC9.2 says "over a TCP listener is
//     refused" and ADR 0248 already publishes "only reachable on a UDS listener";
//     a loopback TCP daemon is a TCP daemon.
//
// The mixed rows are the load-bearing ones for (1): one *Service backs BOTH
// listeners, so a daemon serving a UNIX socket AND a TCP port refuses the field on
// both. A per-connection answer would contradict 0237 as written.
func TestSDKServerEnablers_Scenario9_ClientMCPPolicyFollowsListenerTopology(t *testing.T) {
	cases := []struct {
		name       string
		grpcAddr   string
		grpcSocket string
		httpAddr   string
		want       bool
	}{
		// The ONE topology that qualifies: the SDK-spawned daemon shape from
		// Scenario 8. A UNIX socket, HTTP disabled, no network surface at all.
		{name: "uds with http disabled", grpcSocket: "/run/mecatl.sock", want: true},
		// A UNIX socket beside HTTP on an explicit loopback address still exposes a
		// TCP port that any local process — including a browser page — can reach.
		// It gives the feature up. (Contrast workspaceAuthorityForListeners, which
		// accepts this shape: see the type-level comment for why they differ.)
		{name: "uds with loopback http", grpcSocket: "/run/mecatl.sock", httpAddr: "127.0.0.1:8081", want: false},
		// Default mecated. Loopback gRPC + loopback HTTP is the shape the review
		// caught advertising and accepting mcp_servers; it must not.
		{name: "loopback tcp both", grpcAddr: "127.0.0.1:8080", httpAddr: "localhost:8081", want: false},
		{name: "ipv6 loopback", grpcAddr: "[::1]:8080", httpAddr: "[::1]:8081", want: false},
		// HTTP disabled is necessary but not sufficient: gRPC is still on TCP here,
		// because only --grpc-unix-socket suppresses that bind.
		{name: "http disabled, loopback grpc", grpcAddr: "127.0.0.1:8080", want: false},

		{name: "wildcard grpc", grpcAddr: "0.0.0.0:8080", httpAddr: "127.0.0.1:8081", want: false},
		{name: "public grpc", grpcAddr: "192.0.2.10:8080", httpAddr: "127.0.0.1:8081", want: false},
		{name: "wildcard http", grpcAddr: "127.0.0.1:8080", httpAddr: "[::]:8081", want: false},
		// The mixed shape: a local socket AND a network-facing HTTP listener. The
		// one Service answers for both, so the wider listener decides.
		{name: "uds plus wildcard http", grpcSocket: "/run/mecatl.sock", httpAddr: "0.0.0.0:8081", want: false},
		// An empty --grpc-addr is a WILDCARD bind, not a disabled listener (gRPC has
		// no disable path). The positive grpcUnixSocket test is what makes this row
		// safe: an absence check on grpcAddr would have granted the feature on a
		// listener reachable from every interface.
		{name: "empty grpc addr is a wildcard", grpcAddr: "", httpAddr: "127.0.0.1:8081", want: false},
		// ...and an empty grpcAddr with HTTP also disabled is STILL a wildcard gRPC
		// bind, so it must not be mistaken for the no-listener daemon.
		{name: "empty grpc addr with http disabled", grpcAddr: "", want: false},
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

// TestSDKServerEnablers_Scenario9_ClientMCPRemainsUDSOnly pins the
// listener-derived MCP policy independently of server-owned placement.
func TestSDKServerEnablers_Scenario9_ClientMCPRemainsUDSOnly(t *testing.T) {
	loopback := config{grpcAddr: "127.0.0.1:8080", httpAddr: "127.0.0.1:8081"}
	if clientMCPOnCreateForListeners(loopback) {
		t.Fatal("loopback TCP must not permit client MCP")
	}
	uds := config{grpcUnixSocket: "/run/mecatl.sock"}
	if !clientMCPOnCreateForListeners(uds) {
		t.Fatal("UDS daemon with HTTP disabled must permit client MCP")
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
		{"default loopback daemon", config{grpcAddr: "127.0.0.1:8080", httpAddr: "127.0.0.1:8081"}, false},
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
