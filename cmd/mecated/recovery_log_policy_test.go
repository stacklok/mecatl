package main

import (
	"github.com/stacklok/mecatl/internal/testutil/recoveryhost"
	"testing"
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
