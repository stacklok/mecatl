package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// subagentinspect.go implements the on-demand, PULL child-transcript inspection tool,
// the sibling of InspectMember (teaminspect.go). It is a parent-catalog tool the parent
// LLM calls DELIBERATELY to read ONE persisted subagent OR Parallel-branch transcript by
// its child id — the value of a Subagent result's 'agentId:' line or a Parallel result's
// 'branch id:' line (issue #30). It does NOT auto-inject any transcript: the pulled
// transcript enters the parent Conversation only as this tool's own ToolResult — exactly
// like every tool, and the parent's explicit choice — so gauntlet #7's "no AUTO-injection"
// property is preserved.

// inspectSubagentToolName is the catalog name of the subagent-inspection tool.
const inspectSubagentToolName = "InspectSubagent"

// InspectSubagentTool reads one persisted subagent OR Parallel-branch session via a
// port.SessionStore and returns a BOUNDED rendering of its conversation as its
// ToolResult. It is PULL and read-only. The agent_id it takes IS the session id verbatim
// — no derivation — so the id from a Subagent result's 'agentId:' line OR a Parallel
// result's 'branch id:' line loads directly (issue #30).
type InspectSubagentTool struct {
	// store reads subagent / parallel-branch sessions by id. Injected by the composition
	// root; the tool consumes the port.SessionStore interface, never a concrete adapter.
	store port.SessionStore
	// allowedPrefixes is the family-aware allow-list gating which ids this tool will load
	// from the SHARED session store: an agent_id that does not start with ANY of these is
	// REJECTED before the store is touched, so the model cannot read team-member
	// ("team-<teamID>-<member>") or service-session transcripts through this tool,
	// bypassing InspectMember's team_id+member framing. The prefixes are sourced from the
	// exported id-minting conventions (childregistry.go): SubagentSessionPrefix
	// ("subagent-") and ParallelSessionPrefix ("parallel-"). team- is DELIBERATELY
	// excluded (InspectMember owns it). A deployment using WithChildSessionPrefix /
	// WithParallelChildSessionPrefix would need a matching entry here — noted, not built,
	// until such a deployment exists (and note such an override ALSO de-scopes those ids
	// from the composition layer's child-session GC).
	allowedPrefixes []string
}

// inspectSubagentArgs is the model-supplied argument payload.
type inspectSubagentArgs struct {
	// AgentID is the child session id verbatim — the value of a Subagent result's
	// 'agentId:' line OR a Parallel result's 'branch id:' line (issue #30).
	AgentID string `json:"agent_id"`
}

// inspectSubagentSchema is the JSON schema the model sees for the tool's arguments.
var inspectSubagentSchema = json.RawMessage(`{"type":"object","properties":{"agent_id":{"type":"string","description":"The child agent's id, exactly as shown on the 'agentId:' line of a Subagent tool result OR the 'branch id:' line of a Parallel tool result."}},"required":["agent_id"]}`)

// NewInspectSubagentTool constructs the InspectSubagent tool over a session store. store
// must be non-nil; NewInspectSubagentTool panics otherwise (a composition-root
// programming error — the tool has nothing to read without a store).
func NewInspectSubagentTool(store port.SessionStore) tool.Tool {
	if store == nil {
		panic("agent: NewInspectSubagentTool requires a non-nil session store")
	}
	// Family-aware allow-list: a Subagent agentId OR a Parallel branch id (issue #30).
	// team- stays excluded — InspectMember owns it.
	return &InspectSubagentTool{store: store, allowedPrefixes: []string{SubagentSessionPrefix, ParallelSessionPrefix}}
}

// Spec returns the model-facing specification for the InspectSubagent tool.
func (*InspectSubagentTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: inspectSubagentToolName,
		// NOTE: this quotes "~40 messages"; keep that phrase in sync if
		// maxInspectMessages changes (the same sync NOTE InspectMember carries).
		Description: "Read a subagent's or Parallel branch's transcript by its agent id — the " +
			"'agentId:' line on a Subagent tool result OR the 'branch id:' line on a Parallel " +
			"tool result. Returns a BOUNDED rendering — the last ~40 messages, each clamped — " +
			"not the full raw transcript. Use it to debug a subagent or branch that stopped or " +
			"errored, to verify how it reached its conclusion, or to pull a detail its summary " +
			"omitted. Each call folds that transcript into this conversation and consumes context " +
			"budget, so prefer the agent's summary when it suffices.",
		Schema: inspectSubagentSchema,
	}
}

// ReadOnly reports that InspectSubagent only READS the store (no workspace mutation),
// so the dispatcher may run it read-parallel.
func (*InspectSubagentTool) ReadOnly() bool { return true }

// Execute loads the subagent's persisted session by its agent id (the id IS the session
// id, verbatim — no derivation) and renders a bounded transcript. An unknown id is a
// model-addressable error (the subagent may not have run yet).
func (t *InspectSubagentTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args inspectSubagentArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "InspectSubagent: "+msg), nil
	}
	id := strings.TrimSpace(args.AgentID)
	if id == "" {
		return session.NewToolError(call.ID, "InspectSubagent: 'agent_id' is required and must be non-empty"), nil
	}
	// Prefix gate: only SUBAGENT and PARALLEL-BRANCH sessions are inspectable here (issue
	// #30). The shared store also holds team-member and service sessions; rejecting any id
	// outside the family allow-list BEFORE the load keeps this tool from becoming a
	// verbatim read of the whole store (team member transcripts go through InspectMember's
	// team_id+member framing instead). Inspection is read-only and engine-agnostic, so it
	// safely spans both families; the SEPARATE `resume` path (subagent.go) stays
	// subagent-only — a branch runs on a different engine and resume re-forks a workspace.
	if !hasAnyPrefix(id, t.allowedPrefixes) {
		return session.NewToolError(call.ID, fmt.Sprintf(
			"InspectSubagent: agent id %q is not an inspectable child session; only a Subagent result's 'agentId:' line or a Parallel result's 'branch id:' line can be inspected (team member transcripts are read via InspectMember)", id)), nil
	}

	sess, err := t.store.Load(ctx, session.SessionID(id))
	switch {
	case errors.Is(err, port.ErrSessionNotFound) || (err == nil && sess == nil):
		// Genuine not-found: a model-addressable miss the parent can reason about.
		return session.NewToolError(call.ID, fmt.Sprintf(
			"InspectSubagent: no transcript for agent id %q; use the id exactly as shown on the 'agentId:' line of a Subagent result or the 'branch id:' line of a Parallel result (the agent may not have run yet)", id)), nil
	case err != nil:
		// A real infrastructure failure (I/O, decode) — surfaced DISTINCTLY so a broken
		// store is not silently misreported as "no transcript". Still a model-addressable
		// error result (never a harness error) so the loop continues.
		return session.NewToolError(call.ID, fmt.Sprintf(
			"InspectSubagent: failed to load agent %q: %v", id, err)), nil
	}
	// Neutral header: the id may be a subagent OR a Parallel branch (issue #30), so a
	// "subagent" label would mislabel a branch transcript.
	return session.NewToolResult(call.ID, renderInspectTranscript(
		fmt.Sprintf("Transcript of agent %q:", id), sess)), nil
}

// hasAnyPrefix reports whether s starts with any prefix in the allow-list. It is the
// family-aware gate (issue #30): the id must belong to an inspectable child family.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Compile-time assertion that InspectSubagentTool satisfies the Tool contract.
var _ tool.Tool = (*InspectSubagentTool)(nil)
