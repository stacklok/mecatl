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
	scheduleconformance.Run(t, func(*testing.T) port.ScheduleStore {
		return memschedulestore.New()
	})
}

// Run the failing suite in a subprocess to observe its failures. Deliberately
// bypassing ClaimNow rejection keeps this negative control valid even when the
// reference backend later adopts execution fencing.
func TestExecutionFencedRejectsPendingBypass(t *testing.T) {
	const childEnv = "MECATL_TEST_EXECUTION_FENCED_BYPASS"
	if os.Getenv(childEnv) == "1" {
		scheduleconformance.RunExecutionFenced(t, func(*testing.T) port.ScheduleStore {
			return pendingBypassStore{ScheduleStore: memschedulestore.New()}
		})
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestExecutionFencedRejectsPendingBypass$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("expected suite failure, got %v:\n%s", err, output)
	}
	for _, diagnostic := range []string{
		"unresolved predecessor bypassed",
		"--- FAIL: TestExecutionFencedRejectsPendingBypass/claims_fence_unresolved_predecessors_and_late_settlement/manual=true/pending",
		"--- PASS: TestExecutionFencedRejectsPendingBypass/save/load_round-trips_spec_and_state",
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
