// Package daemonconfig is a strict, versioned, operator-selected daemon
// configuration file loaded ONLY when `mecated serve --config PATH` is
// explicitly supplied. No conventional auto-load, no project discovery, no
// app.Config widening.
//
// Schema v1 is a small API-edge subset: gRPC listen address, HTTP/SSE listen
// address, metrics/admin address, TLS cert/key/CA paths, and rate-limit/burst.
// Authentication TOKEN VALUE is NOT accepted in YAML; server auth remains
// MECATL_AUTH_TOKEN / existing CLI behaviour. OTLP is deferred.
//
// Precedence: built-in defaults < config file < explicitly supplied CLI.
//
// Security note on logging: the daemon config file's RAW content and any
// future secret-bearing fields are NEVER logged. Effective security-POSTURE
// values (listen addresses, TLS presence, rate-limit/burst, auth on/off) ARE
// logged by the cmd main's logSecurityPosture, because an operator must be
// able to confirm what is actually bound — that is the posture, not the raw
// file. Do not conflate the two: "raw content/secrets not logged" is the rule;
// "effective posture values may be logged" is the deliberate exception.
package daemonconfig

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// SchemaVersionV1 is the only supported config version.
const SchemaVersionV1 = "v1"

// DaemonConfigRelPath is the conventional daemon.yaml location relative to the
// XDG config base (`mecatl/daemon.yaml`), re-exported so the WRITE paths
// (`mecated config daemon init` / `config daemon validate`) and the docs agree
// on a single relative path. It is NEVER used for auto-load — the daemon config
// is loaded ONLY when --config is supplied explicitly (issue #338, ADR 0088).
// Re-exporting it here keeps the conventional path in the SAME package that owns
// the schema, mirroring the configgen/permconfig SettingsRelPath pattern.
const DaemonConfigRelPath = "mecatl/daemon.yaml"

// daemonSkeleton is the committed, commented v1 daemon.yaml skeleton, embedded so
// `mecated config daemon init` can write it without a runtime renderer. It is the
// minimal, runnable skeleton: version plus loopback defaults and concise commented
// TLS/rate examples. No secret values ever appear in it (the auth TOKEN is env-only).
//
//go:embed daemon.skeleton.yaml
var daemonSkeleton string

// Skeleton returns the committed commented daemon.yaml skeleton that
// `mecated config daemon init` writes (or prints with --print). It is the embedded
// artifact, NOT a fresh render.
func Skeleton() string {
	return daemonSkeleton
}

// Validate runs the EFFECTIVE semantic validation possible WITHOUT starting or
// binding the server: rate_limit/rate_burst sanity bounds. The schema (unknown
// keys, version, types) is already enforced by Load/parse; Validate adds the
// cross-field bounds that mirror the runtime validateEffectiveConfig rate checks
// so a `mecated config daemon validate` catches the same misconfiguration before
// serve. 0 values are meaningful (disable / derive); only negative and
// non-finite (NaN/Inf) values are rejected. It does NOT validate listener
// binding (loopback/perf-mcp guards) — those depend on the full effective config
// + CLI flags, not the file alone.
func Validate(c *Config) error {
	if c.RateLimit != nil {
		v := *c.RateLimit
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("rate_limit %v is invalid: must be >= 0 and finite (0 disables rate limiting)", v)
		}
	}
	if c.RateBurst != nil {
		if *c.RateBurst < 0 {
			return fmt.Errorf("rate_burst %d is invalid: must be >= 0 (0 derives a sane default from rate_limit)", *c.RateBurst)
		}
	}
	return nil
}

// Config is the parsed daemon configuration. It carries only the API-edge slice
// for v1. Every pointer field distinguishes absent (nil) from an explicitly
// supplied zero/empty value (non-nil pointer to zero/empty). Version is a plain
// string because the schema requires a non-empty value (absent and explicit ""
// are both rejected identically), so a pointer would add no information.
type Config struct {
	// Version is required; it must be exactly "v1". Any other value or a missing
	// key is a parse error.
	Version string `yaml:"version"`

	// GRPCAddr is the gRPC listen address (host:port). nil = not in file.
	GRPCAddr *string `yaml:"grpc_addr,omitempty"`

	// HTTPAddr is the HTTP/SSE listen address (host:port). nil = not in file.
	HTTPAddr *string `yaml:"http_addr,omitempty"`

	// MetricsAddr is the metrics/admin listen address (host:port). nil = not in
	// file. An explicitly supplied empty string means "disable metrics".
	MetricsAddr *string `yaml:"metrics_addr,omitempty"`

	// TLSCert is the path to the PEM server certificate. nil = not in file.
	TLSCert *string `yaml:"tls_cert,omitempty"`

	// TLSKey is the path to the PEM server private key. nil = not in file.
	TLSKey *string `yaml:"tls_key,omitempty"`

	// ClientCA is the path to the PEM client CA bundle for mutual TLS. nil =
	// not in file.
	ClientCA *string `yaml:"client_ca,omitempty"`

	// RateLimit is the sustained per-client request rate (req/s). nil = not in
	// file. An explicitly supplied 0 means "no rate limiting".
	RateLimit *float64 `yaml:"rate_limit,omitempty"`

	// RateBurst is the token-bucket burst size. nil = not in file. An explicitly
	// supplied 0 means "derive from rate limit".
	RateBurst *int `yaml:"rate_burst,omitempty"`
}

// Load reads and validates the daemon config at path. It returns the parsed
// Config and a line-aware error on any problem: unknown top-level keys,
// unsupported or missing version, a multi-document file, trailing content after
// the single document, or an unreadable file. It does NOT parse any nested key
// (the schema has none today, but the strict parser rejects unknown top-level
// keys).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("daemon config file not found: %s", path)
		}
		return nil, fmt.Errorf("reading daemon config %s: %w", path, err)
	}

	return parse(data, path)
}

// parse decodes YAML data with strict unknown-key rejection and returns the
// validated Config. path is used in error messages. It decodes directly into
// Config (no intermediate mirror): every pointer field preserves absent-vs-zero
// presence on its own, and Version is a plain string because absent and explicit
// "" are rejected identically by the required-version check — so the mirror
// would have been pure duplication.
func parse(data []byte, path string) (*Config, error) {
	// Strict (KnownFields) decode: an unrecognized key in the document is a
	// parse error, not a silently-ignored typo. Both error paths deliberately
	// do NOT echo raw YAML content: a *yaml.TypeError (unknown key / wrong type)
	// carries only the key name by construction; a plain decode error (syntax /
	// bad indentation / stray marker) embeds the offending source line in its
	// message, so it must NOT be passed through with %w (CWE-209). Both paths
	// name only the path and the operator-visible resolution.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		// Distinguish a schema error (unknown key / wrong type) from a YAML
		// syntax error so operator guidance is accurate. yaml.v3 returns a
		// *yaml.TypeError for unknown-key/type mismatches and a plain error
		// (carrying line/column AND the offending source text) for malformed-
		// document syntax problems. The syntax-equals-failure branch below must
		// NOT wrap the raw error: its message embeds the source line, which
		// leaks file content to a caller (CWE-209).
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("parsing daemon config %s: does not match the expected schema (unknown key or type); the only recognized keys are version, grpc_addr, http_addr, metrics_addr, tls_cert, tls_key, client_ca, rate_limit, and rate_burst", path)
		}
		return nil, fmt.Errorf("parsing daemon config %s: invalid YAML syntax (the document must be valid YAML matching the v1 schema)", path)
	}

	// Reject a multi-document file or trailing content. A single decode above
	// consumed the first (or only) document; a second decode MUST hit io.EOF —
	// the v1 schema is a single document. A nil return (a second document
	// decoded) or any non-EOF error (a second document with an unknown key, or
	// trailing garbage) means the file carries more than one document, so refuse
	// rather than silently drop the rest.
	if err := dec.Decode(&Config{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing daemon config %s: multiple documents are not supported (the v1 schema is a single document)", path)
	}

	if cfg.Version == "" {
		return nil, fmt.Errorf("%s: version key is required and must be %q", path, SchemaVersionV1)
	}
	if cfg.Version != SchemaVersionV1 {
		return nil, fmt.Errorf("%s: unsupported version %q (only %q is supported)", path, cfg.Version, SchemaVersionV1)
	}

	return &cfg, nil
}
