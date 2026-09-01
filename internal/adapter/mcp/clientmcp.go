package mcp

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxClientServers caps how many MCP servers ONE client may declare on a single
// session-creating request. The factory connects them CONCURRENTLY under
// maxConnectConcurrency (16, above this cap), so the cap bounds the goroutine and
// connection blast of one create (CWE-400, resource exhaustion) rather than its
// wall-clock — the worst-case create stall is about ONE ClientConnectTimeout, not
// MaxClientServers of them. 8 is generous for a real editor or SDK.
const MaxClientServers = 8

// MaxClientServerNameLen bounds a client-supplied server name. It matches the
// server package's validateDebugMCPNames bound for the sibling debug_mcp_servers
// field: same message, same tool namespace, so the same rules.
const MaxClientServerNameLen = 64

// ClientConnectTimeout bounds the connect handshake + tool listing for ONE
// client-provided MCP server, deliberately shorter than the operator-path
// defaultConnectTimeout (30s): a slow client server must not hold a session
// create open for the full operator budget. It rides each spec's
// ServerConfig.Timeout seam.
const ClientConnectTimeout = 10 * time.Second

// ErrClientServerRejected is the sentinel every PartitionClientServers rejection
// wraps. Callers map it to their own transport error (the ACP adapter to
// codeInvalidParams, the gRPC/HTTP surface to ErrInvalidArgument) WITHOUT
// re-classifying the entry themselves — the classification happens here, once.
var ErrClientServerRejected = errors.New("mcp: client MCP server rejected")

// ClientServer is a transport-neutral, client-supplied MCP server entry: the
// discriminant fields (type/command/url) plus the optional per-server headers.
//
// It is deliberately the SHARED shape rather than each surface's own: the ACP
// session/new mcpServers list and the CreateSessionRequest.mcp_servers wire
// field both map INTO it, so both reach the same classifier. A surface that
// grew its own partition function would be the second, divergent validator
// this type exists to prevent.
//
// Command is carried but NEVER executed. It exists so a client that mirrors the
// ACP entry shape (a command-shaped entry with no explicit type) is classified
// as stdio and hard-rejected AS stdio, instead of falling through to the vaguer
// "no recognized transport" arm. mecatl never spawns an MCP server process
// (AGENTS.md: "No stdio MCP, ever").
type ClientServer struct {
	Name    string
	Command string
	URL     string
	Type    string
	Headers map[string]string
}

// clientServerNameRune reports whether r is legal in a client-supplied MCP server
// name. It is the same charset the server package's debugMCPNameRune allows for
// debug_mcp_servers, deliberately: both feed the SAME tool namespace, so a
// character illegal in one is illegal in the other.
func clientServerNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}

// validateClientServerName bounds a client-supplied server name BEFORE anything
// mounts it. Without it the name reached mcp.Connect, which does reject "" and
// "__" — but at CONNECT time, inside the best-effort manager, where a violation
// surfaced as "server unreachable; skipping for this session". That message was
// doubly wrong: the server was never dialled, and the create still returned a
// session. (Since the all-or-nothing wire check it returns client_mcp_unreachable
// instead, which is still the wrong diagnosis for a malformed name.)
//
// Three things the connect-time check does NOT cover, which is why validation
// belongs here:
//
//   - DUPLICATES. Two entries named "notes" both connect; their tools then
//     collide in tool.Catalog.Register and one set is dropped with a WARN only.
//   - NAMESPACE FORGERY. namespacedName is raw concatenation
//     ("mcp__" + server + "__" + tool), so "__" inside a name forges another
//     server's namespace. Arbitrary bytes would also reach the tool schema the
//     provider sees.
//   - PERMISSION-RULE INHERITANCE. governance rules exact-match or glob a tool
//     name ("mcp__github__*"), so a client server named "github" exposing
//     "create_issue" registers as "mcp__github__create_issue" and inherits an
//     operator rule written for the REAL github server — reachable whenever the
//     global github server is absent or does not expose that tool, since
//     global-wins only fires on an exact full-name collision.
//
// The charset+length+"__" rules close forgery and bound what reaches a schema.
// They do NOT close the last one: "github" is a perfectly legal name. Nothing
// here can distinguish "a client naming its own server github" from
// "impersonation", because the operator's namespace and the client's are the same
// flat namespace by construction. That is a REAL residual, and the reason the
// field is gated to a UNIX-socket-only deployment where the caller is a local
// process the operator already trusts. Closing it properly means prefixing
// client servers into their own namespace, which changes every tool name a
// client sees and is a wire-visible decision for its own ADR.
func validateClientServerName(name string, seen map[string]struct{}) error {
	if name == "" {
		return fmt.Errorf("%w: MCP server name is required", ErrClientServerRejected)
	}
	if len(name) > MaxClientServerNameLen {
		return fmt.Errorf("%w: MCP server name %q is longer than %d characters",
			ErrClientServerRejected, name, MaxClientServerNameLen)
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("%w: MCP server name %q must not contain %q (it would forge another server's tool namespace)",
			ErrClientServerRejected, name, "__")
	}
	for _, r := range name {
		if !clientServerNameRune(r) {
			return fmt.Errorf("%w: MCP server name %q must use only [A-Za-z0-9._-]", ErrClientServerRejected, name)
		}
	}
	if _, dup := seen[name]; dup {
		return fmt.Errorf("%w: duplicate MCP server name %q", ErrClientServerRejected, name)
	}
	seen[name] = struct{}{}
	return nil
}

// PartitionClientServers classifies client-provided MCP server entries and
// returns the streaming-HTTP ones as ServerConfig specs to mount per-session. It
// is the SINGLE validation path for every client-supplied MCP surface.
//
// It is FAIL-LOUD: the first bad entry rejects the whole request, so a session
// never silently drops a server the client asked for.
//
// Classification per entry:
//
//   - STDIO — Type=="stdio", or Type=="" with a non-empty Command: hard-rejected
//     (mecatl never spawns an MCP server process). This arm is UNCONDITIONAL —
//     no deployment policy, listener topology, or configuration reaches it, which
//     is what makes "no stdio MCP, ever" an invariant rather than a default.
//   - SSE — Type=="sse": rejected (streaming-HTTP transport only).
//   - HTTP — Type=="http", or Type=="" with a non-empty URL: validated via
//     ValidateClientURL (SSRF scheme/host allowlist; see its doc for what that
//     does and does NOT cover) and, on success, appended as a ServerConfig
//     carrying the entry's Name, URL, Headers, the bounded per-server
//     ClientConnectTimeout, and NoRedirects — a client endpoint may not redirect
//     the daemon onward to a host this validator never saw.
//
// Every entry's NAME is validated first, for every transport, via
// validateClientServerName: non-empty, <= MaxClientServerNameLen, [A-Za-z0-9._-]
// only, no "__", and unique within the request. See that function for why the
// connect-time check in Connect is not sufficient.
//
// It rejects a request declaring more than MaxClientServers. It returns nil
// specs (no error) for an empty list, so a create with no MCP servers takes the
// shared-engine path.
//
// Header VALUES are never included in any returned error: an error naming a
// server names it by Name and URL only. Headers are secret-shaped (AGENTS.md).
func PartitionClientServers(servers []ClientServer) ([]ServerConfig, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	if len(servers) > MaxClientServers {
		return nil, fmt.Errorf("%w: too many MCP servers (%d > %d max)", ErrClientServerRejected, len(servers), MaxClientServers)
	}
	specs := make([]ServerConfig, 0, len(servers))
	seen := make(map[string]struct{}, len(servers))
	for _, m := range servers {
		// The NAME is validated first, for every transport. A malformed name is a
		// malformed name whether the entry would have been rejected as stdio anyway,
		// and checking it before the switch means no arm can forget it.
		if nerr := validateClientServerName(m.Name, seen); nerr != nil {
			return nil, nerr
		}
		switch {
		case m.Type == "stdio" || (m.Type == "" && m.Command != ""):
			return nil, fmt.Errorf("%w: stdio MCP server %q rejected (mecatl is streaming-HTTP MCP only)", ErrClientServerRejected, m.Name)
		case m.Type == "sse":
			return nil, fmt.Errorf("%w: sse transport not supported for MCP server %q (streaming-HTTP only)", ErrClientServerRejected, m.Name)
		case m.Type == "http" || (m.Type == "" && m.URL != ""):
			if verr := ValidateClientURL(m.URL); verr != nil {
				return nil, fmt.Errorf("%w: MCP server %q rejected: %w", ErrClientServerRejected, m.Name, verr)
			}
			specs = append(specs, ServerConfig{
				Name:    m.Name,
				URL:     m.URL,
				Headers: cloneHeaders(m.Headers),
				Timeout: ClientConnectTimeout,
				// A client-supplied endpoint may not redirect: ValidateClientURL vets
				// the URL given, and this closes the one it could be sent to next.
				NoRedirects: true,
			})
		default:
			return nil, fmt.Errorf("%w: MCP server %q has no recognized transport (need http url)", ErrClientServerRejected, m.Name)
		}
	}
	return specs, nil
}

// cloneHeaders copies the caller's header map so a spec never aliases the
// decoded request body (which the transport may reuse or the caller may mutate).
// An empty/absent map yields nil so a server with no headers carries none.
func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
