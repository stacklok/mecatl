package main

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/testutil/mcpauth"
)

func TestMecak8sMCPLogicalCredentials(t *testing.T) {
	mcpauth.CheckRoot(t, func(t *testing.T, path string, diag port.Diagnostics) app.Config {
		cfg, err := parseFlags([]string{"--api-key-file", path})
		if err != nil {
			t.Fatal(err)
		}
		return appConfig(cfg, diag, observability{})
	})
}
