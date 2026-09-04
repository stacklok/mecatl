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
	automaticconformance.Run(t, func(t *testing.T, policy learning.AutomaticAdmissionPolicy, clock *automaticconformance.Clock) learning.AutomaticAdmissionLedger {
		ledger, err := automaticstore.New(t.TempDir(), policy, clock)
		if err != nil {
			t.Fatal(err)
		}
		return ledger
	})
}

func TestAutomaticLedgerDriverConformance(t *testing.T) {
	automaticconformance.Run(t, func(t *testing.T, policy learning.AutomaticAdmissionPolicy, clock *automaticconformance.Clock) learning.AutomaticAdmissionLedger {
		return newIndependentAutomaticClients(t, policy, clock)[0]
	})
}

func TestADR_0259_AutomaticLedgerRejectsClientPolicyAndClockAuthority(t *testing.T) {
	policy := automaticPolicy(1, 100, 10, 100, 0)
	backendClock := automaticconformance.NewClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	clients := newIndependentAutomaticClients(t, policy, backendClock)
	if _, err := clients[0].Reserve(context.Background(), automaticRequest(t, "authoritative-first", automaticInput{"a", "one", 10}, policy)); err != nil {
		t.Fatal(err)
	}

	t.Run("enlarged client policy revision cannot raise the configured maximum", func(t *testing.T) {
		enlarged := policy
		enlarged.MaxCount = 100
		_, reserveErr := clients[1].Reserve(context.Background(), automaticRequest(t, "enlarged-policy", automaticInput{"b", "two", 10}, enlarged))
		if !errors.Is(reserveErr, learning.ErrInvalidAutomaticReservation) {
			t.Fatalf("Reserve with enlarged client policy revision error = %v, want ErrInvalidAutomaticReservation", reserveErr)
		}
	})

	t.Run("durable policy revision rejects a differently configured replica", func(t *testing.T) {
		dir := t.TempDir()
		strict, err := automaticstore.New(dir, policy, backendClock)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = strict.Reserve(context.Background(), automaticRequest(t, "durable-policy", automaticInput{"a", "durable", 10}, policy)); err != nil {
			t.Fatal(err)
		}
		enlarged := policy
		enlarged.MaxCount = 100
		drifted, err := automaticstore.New(dir, enlarged, backendClock)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = drifted.Get(context.Background(), automaticRequest(t, "durable-policy", automaticInput{"a", "durable", 10}, policy).ID); err == nil {
			t.Fatal("differently configured replica read a ledger bound to another policy revision")
		}
	})

	t.Run("skewed client clock cannot age out a charge", func(t *testing.T) {
		fields := (&driverv1.AutomaticReservationRequest{}).ProtoReflect().Descriptor().Fields()
		if fields.ByJSONName("now") != nil || fields.ByJSONName("policy") != nil {
			t.Fatal("automatic reservation wire request exposes client time or policy authority")
		}
		_, reserveErr := clients[1].Reserve(context.Background(), automaticRequest(t, "skewed-clock", automaticInput{"b", "three", 10}, policy))
		if !errors.Is(reserveErr, learning.ErrAutomaticAdmissionLimit) {
			t.Fatalf("Reserve from skewed client error = %v, want ErrAutomaticAdmissionLimit", reserveErr)
		}
	})
}

func TestInvariant_automatic_reservation_records_are_durably_bounded(t *testing.T) {
	policy := automaticPolicy(10000, 10000, 10000, 10000, 0)
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	t.Run("per principal", func(t *testing.T) {
		clock := automaticconformance.NewClock(start)
		ledger, err := automaticstore.New(t.TempDir(), policy, clock)
		if err != nil {
			t.Fatal(err)
		}
		for i := range learning.MaxAutomaticReservationRecordsPerPrincipal {
			if _, err = ledger.Reserve(context.Background(), automaticRequest(t, fmt.Sprintf("owner-%d", i), automaticInput{"same", fmt.Sprintf("digest-%d", i), 1}, policy)); err != nil {
				t.Fatalf("Reserve %d: %v", i, err)
			}
			clock.Set(clock.Now().Add(policy.Window))
		}
		_, err = ledger.Reserve(context.Background(), automaticRequest(t, "owner-overflow", automaticInput{"same", "overflow", 1}, policy))
		if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
			t.Fatalf("per-principal overflow error = %v, want ErrAutomaticAdmissionLimit", err)
		}
	})

	t.Run("global", func(t *testing.T) {
		clock := automaticconformance.NewClock(start)
		ledger, err := automaticstore.New(t.TempDir(), policy, clock)
		if err != nil {
			t.Fatal(err)
		}
		for i := range learning.MaxAutomaticReservationRecords {
			if _, err = ledger.Reserve(context.Background(), automaticRequest(t, fmt.Sprintf("global-%d", i), automaticInput{fmt.Sprintf("owner-%d", i), fmt.Sprintf("digest-%d", i), 1}, policy)); err != nil {
				t.Fatalf("Reserve %d: %v", i, err)
			}
			clock.Set(clock.Now().Add(policy.Window))
		}
		_, err = ledger.Reserve(context.Background(), automaticRequest(t, "global-overflow", automaticInput{"overflow", "overflow", 1}, policy))
		if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
			t.Fatalf("global overflow error = %v, want ErrAutomaticAdmissionLimit", err)
		}
	})
}

func TestADR_0259_AutomaticAdmissionControlsAreProcessIndependent(t *testing.T) {
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
			clock := automaticconformance.NewClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
			clients := newIndependentAutomaticClients(t, check.policy, clock)
			if _, err := clients[0].Reserve(context.Background(), automaticRequest(t, check.name+"-first", check.first, check.policy)); err != nil {
				t.Fatalf("first Reserve: %v", err)
			}
			clock.Set(clock.Now().Add(time.Second))
			_, err := clients[1].Reserve(context.Background(), automaticRequest(t, check.name+"-second", check.second, check.policy))
			if !errors.Is(err, check.want) {
				t.Fatalf("second independent-client Reserve error = %v, want %v", err, check.want)
			}
		})
	}

	t.Run("concurrent replicas do not multiply the global maximum", func(t *testing.T) {
		policy := automaticPolicy(1, 100, 10, 100, 0)
		clients := newIndependentAutomaticClients(t, policy, automaticconformance.NewClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)))
		start := make(chan struct{})
		results := make(chan error, len(clients))
		var wg sync.WaitGroup
		for i, client := range clients {
			wg.Add(1)
			go func(i int, client learning.AutomaticAdmissionLedger) {
				defer wg.Done()
				<-start
				_, err := client.Reserve(context.Background(), automaticRequest(t, fmt.Sprintf("race-%d", i), automaticInput{fmt.Sprintf("p-%d", i), fmt.Sprintf("digest-%d", i), 10}, policy))
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
	return learning.AutomaticAdmissionPolicy{Window: time.Hour, Cooldown: cooldown, DedupeWindow: 24 * time.Hour, MaxCount: globalCount, MaxTokens: globalTokens, MaxCountPerPrincipal: principalCount, MaxTokensPerPrincipal: principalTokens, ReservationClaimDuration: 5 * time.Minute}
}

func automaticRequest(t *testing.T, suffix string, in automaticInput, policy learning.AutomaticAdmissionPolicy) learning.AutomaticReservationRequest {
	t.Helper()
	attempt := learning.AttemptID("attempt-" + suffix)
	id, err := learning.AutomaticReservationIDForAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := learning.AutomaticAdmissionPolicyRevisionFor(policy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(in.digest))
	return learning.AutomaticReservationRequest{ID: id, AttemptID: attempt, Principal: learning.AttemptPartition("partition-" + in.principal), Digest: learning.CanonicalDigest(fmt.Sprintf("%x", sum)), Class: learning.AdmissionWeighted, Tokens: in.tokens, ExpectedPolicyRevision: revision}
}

func newIndependentAutomaticClients(t *testing.T, policy learning.AutomaticAdmissionPolicy, clock *automaticconformance.Clock) []learning.AutomaticAdmissionLedger {
	t.Helper()
	dir := t.TempDir()
	clients := make([]learning.AutomaticAdmissionLedger, 0, 8)
	for range 8 {
		backend, err := automaticstore.New(dir, policy, clock)
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
