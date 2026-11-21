package sessiongroup_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/sessiongroup"
)

func TestShutdownObligationCauseErrorIsSafeAndFixed(t *testing.T) {
	for cause, want := range map[sessiongroup.ShutdownObligationCause]string{
		sessiongroup.ShutdownCauseUnknown:          "session group shutdown cause unknown",
		sessiongroup.ShutdownCauseDeadline:         "session group shutdown deadline elapsed",
		sessiongroup.ShutdownCauseOperationBlocked: "session group shutdown operation blocked",
		sessiongroup.ShutdownCauseStillActive:      "session group shutdown obligation remains active",
		sessiongroup.ShutdownObligationCause(255):  "session group shutdown cause unknown",
	} {
		if got := cause.Error(); got != want {
			t.Errorf("ShutdownObligationCause(%d).Error() = %q, want %q", cause, got, want)
		}
	}
}
