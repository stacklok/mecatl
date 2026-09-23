package mcpbroker

import (
	"errors"
	"testing"
	"time"

	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestCredentialCustodyStageRejectsAttemptPastTwoMinutes(t *testing.T) {
	fixture := newFixture(t)
	request := fixture.request
	request.AttemptDeadline = fixture.clock.now.Add(contract.ContinuityAttemptTTL + time.Nanosecond)
	if _, err := fixture.core.Stage(t.Context(), request, "tsid"); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("Stage overlong attempt error = %v", err)
	}
}
