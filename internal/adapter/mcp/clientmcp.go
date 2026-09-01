package mcp

import (
	"errors"
	"fmt"
	"time"
)

// MaxClientServers caps how many MCP servers ONE client may declare on a single
// session-creating request. The factory connects them SERIALLY, each bounded by
// ClientConnectTimeout, so an uncapped count would let a client stall one create
// for count x timeout (CWE-400, resource exhaustion). 8 is generous for a real
// editor or SDK while bounding the worst-case connect wall-clock.
const MaxClientServers = 8

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
//     ValidateClientURL (SSRF scheme/host allowlist) and, on success, appended as
//     a ServerConfig carrying the entry's Name, URL, Headers, and the bounded
//     per-server ClientConnectTimeout.
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
	for _, m := range servers {
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
