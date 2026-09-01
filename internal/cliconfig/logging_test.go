package cliconfig

import (
	"bytes"
	"context"
	"flag"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func TestParseLogLevelExactTokens(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug}, {"info", slog.LevelInfo}, {"warn", slog.LevelWarn}, {"error", slog.LevelError},
	} {
		got, ok := ParseLogLevel(tc.value)
		if !ok || got != tc.want {
			t.Errorf("ParseLogLevel(%q) = %v, %v; want %v, true", tc.value, got, ok, tc.want)
		}
	}
	for _, value := range []string{"", "DEBUG", "warning", "info "} {
		if _, ok := ParseLogLevel(value); ok {
			t.Errorf("ParseLogLevel(%q) accepted invalid token", value)
		}
	}
}

func TestNewTextLoggerEmitsOneInvalidLevelWarning(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	flags := RegisterLogLevelFlag(fs)
	if err := fs.Parse([]string{"--log-level=bogus"}); err != nil {
		t.Fatal(err)
	}
	level, warning := flags.Resolve()
	var output bytes.Buffer
	NewTextLogger(&output, level, warning)
	if got := strings.Count(output.String(), "level=WARN msg=\"invalid --log-level"); got != 1 {
		t.Fatalf("invalid-level WARN count = %d, want 1; output %q", got, output.String())
	}
}

func TestNewTextLoggerFiltersAmbientAndDiagnosticsTogether(t *testing.T) {
	var output bytes.Buffer
	logger := NewTextLogger(&output, slog.LevelWarn, "")
	slogdiag.NewFromLogger(logger).Log(context.Background(), port.LevelInfo, "diagnostic info")
	slogdiag.NewFromLogger(logger).Log(context.Background(), port.LevelWarn, "diagnostic warn")
	logger.Info("ambient info")
	logger.Warn("ambient warn")
	if strings.Contains(output.String(), "info") {
		t.Fatalf("info record passed warn filter: %q", output.String())
	}
	if got := strings.Count(output.String(), "diagnostic warn") + strings.Count(output.String(), "ambient warn"); got != 2 {
		t.Fatalf("warn records = %d, want 2; output %q", got, output.String())
	}
}
