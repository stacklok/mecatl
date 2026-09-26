package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func TestMecabrokerAmbientSlogIsDiscarded(t *testing.T) {
	previous := slog.Default()
	defer slog.SetDefault(previous)
	var ambient bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&ambient, nil)))
	installAmbientSlogDiscard()
	slog.Info("ambient-info")
	slog.Error("ambient-error")
	if ambient.Len() != 0 {
		t.Fatalf("ambient slog output = %q", ambient.String())
	}
}

// TestMecabrokerInjectedDiagnosticsRemainVisible mirrors main's order: build the
// operator logger, then discard ambient slog. The injected logger must still write.
func TestMecabrokerInjectedDiagnosticsRemainVisible(t *testing.T) {
	previous := slog.Default()
	defer slog.SetDefault(previous)
	var output bytes.Buffer
	logger := cliconfig.NewTextLogger(&output, slog.LevelInfo, "")
	installAmbientSlogDiscard()
	diagnostics := slogdiag.NewFromLogger(logger)
	diagnostics.Log(t.Context(), port.LevelInfo, "injected-diagnostic")
	slog.Info("ambient-after-discard")
	if !strings.Contains(output.String(), "injected-diagnostic") {
		t.Fatalf("injected diagnostics = %q", output.String())
	}
	if strings.Contains(output.String(), "ambient-after-discard") {
		t.Fatalf("ambient slog reached operator output: %q", output.String())
	}
}
func TestMecabrokerStartupErrorsUseClosedVocabulary(t *testing.T) {
	secret := errors.New("redis password=super-secret tsid=private")
	got := publicStartupError(errors.Join(errors.New("broker listener stopped"), secret))
	if got.Error() != "broker listener stopped" {
		t.Fatalf("startup error = %q", got)
	}
	if strings.Contains(got.Error(), "super-secret") || strings.Contains(got.Error(), "tsid") {
		t.Fatalf("startup error leaked dependency detail: %q", got)
	}
}
