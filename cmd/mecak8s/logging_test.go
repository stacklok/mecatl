package main

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

func TestMecak8sCallerAuthenticationPosture(t *testing.T) {
	ordinaryTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	for _, tc := range []struct {
		name     string
		cfg      config
		tls      *tls.Config
		wantWarn bool
	}{
		{name: "TLS only warns", tls: ordinaryTLS, wantWarn: true},
		{name: "bearer authenticates", cfg: config{authToken: "token"}, tls: ordinaryTLS},
		{name: "OIDC authenticates", cfg: config{oidc: cliconfig.OIDCConfig{Issuer: "https://issuer.example"}}, tls: ordinaryTLS},
		{name: "verified mTLS authenticates", tls: &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			callerAuthenticated := callerAuthenticationConfigured(tc.cfg, tc.tls)
			warnIfNonLoopback("grpc-addr", "100.64.0.10:9080", callerAuthenticated)
			got := logs.String()
			warned := strings.Contains(got, "NO caller authentication")
			if warned != tc.wantWarn {
				t.Fatalf("warning=%v, want %v; logs: %s", warned, tc.wantWarn, got)
			}
			if tc.wantWarn && !strings.Contains(got, "TLS alone is not caller authentication") {
				t.Fatalf("TLS-only posture omitted warning rationale: %s", got)
			}
			if !tc.wantWarn && !strings.Contains(got, "WITH caller authentication") {
				t.Fatalf("authenticated posture not reported: %s", got)
			}
		})
	}
}

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
