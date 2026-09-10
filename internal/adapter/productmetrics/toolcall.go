package productmetrics

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/mecatl/engine/session"
)

// Attribute keys for the tool_calls instrument. Both draw from a closed set:
// attrCategory from builtinToolCategories ∪ {"mcp", "other"} (see
// toolCategory), attrOutcome from {"success", "error"}.
const (
	attrCategory = "category"
	attrOutcome  = "outcome"
)

// The two bounded category values that are not a built-in tool's own name.
const (
	categoryMCP   = "mcp"
	categoryOther = "other"
)

// mcpToolPrefix is the STRUCTURAL naming convention every MCP-server tool is
// registered under (internal/adapter/mcp/clientmcp.go: `"mcp__" + server +
// "__" + toolName`). Bucketing the whole prefix under one literal means an
// MCP server or remote tool name — operator-chosen free text, and the one
// genuinely unbounded slice of the catalog — can never reach an attribute
// value, structurally, whatever a future MCP integration is named.
const mcpToolPrefix = "mcp__"

// builtinToolCategories is the CLOSED set of tool names this package may emit
// verbatim as a category: mecatl's own fixed catalog, where the name carries
// no operator or user content and the cardinality is fixed at compile time.
//
// It is deliberately an allowlist rather than a "not mcp__-prefixed ⇒ safe"
// inference: a catalog is not only built-ins plus MCP tools (an agent def,
// a learned skill, or a future extension seam can register a name derived
// from operator or model input), so the safe default for an unrecognised
// name is the single literal categoryOther — never the name itself. A new
// built-in showing up as "other" in the metric is the visible, harmless
// prompt to add a line here.
var builtinToolCategories = map[string]bool{
	// Filesystem + shell (engine/adapter/fstools, engine/agent).
	"Read": true, "ListDir": true, "Edit": true, "Write": true,
	"Copy": true, "Move": true, "Remove": true, "Grep": true, "Glob": true,
	"Bash": true, "BashStatus": true, "BashSystemTemp": true,
	// Outbound reads (engine/adapter/webfetch, engine/adapter/search).
	"WebFetch": true, "WebSearch": true,
	// MCP meta-tools — mecatl's OWN fixed names, distinct from the
	// mcp__-prefixed server tools they operate over.
	"ListMcpResources": true, "ReadMcpResource": true,
	"CallMcpWithQuery": true, "FetchMcpResource": true,
	// Delegation (engine/agent).
	"Subagent": true, "SubagentStatus": true, "InspectSubagent": true,
	"Parallel": true, "Team": true, "InspectMember": true, "SubmitResult": true,
	// Skills.
	"Skill": true, "SkillDraft": true,
	// Project memory (internal/adapter/memory).
	"Remember": true, "Recall": true, "SearchMemory": true,
	"InspectMemory": true, "ForgetMemory": true, "UndoMemory": true,
	// User model (internal/adapter/memory).
	"RememberUser": true, "RecallUser": true, "SearchUserModel": true,
	"InspectUserMemory": true, "ForgetUserMemory": true, "UndoUserMemory": true,
	// Composition-owned + miscellaneous built-ins.
	"PresentPlan": true, "Schedule": true, "ScheduleQuery": true,
	"DiscoverModels": true, "ToolSearch": true,
}

// The two bounded outcome values.
const (
	outcomeSuccess = "success"
	outcomeError   = "error"
)

// toolCategory projects a tool name onto the bounded category attribute: the
// tool's own name for a recognised built-in, categoryMCP for anything
// MCP-server-provided, categoryOther for everything else.
func toolCategory(name string) string {
	if strings.HasPrefix(name, mcpToolPrefix) {
		return categoryMCP
	}
	if builtinToolCategories[name] {
		return name
	}
	return categoryOther
}

// ToolCall satisfies port.ToolCallRecorder. It is the fallback path for a
// caller that drives the base port without the run correlation — it records
// with no run id, so the call is counted on tool_calls but can contribute to
// no run's had_tool_call. The engine itself always prefers ToolCallForRun
// (it type-asserts port.RunAwareToolCallRecorder), so in production this arm
// serves only a non-loop caller.
func (r *Recorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	r.ToolCallForRun("", id, call, result, queued, took)
}

// ToolCallForRun satisfies port.RunAwareToolCallRecorder. It records the
// bounded category/outcome attributes and tallies the run's per-run state
// (had_tool_call, and the tool-call count a later task publishes).
//
// It reads exactly two things off its arguments: call.Name, only through the
// closed-set toolCategory projection, and result.IsError, a boolean. The
// session id, the queued/took durations, and every free-text field
// (result.Content, call arguments) are ignored — the ignored parameters are
// accepted only because the port's signature requires them.
func (r *Recorder) ToolCallForRun(runID string, _ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	outcome := outcomeSuccess
	if result.IsError {
		outcome = outcomeError
	}
	r.toolCalls.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String(attrCategory, toolCategory(call.Name)),
		attribute.String(attrOutcome, outcome)))
	r.perRun.markToolCall(runID, result.IsError)
}
