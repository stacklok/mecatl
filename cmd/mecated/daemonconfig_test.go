package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/daemonconfig"
)

// --- Daemon config integration tests (issue #338) ---

// writeTempConfig creates a temp YAML file with the given content and returns its
// path. The caller is responsible for cleanup (via t.TempDir).
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDaemonConfigACPFlagSet: --config in ACP mode must be tracked via
// configPathFlagSet so run() can reject it at merge time. parseFlagsMode alone
// does not reject it (the flag itself is parseable).
func TestDaemonConfigACPFlagSet(t *testing.T) {
	path := writeTempConfig(t, "version: v1\n")

	cfg, err := parseFlagsMode(modeACP, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode error: %v", err)
	}
	if !cfg.configPathFlagSet {
		t.Fatal("configPathFlagSet should be true when --config is passed")
	}
	if cfg.configPath != path {
		t.Errorf("configPath = %q, want %q", cfg.configPath, path)
	}
}

// TestDaemonConfigPrecedenceCliWinsOverFile: explicit CLI flag values override
// config file values for every migrated field.
func TestDaemonConfigPrecedenceCliWinsOverFile(t *testing.T) {
	path := writeTempConfig(t, `version: v1
grpc_addr: "file:9090"
http_addr: "file:9091"
metrics_addr: "file:9092"
tls_cert: "/file/cert.pem"
tls_key: "/file/key.pem"
client_ca: "/file/ca.pem"
rate_limit: 50
rate_burst: 100
`)

	// Parse with CLI flags overriding every file value.
	cfg, err := parseFlagsMode(modeServe, []string{
		"--config", path,
		"--grpc-addr", "cli:8080",
		"--http-addr", "cli:8081",
		"--metrics-addr", "",
		"--tls-cert", "/cli/cert.pem",
		"--tls-key", "/cli/key.pem",
		"--client-ca", "/cli/ca.pem",
		"--rate-limit", "0",
		"--rate-burst", "0",
	})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	// Load the config file and merge.
	dc, derr := daemonconfig.Load(path)
	if derr != nil {
		t.Fatalf("daemonconfig.Load: %v", derr)
	}
	mergeDaemonConfig(&cfg, dc)

	// All values should be CLI-supplied (explicit), NOT file values.
	if cfg.grpcAddr != "cli:8080" {
		t.Errorf("grpcAddr = %q, want cli:8080 (CLI must win)", cfg.grpcAddr)
	}
	if cfg.httpAddr != "cli:8081" {
		t.Errorf("httpAddr = %q, want cli:8081 (CLI must win)", cfg.httpAddr)
	}
	if cfg.metricsAddr != "" {
		t.Errorf("metricsAddr = %q, want empty (CLI explicit empty must win)", cfg.metricsAddr)
	}
	if cfg.tlsCert != "/cli/cert.pem" {
		t.Errorf("tlsCert = %q, want /cli/cert.pem (CLI must win)", cfg.tlsCert)
	}
	if cfg.tlsKey != "/cli/key.pem" {
		t.Errorf("tlsKey = %q, want /cli/key.pem (CLI must win)", cfg.tlsKey)
	}
	if cfg.clientCA != "/cli/ca.pem" {
		t.Errorf("clientCA = %q, want /cli/ca.pem (CLI must win)", cfg.clientCA)
	}
	if cfg.rateLimit != 0 {
		t.Errorf("rateLimit = %v, want 0 (CLI explicit zero must win)", cfg.rateLimit)
	}
	if cfg.rateBurst != 0 {
		t.Errorf("rateBurst = %v, want 0 (CLI explicit zero must win)", cfg.rateBurst)
	}
}

// TestDaemonConfigFileFillsDefaults: when no CLI flag is set for a migrated field,
// the config file value fills in.
func TestDaemonConfigFileFillsDefaults(t *testing.T) {
	path := writeTempConfig(t, `version: v1
grpc_addr: "file:9090"
http_addr: "file:9091"
metrics_addr: "file:9092"
rate_limit: 10
rate_burst: 20
`)

	// Parse with ONLY --config, no other CLI flags for the migrated fields.
	cfg, err := parseFlagsMode(modeServe, []string{
		"--config", path,
		"--workspace", "/tmp/ws", // a non-migrated flag to ensure the parser works
	})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	dc, derr := daemonconfig.Load(path)
	if derr != nil {
		t.Fatalf("daemonconfig.Load: %v", derr)
	}

	mergeDaemonConfig(&cfg, dc)

	if cfg.grpcAddr != "file:9090" {
		t.Errorf("grpcAddr = %q, want file:9090 (from config file)", cfg.grpcAddr)
	}
	if cfg.httpAddr != "file:9091" {
		t.Errorf("httpAddr = %q, want file:9091 (from config file)", cfg.httpAddr)
	}
	if cfg.metricsAddr != "file:9092" {
		t.Errorf("metricsAddr = %q, want file:9092 (from config file)", cfg.metricsAddr)
	}
	if cfg.rateLimit != 10 {
		t.Errorf("rateLimit = %v, want 10 (from config file)", cfg.rateLimit)
	}
	if cfg.rateBurst != 20 {
		t.Errorf("rateBurst = %v, want 20 (from config file)", cfg.rateBurst)
	}
}

// TestDaemonConfigPartialOverride: some fields from CLI, some from config file.
func TestDaemonConfigPartialOverride(t *testing.T) {
	path := writeTempConfig(t, `version: v1
grpc_addr: "file:9090"
http_addr: "file:9091"
rate_limit: 10
`)

	// CLI overrides http-addr and rate-limit, leaves grpc-addr from file.
	cfg, err := parseFlagsMode(modeServe, []string{
		"--config", path,
		"--http-addr", "cli:8081",
		"--rate-limit", "0",
	})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	dc, derr := daemonconfig.Load(path)
	if derr != nil {
		t.Fatalf("daemonconfig.Load: %v", derr)
	}

	mergeDaemonConfig(&cfg, dc)

	if cfg.grpcAddr != "file:9090" {
		t.Errorf("grpcAddr = %q, want file:9090 (from config file)", cfg.grpcAddr)
	}
	if cfg.httpAddr != "cli:8081" {
		t.Errorf("httpAddr = %q, want cli:8081 (CLI override)", cfg.httpAddr)
	}
	if cfg.rateLimit != 0 {
		t.Errorf("rateLimit = %v, want 0 (CLI explicit zero overrides file)", cfg.rateLimit)
	}
}

// TestDaemonConfigZeroConfigNoFlag: when --config is not passed, behavior is
// byte-identical to before.
func TestDaemonConfigZeroConfigNoFlag(t *testing.T) {
	cfg, err := parseFlagsMode(modeServe, []string{
		"--grpc-addr", "0.0.0.0:9090",
	})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	if cfg.configPathFlagSet {
		t.Error("configPathFlagSet should be false when --config not passed")
	}
	if cfg.grpcAddr != "0.0.0.0:9090" {
		t.Errorf("grpcAddr = %q, want 0.0.0.0:9090", cfg.grpcAddr)
	}
}

// TestDaemonConfigPresenceSemantics: an absent field in the config file leaves the
// CLI default untouched; a PRESENT field in the file overrides the default. The
// rate_limit oracle is a NON-default value (7), because the CLI default is 0 —
// asserting `rate_limit: 0` => 0 is vacuous (it passes whether the file value was
// applied or ignored). A non-zero file value proves the merge actually read and
// applied the file. The explicit-zero override cases (CLI --rate-limit 0 over a
// non-zero file) are covered separately by TestDaemonConfigExplicitZeroOverrides
// and TestDaemonConfigPrecedenceCliWinsOverFile.
func TestDaemonConfigPresenceSemantics(t *testing.T) {
	path := writeTempConfig(t, `version: v1
rate_limit: 7
metrics_addr: ""
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	dc, derr := daemonconfig.Load(path)
	if derr != nil {
		t.Fatalf("daemonconfig.Load: %v", derr)
	}

	mergeDaemonConfig(&cfg, dc)

	if cfg.rateLimit != 7 {
		t.Errorf("rateLimit = %v, want 7 (non-default oracle proving the file value was applied, not the 0 default)", cfg.rateLimit)
	}
	if cfg.metricsAddr != "" {
		t.Errorf("metricsAddr = %q, want empty (explicit empty in file overrides the non-empty default)", cfg.metricsAddr)
	}
	if cfg.grpcAddr != defaultGRPCAddr {
		t.Errorf("grpcAddr = %q, want built-in default %q (absent from file => default retained)", cfg.grpcAddr, defaultGRPCAddr)
	}
}

// TestDaemonConfigExplicitZeroOverrides: an explicit CLI --rate-limit 0 / --rate-
// burst 0 overrides a non-zero file value. This is the non-vacuous explicit-zero
// override test (file has non-zero values; CLI zero wins), kept SEPARATE from the
// presence test above.
func TestDaemonConfigExplicitZeroOverrides(t *testing.T) {
	path := writeTempConfig(t, `version: v1
rate_limit: 50
rate_burst: 100
`)

	cfg, err := parseFlagsMode(modeServe, []string{
		"--config", path,
		"--rate-limit", "0",
		"--rate-burst", "0",
	})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	dc, derr := daemonconfig.Load(path)
	if derr != nil {
		t.Fatalf("daemonconfig.Load: %v", derr)
	}

	mergeDaemonConfig(&cfg, dc)

	if cfg.rateLimit != 0 {
		t.Errorf("rateLimit = %v, want 0 (CLI explicit zero overrides file 50)", cfg.rateLimit)
	}
	if cfg.rateBurst != 0 {
		t.Errorf("rateBurst = %v, want 0 (CLI explicit zero overrides file 100)", cfg.rateBurst)
	}
}

// TestDaemonConfigFlagRegistered: --config appears in serve --help-all output.
func TestDaemonConfigFlagRegistered(t *testing.T) {
	out := helpRenderOut(t, modeServe, []string{"--help-all"})
	if !hasFlagHeader(out, "config") {
		t.Errorf("serve --help-all missing -config flag:\n%s", out)
	}
}

// TestDaemonConfigFlagACPAbsent: --config must NOT appear in acp --help-all output.
func TestDaemonConfigFlagACPAbsent(t *testing.T) {
	out := helpRenderOut(t, modeACP, []string{"--help-all"})
	if hasFlagHeader(out, "config") {
		t.Errorf("acp --help-all leaked -config flag:\n%s", out)
	}
}

// TestDaemonConfigTLSValuesMerged: the existing TLS pair validation (buildTLSConfig)
// must operate on the merged effective values.
func TestDaemonConfigTLSValuesMerged(t *testing.T) {
	path := writeTempConfig(t, `version: v1
tls_cert: /file/cert.pem
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}

	dc, derr := daemonconfig.Load(path)
	if derr != nil {
		t.Fatalf("daemonconfig.Load: %v", derr)
	}

	mergeDaemonConfig(&cfg, dc)

	if cfg.tlsCert != "/file/cert.pem" {
		t.Errorf("tlsCert = %q, want /file/cert.pem (from config file)", cfg.tlsCert)
	}
	if cfg.tlsKey != "" {
		t.Errorf("tlsKey = %q, want empty (not in file, not on CLI)", cfg.tlsKey)
	}
}

// --- Review fix #1: file-source perf-MCP bypass ---

// TestDaemonConfigFileNonLoopbackPerfMCPFailsBeforeRun: a config-file-supplied
// metrics_addr: 0.0.0.0:9090 with --perf-mcp must be rejected by the POST-merge
// validateEffectiveConfig, NOT slip past an earlier CLI-only guard. This is the
// ship-blocker: the guard must run on effective (merged) values, before
// app.Build / listener binding.
func TestDaemonConfigFileNonLoopbackPerfMCPFailsBeforeRun(t *testing.T) {
	path := writeTempConfig(t, `version: v1
metrics_addr: "0.0.0.0:9090"
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path, "--perf-mcp"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	// The merged effective metricsAddr is the file's non-loopback value.
	if cfg.metricsAddr != "0.0.0.0:9090" {
		t.Fatalf("metricsAddr = %q, want the file value 0.0.0.0:9090 post-merge", cfg.metricsAddr)
	}
	if err := validateEffectiveConfig(cfg); err == nil {
		t.Fatal("validateEffectiveConfig should reject a file-supplied non-loopback metrics_addr with --perf-mcp (the bypass the post-merge guard closes)")
	} else if !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("error %q should mention non-loopback", err)
	}
}

// TestDaemonConfigFileEmptyPerfMCPFailsBeforeRun: a config-file-supplied
// metrics_addr: "" (disable metrics) with --perf-mcp must be rejected post-merge.
func TestDaemonConfigFileEmptyPerfMCPFailsBeforeRun(t *testing.T) {
	path := writeTempConfig(t, `version: v1
metrics_addr: ""
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path, "--perf-mcp"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if err := validateEffectiveConfig(cfg); err == nil {
		t.Fatal("validateEffectiveConfig should reject a file-supplied empty metrics_addr with --perf-mcp")
	} else if !strings.Contains(err.Error(), "requires --metrics-addr") {
		t.Errorf("error %q should mention --metrics-addr requirement", err)
	}
}

// TestDaemonConfigFileLoopbackPerfMCPPasses: a file-supplied LOOPBACK
// metrics_addr with --perf-mcp passes the post-merge guard (the gate is
// targeted, not a blanket file refusal).
func TestDaemonConfigFileLoopbackPerfMCPPasses(t *testing.T) {
	path := writeTempConfig(t, `version: v1
metrics_addr: "127.0.0.1:9090"
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path, "--perf-mcp"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if err := validateEffectiveConfig(cfg); err != nil {
		t.Errorf("validateEffectiveConfig should accept a file-supplied loopback metrics_addr with --perf-mcp: %v", err)
	}
}

// --- Review fix #2: load/reject/merge helper with injected loader ---

// TestLoadAndMergeDaemonConfigACPRejectsConfig: ACP --config PATH is rejected at
// the load/merge step (the ACTUAL path is named in the rejection), before any
// loader call / listener path.
func TestLoadAndMergeDaemonConfigACPRejectsConfig(t *testing.T) {
	path := writeTempConfig(t, "version: v1\n")
	called := false
	cfg, err := parseFlagsMode(modeACP, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	err = loadAndMergeDaemonConfig(&cfg, true, func(_ string) (*daemonconfig.Config, error) {
		called = true
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected ACP --config rejection")
	}
	if !strings.Contains(err.Error(), "ACP mode") {
		t.Errorf("error %q should mention ACP mode", err)
	}
	if called {
		t.Error("loader must NOT be called when ACP rejects --config")
	}
}

// TestLoadAndMergeDaemonConfigAbsentConfigNoAutoLoad: absent --config never
// calls the loader (no conventional auto-load), preserving byte-identical
// zero-config behaviour.
func TestLoadAndMergeDaemonConfigAbsentConfigNoAutoLoad(t *testing.T) {
	called := false
	cfg, err := parseFlagsMode(modeServe, []string{"--grpc-addr", "0.0.0.0:9090"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if cfg.configPathFlagSet {
		t.Fatal("configPathFlagSet should be false when --config not passed")
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, func(_ string) (*daemonconfig.Config, error) {
		called = true
		return nil, nil
	}); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if called {
		t.Error("loader must NOT be called when --config is absent (no auto-load)")
	}
}

// TestLoadAndMergeDaemonConfigMalformedFailsBeforeListener: a malformed
// (unknown-key) file fails at the load step, before validateEffectiveConfig /
// listener binding.
func TestLoadAndMergeDaemonConfigMalformedFailsBeforeListener(t *testing.T) {
	path := writeTempConfig(t, "version: v1\nbogus_key: oops\n")
	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err == nil {
		t.Fatal("expected unknown-key file to fail at load")
	} else if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error %q should mention schema mismatch", err)
	}
}

// TestLoadAndMergeDaemonConfigSuccessMerges: a successful load merges file
// values into cfg (proved via the real loader over a real file).
func TestLoadAndMergeDaemonConfigSuccessMerges(t *testing.T) {
	path := writeTempConfig(t, `version: v1
grpc_addr: "file:9090"
rate_limit: 12
`)
	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if cfg.grpcAddr != "file:9090" {
		t.Errorf("grpcAddr = %q, want file:9090 (merged)", cfg.grpcAddr)
	}
	if cfg.rateLimit != 12 {
		t.Errorf("rateLimit = %v, want 12 (merged)", cfg.rateLimit)
	}
}

// --- Review fix #5: effective rate_limit/rate_burst validation ---

// TestValidateEffectiveConfigRateLimitInvalid: negative and non-finite
// rate_limit are rejected from BOTH file and CLI sources, while 0 (disable) is
// preserved.
func TestValidateEffectiveConfigRateLimitInvalid(t *testing.T) {
	// CLI source: negative.
	cfg, err := parseFlagsMode(modeServe, []string{"--rate-limit", "-5"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := validateEffectiveConfig(cfg); err == nil {
		t.Error("validateEffectiveConfig should reject a negative CLI rate_limit")
	} else if !strings.Contains(err.Error(), "rate_limit") {
		t.Errorf("error %q should mention rate_limit", err)
	}

	// File source: negative via merge.
	path := writeTempConfig(t, `version: v1
rate_limit: -1
`)
	cfg2, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg2, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if err := validateEffectiveConfig(cfg2); err == nil {
		t.Error("validateEffectiveConfig should reject a file-supplied negative rate_limit")
	} else if !strings.Contains(err.Error(), "rate_limit") {
		t.Errorf("error %q should mention rate_limit", err)
	}

	// Non-finite (NaN) via direct construction — a negative-or-non-finite gate.
	// (A CLI/file cannot easily express NaN; the bound still guards it.)
	cfg3, err := parseFlagsMode(modeServe, nil)
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	cfg3.rateLimit = math.NaN()
	if err := validateEffectiveConfig(cfg3); err == nil {
		t.Error("validateEffectiveConfig should reject a NaN rate_limit")
	}
	cfg3.rateLimit = math.Inf(1)
	if err := validateEffectiveConfig(cfg3); err == nil {
		t.Error("validateEffectiveConfig should reject an +Inf rate_limit")
	}

	// 0 (disable) is preserved and valid.
	cfg4, err := parseFlagsMode(modeServe, []string{"--rate-limit", "0"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := validateEffectiveConfig(cfg4); err != nil {
		t.Errorf("validateEffectiveConfig should accept rate_limit=0 (disable): %v", err)
	}
}

// TestValidateEffectiveConfigRateBurstInvalid: negative rate_burst is rejected
// from both sources, while 0 (derive) is preserved.
func TestValidateEffectiveConfigRateBurstInvalid(t *testing.T) {
	// CLI source: negative.
	cfg, err := parseFlagsMode(modeServe, []string{"--rate-burst", "-3"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := validateEffectiveConfig(cfg); err == nil {
		t.Error("validateEffectiveConfig should reject a negative CLI rate_burst")
	} else if !strings.Contains(err.Error(), "rate_burst") {
		t.Errorf("error %q should mention rate_burst", err)
	}

	// File source: negative via merge.
	path := writeTempConfig(t, `version: v1
rate_burst: -2
`)
	cfg2, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg2, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if err := validateEffectiveConfig(cfg2); err == nil {
		t.Error("validateEffectiveConfig should reject a file-supplied negative rate_burst")
	} else if !strings.Contains(err.Error(), "rate_burst") {
		t.Errorf("error %q should mention rate_burst", err)
	}

	// 0 (derive) is preserved and valid.
	cfg3, err := parseFlagsMode(modeServe, []string{"--rate-burst", "0"})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := validateEffectiveConfig(cfg3); err != nil {
		t.Errorf("validateEffectiveConfig should accept rate_burst=0 (derive): %v", err)
	}
}

// --- Review fix #3: TLS integration with merged file values ---

// TestDaemonConfigTLSBuildTLSConfigUsesMergedFileValues: after merge, the real
// buildTLSConfig must operate on the merged effective TLS paths. Supplying an
// invalid (non-existent) cert/key pair through the FILE (proving the merge
// carried them) must make buildTLSConfig error referencing those merged file
// values — fail before listener binding.
func TestDaemonConfigTLSBuildTLSConfigUsesMergedFileValues(t *testing.T) {
	path := writeTempConfig(t, `version: v1
tls_cert: "/file/does/not/exist/cert.pem"
tls_key: "/file/does/not/exist/key.pem"
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}

	// Prove the merge carried the file values (not the CLI defaults).
	if cfg.tlsCert != "/file/does/not/exist/cert.pem" {
		t.Fatalf("tlsCert = %q, want the merged file path", cfg.tlsCert)
	}
	if cfg.tlsKey != "/file/does/not/exist/key.pem" {
		t.Fatalf("tlsKey = %q, want the merged file path", cfg.tlsKey)
	}

	// validateEffectiveConfig must pass (TLS paths are not validated there — only
	// perf-mcp/rate bounds — so the TLS failure is deferred to buildTLSConfig,
	// preserving the fail-before-bind ordering: validation first, then TLS build).
	if err := validateEffectiveConfig(cfg); err != nil {
		t.Fatalf("validateEffectiveConfig: %v (TLS paths are validated in buildTLSConfig, not here)", err)
	}

	// The real buildTLSConfig over the merged invalid paths must fail, proving it
	// consumes the merged effective values (not stale CLI defaults) and that the
	// failure references/uses them. This is the fail-before-bind ordering: the
	// error surfaces before any net.Listen.
	tlsCfg, err := buildTLSConfig(cfg)
	if err == nil {
		if tlsCfg != nil {
			t.Error("buildTLSConfig returned a non-nil config for non-existent merged cert/key paths")
		}
		t.Fatal("buildTLSConfig should fail for merged non-existent cert/key paths")
	}
	// The error originates from tls.LoadX509KeyPair; it references the cert/key
	// load step (the merged values are in use).
	if !strings.Contains(err.Error(), "load TLS keypair") {
		t.Errorf("error %q should reference the TLS keypair load (using the merged file paths)", err)
	}
}

// TestDaemonConfigTLSBuildTLSConfigMergedClientCA: a file-supplied client_ca
// alone (no cert/key) is a misconfiguration that buildTLSConfig must catch over
// the merged values — proving mTLS validation sees the merged client_ca.
func TestDaemonConfigTLSBuildTLSConfigMergedClientCA(t *testing.T) {
	path := writeTempConfig(t, `version: v1
client_ca: "/file/does/not/exist/ca.pem"
`)

	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if err := loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("loadAndMergeDaemonConfig: %v", err)
	}
	if cfg.clientCA != "/file/does/not/exist/ca.pem" {
		t.Fatalf("clientCA = %q, want the merged file path", cfg.clientCA)
	}
	_, err = buildTLSConfig(cfg)
	if err == nil {
		t.Fatal("buildTLSConfig should reject a merged client_ca without cert/key")
	}
	if !strings.Contains(err.Error(), "client-ca") && !strings.Contains(err.Error(), "tls-cert") {
		t.Errorf("error %q should reference the client-ca/cert-key requirement", err)
	}
}

// --- QA integration tests (mecatl-cli-ux-simplification branch) ---
// These are aggregate integration tests exercising the REAL daemonconfig.Load
// through loadAndMergeDaemonConfig across both canonical serve and legacy modes.

// TestDaemonConfigNonexistentFileErrorBeforeRun: an explicit --config PATH to a
// file that does NOT exist returns a clear, path-naming "not found" error through
// the real daemonconfig.Load → loadAndMergeDaemonConfig chain — before any
// validateEffectiveConfig / listener binding / app.Build path.
func TestDaemonConfigNonexistentFileErrorBeforeRun(t *testing.T) {
	path := "/nonexistent/daemon/config.yaml"

	// Parse in canonical serve mode with the nonexistent --config.
	cfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	if !cfg.configPathFlagSet {
		t.Fatal("configPathFlagSet should be true when --config is passed")
	}

	// Drive through the REAL loadAndMergeDaemonConfig with the REAL loader.
	err = loadAndMergeDaemonConfig(&cfg, false, daemonconfig.Load)
	if err == nil {
		t.Fatal("expected a not-found error for a nonexistent --config file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should say file not found", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should name the nonexistent path %q", err, path)
	}
}

// TestDaemonConfigServeMergeIsEffective: a canonical `mecated serve --config PATH`
// invocation folds the file values into the config — a non-default file value is
// proven effective (it overrides the built-in default, not the 0 zero-value).
func TestDaemonConfigServeMergeIsEffective(t *testing.T) {
	// Write a config file with a non-default rate_limit (42 — well above the 0
	// default that would be vacuous to assert against) and a non-default grpc_addr,
	// with NO CLI overrides for those fields, so the merge is the only path that
	// sets them.
	path := writeTempConfig(t, `version: v1
grpc_addr: "file:9999"
rate_limit: 42
`)

	serveCfg, err := parseFlagsMode(modeServe, []string{"--config", path})
	if err != nil {
		t.Fatalf("parseFlagsMode(serve): %v", err)
	}
	if err := loadAndMergeDaemonConfig(&serveCfg, false, daemonconfig.Load); err != nil {
		t.Fatalf("serve loadAndMergeDaemonConfig: %v", err)
	}

	// Prove the non-default file value IS effective (not the built-in 0 default,
	// and not a stray zero-value the merge would vacuously satisfy).
	if serveCfg.rateLimit != 42 {
		t.Errorf("serve rateLimit = %v, want 42 (non-default oracle — file value must be effective)", serveCfg.rateLimit)
	}
	if serveCfg.grpcAddr != "file:9999" {
		t.Errorf("serve grpcAddr = %q, want file:9999 (file value must be effective)", serveCfg.grpcAddr)
	}
}
