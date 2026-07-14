package toolhivellm

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestDetectConfig_MissingFile(t *testing.T) {
	if _, ok := DetectConfig(filepath.Join(t.TempDir(), "nope.yaml")); ok {
		t.Fatal("expected false for a missing file")
	}
}

func TestDetectConfig_NoLLMBlock(t *testing.T) {
	path := writeConfig(t, "other: value\n")
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false with no llm: block")
	}
}

func TestDetectConfig_EmptyGatewayURL(t *testing.T) {
	path := writeConfig(t, "llm:\n  gateway_url: \"\"\n")
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false with an empty gateway_url")
	}
}

func TestDetectConfig_GatewayURLOnly_DefaultsPort(t *testing.T) {
	path := writeConfig(t, "llm:\n  gateway_url: https://upstream.example/gw\n")
	cfg, ok := DetectConfig(path)
	if !ok {
		t.Fatal("expected true for a bare gateway_url")
	}
	if cfg.ListenPort != defaultListenPort {
		t.Errorf("ListenPort = %d, want default %d", cfg.ListenPort, defaultListenPort)
	}
	if cfg.GatewayURL != "https://upstream.example/gw" {
		t.Errorf("GatewayURL = %q", cfg.GatewayURL)
	}
	if want := "http://127.0.0.1:14000/v1"; cfg.BaseURL() != want {
		t.Errorf("BaseURL() = %q, want %q", cfg.BaseURL(), want)
	}
}

func TestDetectConfig_CustomPort(t *testing.T) {
	path := writeConfig(t, "llm:\n  gateway_url: https://upstream.example/gw\n  proxy:\n    listen_port: 15551\n")
	cfg, ok := DetectConfig(path)
	if !ok {
		t.Fatal("expected true")
	}
	if cfg.ListenPort != 15551 {
		t.Errorf("ListenPort = %d, want 15551", cfg.ListenPort)
	}
	if want := "http://127.0.0.1:15551/v1"; cfg.BaseURL() != want {
		t.Errorf("BaseURL() = %q, want %q", cfg.BaseURL(), want)
	}
}

func TestDetectConfig_OutOfRangePort(t *testing.T) {
	for _, port := range []string{"0", "-1", "65536", "999999"} {
		path := writeConfig(t, "llm:\n  gateway_url: https://upstream.example/gw\n  proxy:\n    listen_port: "+port+"\n")
		if _, ok := DetectConfig(path); ok && port != "0" {
			t.Errorf("port %q: expected false (out of range)", port)
		}
	}
}

func TestDetectConfig_OversizedFileIsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// A YAML comment line repeated well past the 1 MiB cap, terminated with a
	// genuine llm block so a size-check bypass would otherwise detect it.
	var sb strings.Builder
	for sb.Len() <= maxConfigBytes {
		sb.WriteString("# padding padding padding padding padding padding padding\n")
	}
	sb.WriteString("llm:\n  gateway_url: https://upstream.example/gw\n")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write oversized fixture: %v", err)
	}
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false for an oversized config file")
	}
}

func TestDetectConfig_NonRegularFile(t *testing.T) {
	dir := t.TempDir()
	// A directory is not a regular file.
	if _, ok := DetectConfig(dir); ok {
		t.Fatal("expected false for a directory path")
	}
}

func TestDetectConfig_Fifo_NonRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unsupported in this environment: %v", err)
	}
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false for a FIFO (non-regular file)")
	}
}

func TestDetectConfig_WrongOwnerIsFalse(t *testing.T) {
	path := writeConfig(t, "llm:\n  gateway_url: https://upstream.example/gw\n")

	orig := statOwner
	defer func() { statOwner = orig }()
	statOwner = func(os.FileInfo) (uint32, bool) {
		return uint32(os.Getuid()) + 1, true // simulate a different owner
	}
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false when the file is owned by a different uid")
	}
}

func TestDetectConfig_OwnerAssertionFailsClosed(t *testing.T) {
	path := writeConfig(t, "llm:\n  gateway_url: https://upstream.example/gw\n")

	orig := statOwner
	defer func() { statOwner = orig }()
	statOwner = func(os.FileInfo) (uint32, bool) {
		return 0, false // simulate a failed type-assertion (e.g. non-unix)
	}
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false when ownership cannot be determined (fail closed)")
	}
}

// TestDetectConfig_TLSSkipVerifyNeverDecoded pins R5.2: a config file setting
// tls_skip_verify (at any nesting the operator might guess) is decoded into a
// struct that HAS NO SUCH FIELD — there is nowhere for the value to land, so
// it can never influence anything downstream. This test would fail to compile
// (not just fail at runtime) if wireConfig ever grew a TLSSkipVerify field
// that the assertion below could then observe as true.
func TestDetectConfig_TLSSkipVerifyNeverDecoded(t *testing.T) {
	path := writeConfig(t, ""+
		"llm:\n"+
		"  gateway_url: https://upstream.example/gw\n"+
		"  tls_skip_verify: true\n"+
		"  proxy:\n"+
		"    listen_port: 14000\n"+
		"    tls_skip_verify: true\n"+
		"  oidc:\n"+
		"    client_id: should-never-be-read\n")
	cfg, ok := DetectConfig(path)
	if !ok {
		t.Fatal("expected true (the unknown tls_skip_verify/oidc keys are simply ignored)")
	}
	if cfg.ListenPort != 14000 {
		t.Errorf("ListenPort = %d, want 14000 (proxy fields besides listen_port ignored)", cfg.ListenPort)
	}
	// The Config struct itself has no TLS/auth field to assert on — that
	// absence (verified by inspection: Config has exactly GatewayURL +
	// ListenPort) IS the security invariant.
}

// secretShapedFieldPattern is the denylist TestNoSecretShapedFields checks
// wireConfig and Config against: any field name or yaml tag matching this is
// grounds to suspect a TLS/auth/secret knob snuck into the decoded shape.
var secretShapedFieldPattern = regexp.MustCompile(`(?i)tls|skip|insecure|oidc|auth|secret`)

// TestNoSecretShapedFields is the FALSIFIABLE twin of
// TestDetectConfig_TLSSkipVerifyNeverDecoded: rather than eyeballing "the
// struct has exactly two fields", it reflects over wireConfig (recursively,
// through nested structs) and the exported Config asserting NO field name or
// yaml tag matches tls|skip|insecure|oidc|auth|secret. Add a
// TLSSkipVerify field (tagged yaml:"tls_skip_verify") anywhere in this
// package's decode shape and this test fails — on the field name AND the tag
// — instead of silently passing.
func TestNoSecretShapedFields(t *testing.T) {
	checkNoSecretShapedFields(t, reflect.TypeOf(wireConfig{}))
	checkNoSecretShapedFields(t, reflect.TypeOf(Config{}))
}

func checkNoSecretShapedFields(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if secretShapedFieldPattern.MatchString(f.Name) {
			t.Errorf("%s.%s: field name matches the secret-shaped denylist (tls|skip|insecure|oidc|auth|secret)", typ.Name(), f.Name)
		}
		if tag := f.Tag.Get("yaml"); tag != "" && secretShapedFieldPattern.MatchString(tag) {
			t.Errorf("%s.%s: yaml tag %q matches the secret-shaped denylist", typ.Name(), f.Name, tag)
		}
		if f.Type.Kind() == reflect.Struct {
			checkNoSecretShapedFields(t, f.Type)
		}
	}
}

func TestDetectConfig_MalformedYAML(t *testing.T) {
	path := writeConfig(t, "llm: [this is not a mapping\n")
	if _, ok := DetectConfig(path); ok {
		t.Fatal("expected false for malformed YAML")
	}
}

func TestBaseURL_AlwaysLoopback(t *testing.T) {
	// Even a Config hand-built with an attacker-shaped GatewayURL must never
	// influence BaseURL()'s host.
	cfg := Config{GatewayURL: "http://evil.example:9999/", ListenPort: 14000}
	if want := "http://127.0.0.1:14000/v1"; cfg.BaseURL() != want {
		t.Errorf("BaseURL() = %q, want %q (host must be hardcoded loopback)", cfg.BaseURL(), want)
	}
}

func TestBaseURL_ZeroPortFallsBackToDefault(t *testing.T) {
	cfg := Config{}
	if want := "http://127.0.0.1:14000/v1"; cfg.BaseURL() != want {
		t.Errorf("BaseURL() = %q, want %q", cfg.BaseURL(), want)
	}
}
