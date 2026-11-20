package sessiongroup_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/sessiongroup"
)

func TestCloseObligationCauseErrorIsSafeAndFixed(t *testing.T) {
	for cause, want := range map[sessiongroup.CloseObligationCause]string{
		sessiongroup.CloseCauseUnknown:          "session group close cause unknown",
		sessiongroup.CloseCauseDeadline:         "session group close deadline elapsed",
		sessiongroup.CloseCauseOperationBlocked: "session group close operation blocked",
		sessiongroup.CloseCauseStillActive:      "session group close obligation remains active",
		sessiongroup.CloseObligationCause(255):  "session group close cause unknown",
	} {
		if got := cause.Error(); got != want {
			t.Errorf("CloseObligationCause(%d).Error() = %q, want %q", cause, got, want)
		}
	}
}
