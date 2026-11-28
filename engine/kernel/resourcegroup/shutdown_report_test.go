package resourcegroup_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
)

func TestShutdownObligationCauseSafeText(t *testing.T) {
	for cause, want := range map[resourcegroup.ShutdownObligationCause]string{
		resourcegroup.ShutdownCauseUnknown:          "resource group shutdown cause unknown",
		resourcegroup.ShutdownCauseDeadline:         "resource group shutdown deadline elapsed",
		resourcegroup.ShutdownCauseOperationBlocked: "resource group shutdown operation blocked",
		resourcegroup.ShutdownCauseStillActive:      "resource group shutdown obligation remains active",
		resourcegroup.ShutdownObligationCause(255):  "resource group shutdown cause unknown",
	} {
		if got := cause.Error(); got != want {
			t.Fatalf("cause %d Error() = %q, want %q", cause, got, want)
		}
		if errors.Unwrap(cause) != nil {
			t.Fatal("cause unwraps a private error")
		}
	}
}
