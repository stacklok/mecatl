package daemonconfig

import (
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestGoccyYAMLMigration_Scenario1_StrictDiagnosticsUseTokenLocationWithoutSource(t *testing.T) {
	const attackerKey = "attacker-controlled-key"
	const attackerValue = "attacker-controlled-value"
	_, err := parse([]byte("version: v1\n  "+attackerKey+": "+attackerValue+"\n"), "test.yml")
	if err == nil {
		t.Fatal("malformed YAML must fail")
	}
	message := err.Error()
	if !strings.Contains(message, "line 1") || !strings.Contains(message, "column") {
		t.Fatalf("syntax diagnostic = %q, want goccy token line and column", message)
	}
	for _, forbidden := range []string{"  " + attackerKey, attackerKey, attackerValue} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("syntax diagnostic leaked YAML content %q: %q", forbidden, message)
		}
	}
}

func TestGoccyYAMLMigration_Scenario2_DaemonConfigStrictContract(t *testing.T) {
	tests := []struct {
		name, input, category string
	}{
		{"unknown field", "version: v1\nunknown-attacker-key: attacker-value\n", "expected schema"},
		{"wrong type", "version: v1\nrate_limit: attacker-value\n", "expected schema"},
		{"malformed syntax", "version: v1\n  attacker-key: attacker-value\n", "invalid YAML syntax"},
		{"second document", "version: v1\n---\nversion: v1\n", "multiple documents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.input), "test.yml")
			if err == nil {
				t.Fatal("strict daemon configuration must reject input")
			}
			message := err.Error()
			if !strings.Contains(message, tt.category) {
				t.Fatalf("error = %q, want category %q", message, tt.category)
			}
			for _, forbidden := range []string{"unknown-attacker-key", "attacker-key", "attacker-value"} {
				if strings.Contains(message, forbidden) {
					t.Fatalf("error leaked YAML content %q: %q", forbidden, message)
				}
			}
		})
	}
}

func TestGoccyYAMLMigration_SemanticMatrixDaemonConfig(t *testing.T) {
	data, err := os.ReadFile("../../../engine/testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Cases []struct {
			Name     string            `yaml:"name"`
			Document string            `yaml:"document"`
			Readers  map[string]string `yaml:"readers"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	for _, tc := range matrix.Cases {
		outcome, ok := tc.Readers["daemonconfig"]
		if !ok {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			_, err := parse([]byte(tc.Document), "semantic-matrix.yaml")
			if accepted, want := err == nil, outcome == "accept"; accepted != want {
				t.Fatalf("parse() accepted=%v, want %v (error=%v)", accepted, want, err)
			}
		})
	}
}

func TestParsePreservesAnchorAndAliasCompatibility(t *testing.T) {
	cfg, err := parse([]byte("version: &version v1\nmetrics_addr: *version\n"), "test.yml")
	if err != nil {
		t.Fatalf("parse anchored daemon config: %v", err)
	}
	if cfg.MetricsAddr == nil || *cfg.MetricsAddr != SchemaVersionV1 {
		t.Fatalf("MetricsAddr = %v, want alias value %q", cfg.MetricsAddr, SchemaVersionV1)
	}
}

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
	const unsupported = "yaml-provided-version-must-not-escape"
	err := func() error {
		_, err := parse([]byte("version: "+unsupported), "test.yml")
		return err
	}()
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
	if want := `test.yml: unsupported version (only "v1" is supported)`; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), unsupported) {
		t.Errorf("unsupported-version error leaked YAML value: %q", err)
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
	// (unknown-key/type) error. The error message must guide the operator to
	// valid YAML WITHOUT echoing the offending source content (CWE-209).
	malformed := []byte("version: v1\n  - bad: indent\n oops\n")
	_, err := parse(malformed, "test.yml")
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
	if strings.Contains(err.Error(), "unknown key or type") {
		t.Errorf("error %q should be a syntax error, not a schema error", err)
	}
	if !strings.Contains(err.Error(), "invalid YAML syntax") {
		t.Errorf("error %q should state 'invalid YAML syntax'", err)
	}
	// Never echo raw file content in the error (CWE-209).
	if strings.Contains(err.Error(), "bad") && strings.Contains(err.Error(), "indent") {
		t.Errorf("error echoes raw file content %q (CWE-209): %q", malformed, err)
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
	// The parser decodes an integer into a string field as a type error under
	// KnownFields, so this is a schema error.
	if strings.Contains(err.Error(), "version key is required") {
		t.Errorf("error %q should be a schema/type error for integer version, not a missing-version error", err)
	}
}

// --- Skeleton + Validate (issue #338, task B) ---

// TestSkeletonRoundTripsThroughStrictParse is the strong agreement guard for the
// daemon.yaml skeleton: uncommenting the skeleton's YAML body and parsing it
// through the v1 strict parser must produce NO error (the skeleton's keys are
// exactly the schema's), and the loopback defaults the skeleton documents must
// match the production built-in defaults.
func TestSkeletonRoundTripsThroughStrictParse(t *testing.T) {
	body := uncommentDaemonSkeleton(Skeleton())
	cfg, err := parse([]byte(body), "daemon.skeleton.yaml")
	if err != nil {
		t.Fatalf("uncommented daemon skeleton failed the strict v1 parse: %v\n\n--- body ---\n%s", err, body)
	}
	if cfg.Version != SchemaVersionV1 {
		t.Errorf("Version = %q, want %q", cfg.Version, SchemaVersionV1)
	}
	// The skeleton documents the production loopback defaults as commented
	// examples; uncommenting applies them. They MUST match the production
	// built-in defaults so a scaffolded file is byte-identical to a no-file run.
	if cfg.GRPCAddr == nil || *cfg.GRPCAddr != "127.0.0.1:8080" {
		got := "<nil>"
		if cfg.GRPCAddr != nil {
			got = *cfg.GRPCAddr
		}
		t.Errorf("skeleton grpc_addr = %q, want 127.0.0.1:8080 (production default)", got)
	}
	if cfg.HTTPAddr == nil || *cfg.HTTPAddr != "127.0.0.1:8081" {
		got := "<nil>"
		if cfg.HTTPAddr != nil {
			got = *cfg.HTTPAddr
		}
		t.Errorf("skeleton http_addr = %q, want 127.0.0.1:8081 (production default)", got)
	}
	if cfg.MetricsAddr == nil || *cfg.MetricsAddr != "127.0.0.1:9090" {
		got := "<nil>"
		if cfg.MetricsAddr != nil {
			got = *cfg.MetricsAddr
		}
		t.Errorf("skeleton metrics_addr = %q, want 127.0.0.1:9090 (production default)", got)
	}
	// The commented TLS / rate-limit EXAMPLES (illustrative paths and 0 values)
	// are uncommented by the round-trip and must STRICT-parse: the example paths
	// are strings and 0 is a valid (disable/derive) value. They are illustrative,
	// not the documented loopback defaults above.
	if cfg.TLSCert == nil || *cfg.TLSCert != "/etc/mecatl/tls/server.crt" {
		t.Error("skeleton TLS cert example should strictly parse")
	}
	zero := 0.0
	if cfg.RateLimit == nil || *cfg.RateLimit != zero {
		t.Error("skeleton rate_limit example should strictly parse as 0")
	}
	// The uncommented skeleton must also pass Validate (the loopback defaults are
	// valid and the example 0 values are valid disable/derive values).
	if err := Validate(cfg); err != nil {
		t.Errorf("uncommented skeleton should pass Validate: %v", err)
	}
}

// TestValidateRateBounds: Validate rejects negative / non-finite rate_limit and
// negative rate_burst, preserves 0 (disable / derive), and accepts absent (nil)
// values (a skeleton with rate-limit commented out is valid).
func TestValidateRateBounds(t *testing.T) {
	// nil (absent) is valid.
	if err := Validate(&Config{Version: SchemaVersionV1}); err != nil {
		t.Errorf("Validate(nil rates) = %v, want nil", err)
	}
	zero := 0.0
	if err := Validate(&Config{Version: SchemaVersionV1, RateLimit: &zero}); err != nil {
		t.Errorf("Validate(rate_limit=0) = %v, want nil (0 disables)", err)
	}
	zi := 0
	if err := Validate(&Config{Version: SchemaVersionV1, RateBurst: &zi}); err != nil {
		t.Errorf("Validate(rate_burst=0) = %v, want nil (0 derives)", err)
	}
	neg := -1.0
	if err := Validate(&Config{Version: SchemaVersionV1, RateLimit: &neg}); err == nil {
		t.Error("Validate(negative rate_limit) should fail")
	}
	ni := -2
	if err := Validate(&Config{Version: SchemaVersionV1, RateBurst: &ni}); err == nil {
		t.Error("Validate(negative rate_burst) should fail")
	}
	nan := float64NaN()
	if err := Validate(&Config{Version: SchemaVersionV1, RateLimit: &nan}); err == nil {
		t.Error("Validate(NaN rate_limit) should fail")
	}
	inf := float64Inf()
	if err := Validate(&Config{Version: SchemaVersionV1, RateLimit: &inf}); err == nil {
		t.Error("Validate(+Inf rate_limit) should fail")
	}
}

func float64NaN() float64 { var z float64; return z / z }
func float64Inf() float64 { var z float64; return 1 / z }

// knownDaemonKeys is the set of v1 schema keys the skeleton may carry as
// commented structure. uncommentDaemonSkeleton keeps ONLY lines that, after the
// "# " strip, begin with one of these keys (followed by ":" or whitespace) — the
// skeleton's prose doc lines (also "# "-prefixed) are dropped. This is the
// key-list equivalent of the configgen skeleton's "#|" vs "# " marker scheme.
var knownDaemonKeys = []string{
	"version", "grpc_addr", "http_addr", "metrics_addr",
	"tls_cert", "tls_key", "client_ca", "rate_limit", "rate_burst",
}

// uncommentDaemonSkeleton turns the commented daemon.yaml skeleton into a
// parseable YAML document for the round-trip. The skeleton uses "# " for BOTH
// prose docs and commented YAML structure (unlike the configgen skeleton's "#|"
// marker), so the uncommenter keeps ONLY lines whose remainder begins with a
// known v1 schema key. The real "version: v1" line (uncommented in the skeleton)
// is kept verbatim. Everything else is dropped.
func uncommentDaemonSkeleton(skeleton string) string {
	var b strings.Builder
	for _, line := range strings.Split(skeleton, "\n") {
		if strings.HasPrefix(line, "version:") {
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		dec := strings.TrimPrefix(line, "# ")
		trimmed := strings.TrimLeft(dec, " \t")
		for _, k := range knownDaemonKeys {
			if strings.HasPrefix(trimmed, k+":") || strings.HasPrefix(trimmed, k+" ") {
				// Preserve the leading indentation dec carried (dec already has it).
				b.WriteString(dec)
				b.WriteString("\n")
				break
			}
		}
	}
	return b.String()
}
