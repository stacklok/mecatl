// Package toolhivellm is the ONLY ToolHive-aware code in mecatl (issue #262):
// it detects, by reading ToolHive's OWN on-disk config file, whether a ToolHive
// LLM gateway proxy is set up for this user — and, if so, what LOOPBACK port it
// listens on. It never imports a ToolHive Go package (there is no dependency on
// ToolHive's module graph) and never talks to the network itself; the actual
// live-listing HTTP call is made by the protocol-generic
// internal/adapter/openaicompat.Lister, which this package merely feeds a
// hardcoded loopback base URL.
//
// # What this reads
//
// ToolHive's config file (default $XDG_CONFIG_HOME/toolhive/config.yaml, or
// ~/.config/toolhive/config.yaml) carries an `llm:` block:
//
//	llm:
//	  gateway_url: https://my-org.example.com/toolhive-gateway
//	  proxy:
//	    listen_port: 14000
//
// gateway_url is the UPSTREAM the proxy forwards to (captured here for
// DIAGNOSTIC display only — R5.1/R5.2: it is NEVER used to construct a request
// URL). listen_port is the proxy's LOCAL loopback listen port (default 14000
// when absent/zero).
//
// # Security invariants (v1-mandatory)
//
//   - BaseURL() is ALWAYS "http://127.0.0.1:<port>/v1" — the host is HARDCODED,
//     never derived from gateway_url or any other config value.
//   - tls_skip_verify and any oidc/auth subtree are NEVER decoded — the wire
//     struct below has no field for them, so even a config file that sets
//     tls_skip_verify: true cannot influence anything (there is nowhere for the
//     value to land).
//   - The config file must be a REGULAR file, size-bounded, and OWNED BY THE
//     CALLING UID before a single byte of it is trusted — a config file another
//     user could plant or a mounted volume permission mistake never gets read.
//   - Unknown YAML keys are silently ignored (no KnownFields strictness): a
//     newer ToolHive config schema must not break detection here.
//
// # Layering
//
// Stdlib + go.yaml.in/yaml/v3 ONLY. No domain, no port, no internal/app, no
// other adapter, and — the one invariant this whole package exists to hold —
// NO ToolHive Go import, ever.
package toolhivellm

import (
	"io"
	"os"
	"strconv"

	yaml "go.yaml.in/yaml/v3"
)

const (
	// DefaultConfigRelPath is ToolHive's config file location, relative to the
	// XDG config home (composition joins it with xdgconfig.UserConfigDir).
	DefaultConfigRelPath = "toolhive/config.yaml"

	// PlaceholderToken is the public inbound-auth convention the ToolHive LLM
	// gateway proxy documents for a bearer credential when no real per-user
	// token is otherwise configured. It is not a secret; sending it (or
	// omitting it) never grants privilege the proxy itself doesn't already
	// extend to the caller.
	PlaceholderToken = "thv-proxy"

	// defaultListenPort is the ToolHive LLM proxy's documented default listen
	// port, used when the config's llm.proxy.listen_port is absent or zero.
	defaultListenPort = 14000

	// maxConfigBytes bounds the config file read (CWE-770): ToolHive's config
	// is a small, hand-editable YAML file — 1 MiB is enormous headroom.
	maxConfigBytes = 1 << 20
)

// Config is the detected ToolHive LLM proxy configuration: the loopback port to
// probe, plus the upstream gateway_url captured for diagnostic display only.
type Config struct {
	// GatewayURL is the upstream the proxy forwards to. DIAGNOSTIC DISPLAY
	// ONLY — never used to construct a request URL (R5.1/R5.2).
	GatewayURL string
	// ListenPort is the proxy's local loopback listen port (already resolved
	// to defaultListenPort by DetectConfig when the config left it unset).
	ListenPort int
}

// BaseURL returns the HARDCODED loopback base URL to probe/list/route
// requests through: "http://127.0.0.1:<port>/v1". The host is NEVER derived
// from GatewayURL or any other config value — this is the ONE security
// invariant this method exists to enforce (R5.1).
func (c Config) BaseURL() string {
	port := c.ListenPort
	if port <= 0 {
		port = defaultListenPort
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/v1"
}

// wireConfig is the ONLY shape this package ever decodes from ToolHive's
// config file. It has no field for tls_skip_verify, oidc, or anything else
// under llm/proxy — a config file setting those has literally nowhere to
// land (TestDetectConfig_TLSSkipVerifyNeverDecoded pins this). Unknown top-
// level and nested keys are ignored (no KnownFields strictness): a config
// schema evolution upstream must not break detection here.
type wireConfig struct {
	LLM struct {
		GatewayURL string `yaml:"gateway_url"`
		Proxy      struct {
			ListenPort int `yaml:"listen_port"`
		} `yaml:"proxy"`
	} `yaml:"llm"`
}

// statOwner extracts the owning uid from a os.FileInfo's platform-specific
// Sys() value. It is an unexported package-level SEAM (not a public API) so
// the wrong-owner unit test can override it — a root-less test process can't
// chown a fixture to a different uid to exercise the ownership-mismatch
// branch for real. Its production implementation is split by build tag
// (detect_unix.go / detect_other.go, mirroring hookexec's `_unix.go`
// convention): the unix implementation asserts the real syscall.Stat_t and
// fails CLOSED (ok=false) on a failed type-assert (an exotic Sys() value);
// the non-unix implementation fails CLOSED unconditionally (ok=false always)
// since there is no uid concept to check — never "trust it" either way.

// DetectConfig reads ToolHive's config file at path and reports whether an
// `llm:` block with a non-empty gateway_url was found. It is two-value, no
// error: EVERY failure mode (missing file, wrong type, oversized, wrong
// owner, malformed YAML, missing/empty llm.gateway_url, out-of-range port) is
// the SAME fail-soft "skip detection" outcome — the caller logs one DEBUG
// diagnostic on a miss, never surfaces a file-parsing error to the operator.
//
// Sequence (each step fails closed, never open):
//  1. os.Stat(path) — must exist.
//  2. Mode().IsRegular() — never a directory/device/pipe/symlink-to-non-regular.
//  3. Size() <= maxConfigBytes.
//  4. Owning uid == the CALLING process's uid (statOwner; a failed
//     type-assertion — e.g. non-unix — fails closed, never "trust it").
//  5. Open + io.LimitReader(maxConfigBytes) read (defense-in-depth against a
//     TOCTOU size change between Stat and Open).
//  6. Typed YAML decode into wireConfig (unknown fields ignored).
//  7. llm.gateway_url non-empty, else a miss.
//  8. llm.proxy.listen_port: 0/absent -> defaultListenPort; out of [1,65535]
//     -> a miss (an explicitly bogus port is a config error, not a silent
//     floor).
func DetectConfig(path string) (Config, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return Config{}, false
	}
	if !fi.Mode().IsRegular() {
		return Config{}, false
	}
	if fi.Size() > maxConfigBytes {
		return Config{}, false
	}
	if uid, ok := statOwner(fi); !ok || uid != uint32(os.Getuid()) { //nolint:gosec // uid is always small
		return Config{}, false
	}

	f, err := os.Open(path) //nolint:gosec // path is composition-resolved (XDG config dir or an operator flag), not user request input
	if err != nil {
		return Config{}, false
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return Config{}, false
	}

	var wire wireConfig
	if err := yaml.Unmarshal(data, &wire); err != nil {
		return Config{}, false
	}
	if wire.LLM.GatewayURL == "" {
		return Config{}, false
	}

	port := wire.LLM.Proxy.ListenPort
	switch {
	case port == 0:
		port = defaultListenPort
	case port < 1 || port > 65535:
		return Config{}, false
	}

	return Config{GatewayURL: wire.LLM.GatewayURL, ListenPort: port}, true
}
