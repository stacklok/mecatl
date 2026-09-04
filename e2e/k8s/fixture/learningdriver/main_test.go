//go:build kind_e2e

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
)

func TestFixtureAutomaticPolicyMatchesAgentDefaults(t *testing.T) {
	want := learning.AutomaticAdmissionPolicy{
		Window: time.Hour, Cooldown: 10 * time.Minute, DedupeWindow: 24 * time.Hour,
		MaxCount: 8, MaxTokens: 100_000, MaxCountPerPrincipal: 4, MaxTokensPerPrincipal: 50_000,
		ReservationClaimDuration: time.Minute,
	}
	if fixtureAutomaticPolicy != want {
		t.Fatalf("automatic policy = %+v, want agent defaults %+v", fixtureAutomaticPolicy, want)
	}
}

func TestFixtureServerRegistersCompleteTrustedLearningRepository(t *testing.T) {
	server, err := newFixtureServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	got, err := grpcdriver.ProbeLearningRepositoryCapabilities(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	want := fixtureCapabilities()
	if got != want {
		t.Fatalf("capabilities = %+v, want %+v", got, want)
	}
	for _, service := range []string{
		"mecatl.driver.v1.LearningRepositoryCapabilitiesService",
		"mecatl.driver.v1.AttemptRepositoryService",
		"mecatl.driver.v1.ProposalRepositoryService",
		"mecatl.driver.v1.SkillRepositoryService",
		"mecatl.driver.v1.AutomaticAdmissionLedgerService",
	} {
		if _, ok := server.GetServiceInfo()[service]; !ok {
			t.Errorf("service %q is not registered", service)
		}
	}
}
