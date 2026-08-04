package cliconfig

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

// DefaultMCPServerFlagHelp is the shared --mcp-server help text (the mecated
// wording plus the ADR-0082 hardening notes). A caller may override it
// per-main (mirroring the other Register* helpers), but the default keeps the
// three mains' --help identical.
const DefaultMCPServerFlagHelp = "remote MCP server as name=URL (repeatable); auth token read from MCP_<NAME>_TOKEN. " +
	"The name must match [A-Za-z0-9_]+ and be case-insensitively unique across entries (it derives the token env var); " +
	"a token-bearing URL must be https, or http to a loopback host (see --mcp-server-insecure-http for the explicit per-server opt-out)"

// DefaultMCPServerInsecureHTTPFlagHelp is the shared --mcp-server-insecure-http
// help text (issue #358, ADR 0090). Unlike --mcp-server it is NOT overridable
// per-main: the acknowledgment wording is the point of the flag, so all three
// mains state it identically.
const DefaultMCPServerInsecureHTTPFlagHelp = "EXPLICIT PER-SERVER OPT-IN (repeatable): name of a --mcp-server entry whose " +
	"MCP_<NAME>_TOKEN bearer may ride plain http to a NON-loopback host. This acknowledges the token travels CLEARTEXT " +
	"on the network path to that server — you are relying on network-layer controls (NetworkPolicy / namespace trust) " +
	"plus short-lived tokens as the mitigations. It relaxes ONLY the http scheme gate, ONLY for the named server, " +
	"order-independently of where its --mcp-server appears; naming a server that is not registered, or whose URL is " +
	"already https / loopback / non-http, is an error (a stale acknowledgment is loud, never silently inert)"

// mcpServerName is the allowed shape of an --mcp-server name (ADR 0082
// hardening, CWE-178): the name derives the MCP_<NAME>_TOKEN env var by ASCII
// upper-casing, so it is restricted to characters that map 1:1 into a settable
// POSIX env name. A hyphen/space/dot would derive an env var a scheduler
// cannot set (MCP_MY-SVC_TOKEN), silently connecting unauthenticated; a
// non-ASCII letter (ı) case-folds surprisingly. --mcp-server-insecure-http
// names run the SAME validation — a relaxation can only ever name a shape a
// server may legally have.
var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// mcpServerEntry is one parsed --mcp-server occurrence plus the parse-time
// metadata the deferred Finalize gate needs to report errors in the operator's
// own terms (the raw flag value and the token env var name).
type mcpServerEntry struct {
	cfg mcp.ServerConfig
	// raw is the original name=URL flag value, quoted in Finalize errors.
	raw string
	// envName is the MCP_<NAME>_TOKEN env var the name derives.
	envName string
	// hasToken records whether envName held a non-empty token at parse time —
	// the condition under which the CWE-319 scheme gate applies.
	hasToken bool
}

// MCPServerList is a repeatable flag.Value collecting --mcp-server name=URL
// entries into mcp.ServerConfig values, plus the --mcp-server-insecure-http
// relaxation names its Finalize step resolves against them. It was extracted
// from cmd/mecated (issue #341) so mecatequi and mecak8s register the SAME
// flag + token convention instead of growing three drifting copies: a
// scheduler (titlani) launching one-shot runs injects a short-lived per-run
// identity as MCP_<NAME>_TOKEN, and the run presents it as a Bearer to the
// named MCP endpoint.
//
// The per-server token env read (MCP_<NAME>_TOKEN, name upper-cased) happens
// in Set — cliconfig is the cmd-side helper that MAY read the process
// environment (see the package comment); the token is SECRET-shaped and is
// never logged. A missing/empty token simply leaves Headers nil (token
// optional — an unauthenticated dev endpoint stays reachable).
//
// LIFECYCLE (issue #358): Set only COLLECTS; the token-bearing scheme gate
// (CWE-319) runs in Finalize, which every main calls right after flag.Parse.
// Deferring the gate is what makes --mcp-server-insecure-http order-independent
// — a relaxation parsed after its --mcp-server would otherwise arrive too late.
// Servers() refuses (panics) before a successful Finalize, so a main cannot
// hand un-gated configs to app.Build by forgetting the call.
type MCPServerList struct {
	entries []mcpServerEntry
	// insecure holds the --mcp-server-insecure-http names in occurrence order,
	// resolved against entries in Finalize (never at Set — order independence).
	insecure []string
	// finalized flips when Finalize succeeds; Servers() requires it.
	finalized bool
}

// String implements flag.Value: the comma-joined server NAMES only (never a
// URL query secret or a header), the stable form flag's default display expects.
func (l *MCPServerList) String() string {
	if l == nil {
		return ""
	}
	names := make([]string, 0, len(l.entries))
	for _, e := range l.entries {
		names = append(names, e.cfg.Name)
	}
	return strings.Join(names, ",")
}

// Set parses a single "name=URL" entry, splitting at the FIRST '=' (a URL may
// itself contain '='). A per-server bearer token is read from the environment
// variable MCP_<NAME>_TOKEN (name upper-cased) when present and becomes an
// "Authorization: Bearer <token>" header on that server only.
//
// Two ADR-0082 hardenings (applied to all three mains, deliberately tightening
// mecated's original behavior):
//
//   - CWE-178: the name must match mcpServerName, and two entries whose
//     upper-cased names collide (vmcp/VMCP/vMcp -> MCP_VMCP_TOKEN) are
//     rejected — otherwise the second server would silently share (or steal)
//     the first one's token. Both checks are entry-local, so they stay inline.
//   - CWE-319: when a token IS attached, the URL must be https — or http to an
//     explicit loopback host — so a scheduler-injected bearer is never sent
//     cleartext off-host. Since issue #358 that gate runs in Finalize, NOT
//     here: it must see the full --mcp-server-insecure-http relaxation set,
//     which argv may order after this entry. A tokenless URL is not gated
//     (unchanged).
func (l *MCPServerList) Set(v string) error {
	name, u, ok := strings.Cut(v, "=")
	if !ok || name == "" || u == "" {
		return fmt.Errorf("invalid --mcp-server %q: want name=URL", v)
	}
	if !mcpServerName.MatchString(name) {
		return fmt.Errorf("invalid --mcp-server %q: name must match [A-Za-z0-9_]+ (it derives the MCP_<NAME>_TOKEN env var)", v)
	}
	envName := "MCP_" + strings.ToUpper(name) + "_TOKEN"
	for _, prev := range l.entries {
		if strings.EqualFold(prev.cfg.Name, name) {
			return fmt.Errorf("invalid --mcp-server %q: name %q collides with earlier server %q on the token env var %s", v, name, prev.cfg.Name, envName)
		}
	}
	entry := mcpServerEntry{cfg: mcp.ServerConfig{Name: name, URL: u}, raw: v, envName: envName}
	if tok := os.Getenv(envName); tok != "" {
		// A bearer will ride every request to this URL. Whether the URL shape
		// may carry it (https, http to loopback, or the per-server insecure-http
		// opt-in) is decided in Finalize, once the whole argv has been parsed.
		entry.hasToken = true
		entry.cfg.Headers = map[string]string{"Authorization": "Bearer " + tok}
	}
	l.entries = append(l.entries, entry)
	return nil
}

// Finalize is the post-parse validation step every main calls right after
// flag.Parse (issue #358). It resolves the --mcp-server-insecure-http
// relaxations against the collected --mcp-server entries and THEN runs the
// token-bearing scheme gate, so the two flags compose order-independently:
//
//  1. Every relaxation must name a registered server (case-insensitively,
//     matching the env-var derivation) whose URL actually IS plain http to a
//     non-loopback host. Anything else — an unknown name, an https server, a
//     loopback server, a non-http scheme — is an error: a stale acknowledgment
//     must be loud, never silently inert.
//  2. Every token-bearing entry NOT relaxed must pass mcp.ValidateClientURL
//     (https, or http to loopback) — the unchanged CWE-319 default posture.
//
// Nil-receiver safe (a config built without RegisterMCPServerFlag has nothing
// to gate). Idempotent on success.
func (l *MCPServerList) Finalize() error {
	if l == nil {
		return nil
	}
	relaxed := make([]bool, len(l.entries))
	for _, name := range l.insecure {
		idx := -1
		for i, e := range l.entries {
			if strings.EqualFold(e.cfg.Name, name) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("invalid --mcp-server-insecure-http %q: no --mcp-server registers that name", name)
		}
		e := l.entries[idx]
		if !plainHTTPOffHost(e.cfg.URL) {
			return fmt.Errorf("invalid --mcp-server-insecure-http %q: server %q URL %q is not plain http to a non-loopback host — the acknowledgment is stale (the relaxation covers ONLY the http scheme gate); drop the flag", name, e.cfg.Name, e.cfg.URL)
		}
		relaxed[idx] = true
	}
	for i, e := range l.entries {
		if !e.hasToken || relaxed[i] {
			continue
		}
		if err := mcp.ValidateClientURL(e.cfg.URL); err != nil {
			return fmt.Errorf("invalid --mcp-server %q: a bearer token from %s is attached, so the URL must be https (or http to a loopback host) — or pass --mcp-server-insecure-http %s to EXPLICITLY accept the token traveling cleartext on the network path to that server: %w", e.raw, e.envName, e.cfg.Name, err)
		}
	}
	l.finalized = true
	return nil
}

// plainHTTPOffHost reports whether raw is exactly the URL shape the
// --mcp-server-insecure-http relaxation covers: an absolute http URL with a
// host that is NOT loopback. It reuses mcp.ValidateClientURL as the single
// loopback oracle (http+loopback validates clean, so it is NOT off-host)
// rather than duplicating the allowlist.
func plainHTTPOffHost(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || !strings.EqualFold(u.Scheme, "http") || u.Hostname() == "" {
		return false
	}
	return mcp.ValidateClientURL(raw) != nil
}

// Servers returns the collected configs as the plain []mcp.ServerConfig
// app.Config.MCPServers expects, or nil for a nil receiver so a config struct
// built WITHOUT RegisterMCPServerFlag (e.g. a test constructing the cmd config
// directly) yields the byte-identical unset default — mirroring
// KeyValueList.AsMap's nil-receiver discipline.
//
// FAIL-CLOSED: a non-nil list panics if Finalize has not succeeded — the
// deferred token-bearing scheme gate (see Finalize) has not run, and handing
// out un-gated configs would silently reopen CWE-319. The panic is a
// programmer-error guard for a future main that forgets the post-parse call,
// never an operator-reachable path (all three mains Finalize in parseFlags).
func (l *MCPServerList) Servers() []mcp.ServerConfig {
	if l == nil {
		return nil
	}
	if !l.finalized {
		panic("cliconfig: MCPServerList.Servers called before a successful Finalize — the deferred --mcp-server token scheme gate has not run")
	}
	if len(l.entries) == 0 {
		return nil
	}
	out := make([]mcp.ServerConfig, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e.cfg)
	}
	return out
}

// mcpInsecureHTTPValue is the flag.Value for --mcp-server-insecure-http. It
// only COLLECTS validated names onto its MCPServerList; resolution against
// the --mcp-server entries happens in Finalize (order independence). Name
// validation mirrors --mcp-server's own (charset + case-insensitive
// uniqueness) so the relaxation grammar cannot drift from the server grammar.
type mcpInsecureHTTPValue struct {
	list *MCPServerList
}

// String implements flag.Value: the comma-joined relaxation names. Nil-safe
// because the flag package probes a zero Value for default display.
func (v *mcpInsecureHTTPValue) String() string {
	if v == nil || v.list == nil {
		return ""
	}
	return strings.Join(v.list.insecure, ",")
}

// Set validates and collects one relaxation name. The charset check is the
// SAME mcpServerName rule as --mcp-server (#347): a name that no server may
// legally carry is rejected immediately, and a case-insensitive duplicate is
// rejected loudly (one acknowledgment per server; a repeat is a copy-paste
// error, consistent with the env-name collision rule).
func (v *mcpInsecureHTTPValue) Set(name string) error {
	if !mcpServerName.MatchString(name) {
		return fmt.Errorf("invalid --mcp-server-insecure-http %q: name must match [A-Za-z0-9_]+ (the --mcp-server name grammar)", name)
	}
	for _, prev := range v.list.insecure {
		if strings.EqualFold(prev, name) {
			return fmt.Errorf("invalid --mcp-server-insecure-http %q: duplicate of earlier %q", name, prev)
		}
	}
	v.list.insecure = append(v.list.insecure, name)
	return nil
}

// RegisterMCPServerFlag registers --mcp-server AND its companion
// --mcp-server-insecure-http on fs, bound to one fresh MCPServerList, and
// returns it, so a cmd main threads the parsed servers onto app.Config via
// Servers() after calling Finalize post-parse. It is the ONE registration
// path for both flags — mecated, mecatequi, and mecak8s all use it, so the
// flag names, parse semantics, the MCP_<NAME>_TOKEN convention, and the
// insecure-http acknowledgment wording cannot drift apart. An empty help
// falls back to DefaultMCPServerFlagHelp (the mecated wording); the
// insecure-http help is deliberately NOT overridable.
func RegisterMCPServerFlag(fs *flag.FlagSet, help string) *MCPServerList {
	if help == "" {
		help = DefaultMCPServerFlagHelp
	}
	list := new(MCPServerList)
	fs.Var(list, "mcp-server", help)
	fs.Var(&mcpInsecureHTTPValue{list: list}, "mcp-server-insecure-http", DefaultMCPServerInsecureHTTPFlagHelp)
	return list
}
