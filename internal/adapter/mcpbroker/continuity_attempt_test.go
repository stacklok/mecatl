package mcpbroker

import (
	"errors"
	"testing"
	"time"

	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestCredentialCustodyRejectsZeroPrincipalPartitionsAtEveryCustodyOperation(t *testing.T) {
	fixture := newFixture(t)
	for _, field := range []string{"owner", "workload"} {
		request := fixture.request
		if field == "owner" {
			request.Guard.OwnerPartition = [32]byte{}
		} else {
			request.Guard.WorkloadPartition = [32]byte{}
		}
		if _, err := fixture.core.Stage(t.Context(), request, "tsid"); !errors.Is(err, errCustodyUnavailable) {
			t.Fatalf("Stage with zero %s partition = %v", field, err)
		}
	}
	staged, err := fixture.core.Stage(t.Context(), fixture.request, "tsid")
	if err != nil {
		t.Fatalf("Stage valid custody: %v", err)
	}
	assertion := custodyAssertion{custodyRequest: fixture.request, Recovery: staged.Recovery}
	assertion.Guard.OwnerPartition = [32]byte{}
	if err := fixture.core.Commit(t.Context(), assertion); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("Commit with zero owner partition = %v", err)
	}
	if _, err := fixture.core.Load(t.Context(), assertion); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("Recover Load with zero owner partition = %v", err)
	}
}

func TestCredentialCustodyStageRejectsAttemptPastTwoMinutes(t *testing.T) {
	fixture := newFixture(t)
	request := fixture.request
	request.AttemptDeadline = fixture.clock.now.Add(contract.ContinuityAttemptTTL + time.Nanosecond)
	if _, err := fixture.core.Stage(t.Context(), request, "tsid"); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("Stage overlong attempt error = %v", err)
	}
}
