package lease_test

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/lease"
)

func TestValidity(t *testing.T) {
	var zero lease.Validity
	if !zero.Until.IsZero() {
		t.Fatal("zero evidence must be unusable")
	}
	until := time.Date(2026, time.November, 19, 0, 0, 0, 0, time.UTC)
	v := lease.Validity{Until: until}
	if v.Until != until {
		t.Fatal("validity changed explicit local clock evidence")
	}
}
