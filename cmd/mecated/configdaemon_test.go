package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/daemonconfig"
)

// --- `mecated config daemon init` (issue #338, task B) ---

// TestConfigDaemonInitPrintWritesNoFile: `config daemon init --print` emits the
// skeleton to the writer and writes NOTHING to disk.
func TestConfigDaemonInitPrintWritesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	if err := runConfigDaemonInit([]string{"--print"}, &out); err != nil {
		t.Fatalf("config daemon init --print: %v", err)
	}
	if out.String() != daemonconfig.Skeleton() {
		t.Error("--print output is not byte-identical to the embedded skeleton")
	}
	target := filepath.Join(home, ".config", daemonconfig.DaemonConfigRelPath)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("--print wrote a file at %s (stat err: %v); it must write nothing", target, err)
	}
}

// TestConfigDaemonInitWritesThenRefusesThenForces: the default write creates the
// file at the conventional path; a second default invocation REFUSES (error
// names the path); --force overwrites.
func TestConfigDaemonInitWritesThenRefusesThenForces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	target := filepath.Join(home, ".config", daemonconfig.DaemonConfigRelPath)

	// First write succeeds and creates the parent dir + file.
	var out bytes.Buffer
	if err := runConfigDaemonInit(nil, &out); err != nil {
		t.Fatalf("first config daemon init: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected a written daemon.yaml at %s: %v", target, err)
	}
	if string(data) != daemonconfig.Skeleton() {
		t.Error("written file is not the embedded daemon skeleton")
	}
	if !strings.Contains(out.String(), "NOT auto-loaded") {
		t.Errorf("init output should remind the file is NOT auto-loaded:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "mecated serve --config") {
		t.Errorf("init output should remind how to use it (mecated serve --config):\n%s", out.String())
	}

	// Second default invocation refuses, and the error names the existing path.
	err = runConfigDaemonInit(nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("second config daemon init should refuse to overwrite, got nil error")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("refusal error must name the path %q, got: %v", target, err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal error should mention --force, got: %v", err)
	}

	// --force overwrites (write a sentinel first to prove it is replaced).
	if err := os.WriteFile(target, []byte("# stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runConfigDaemonInit([]string{"--force"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("config daemon init --force: %v", err)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != daemonconfig.Skeleton() {
		t.Error("--force did not overwrite with the embedded skeleton")
	}
}

// TestConfigDaemonInitUnresolvableConfigDir: when neither XDG_CONFIG_HOME nor
// HOME resolves a config dir, the write path fails with an actionable error
// pointing at --print.
func TestConfigDaemonInitUnresolvableConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	err := runConfigDaemonInit(nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error when the config dir is unresolvable, got nil")
	}
	if !strings.Contains(err.Error(), "--print") {
		t.Errorf("error should point the operator at --print, got: %v", err)
	}
}

// TestConfigDaemonInitXDGRespected: the conventional path follows XDG_CONFIG_HOME
// (not just $HOME/.config), so a custom XDG lands the file there.
func TestConfigDaemonInitXDGRespected(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "custom-xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	target := filepath.Join(xdg, daemonconfig.DaemonConfigRelPath)
	if err := runConfigDaemonInit(nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("config daemon init: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected the file at the XDG path %s: %v", target, err)
	}
}

// --- `mecated config daemon validate` (issue #338, task B) ---

// TestConfigDaemonValidateSuccess: a valid v1 file validates and the success
// output names the file + version and reminds how to use it.
func TestConfigDaemonValidateSuccess(t *testing.T) {
	path := writeTempConfig(t, "version: v1\ngrpc_addr: 127.0.0.1:8080\n")
	var out bytes.Buffer
	if err := runConfigDaemonValidate([]string{"--file", path}, &out); err != nil {
		t.Fatalf("config daemon validate: %v", err)
	}
	if !strings.Contains(out.String(), "valid daemon config") {
		t.Errorf("success output should say 'valid daemon config':\n%s", out.String())
	}
	if !strings.Contains(out.String(), "v1") {
		t.Errorf("success output should name the version v1:\n%s", out.String())
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("success output should name the file %s:\n%s", path, out.String())
	}
	if !strings.Contains(out.String(), "mecated serve --config") {
		t.Errorf("success output should remind how to use it:\n%s", out.String())
	}
}

// TestConfigDaemonValidateSchemaFailure: an unknown-key file fails with the
// schema error, before any listener.
func TestConfigDaemonValidateSchemaFailure(t *testing.T) {
	path := writeTempConfig(t, "version: v1\nbogus_key: oops\n")
	err := runConfigDaemonValidate([]string{"--file", path}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a schema error for an unknown key")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error %q should mention schema mismatch", err)
	}
}

// TestConfigDaemonValidateSemanticFailure: a negative rate_limit fails Validate
// (the semantic bound), proving validate reuses the runtime bound.
func TestConfigDaemonValidateSemanticFailure(t *testing.T) {
	path := writeTempConfig(t, "version: v1\nrate_limit: -5\n")
	err := runConfigDaemonValidate([]string{"--file", path}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a semantic error for a negative rate_limit")
	}
	if !strings.Contains(err.Error(), "rate_limit") {
		t.Errorf("error %q should mention rate_limit", err)
	}
}

// TestConfigDaemonValidateVersionFailure: a missing version fails.
func TestConfigDaemonValidateVersionFailure(t *testing.T) {
	path := writeTempConfig(t, "grpc_addr: 127.0.0.1:8080\n")
	err := runConfigDaemonValidate([]string{"--file", path}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error for missing version")
	}
	if !strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should mention the required version key", err)
	}
}

// TestConfigDaemonValidateMissingFile: a missing --file path fails with the
// not-found error (not a panic, not a silent pass).
func TestConfigDaemonValidateMissingFile(t *testing.T) {
	err := runConfigDaemonValidate([]string{"--file", "/nonexistent/daemon.yaml"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should say file not found", err)
	}
}

// TestConfigDaemonValidateDefaultsToConventionalPath: with no --file, validate
// resolves the conventional XDG path (for convenience) and reports not-found
// when it is absent — proving the default path resolution works and does NOT
// auto-load anything (it is a validate action, not a serve path).
func TestConfigDaemonValidateDefaultsToConventionalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// No file written at the conventional path → not-found error naming the path.
	err := runConfigDaemonValidate(nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a not-found error when the conventional path is absent")
	}
	conventional := filepath.Join(home, ".config", daemonconfig.DaemonConfigRelPath)
	if !strings.Contains(err.Error(), conventional) {
		t.Errorf("error %q should name the conventional path %q", err, conventional)
	}

	// Scaffold via init, then validate the conventional path with no --file.
	if err := runConfigDaemonInit(nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("config daemon init: %v", err)
	}
	var out bytes.Buffer
	if err := runConfigDaemonValidate(nil, &out); err != nil {
		t.Fatalf("config daemon validate (conventional path): %v", err)
	}
	if !strings.Contains(out.String(), "valid daemon config") {
		t.Errorf("conventional-path validate should succeed after init:\n%s", out.String())
	}
}

// TestConfigDaemonValidateNeverPrintsSecrets: the daemon config carries no
// token value (TOKEN is env-only), and the success output carries only path +
// version — never raw file content. A file with many fields produces NO field
// echo in the success output.
func TestConfigDaemonValidateNeverPrintsSecrets(t *testing.T) {
	path := writeTempConfig(t, `version: v1
grpc_addr: 127.0.0.1:8080
http_addr: 127.0.0.1:8081
tls_cert: /etc/mecatl/tls/server.crt
tls_key: /etc/mecatl/tls/server.key
client_ca: /etc/mecatl/tls/clients-ca.crt
rate_limit: 10
rate_burst: 20
`)
	var out bytes.Buffer
	if err := runConfigDaemonValidate([]string{"--file", path}, &out); err != nil {
		t.Fatalf("config daemon validate: %v", err)
	}
	// The success line must NOT echo any field value or raw file content: only
	// the path + version + the usage reminder. The TLS paths are the strongest
	// oracle (they never appear in the path or version). Numeric/string field
	// values are deliberately NOT asserted against because the temp path and the
	// reminder carry digits.
	for _, leaked := range []string{"/etc/mecatl/tls", "server.crt", "server.key", "clients-ca.crt"} {
		if strings.Contains(out.String(), leaked) {
			t.Errorf("validate success output leaked a file field value %q:\n%s", leaked, out.String())
		}
	}
}

// --- Command resolution fail-closed (issue #338, task B req. 3) ---

// TestConfigDaemonResolutionFailClosed: a bare `config daemon` or an unknown
// `config daemon <x>` resolves to a usage error and never reaches run/listeners.
func TestConfigDaemonResolutionFailClosed(t *testing.T) {
	cases := [][]string{
		{"mecated", "config", "daemon"},
		{"mecated", "config", "daemon", "bogus"},
	}
	for _, argv := range cases {
		res := resolveCommand(argv)
		if res.err == nil {
			t.Errorf("argv %v should resolve to a usage error, got handled=%v mode=%v", argv, res.handled, res.mode)
			continue
		}
		if res.handled {
			t.Errorf("argv %v should NOT be handled (no runner), got handled=true", argv)
		}
		if res.mode != "" {
			t.Errorf("argv %v should not select a daemon mode, got %v", argv, res.mode)
		}
		if !strings.Contains(res.err.Error(), "config daemon") {
			t.Errorf("argv %v error %q should mention 'config daemon'", argv, res.err)
		}
	}
}

// TestConfigDaemonResolutionInitValidateHandled: `config daemon init` and
// `config daemon validate` resolve as handled one-shot subcommands (never reach
// run/listeners).
func TestConfigDaemonResolutionInitValidateHandled(t *testing.T) {
	for _, c := range []struct {
		argv []string
		sub  string
	}{
		{[]string{"mecated", "config", "daemon", "init"}, "init"},
		{[]string{"mecated", "config", "daemon", "validate"}, "validate"},
	} {
		res := resolveCommand(c.argv)
		if res.err != nil {
			t.Errorf("argv %v should resolve handled, got err: %v", c.argv, res.err)
			continue
		}
		if !res.handled || res.run == nil {
			t.Errorf("argv %v should be handled with a runner", c.argv)
		}
		if res.mode != "" {
			t.Errorf("argv %v should not select a daemon mode", c.argv)
		}
	}
}

// TestConfigDaemonSkeletonDefaultsMatchProduction: the loopback defaults the
// daemon.yaml skeleton documents MUST match the production built-in defaults, so
// a scaffolded-and-uncommented file is byte-identical to a no-file run.
func TestConfigDaemonSkeletonDefaultsMatchProduction(t *testing.T) {
	if defaultGRPCAddr != "127.0.0.1:8080" || defaultHTTPAddr != "127.0.0.1:8081" || defaultMetricsAddr != "127.0.0.1:9090" {
		// Sanity: if the production defaults ever change, the skeleton must be
		// regenerated to match — this guard makes the coupling explicit.
		t.Fatalf("production loopback defaults changed: grpc=%s http=%s metrics=%s — regenerate the daemon skeleton", defaultGRPCAddr, defaultHTTPAddr, defaultMetricsAddr)
	}
	skel := daemonconfig.Skeleton()
	for _, want := range []string{"127.0.0.1:8080", "127.0.0.1:8081", "127.0.0.1:9090"} {
		if !strings.Contains(skel, want) {
			t.Errorf("daemon skeleton should document the production default %s", want)
		}
	}
	// The skeleton must NOT carry any token/secret value.
	lower := strings.ToLower(skel)
	for _, bad := range []string{"token:", "password:", "secret:", "api_key:"} {
		if strings.Contains(lower, bad) {
			t.Errorf("daemon skeleton must NOT carry a secret-shaped key %q", bad)
		}
	}
}
