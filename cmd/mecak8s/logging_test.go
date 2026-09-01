package main

import (
	"log/slog"
	"testing"
)

func TestMecak8sLogLevelDefaultsToInfo(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logLevel != slog.LevelInfo || cfg.logLevelWarning != "" {
		t.Fatalf("default log level = %v, warning %q; want info, no warning", cfg.logLevel, cfg.logLevelWarning)
	}
}

func TestMecak8sLogLevelFlagWiresParser(t *testing.T) {
	cfg, err := parseFlags([]string{"--log-level=error"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logLevel != slog.LevelError || cfg.logLevelWarning != "" {
		t.Fatalf("parsed log level = %v, warning %q; want error, no warning", cfg.logLevel, cfg.logLevelWarning)
	}
}

func TestMecak8sInvalidLogLevelIsFailSoft(t *testing.T) {
	cfg, err := parseFlags([]string{"--log-level="})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.logLevel != slog.LevelInfo || cfg.logLevelWarning == "" {
		t.Fatalf("invalid log level = %v, warning %q; want info and warning", cfg.logLevel, cfg.logLevelWarning)
	}
}
