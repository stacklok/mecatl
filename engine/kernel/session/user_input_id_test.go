package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestUserInputID(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	first, second := allocatedInput(t, run, 1), allocatedInput(t, run, 2)
	copied := first
	if first.Validate() != nil || first.RunID() != run || !first.Equal(copied) || first == second {
		t.Fatal("allocated input identity or scope")
	}
	if (session.UserInputID{}).Validate() == nil {
		t.Fatal("zero input identity validated")
	}
}
