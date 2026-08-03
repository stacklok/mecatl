package daemonconfig

import (
	"strings"
	"testing"
)

func TestLoadVersionRequired(t *testing.T) {
	_, err := parse([]byte(``), "test.yml")
	if err == nil {
		t.Fatal("expected error for missing version")
	}
	if !strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should mention version key", err)
	}
}

func TestLoadVersionEmpty(t *testing.T) {
	_, err := parse([]byte(`version: ""`), "test.yml")
	if err == nil {
		t.Fatal("expected error for empty version")
	}
	if !strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should mention version key", err)
	}
}

func TestLoadVersionUnsupported(t *testing.T) {
	_, err := parse([]byte(`version: v2`), "test.yml")
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("error %q should mention unsupported version", err)
	}
	if !strings.Contains(err.Error(), SchemaVersionV1) {
		t.Errorf("error %q should name supported version %q", err, SchemaVersionV1)
	}
}

func TestLoadVersionV1EmptyConfig(t *testing.T) {
	cfg, err := parse([]byte(`version: v1`), "test.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != SchemaVersionV1 {
		t.Errorf("Version = %q, want %q", cfg.Version, SchemaVersionV1)
	}
	if cfg.GRPCAddr != nil {
		t.Error("GRPCAddr should be nil when not in file")
	}
	if cfg.HTTPAddr != nil {
		t.Error("HTTPAddr should be nil when not in file")
	}
	if cfg.MetricsAddr != nil {
		t.Error("MetricsAddr should be nil when not in file")
	}
	if cfg.TLSCert != nil {
		t.Error("TLSCert should be nil when not in file")
	}
	if cfg.TLSKey != nil {
		t.Error("TLSKey should be nil when not in file")
	}
	if cfg.ClientCA != nil {
		t.Error("ClientCA should be nil when not in file")
	}
	if cfg.RateLimit != nil {
		t.Error("RateLimit should be nil when not in file")
	}
	if cfg.RateBurst != nil {
		t.Error("RateBurst should be nil when not in file")
	}
}

func TestLoadUnknownKeyRejected(t *testing.T) {
	_, err := parse([]byte("version: v1\nunknown_key: foo"), "test.yml")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestLoadExplicitZeroEmptyValues(t *testing.T) {
	// RateLimit explicitly set to 0 and MetricsAddr to "" should come through as
	// non-nil pointers to zero/empty values.
	cfg, err := parse([]byte(`version: v1
rate_limit: 0
metrics_addr: ""
`), "test.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RateLimit == nil {
		t.Fatal("RateLimit should be non-nil when explicitly set to 0")
	}
	if *cfg.RateLimit != 0 {
		t.Errorf("RateLimit = %v, want 0", *cfg.RateLimit)
	}
	if cfg.MetricsAddr == nil {
		t.Fatal("MetricsAddr should be non-nil when explicitly set to empty")
	}
	if *cfg.MetricsAddr != "" {
		t.Errorf("MetricsAddr = %q, want empty", *cfg.MetricsAddr)
	}
}

func TestLoadFullConfig(t *testing.T) {
	grpcAddr := "0.0.0.0:8080"
	httpAddr := "0.0.0.0:8081"
	metricsAddr := "127.0.0.1:9090"
	tlsCert := "/path/to/cert.pem"
	tlsKey := "/path/to/key.pem"
	clientCA := "/path/to/ca.pem"
	rateLimit := 10.0
	rateBurst := 20

	cfg, err := parse([]byte(`version: v1
grpc_addr: 0.0.0.0:8080
http_addr: 0.0.0.0:8081
metrics_addr: 127.0.0.1:9090
tls_cert: /path/to/cert.pem
tls_key: /path/to/key.pem
client_ca: /path/to/ca.pem
rate_limit: 10
rate_burst: 20
`), "test.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != SchemaVersionV1 {
		t.Errorf("Version = %q, want %q", cfg.Version, SchemaVersionV1)
	}
	if cfg.GRPCAddr == nil || *cfg.GRPCAddr != grpcAddr {
		t.Errorf("GRPCAddr = %v, want %q", cfg.GRPCAddr, grpcAddr)
	}
	if cfg.HTTPAddr == nil || *cfg.HTTPAddr != httpAddr {
		t.Errorf("HTTPAddr = %v, want %q", cfg.HTTPAddr, httpAddr)
	}
	if cfg.MetricsAddr == nil || *cfg.MetricsAddr != metricsAddr {
		t.Errorf("MetricsAddr = %v, want %q", cfg.MetricsAddr, metricsAddr)
	}
	if cfg.TLSCert == nil || *cfg.TLSCert != tlsCert {
		t.Errorf("TLSCert = %v, want %q", cfg.TLSCert, tlsCert)
	}
	if cfg.TLSKey == nil || *cfg.TLSKey != tlsKey {
		t.Errorf("TLSKey = %v, want %q", cfg.TLSKey, tlsKey)
	}
	if cfg.ClientCA == nil || *cfg.ClientCA != clientCA {
		t.Errorf("ClientCA = %v, want %q", cfg.ClientCA, clientCA)
	}
	if cfg.RateLimit == nil || *cfg.RateLimit != rateLimit {
		t.Errorf("RateLimit = %v, want %v", cfg.RateLimit, rateLimit)
	}
	if cfg.RateBurst == nil || *cfg.RateBurst != rateBurst {
		t.Errorf("RateBurst = %v, want %v", cfg.RateBurst, rateBurst)
	}
}

func TestLoadMissingExplicitFileError(t *testing.T) {
	_, err := Load("/nonexistent/path/daemon.yml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should say file not found", err)
	}
}

func TestLoadTypeError(t *testing.T) {
	// rate_limit must be a number, not a string.
	_, err := parse([]byte(`version: v1
rate_limit: "not-a-number"`), "test.yml")
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if !strings.Contains(err.Error(), "does not match the expected schema") {
		t.Errorf("error %q should mention schema mismatch", err)
	}
}

func TestPrecedencePresenceSemantics(t *testing.T) {
	// When a field is absent from config, it stays nil — the caller can
	// distinguish "not in file" from "explicitly zero".
	cfg, err := parse([]byte(`version: v1
grpc_addr: ""`), "test.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr == nil {
		t.Fatal("GRPCAddr should be non-nil when explicitly set to empty")
	}
	if *cfg.GRPCAddr != "" {
		t.Errorf("GRPCAddr = %q, want empty", *cfg.GRPCAddr)
	}
	// HTTPAddr was absent, should be nil.
	if cfg.HTTPAddr != nil {
		t.Error("HTTPAddr should be nil when absent from file")
	}
}

// --- Strict parser edge cases (review fix #6) ---

// TestParseWhitespaceOnly is accepted as an empty document (no version present)
// and must fail on the required-version check, NOT as a schema/syntax error.
func TestParseWhitespaceOnly(t *testing.T) {
	_, err := parse([]byte("   \n  \n   \n"), "test.yml")
	if err == nil {
		t.Fatal("expected error for whitespace-only file")
	}
	if !strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should be the required-version error, not a syntax/schema error", err)
	}
}

// TestParseCommentOnly is accepted as an empty document and must fail on the
// required-version check (a comment carries no keys).
func TestParseCommentOnly(t *testing.T) {
	_, err := parse([]byte("# just a comment\n# another\n"), "test.yml")
	if err == nil {
		t.Fatal("expected error for comment-only file")
	}
	if !strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should be the required-version error, not a syntax/schema error", err)
	}
}

// TestParseMultiDocumentRejected: a second `---` document is refused even when
// both documents are individually valid, because the v1 schema is a single
// document.
func TestParseMultiDocumentRejected(t *testing.T) {
	_, err := parse([]byte("version: v1\n---\nversion: v1\n"), "test.yml")
	if err == nil {
		t.Fatal("expected error for multi-document file")
	}
	if !strings.Contains(err.Error(), "multiple documents are not supported") {
		t.Errorf("error %q should reject multiple documents", err)
	}
}

// TestParseMultiDocumentSecondUnknownRejected: a second document with an unknown
// key is still rejected as multi-document (not surfaced as a schema error on
// the second doc), because the whole file is refused once a second document
// exists.
func TestParseMultiDocumentSecondUnknownRejected(t *testing.T) {
	_, err := parse([]byte("version: v1\n---\nfoo: bar\n"), "test.yml")
	if err == nil {
		t.Fatal("expected error for multi-document file with unknown key in second doc")
	}
	if !strings.Contains(err.Error(), "multiple documents are not supported") {
		t.Errorf("error %q should reject multiple documents", err)
	}
}

// TestParseSyntaxErrorDistinguishedFromSchema: a malformed YAML document (bad
// indentation / stray marker) is a SYNTAX error, not a schema (unknown-key/type)
// error, so the message guides the operator to "valid YAML" rather than
// "unknown key or type".
func TestParseSyntaxErrorDistinguishedFromSchema(t *testing.T) {
	// A bad indentation / stray mapping key is a YAML syntax error, not a schema
	// (unknown-key/type) error.
	_, err := parse([]byte("version: v1\n  - bad: indent\n oops\n"), "test.yml")
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	if strings.Contains(err.Error(), "unknown key or type") {
		t.Errorf("error %q should be a syntax error, not a schema error", err)
	}
	if !strings.Contains(err.Error(), "valid YAML") {
		t.Errorf("error %q should guide to valid YAML", err)
	}
}

// TestParseVersionIntegerType: version supplied as an integer (1) is a TYPE
// mismatch against the string version field, so it is a schema error, not a
// "version key is required" error.
func TestParseVersionIntegerType(t *testing.T) {
	_, err := parse([]byte("version: 1\n"), "test.yml")
	if err == nil {
		t.Fatal("expected error for integer version")
	}
	// yaml.v3 decodes an integer into a string field as a type error under
	// KnownFields, so this is a schema error.
	if strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should be a schema/type error for integer version, not a missing-version error", err)
	}
}
