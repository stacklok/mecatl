package main

import (
	"strings"
	"testing"
	"time"
)

func TestSessionStorageContinuity_Scenario6_EmbeddedAndRemotePolicyTruth(t *testing.T) {
	t.Parallel()
	local, err := parseFlags([]string{"--child-retention=48h", "--main-retention=0", "--retention-sweep-cadence=30m"})
	if err != nil {
		t.Fatal(err)
	}
	appCfg := embeddedConfig(local, nil)
	if appCfg.ChildRetention != 48*time.Hour || appCfg.MainRetention != 0 || appCfg.ChildGCInterval != 30*time.Minute {
		t.Fatalf("embedded effective retention = %+v", appCfg)
	}
	_, _, err = parseTransportFlags(modeConnect, &strings.Builder{}, []string{"--main-retention=24h"})
	if err == nil || !strings.Contains(err.Error(), "not applicable") {
		t.Fatalf("connected local-policy flag error = %v", err)
	}
}
