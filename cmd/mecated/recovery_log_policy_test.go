package main

import (
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/recoveryhost"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	if args, child := recoveryhost.ChildArgs(t); child {
		if err := run(modeServe, args); err != nil {
			t.Fatal(err)
		}
		return
	}
	recoveryhost.Daemon(t, []string{"--metrics-addr=", "--flight-recorder=false"})
}

func TestServerProviderRecovery_Scenario6_NoDetachedOrRestartContinuation(t *testing.T) {
	recoveryhost.DaemonShutdown(t, []string{"--metrics-addr=", "--flight-recorder=false"})
}
