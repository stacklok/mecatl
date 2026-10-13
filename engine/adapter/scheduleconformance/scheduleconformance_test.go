package scheduleconformance_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/scheduleconformance"
	"github.com/stacklok/mecatl/engine/port"
)

// TestScheduleConformanceSelfTest proves the suite itself is sound by running
// it against the in-memory reference store (the leaseconformance_test.go
// precedent — a shared suite ships with a self-test against its reference
// adapter so a suite bug is caught in the suite's own package, not only in an
// adapter's).
func TestScheduleConformanceSelfTest(t *testing.T) {
	scheduleconformance.RunClaimAndRetryConformance(t, func(*testing.T) port.ScheduleStore {
		return memschedulestore.New()
	})
}

// Run the failing suite in a subprocess to observe its failures. Deliberately
// bypassing the unresolved-claim rejection keeps this negative control valid
// even when the reference backend later adopts claim and retry safety.
func TestClaimAndRetryConformanceRejectsPendingBypass(t *testing.T) {
	const childEnv = "MECATL_TEST_CLAIM_AND_RETRY_BYPASS"
	if os.Getenv(childEnv) == "1" {
		scheduleconformance.RunClaimAndRetryConformance(t, func(*testing.T) port.ScheduleStore {
			return pendingBypassStore{ScheduleStore: memschedulestore.New()}
		})
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestClaimAndRetryConformanceRejectsPendingBypass$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("expected suite failure, got %v:\n%s", err, output)
	}
	for _, diagnostic := range []string{
		"unresolved prior claim accepted later claim",
		"--- FAIL: TestClaimAndRetryConformanceRejectsPendingBypass/claims_reject_unresolved_prior_claims_and_preserve_later_claims/manual=true/pending",
		"--- PASS: TestClaimAndRetryConformanceRejectsPendingBypass/save/load_round-trips_spec_and_state",
	} {
		if !strings.Contains(string(output), diagnostic) {
			t.Errorf("missing diagnostic %q:\n%s", diagnostic, output)
		}
	}
}

type pendingBypassStore struct {
	port.ScheduleStore
}

func (s pendingBypassStore) ClaimNow(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	claimed, err := s.ScheduleStore.ClaimNow(ctx, name, now, nextFire)
	if errors.Is(err, port.ErrScheduleNotFound) {
		return s.Load(ctx, name)
	}
	return claimed, err
}
