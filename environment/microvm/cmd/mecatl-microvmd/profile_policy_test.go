package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm"
)

func TestADR_0224_DaemonProfilesAreAuthoritative(t *testing.T) {
	resources, err := microvm.NewOpaqueIdentityAllocator(t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := map[microvm.ArtifactKind]microvm.ArtifactRequest{
		microvm.ArtifactExecutionImage: {Kind: microvm.ArtifactExecutionImage, Reference: "image@sha256:daemon"},
		microvm.ArtifactGuestAgent:     {Kind: microvm.ArtifactGuestAgent, Reference: "guest-agent@sha256:daemon"},
	}
	profiles := map[string]profileConfig{
		"secure": {Resources: map[string]string{"cpus": "2", "memory": "512MiB"}},
	}
	builder := newPlacementBuilder(resources, artifacts, profiles, microvm.GuestEgressPolicy{Mode: microvm.EgressDenyAll})

	if _, err := builder(context.Background(), microvm.ProvisionRequest{Owner: "local", SessionID: "s1", Profile: "unknown", SourceCheckout: "/source"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown profile error = %v", err)
	}
	request, err := builder(context.Background(), microvm.ProvisionRequest{Owner: "local", SessionID: "s1", Profile: "secure", SourceCheckout: "/source"})
	if err != nil {
		t.Fatalf("build placement: %v", err)
	}
	if request.Resources.CPU != 2 || request.Resources.RAMBytes != 512<<20 {
		t.Fatalf("daemon profile resources = %+v", request.Resources)
	}
	if request.ProfileStatus.Profile != "secure" || request.ProfileStatus.GuestEgress != "deny-all (IPv4 filtered; IPv6 disabled)" || request.ProfileStatus.HostEgress == "" {
		t.Fatalf("daemon enforced status = %+v", request.ProfileStatus)
	}
}
