package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestMecatedLogLevelDefaultsToInfo(t *testing.T) {
	_, cfg, err := parseFlagsModeOut(modeServe, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logLevel != slog.LevelInfo || cfg.logLevelWarning != "" {
		t.Fatalf("default log level = %v, warning %q; want info, no warning", cfg.logLevel, cfg.logLevelWarning)
	}
}

func TestMecatedLogLevelFlagWiresParser(t *testing.T) {
	_, cfg, err := parseFlagsModeOut(modeServe, []string{"--log-level=debug"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logLevel != slog.LevelDebug || cfg.logLevelWarning != "" {
		t.Fatalf("parsed log level = %v, warning %q; want debug, no warning", cfg.logLevel, cfg.logLevelWarning)
	}
}

func TestMecatedInvalidLogLevelIsFailSoft(t *testing.T) {
	_, cfg, err := parseFlagsModeOut(modeServe, []string{"--log-level="}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logLevel != slog.LevelInfo || cfg.logLevelWarning == "" {
		t.Fatalf("invalid log level = %v, warning %q; want info and warning", cfg.logLevel, cfg.logLevelWarning)
	}
}
