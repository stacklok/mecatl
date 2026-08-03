package cliconfig

import (
	"flag"
	"fmt"
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
	"a token-bearing URL must be https, or http to a loopback host"

// mcpServerName is the allowed shape of an --mcp-server name (ADR 0082
// hardening, CWE-178): the name derives the MCP_<NAME>_TOKEN env var by ASCII
// upper-casing, so it is restricted to characters that map 1:1 into a settable
// POSIX env name. A hyphen/space/dot would derive an env var a scheduler
// cannot set (MCP_MY-SVC_TOKEN), silently connecting unauthenticated; a
// non-ASCII letter (ı) case-folds surprisingly.
var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// MCPServerList is a repeatable flag.Value collecting --mcp-server name=URL
// entries into a slice of mcp.ServerConfig. It was extracted VERBATIM from
// cmd/mecated (issue #341) so mecatequi and mecak8s register the SAME flag +
// token convention instead of growing three drifting copies: a scheduler
// (titlani) launching one-shot runs injects a short-lived per-run identity as
// MCP_<NAME>_TOKEN, and the run presents it as a Bearer to the named MCP
// endpoint.
//
// The per-server token env read (MCP_<NAME>_TOKEN, name upper-cased) happens
// in Set — cliconfig is the cmd-side helper that MAY read the process
// environment (see the package comment); the token is SECRET-shaped and is
// never logged. A missing/empty token simply leaves Headers nil (token
// optional — an unauthenticated dev endpoint stays reachable).
type MCPServerList []mcp.ServerConfig

// String implements flag.Value: the comma-joined server NAMES only (never a
// URL query secret or a header), the stable form flag's default display expects.
func (l *MCPServerList) String() string {
	names := make([]string, 0, len(*l))
	for _, c := range *l {
		names = append(names, c.Name)
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
//     the first one's token.
//   - CWE-319: when a token IS attached, the URL must be https — or http to an
//     explicit loopback host (the same allowlist as mcp.ValidateClientURL, which
//     this reuses) — so a scheduler-injected bearer is never sent cleartext
//     off-host. A tokenless URL is not gated (unchanged).
func (l *MCPServerList) Set(v string) error {
	name, url, ok := strings.Cut(v, "=")
	if !ok || name == "" || url == "" {
		return fmt.Errorf("invalid --mcp-server %q: want name=URL", v)
	}
	if !mcpServerName.MatchString(name) {
		return fmt.Errorf("invalid --mcp-server %q: name must match [A-Za-z0-9_]+ (it derives the MCP_<NAME>_TOKEN env var)", v)
	}
	envName := "MCP_" + strings.ToUpper(name) + "_TOKEN"
	for _, prev := range *l {
		if strings.EqualFold(prev.Name, name) {
			return fmt.Errorf("invalid --mcp-server %q: name %q collides with earlier server %q on the token env var %s", v, name, prev.Name, envName)
		}
	}
	cfg := mcp.ServerConfig{Name: name, URL: url}
	if tok := os.Getenv(envName); tok != "" {
		// A bearer will ride every request to this URL: refuse shapes that would
		// leak it in cleartext off-host (https, or http to loopback, only).
		if err := mcp.ValidateClientURL(url); err != nil {
			return fmt.Errorf("invalid --mcp-server %q: a bearer token from %s is attached, so the URL must be https (or http to a loopback host): %w", v, envName, err)
		}
		cfg.Headers = map[string]string{"Authorization": "Bearer " + tok}
	}
	*l = append(*l, cfg)
	return nil
}

// Servers returns the collected configs as the plain []mcp.ServerConfig
// app.Config.MCPServers expects, or nil for a nil receiver so a config struct
// built WITHOUT RegisterMCPServerFlag (e.g. a test constructing the cmd config
// directly) yields the byte-identical unset default — mirroring
// KeyValueList.AsMap's nil-receiver discipline.
func (l *MCPServerList) Servers() []mcp.ServerConfig {
	if l == nil {
		return nil
	}
	return []mcp.ServerConfig(*l)
}

// RegisterMCPServerFlag registers --mcp-server on fs bound to a fresh
// MCPServerList and returns it, so a cmd main threads the parsed servers onto
// app.Config via Servers(). It is the ONE registration path for the flag —
// mecated, mecatequi, and mecak8s all use it, so the flag name, parse
// semantics, and MCP_<NAME>_TOKEN convention cannot drift apart. An empty help
// falls back to DefaultMCPServerFlagHelp (the mecated wording).
func RegisterMCPServerFlag(fs *flag.FlagSet, help string) *MCPServerList {
	if help == "" {
		help = DefaultMCPServerFlagHelp
	}
	list := new(MCPServerList)
	fs.Var(list, "mcp-server", help)
	return list
}
