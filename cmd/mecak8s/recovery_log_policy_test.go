package main

import (
	"os"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/recoveryhost"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	if args, child := recoveryhost.ChildArgs(t); child {
		os.Args = append([]string{"mecak8s"}, args...)
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	recoveryhost.Daemon(t, []string{"--session-lease-k8s-namespace="})
}
