package main

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestListenerScopedWorkspaceAuthority_Scenario1_NetworkFilesystemRequiresConfiguredRoot(t *testing.T) {
	cfg := config{grpcAddr: "0.0.0.0:8080", httpAddr: "127.0.0.1:8081"}
	if err := validateWorkspaceAuthority(cfg); err == nil {
		t.Fatal("network filesystem deployment without --workspace must fail before listeners start")
	}

	cfg.workspace = t.TempDir()
	if err := validateWorkspaceAuthority(cfg); err != nil {
		t.Fatalf("network deployment with authoritative workspace: %v", err)
	}

	// A relative or unclean --workspace on a network listener must fail at the flag
	// layer (matching NewService's clean-absolute rule and mecak8s), not surface
	// later from app.Build.
	for _, ws := range []string{"relative/root", "/srv/../srv/repo", "/srv/repo/"} {
		bad := config{grpcAddr: "0.0.0.0:8080", httpAddr: "127.0.0.1:8081", workspace: ws}
		if err := validateWorkspaceAuthority(bad); err == nil {
			t.Fatalf("validateWorkspaceAuthority(--workspace %q) = nil, want a clean-absolute-path error", ws)
		}
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario2_MecatedPolicyFollowsAPIListenerTopology(t *testing.T) {
	cases := []struct {
		name     string
		grpcAddr string
		httpAddr string
		want     server.WorkspaceAuthority
	}{
		{name: "ipv4 loopback", grpcAddr: "127.0.0.1:8080", httpAddr: "localhost:8081", want: server.WorkspaceAuthorityClientSelected},
		{name: "ipv6 loopback", grpcAddr: "[::1]:8080", httpAddr: "[::1]:8081", want: server.WorkspaceAuthorityClientSelected},
		{name: "wildcard", grpcAddr: "0.0.0.0:8080", httpAddr: "127.0.0.1:8081", want: server.WorkspaceAuthorityServerAssigned},
		{name: "public", grpcAddr: "192.0.2.10:8080", httpAddr: "127.0.0.1:8081", want: server.WorkspaceAuthorityServerAssigned},
		{name: "mixed", grpcAddr: "127.0.0.1:8080", httpAddr: "[::]:8081", want: server.WorkspaceAuthorityServerAssigned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspaceAuthorityForListeners(config{grpcAddr: tc.grpcAddr, httpAddr: tc.httpAddr})
			if err != nil {
				t.Fatalf("workspaceAuthorityForListeners: %v", err)
			}
			if got != tc.want {
				t.Fatalf("authority = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListenerScopedWorkspaceAuthority_Scenario2_ExplicitAuthorityOverridesTopology(t *testing.T) {
	got, err := workspaceAuthorityForListeners(config{
		grpcAddr:           "127.0.0.1:8080",
		httpAddr:           "127.0.0.1:8081",
		workspaceAuthority: "server-assigned",
	})
	if err != nil {
		t.Fatalf("workspaceAuthorityForListeners: %v", err)
	}
	if got != server.WorkspaceAuthorityServerAssigned {
		t.Fatalf("authority = %v, want explicit server-assigned", got)
	}
}
