package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	var sink bytes.Buffer
	diag := slogdiag.NewFromLogger(cliconfig.NewTextLogger(&sink, slog.LevelInfo, ""))
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	composed := appConfig(cfg, nil, nil, nil, nil, diag)
	composed.Diagnostics.Log(context.Background(), port.LevelInfo, "recovery decision", "decision", "recovered")
	if !strings.Contains(sink.String(), "recovery decision") {
		t.Fatalf("daemon operational sink did not receive recovery log: %q", &sink)
	}
}
