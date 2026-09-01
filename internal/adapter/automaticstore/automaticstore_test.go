package automaticstore_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/automaticconformance"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/automaticstore"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
)

func TestAutomaticStoreConformance(t *testing.T) {
	automaticconformance.Run(t, func(t *testing.T) learning.AutomaticAdmissionLedger {
		ledger, err := automaticstore.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return ledger
	})
}

func TestAutomaticLedgerDriverConformance(t *testing.T) {
	automaticconformance.Run(t, func(t *testing.T) learning.AutomaticAdmissionLedger {
		return newIndependentAutomaticClients(t)[0]
	})
}

func TestADR_0254_AutomaticAdmissionControlsAreProcessIndependent(t *testing.T) {
	checks := []struct {
		name   string
		policy learning.AutomaticAdmissionPolicy
		first  automaticInput
		second automaticInput
		want   error
	}{
		{name: "global count", policy: automaticPolicy(1, 100, 10, 100, 0), first: automaticInput{"a", "one", 10}, second: automaticInput{"b", "two", 10}, want: learning.ErrAutomaticAdmissionLimit},
		{name: "global tokens", policy: automaticPolicy(10, 15, 10, 100, 0), first: automaticInput{"a", "one", 10}, second: automaticInput{"b", "two", 6}, want: learning.ErrAutomaticAdmissionLimit},
		{name: "principal count", policy: automaticPolicy(10, 100, 1, 100, 0), first: automaticInput{"a", "one", 10}, second: automaticInput{"a", "two", 10}, want: learning.ErrAutomaticAdmissionLimit},
		{name: "principal tokens", policy: automaticPolicy(10, 100, 10, 15, 0), first: automaticInput{"a", "one", 10}, second: automaticInput{"a", "two", 6}, want: learning.ErrAutomaticAdmissionLimit},
		{name: "cooldown", policy: automaticPolicy(10, 100, 10, 100, time.Minute), first: automaticInput{"a", "one", 10}, second: automaticInput{"a", "two", 10}, want: learning.ErrAutomaticAdmissionCooldown},
		{name: "dedupe", policy: automaticPolicy(10, 100, 10, 100, 0), first: automaticInput{"a", "same", 10}, second: automaticInput{"b", "same", 10}, want: learning.ErrAutomaticAdmissionDuplicate},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			clients := newIndependentAutomaticClients(t)
			now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
			if _, err := clients[0].Reserve(context.Background(), automaticRequest(t, check.name+"-first", check.first, now, check.policy)); err != nil {
				t.Fatalf("first Reserve: %v", err)
			}
			_, err := clients[1].Reserve(context.Background(), automaticRequest(t, check.name+"-second", check.second, now.Add(time.Second), check.policy))
			if !errors.Is(err, check.want) {
				t.Fatalf("second independent-client Reserve error = %v, want %v", err, check.want)
			}
		})
	}

	t.Run("concurrent replicas do not multiply the global maximum", func(t *testing.T) {
		clients := newIndependentAutomaticClients(t)
		now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		policy := automaticPolicy(1, 100, 10, 100, 0)
		start := make(chan struct{})
		results := make(chan error, len(clients))
		var wg sync.WaitGroup
		for i, client := range clients {
			wg.Add(1)
			go func(i int, client learning.AutomaticAdmissionLedger) {
				defer wg.Done()
				<-start
				_, err := client.Reserve(context.Background(), automaticRequest(t, fmt.Sprintf("race-%d", i), automaticInput{fmt.Sprintf("p-%d", i), fmt.Sprintf("digest-%d", i), 10}, now, policy))
				results <- err
			}(i, client)
		}
		close(start)
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
				t.Fatalf("Reserve error = %v", err)
			}
		}
		if successes != 1 {
			t.Fatalf("successful reservations = %d, want exactly 1", successes)
		}
	})
}

type automaticInput struct {
	principal string
	digest    string
	tokens    uint64
}

func automaticPolicy(globalCount, globalTokens, principalCount, principalTokens uint64, cooldown time.Duration) learning.AutomaticAdmissionPolicy {
	return learning.AutomaticAdmissionPolicy{
		Window: time.Hour, Cooldown: cooldown, DedupeWindow: 24 * time.Hour,
		MaxCount: globalCount, MaxTokens: globalTokens, MaxCountPerPrincipal: principalCount, MaxTokensPerPrincipal: principalTokens,
		ReservationClaimDuration: 5 * time.Minute,
	}
}

func automaticRequest(t *testing.T, suffix string, in automaticInput, now time.Time, policy learning.AutomaticAdmissionPolicy) learning.AutomaticReservationRequest {
	t.Helper()
	attempt := learning.AttemptID("attempt-" + suffix)
	id, err := learning.AutomaticReservationIDForAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(in.digest))
	return learning.AutomaticReservationRequest{
		ID: id, AttemptID: attempt, Principal: learning.AttemptPartition("partition-" + in.principal),
		Digest: learning.CanonicalDigest(fmt.Sprintf("%x", sum)), Class: learning.AdmissionWeighted,
		Tokens: in.tokens, Now: now, Policy: policy,
	}
}

func newIndependentAutomaticClients(t *testing.T) []learning.AutomaticAdmissionLedger {
	t.Helper()
	dir := t.TempDir()
	clients := make([]learning.AutomaticAdmissionLedger, 0, 8)
	for range 8 {
		backend, err := automaticstore.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		listener := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
		driverv1.RegisterAutomaticAdmissionLedgerServiceServer(server, grpcdriver.NewAutomaticAdmissionLedgerServer(backend))
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
		conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		clients = append(clients, grpcdriver.NewAutomaticAdmissionLedger(conn))
	}
	return clients
}
