package lease_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/lease"
)

func TestHoldCausesAreDistinct(t *testing.T) {
	causes := []error{lease.ErrReleased, lease.ErrExpired, lease.ErrSuperseded, lease.ErrValidityUnknown, lease.ErrNotMember}
	for i, cause := range causes {
		wrapped := fmt.Errorf("lease operation: %w", cause)
		for j, other := range causes {
			if errors.Is(wrapped, other) != (i == j) {
				t.Fatalf("cause %v conflated with %v", cause, other)
			}
		}
	}
}
