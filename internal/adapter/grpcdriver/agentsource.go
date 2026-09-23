package grpcdriver

import (
	"context"
	"sort"
	"strings"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// Count caps on the agent-def normalization (the C1 asset-cap discipline,
// applied to the def snapshot): a def is a bounded operator artifact — a
// real frontmatter file never carries thousands of hooks or tools — so a
// wire entry past these bounds is a defective or hostile driver, not a
// configuration. Over-cap handling is FAIL-SOFT PER DEF (a WARN naming the
// def, the def dropped), NOT a fatal snapshot error: defs are INDEPENDENT —
// one oversized def must not take down the whole roster the way a failed
// snapshot RPC rightly does — mirroring how the FS source skips one
// malformed <name>.md and keeps scanning.
const (
	maxAgentDefs        = 1024 // defs kept per snapshot; the excess is dropped with one WARN
	maxAgentHookEntries = 32   // per-def hooks map entries
	maxAgentListEntries = 256  // per-def tools / disallowed_tools / skills entries (each)
	maxAgentMCPServers  = 64   // per-def mcp_servers entries
)

// AgentOptions configures a remote AgentSource client.
type AgentOptions struct {
	// Diagnostics is the operational-logging sink for the defensive-drop WARNs
	// (an over-cap def, the def-count ceiling). nil defaults to
	// port.NopDiagnostics.
	Diagnostics port.Diagnostics
}

// AgentSource is a tool.AgentDefSource over a remote AgentSourceService
// driver. It is translation plus a DEFENSIVE normalization layer (the driver
// sits at the operator-infrastructure trust tier, but its metadata feeds the
// always-in-context Subagent roster and per-def system prompts, so the client
// re-enforces the invariants the port promises rather than trusting the
// wire): names are TRIMMED before every use (the FS parser trims frontmatter
// names; the wire client must not be weaker), blank-name defs are dropped,
// duplicate names de-dup first-wins, the result is name-sorted, descriptions
// are forced single-line then re-truncated to tool.MaxAgentDescriptionBytes,
// bodies are re-truncated to tool.MaxAgentBodyBytes, per-def collection
// sizes are count-capped (see the caps above — an over-cap def is dropped
// with a WARN), hooks/headers are re-normalized via the SAME exported helpers
// the parser uses (agents.NormalizeHooks/NormalizeHeaders), and Origin is
// stamped AgentOriginDriver UNCONDITIONALLY — a driver-served def IS driver
// tier; a driver must not claim the "project"/"user" admission labels (the
// wire origin field stays driver-side observability only).
type AgentSource struct {
	client driverv1.AgentSourceServiceClient
	diag   port.Diagnostics
}

// compile-time assertion that AgentSource satisfies the port.
var _ tool.AgentDefSource = (*AgentSource)(nil)

// NewAgentSource wraps an established driver connection (see Dial) as a
// tool.AgentDefSource.
func NewAgentSource(conn grpc.ClientConnInterface, opts AgentOptions) *AgentSource {
	diag := opts.Diagnostics
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &AgentSource{client: driverv1.NewAgentSourceServiceClient(conn), diag: diag}
}

// ListAgentDefs returns the driver's definition snapshot, defensively
// normalized (see the type doc).
func (s *AgentSource) ListAgentDefs(ctx context.Context) ([]tool.AgentDef, error) {
	resp, err := s.client.ListAgentDefs(ctx, &driverv1.ListAgentDefsRequest{})
	if err != nil {
		return nil, rpcErr(ctx, "list agent defs", err)
	}
	wire := resp.GetAgentDefs()
	out := make([]tool.AgentDef, 0, min(len(wire), maxAgentDefs))
	seen := make(map[string]bool, len(wire))
	droppedOverCount := 0
	for _, d := range wire {
		// TRIM the name before EVERY use — the de-dup key AND the stored name —
		// so " x" and "x" are the same def, exactly as the FS parser's trimmed
		// frontmatter names behave.
		name := strings.TrimSpace(d.GetName())
		if name == "" {
			continue // drop blank-name defs
		}
		if seen[name] {
			continue // de-dup first-wins (wire order)
		}
		if len(out) >= maxAgentDefs {
			droppedOverCount++
			continue
		}
		if reason := overCapReason(d); reason != "" {
			s.diag.Log(ctx, port.LevelWarn, "agent def from driver exceeds a count cap; def dropped (fail-soft — defs are independent)",
				"agent", name, "reason", reason)
			continue
		}
		seen[name] = true
		out = append(out, tool.AgentDef{
			Name:            name,
			Description:     toolkit.TruncateRunes(singleLine(d.GetDescription()), tool.MaxAgentDescriptionBytes),
			Tools:           d.GetTools(),
			DisallowedTools: d.GetDisallowedTools(),
			Model:           d.GetModel(),
			Provider:        d.GetProvider(),
			PermissionMode:  d.GetPermissionMode(),
			MaxTurns:        int(d.GetMaxTurns()),
			MaxToolCalls:    int(d.GetMaxToolCalls()),
			Color:           d.GetColor(),
			Skills:          d.GetSkills(),
			MCPServers:      fromProtoMCPServers(d.GetMcpServers()),
			Hooks:           agents.NormalizeHooks(d.GetHooks()),
			Body:            toolkit.TruncateRunes(d.GetBody(), tool.MaxAgentBodyBytes),
			// UNCONDITIONAL: every def listed by a driver is driver tier — the
			// wire's origin label is never adopted (a driver claiming
			// "project"/"user" would launder itself into a trusted-looking tier).
			Origin: tool.AgentOriginDriver,
		})
	}
	if droppedOverCount > 0 {
		s.diag.Log(ctx, port.LevelWarn, "agent-def snapshot exceeds the def-count cap; excess defs dropped",
			"cap", maxAgentDefs, "dropped", droppedOverCount)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// overCapReason reports why a wire def violates the per-def count caps, or ""
// when it is within bounds.
func overCapReason(d *driverv1.AgentDef) string {
	switch {
	case len(d.GetHooks()) > maxAgentHookEntries:
		return "too many hooks entries"
	case len(d.GetTools()) > maxAgentListEntries:
		return "too many tools entries"
	case len(d.GetDisallowedTools()) > maxAgentListEntries:
		return "too many disallowed_tools entries"
	case len(d.GetSkills()) > maxAgentListEntries:
		return "too many skills entries"
	case len(d.GetMcpServers()) > maxAgentMCPServers:
		return "too many mcp_servers entries"
	default:
		return ""
	}
}

// fromProtoMCPServers maps the wire mcp_servers onto the port shape, with
// headers re-normalized through the SAME helper the frontmatter parser uses
// (trimmed keys/values, empties dropped, nil for an empty map). The header
// VALUES are secret-shaped and are never logged anywhere downstream.
func fromProtoMCPServers(in []*driverv1.AgentMCPServer) []tool.AgentMCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]tool.AgentMCPServer, 0, len(in))
	for _, s := range in {
		out = append(out, tool.AgentMCPServer{
			Name:    s.GetName(),
			URL:     s.GetUrl(),
			Headers: agents.NormalizeHeaders(s.GetHeaders()),
		})
	}
	return out
}
