package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// subagentstatus.go implements the LIVE this-run child status/collection tool,
// the registry-backed sibling of InspectSubagent (subagentinspect.go). The split
// is deliberate (D15): InspectSubagent reads PERSISTED transcripts of finished
// children through the session store (cross-run, bounded rendering, costly);
// SubagentStatus reads the parent run's LIVE child registry (this run only,
// cheap, no store) and is the SOLE channel a BACKGROUND child's result body is
// delivered through (A2 — notice-only injection means bodies return as tool
// results, the sanctioned role).

// subagentStatusToolName is the catalog name of the live child-status tool.
const subagentStatusToolName = "SubagentStatus"

// maxSubagentStatusWaitMs caps the wait_ms park (A3): two minutes. A longer wait
// would hold a dispatch slot (the tool blocks in its own read-parallel dispatch
// goroutine, exactly like a foreground Subagent call) for longer than any
// reasonable poll interval; the model can simply call again.
const maxSubagentStatusWaitMs = 120000

// delegationFamiliesOnly is the family-exclusion set for SubagentStatus's
// registry reads: it projects the THREE delegation families
// (subagent/parallel-branch/team-member) and filters OUT the bash-cmd
// background jobs — those are BashStatus's projection. The registry is SHARED;
// the projections are disjoint (an entry of one family is invisible to the
// other tool), so a bash job id is never mislabeled "a subagent" and a
// delegation id is never collected through BashStatus.
var delegationFamiliesOnly = map[childFamily]bool{childFamilyBashCmd: true}

// bashCmdFamiliesOnly is the mirror set for BashStatus: it projects ONLY the
// bash-cmd background jobs, excluding every delegation family.
var bashCmdFamiliesOnly = map[childFamily]bool{
	childFamilySubagent:       true,
	childFamilyParallelBranch: true,
	childFamilyTeamMember:     true,
}

// subagentStatusArgs is the model-supplied argument payload. Both fields are
// optional: no args → the roster of THIS run's children.
type subagentStatusArgs struct {
	// AgentID targets one child by its session id (the 'agentId:' line of its
	// Subagent result / started-result). Empty → roster.
	AgentID string `json:"agent_id,omitempty"`
	// WaitMs parks up to this many milliseconds (capped at
	// maxSubagentStatusWaitMs) for the target child — or, with no AgentID, for ANY
	// child — to reach its terminal before reporting. Pointer so an omitted value
	// is distinguishable from 0; non-positive → no wait.
	WaitMs *int `json:"wait_ms,omitempty"`
}

// subagentStatusSchema is the JSON schema the model sees for the tool's arguments.
var subagentStatusSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "agent_id": {
      "type": "string",
      "description": "Optional id of one subagent (the 'agentId:' line on its Subagent result). With it: that subagent's state, and — for a finished background subagent — its full result (delivered once). Without it: a roster of every subagent started in this run."
    },
    "wait_ms": {
      "type": "integer",
      "description": "Optional: wait up to this many milliseconds (max 120000) for the target subagent (or, with no agent_id, for any subagent) to finish before reporting. While waiting this call occupies one tool slot of the current turn, like any other running tool."
    }
  }
}`)

// SubagentStatusTool reports the LIVE state of this run's delegated children and
// collects background subagent results. It reaches the parent run's child
// registry through parentCaps (the childCapableTool seam) — no store, no
// adapter, no layering cost. It is registered wherever Subagent is (the
// composition root's main catalogs), never in child catalogs.
type SubagentStatusTool struct{}

// NewSubagentStatusTool constructs the SubagentStatus tool. It is stateless: all
// state lives in the per-run registry handed down via parentCaps.
func NewSubagentStatusTool() tool.Tool { return &SubagentStatusTool{} }

// Spec returns the model-facing specification for the SubagentStatus tool.
func (*SubagentStatusTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: subagentStatusToolName,
		Description: "Check on the subagents of THIS run and collect background results. With no " +
			"arguments: a roster of every subagent started in this run (id, kind, running/done, stop " +
			"reason). With agent_id: that subagent's state — and, for a finished BACKGROUND subagent, " +
			"its full result body (delivered exactly once; afterwards it reports 'already delivered'). " +
			"With wait_ms (max 120000): wait up to that long for the target (or, with no agent_id, for " +
			"any subagent) to finish first — while waiting it occupies one tool slot of the current " +
			"turn. Use this for LIVE state and background-result collection; use InspectSubagent to " +
			"read a finished subagent's persisted transcript (works across runs, but folds the whole " +
			"transcript into this conversation).",
		Schema: subagentStatusSchema,
	}
}

// ReadOnly reports that SubagentStatus only reads the in-memory registry (no
// workspace mutation), so the dispatcher may run it read-parallel — which is
// also what lets a wait_ms park overlap other tools in the same turn.
func (*SubagentStatusTool) ReadOnly() bool { return true }

// Execute is the caps-less path (plain Execute, no parent run threaded): there is
// no registry to read, which is an honest model-addressable error — this tool is
// only meaningful inside a run that registers its children.
func (*SubagentStatusTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolError(call.ID,
		"SubagentStatus: no live subagent registry is available on this run"), nil
}

// ExecuteWithParent is the childCapableTool seam: it receives the parent run's
// capabilities and reads/collects from caps.children.
func (t *SubagentStatusTool) ExecuteWithParent(ctx context.Context, call session.ToolCall, env tool.Environment, _ func(session.Event), caps parentCaps) (session.ToolResult, error) {
	var args subagentStatusArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "SubagentStatus: "+msg), nil
	}
	reg := caps.children
	if reg == nil {
		return t.Execute(ctx, call, env)
	}
	id := strings.TrimSpace(args.AgentID)

	// Wait phase (A3): park on the target's doneCh (or the registry's terminal
	// generation for "any") up to the capped wait, ctx-aware so a run cancel
	// unwinds the park immediately. An unknown target id fails before parking.
	if d := effectiveStatusWait(args.WaitMs); d > 0 {
		if unknown := waitForChild(ctx, reg, id, d); unknown {
			return unknownChildError(call.ID, id), nil
		}
	}

	if id == "" {
		return session.NewToolResult(call.ID, renderChildRoster(reg.statusSnapshotMatching(delegationFamiliesOnly))), nil
	}
	return collectChild(call.ID, reg, id), nil
}

// effectiveStatusWait normalises wait_ms: nil / non-positive → 0 (no wait); a
// positive value is capped at maxSubagentStatusWaitMs.
func effectiveStatusWait(waitMs *int) time.Duration {
	if waitMs == nil || *waitMs <= 0 {
		return 0
	}
	ms := *waitMs
	if ms > maxSubagentStatusWaitMs {
		ms = maxSubagentStatusWaitMs
	}
	return time.Duration(ms) * time.Millisecond
}

// waitForChild parks up to d for the target child's terminal (id != "") or for
// the NEXT terminal of any child (id == ""; returns immediately when nothing is
// live). It returns unknown=true only for a targeted id the registry does not
// hold; a timeout or ctx cancel just ends the wait — the report phase states
// whatever is then true.
func waitForChild(ctx context.Context, reg *childRunRegistry, id string, d time.Duration) (unknown bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	if id != "" {
		ch, ok := reg.doneChFor(id)
		if !ok {
			return true
		}
		select {
		case <-ch:
		case <-timer.C:
		case <-ctx.Done():
		}
		return false
	}
	// Any-child wait: nothing live → nothing to wait for; otherwise park on the
	// terminal generation (closed when the next markDone/remove lands). Liveness
	// and the generation channel are taken as ONE locked snapshot
	// (liveGeneration) — separate reads had a TOCTOU window where a terminal
	// landing between them handed back the FRESH generation, parking the waiter
	// for its full capped wait on an event that had already happened.
	gen, live := reg.liveGeneration()
	if !live {
		return false
	}
	select {
	case <-gen:
	case <-timer.C:
	case <-ctx.Done():
	}
	return false
}

// renderChildRoster renders the no-args roster: one line per child — id, family,
// background marker, state, stop — ids and enum-ish labels ONLY (A9; the goal
// label is clamped metadata but the roster stays lean).
func renderChildRoster(sts []childStatus) string {
	if len(sts) == 0 {
		return "No subagents have been started in this run."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Subagents of this run (%d):", len(sts))
	for _, s := range sts {
		kind := string(s.family)
		if s.background {
			kind += ", background"
		}
		fmt.Fprintf(&b, "\n- %s [%s] %s", s.id, kind, s.state)
		if s.state == childDone {
			fmt.Fprintf(&b, " (%s)", s.stop)
			switch {
			case s.background && s.delivered:
				b.WriteString(" — result delivered")
			case s.background:
				b.WriteString(" — result ready; collect it with agent_id")
			}
		}
	}
	return b.String()
}

// childKindLabel renders a status entry's family (plus the background marker)
// as the model-facing noun, so a team-member or parallel-branch id is never
// described as "a subagent" with subagent-only affordances.
func childKindLabel(st childStatus) string {
	switch st.family {
	case childFamilyTeamMember:
		return "team member"
	case childFamilyParallelBranch:
		return "parallel branch"
	case childFamilyBashCmd:
		return "background command"
	default:
		if st.background {
			return "background subagent"
		}
		return "subagent"
	}
}

// collectChild reports one child's state and, for a finished background child
// with an uncollected result, delivers the stored body (exactly once) — re-keyed
// to THIS call's id, preserving the error bit, so a failed background child
// surfaces as a real error tool result. Wording is FAMILY-AWARE: a done
// team-member / parallel-branch id points at its family's own delivery channel
// (the team's consolidated report / the Parallel call's result), never at "its
// own Subagent call".
func collectChild(callID session.ToolCallID, reg *childRunRegistry, id string) session.ToolResult {
	res, st, outcome := reg.collectMatching(id, delegationFamiliesOnly)
	switch outcome {
	case collectUnknown:
		return unknownChildError(callID, id)
	case collectRunning:
		return session.NewToolResult(callID, fmt.Sprintf(
			"%s %s is still %s. Wait for it with wait_ms, or keep working and check again later.",
			childKindLabel(st), id, st.state))
	case collectForeground:
		switch st.family {
		case childFamilyTeamMember:
			return session.NewToolResult(callID, fmt.Sprintf(
				"team member %s finished (%s); its contribution is folded into its team's consolidated report. Use InspectMember (with its team id) to read its transcript.",
				id, st.stop))
		case childFamilyParallelBranch:
			return session.NewToolResult(callID, fmt.Sprintf(
				"parallel branch %s finished (%s); its outcome was reported on its Parallel call's result.",
				id, st.stop))
		default:
			return session.NewToolResult(callID, fmt.Sprintf(
				"subagent %s finished (%s); its result was already returned on its own Subagent call. Use InspectSubagent for its full transcript.",
				id, st.stop))
		}
	case collectAlready:
		return session.NewToolResult(callID, fmt.Sprintf(
			"background subagent %s (%s): result already delivered. Use InspectSubagent %s to re-read its transcript if needed.",
			id, st.stop, id))
	default: // collectOK
		if res == nil {
			// Defensive: a background terminal that stored no result (should not
			// happen — driveBackground always stores one).
			return session.NewToolResult(callID, fmt.Sprintf(
				"background subagent %s finished (%s) but recorded no result body.", id, st.stop))
		}
		if res.IsError {
			return session.NewToolError(callID, res.Content)
		}
		return session.NewToolResult(callID, res.Content)
	}
}

// unknownChildError is the model-addressable miss for an agent_id this run never
// started.
func unknownChildError(callID session.ToolCallID, id string) session.ToolResult {
	return session.NewToolError(callID, fmt.Sprintf(
		"SubagentStatus: no subagent %q in this run; call SubagentStatus with no arguments for the roster, or InspectSubagent for a previous run's subagent", id))
}

// Compile-time assertions: SubagentStatusTool is a Tool and receives parent caps.
var (
	_ tool.Tool        = (*SubagentStatusTool)(nil)
	_ childCapableTool = (*SubagentStatusTool)(nil)
)
