// Package automaticconformance provides the shared distributed-accounting
// contract for learning.AutomaticAdmissionLedger adapters. Implementations must
// run this suite; storage layout, transport, and scheduling are not part of it.
package automaticconformance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

// Factory returns a fresh, isolated ledger for each subtest.
type Factory func(*testing.T) learning.AutomaticAdmissionLedger

// Run executes the complete automatic-admission ledger contract.
//
//nolint:gocyclo // one visible suite keeps adapter coverage auditable
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("deterministic attempt linkage idempotency and global dedupe", func(t *testing.T) {
		ledger := factory(t)
		now := baseTime()
		first := request(t, "same-attempt", "a", "1", learning.AdmissionWeighted, 10, now, policy())
		created := mustReserve(t, ledger, first)
		retry := first
		retry.Now = now.Add(time.Second)
		again := mustReserve(t, ledger, retry)
		if created != again {
			t.Fatalf("idempotent Reserve changed record: first=%+v again=%+v", created, again)
		}
		changed := retry
		changed.Tokens++
		if _, err := ledger.Reserve(context.Background(), changed); !errors.Is(err, learning.ErrAutomaticReservationConflict) {
			t.Fatalf("same-id immutable conflict error = %v, want ErrAutomaticReservationConflict", err)
		}
		duplicate := request(t, "other-attempt", "b", "1", learning.AdmissionWeighted, 10, now, policy())
		duplicate.Digest = first.Digest
		if _, err := ledger.Reserve(context.Background(), duplicate); !errors.Is(err, learning.ErrAutomaticAdmissionDuplicate) {
			t.Fatalf("duplicate digest error = %v, want ErrAutomaticAdmissionDuplicate", err)
		}
	})

	t.Run("global and principal count and token windows", func(t *testing.T) {
		tests := []struct {
			name   string
			policy learning.AutomaticAdmissionPolicy
			first  struct {
				principal, digest string
				tokens            uint64
			}
			second struct {
				principal, digest string
				tokens            uint64
			}
		}{
			{name: "global count", policy: withLimits(1, 1000, 10, 1000), first: entry("a", "1", 10), second: entry("b", "2", 10)},
			{name: "global tokens", policy: withLimits(10, 15, 10, 1000), first: entry("a", "1", 10), second: entry("b", "2", 6)},
			{name: "principal count", policy: withLimits(10, 1000, 1, 1000), first: entry("a", "1", 10), second: entry("a", "2", 10)},
			{name: "principal tokens", policy: withLimits(10, 1000, 10, 15), first: entry("a", "1", 10), second: entry("a", "2", 6)},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				ledger := factory(t)
				now := baseTime()
				mustReserve(t, ledger, request(t, test.name+"-1", test.first.principal, test.first.digest, learning.AdmissionWeighted, test.first.tokens, now, test.policy))
				_, err := ledger.Reserve(context.Background(), request(t, test.name+"-2", test.second.principal, test.second.digest, learning.AdmissionWeighted, test.second.tokens, now, test.policy))
				if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
					t.Fatalf("second Reserve error = %v, want ErrAutomaticAdmissionLimit", err)
				}
			})
		}
	})

	t.Run("weighted cooldown does not collapse hard admission", func(t *testing.T) {
		ledger := factory(t)
		now := baseTime()
		cfg := policy()
		mustReserve(t, ledger, request(t, "weighted", "a", "1", learning.AdmissionWeighted, 10, now, cfg))
		if _, err := ledger.Reserve(context.Background(), request(t, "cooled", "a", "2", learning.AdmissionWeighted, 10, now.Add(time.Second), cfg)); !errors.Is(err, learning.ErrAutomaticAdmissionCooldown) {
			t.Fatalf("weighted cooldown error = %v, want ErrAutomaticAdmissionCooldown", err)
		}
		hard := mustReserve(t, ledger, request(t, "hard", "a", "3", learning.AdmissionHard, 10, now.Add(time.Second), cfg))
		if hard.Class != learning.AdmissionHard {
			t.Fatalf("hard reservation class = %q", hard.Class)
		}
	})

	t.Run("reassignment fences expired owners without adding a charge", func(t *testing.T) {
		ledger := factory(t)
		now := baseTime()
		cfg := withLimits(1, 100, 1, 100)
		first := mustReserve(t, ledger, request(t, "reassign", "a", "1", learning.AdmissionWeighted, 10, now, cfg))
		if _, err := ledger.Reassign(context.Background(), first.ID, first.Version, now, now.Add(time.Minute)); !errors.Is(err, learning.ErrAutomaticReservationFence) {
			t.Fatalf("live Reassign error = %v, want ErrAutomaticReservationFence", err)
		}
		successor, err := ledger.Reassign(context.Background(), first.ID, first.Version, first.Fence.ExpiresAt, first.Fence.ExpiresAt.Add(cfg.ReservationClaimDuration))
		if err != nil {
			t.Fatal(err)
		}
		if successor.Fence.Generation <= first.Fence.Generation || successor.ReservedAt != first.ReservedAt || successor.ChargeExpiresAt != first.ChargeExpiresAt {
			t.Fatalf("reassignment changed charge or failed to advance fence: first=%+v successor=%+v", first, successor)
		}
		if _, err = ledger.Retain(context.Background(), successor.ID, successor.Version, first.Fence, successor.Fence.ExpiresAt.Add(-time.Second)); !errors.Is(err, learning.ErrAutomaticReservationFence) {
			t.Fatalf("stale fence Retain error = %v, want ErrAutomaticReservationFence", err)
		}
		retained, err := ledger.Retain(context.Background(), successor.ID, successor.Version, successor.Fence, successor.Fence.ExpiresAt.Add(-time.Second))
		if err != nil || retained.Charge != learning.AutomaticChargeRetained || !retained.AttemptCreated {
			t.Fatalf("Retain = %+v, err=%v", retained, err)
		}
		if _, err = ledger.Reserve(context.Background(), request(t, "replacement", "b", "2", learning.AdmissionHard, 10, now.Add(2*time.Minute), cfg)); !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
			t.Fatalf("reassigned reservation admitted a replacement: %v", err)
		}
	})

	t.Run("reclaimed charge releases effects while retained charge ages out", func(t *testing.T) {
		ledger := factory(t)
		now := baseTime()
		cfg := withLimits(1, 100, 1, 100)
		cfg.Cooldown = 10 * time.Minute
		firstReq := request(t, "reclaim", "a", "1", learning.AdmissionWeighted, 10, now, cfg)
		first := mustReserve(t, ledger, firstReq)
		reclaimed, err := ledger.Reclaim(context.Background(), first.ID, first.Version, first.Fence, now.Add(time.Second))
		if err != nil || reclaimed.Charge != learning.AutomaticChargeReclaimed || reclaimed.AttemptCreated {
			t.Fatalf("Reclaim = %+v, err=%v", reclaimed, err)
		}
		replacement := request(t, "replacement", "a", "1", learning.AdmissionWeighted, 10, now.Add(2*time.Second), cfg)
		replacement.Digest = firstReq.Digest
		second := mustReserve(t, ledger, replacement)
		retained, err := ledger.Retain(context.Background(), second.ID, second.Version, second.Fence, now.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ledger.Reclaim(context.Background(), retained.ID, retained.Version, retained.Fence, now.Add(4*time.Second)); !errors.Is(err, learning.ErrAutomaticReservationState) {
			t.Fatalf("retained Reclaim error = %v, want ErrAutomaticReservationState", err)
		}
		beforeDedupeExpiry := request(t, "duplicate", "b", "1", learning.AdmissionHard, 10, retained.ChargeExpiresAt, cfg)
		beforeDedupeExpiry.Digest = retained.Digest
		if _, err = ledger.Reserve(context.Background(), beforeDedupeExpiry); !errors.Is(err, learning.ErrAutomaticAdmissionDuplicate) {
			t.Fatalf("retained digest before expiry error = %v, want ErrAutomaticAdmissionDuplicate", err)
		}
		after := request(t, "after-dedupe", "b", "2", learning.AdmissionHard, 10, retained.DedupeExpiresAt, cfg)
		mustReserve(t, ledger, after)
	})

	t.Run("opaque CAS and concurrent global fencing permit one winner", func(t *testing.T) {
		ledger := factory(t)
		now := baseTime()
		cfg := withLimits(1, 100, 10, 100)
		created := mustReserve(t, ledger, request(t, "cas", "a", "1", learning.AdmissionHard, 10, now, cfg))
		if _, err := ledger.Retain(context.Background(), created.ID, learning.AutomaticReservationVersion("stale"), created.Fence, now.Add(time.Second)); !errors.Is(err, learning.ErrAutomaticReservationVersion) {
			t.Fatalf("stale Retain error = %v, want ErrAutomaticReservationVersion", err)
		}
		stored, found, err := ledger.Get(context.Background(), created.ID)
		if err != nil || !found || stored != created {
			t.Fatalf("stale CAS changed record: found=%v got=%+v err=%v", found, stored, err)
		}

		ledger = factory(t)
		const contenders = 16
		requests := make([]learning.AutomaticReservationRequest, contenders)
		for i := range contenders {
			requests[i] = request(t, fmt.Sprintf("race-%d", i), fmt.Sprintf("p-%d", i), fmt.Sprintf("%x", i+1), learning.AdmissionHard, 10, now, cfg)
		}
		start := make(chan struct{})
		results := make(chan error, contenders)
		var wg sync.WaitGroup
		for i := range contenders {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, reserveErr := ledger.Reserve(context.Background(), requests[i])
				results <- reserveErr
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		successes, limited := 0, 0
		for result := range results {
			switch {
			case result == nil:
				successes++
			case errors.Is(result, learning.ErrAutomaticAdmissionLimit):
				limited++
			default:
				t.Fatalf("concurrent Reserve error = %v", result)
			}
		}
		if successes != 1 || limited != contenders-1 {
			t.Fatalf("concurrent Reserve successes=%d limited=%d", successes, limited)
		}
	})
}

func policy() learning.AutomaticAdmissionPolicy {
	return learning.AutomaticAdmissionPolicy{
		Window: time.Hour, Cooldown: time.Minute, DedupeWindow: 24 * time.Hour,
		MaxCount: 10, MaxTokens: 1000, MaxCountPerPrincipal: 10, MaxTokensPerPrincipal: 1000,
		ReservationClaimDuration: 5 * time.Minute,
	}
}

func withLimits(globalCount, globalTokens, principalCount, principalTokens uint64) learning.AutomaticAdmissionPolicy {
	cfg := policy()
	cfg.MaxCount, cfg.MaxTokens = globalCount, globalTokens
	cfg.MaxCountPerPrincipal, cfg.MaxTokensPerPrincipal = principalCount, principalTokens
	cfg.Cooldown = 0
	return cfg
}

func entry(principal, digest string, tokens uint64) struct {
	principal, digest string
	tokens            uint64
} {
	return struct {
		principal, digest string
		tokens            uint64
	}{principal, digest, tokens}
}

func request(t *testing.T, suffix, principal, digest string, class learning.AdmissionClass, tokens uint64, now time.Time, cfg learning.AutomaticAdmissionPolicy) learning.AutomaticReservationRequest {
	t.Helper()
	attempt := learning.AttemptID("attempt-" + suffix)
	id, err := learning.AutomaticReservationIDForAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	digestSum := sha256.Sum256([]byte(digest))
	return learning.AutomaticReservationRequest{
		ID: id, AttemptID: attempt, Principal: learning.AttemptPartition("partition-" + principal),
		Digest: learning.CanonicalDigest(fmt.Sprintf("%x", digestSum)), Class: class, Tokens: tokens, Now: now, Policy: cfg,
	}
}

func mustReserve(t *testing.T, ledger learning.AutomaticAdmissionLedger, req learning.AutomaticReservationRequest) learning.AutomaticReservation {
	t.Helper()
	record, err := ledger.Reserve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err = record.Validate(); err != nil {
		t.Fatalf("invalid reservation: %v (%+v)", err, record)
	}
	return record
}

func baseTime() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }
