package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// bashstatus.go implements the LIVE this-run background-Bash job tool, the
// registry-backed companion of the agent BashTool's `background: true` flag
// (bashtool.go). The split from SubagentStatus is deliberate: the registry is
// SHARED across every child family, but the projections are DISJOINT —
// SubagentStatus serves the three delegation families (bash-cmd entries are
// filtered out of its roster/collect) and BashStatus serves ONLY the bash-cmd
// jobs. A background Bash job is NOT a delegation family (no child session, no
// engine, no observability events), so it gets its own lean channel rather
// than growing delegation affordances it does not have (no InspectSubagent
// transcript, no resume).

// bashStatusToolName is the catalog name of the background-Bash job tool.
const bashStatusToolName = "BashStatus"

// bashStatusArgs is the model-supplied argument payload. All fields are
// optional: no args → the roster of THIS run's background bash jobs.
type bashStatusArgs struct {
	// JobID targets one job by its id (the 'job id:' line of its Bash
	// background started-result). Empty → roster.
	JobID string `json:"job_id,omitempty"`
	// WaitMs parks up to this many milliseconds (capped at
	// maxSubagentStatusWaitMs — the SAME 120s discipline as SubagentStatus, one
	// wait vocabulary across both registry-backed tools) for the target job —
	// or, with no JobID, for ANY live registry child — to reach its terminal
	// before reporting. Pointer so an omitted value is distinguishable from 0;
	// non-positive → no wait.
	WaitMs *int `json:"wait_ms,omitempty"`
	// Cancel requests the named job's cancellation (its per-job context
	// cancel). It only SIGNALS — the tool stays read-only (the process kill is
	// the job drive's own ctx reaction, exactly as Run.CancelChild) — and the
	// confirmation result does not wait for the terminal: poll the job to see
	// it land.
	Cancel string `json:"cancel,omitempty"`
}

// bashStatusSchema is the JSON schema the model sees for the tool's arguments.
var bashStatusSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "job_id": {
      "type": "string",
      "description": "Optional id of one background command (the 'job id:' line on its Bash result). With it: that job's state, its command, and its retained output tail — and, for a finished job, its full result (delivered once). Without it: a roster of every background command started in this run."
    },
    "wait_ms": {
      "type": "integer",
      "description": "Optional: wait up to this many milliseconds (max 120000) for the target job (or, with no job_id, for any background child) to finish before reporting. While waiting this call occupies one tool slot of the current turn, like any other running tool."
    },
    "cancel": {
      "type": "string",
      "description": "Optional id of one background command to cancel. The cancellation is requested immediately; poll the job with job_id to see it stop."
    }
  }
}`)

// BashStatusTool reports the LIVE state of this run's background Bash jobs,
// collects a finished job's result, and cancels a live job. It reaches the
// parent run's child registry through parentCaps (the childCapableTool seam) —
// the same seam SubagentStatus uses, filtered to the bash-cmd family. It is
// registered wherever the agent Bash tool is (the composition root's main
// catalogs), never in child catalogs.
type BashStatusTool struct{}

// NewBashStatusTool constructs the BashStatus tool. It is stateless: all state
// lives in the per-run registry handed down via parentCaps.
func NewBashStatusTool() tool.Tool { return &BashStatusTool{} }

// Spec returns the model-facing specification for the BashStatus tool.
func (*BashStatusTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: bashStatusToolName,
		Description: "Check on the background commands started by Bash in THIS run, collect a " +
			"finished job's output, or cancel a running job. With no arguments: a roster of every " +
			"background command started in this run (id, running/done, stop reason). With job_id: " +
			"that job's state, its command, and its retained output tail — and, for a FINISHED job, " +
			"its full result (delivered exactly once; afterwards it reports 'already delivered'). " +
			"With wait_ms (max 120000): wait up to that long for the target (or, with no job_id, for " +
			"any background child) to finish first — while waiting it occupies one tool slot of the " +
			"current turn. With cancel: request that job's cancellation. Output is NOT delivered back " +
			"automatically: this tool is the SOLE channel to a background command's result.",
		Schema: bashStatusSchema,
	}
}

// ReadOnly reports that BashStatus never mutates the workspace: its reads hit
// the in-memory registry, and cancel only signals the job's context (the kill
// is the job drive's own ctx reaction). The dispatcher may therefore run it
// read-parallel — which is also what lets a wait_ms park overlap other tools
// in the same turn.
func (*BashStatusTool) ReadOnly() bool { return true }

// Execute is the caps-less path (plain Execute, no parent run threaded): there
// is no registry to read, which is an honest model-addressable error — this
// tool is only meaningful inside a run that registers its children.
func (*BashStatusTool) Execute(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	return session.NewToolError(call.ID,
		"BashStatus: no live registry is available on this run"), nil
}

// ExecuteWithParent is the childCapableTool seam: it receives the parent run's
// capabilities and reads/collects/cancels through caps.children, filtered to
// the bash-cmd family (bashCmdFamiliesOnly).
func (t *BashStatusTool) ExecuteWithParent(ctx context.Context, call session.ToolCall, ws tool.Workspace, _ func(session.Event), caps parentCaps) (session.ToolResult, error) {
	var args bashStatusArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "BashStatus: "+msg), nil
	}
	reg := caps.children
	if reg == nil {
		return t.Execute(ctx, call, ws)
	}
	id := strings.TrimSpace(args.JobID)

	// Cancel verb first: an explicit per-job signal that does not depend on the
	// wait or the report. Unknown / non-bash-cmd / already-done ids fail
	// honestly (requestCancel reports not-live as false; the family check keeps
	// a delegation id from being cancellable through the wrong tool).
	if target := strings.TrimSpace(args.Cancel); target != "" {
		return cancelBashJob(call.ID, reg, target), nil
	}

	// Wait phase: park on the target's doneCh (or the registry's terminal
	// generation for "any") up to the capped wait, ctx-aware so a run cancel
	// unwinds the park immediately. An unknown target id fails before parking.
	// This reuses the SubagentStatus waitForChild discipline verbatim — one
	// wait vocabulary; a DONE-target wait returns at once (doneCh is closed),
	// so collecting a finished job with wait_ms set is not a stall.
	if d := effectiveStatusWait(args.WaitMs); d > 0 {
		if unknown := waitForChild(ctx, reg, id, d); unknown {
			return unknownBashJobError(call.ID, id), nil
		}
	}

	if id == "" {
		return session.NewToolResult(call.ID, renderBashJobRoster(reg.statusSnapshotMatching(bashCmdFamiliesOnly))), nil
	}
	return bashJobDetail(call.ID, reg, id), nil
}

// renderBashJobRoster renders the no-args roster: one line per bash job — id,
// state, stop — ids and enum-ish labels ONLY (the A9 trust posture: the command
// text is model-authored untrusted, so it never rides a bulk roster; the
// per-job detail view is where a deliberately-inspecting model may see it).
func renderBashJobRoster(sts []childStatus) string {
	if len(sts) == 0 {
		return "No background commands have been started in this run."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background commands of this run (%d):", len(sts))
	for _, s := range sts {
		fmt.Fprintf(&b, "\n- %s %s", s.id, s.state)
		if s.state == childDone {
			fmt.Fprintf(&b, " (%s)", s.stop)
			if s.delivered {
				b.WriteString(" — result delivered")
			} else {
				b.WriteString(" — result ready; collect it with job_id")
			}
		}
	}
	return b.String()
}

// bashJobDetail is the per-job view. For a LIVE job: state + the command +
// the CURRENT output tail snapshot (a peek — NOT marked delivered; the stored
// terminal result remains collectible). For a DONE job: the stored result via
// the registry's collect machinery, exactly once (collectOK delivers the body;
// collectAlready reports the delivery happened). The command rides the detail
// view on the same trust footing as the Bash result's own echo: the model is
// deliberately inspecting ONE job, never being bulk-fed model-authored text.
func bashJobDetail(callID session.ToolCallID, reg *childRunRegistry, id string) session.ToolResult {
	res, st, outcome := reg.collectMatching(id, bashCmdFamiliesOnly)
	switch outcome {
	case collectUnknown:
		return unknownBashJobError(callID, id)
	case collectRunning:
		return session.NewToolResult(callID, renderLiveBashJob(reg, id, st))
	case collectAlready:
		return session.NewToolResult(callID, fmt.Sprintf(
			"background command %s (%s): result already delivered.", id, st.stop))
	default: // collectOK (a bash job is background by construction; collectForeground is unreachable)
		if res == nil {
			// Defensive: a bash terminal that stored no result (should not
			// happen — BashTool.driveBackground always stores one).
			return session.NewToolResult(callID, fmt.Sprintf(
				"background command %s finished (%s) but recorded no result body.", id, st.stop))
		}
		if res.IsError {
			return session.NewToolError(callID, res.Content)
		}
		return session.NewToolResult(callID, res.Content)
	}
}

// renderLiveBashJob renders the RUNNING-job detail: state + command + the
// retained output tail as it stands NOW. It is a snapshot, never a stream —
// the model polls again (or waits with wait_ms) for fresher output.
func renderLiveBashJob(reg *childRunRegistry, id string, st childStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "background command %s is %s.\ncommand: %s", id, st.state, st.goal)
	tail, truncated, ok := reg.outputTailSnapshot(id)
	switch {
	case !ok || strings.TrimRight(tail, "\n") == "":
		b.WriteString("\n\n(no output yet)")
	default:
		b.WriteString("\n\n--- output so far (tail) ---\n")
		b.WriteString(bashTruncate(strings.TrimRight(tail, "\n")))
		if truncated {
			b.WriteString("\n[older output dropped: retained the last 65536 bytes]")
		}
	}
	b.WriteString("\n\nWait for it with wait_ms, or keep working and check again later.")
	return b.String()
}

// cancelBashJob requests one job's cancellation through the registry and
// reports the outcome. It does NOT wait for the terminal — the drive's ctx
// reaction (process kill + doneCh close) lands asynchronously; the model polls
// the job to observe it. The confirmation names the job ONLY (ids — nothing
// model-authored).
func cancelBashJob(callID session.ToolCallID, reg *childRunRegistry, id string) session.ToolResult {
	// Family check first: the cancel verb is bash-cmd-only, so an id of another
	// family gets the same honest miss as an unknown id (never "cancelled").
	sts := reg.statusSnapshotMatching(bashCmdFamiliesOnly)
	known := false
	for _, st := range sts {
		if st.id == id {
			known = true
			break
		}
	}
	if !known {
		return unknownBashJobError(callID, id)
	}
	cancel, _, ok := reg.requestCancel(id)
	if !ok {
		return session.NewToolResult(callID, fmt.Sprintf(
			"background command %s is already done; there is nothing to cancel.", id))
	}
	cancel()
	return session.NewToolResult(callID, fmt.Sprintf(
		"background command %s: cancellation requested. Poll it with job_id (or wait_ms) to see it stop; "+
			"anything still running when this run ends is cancelled anyway.", id))
}

// unknownBashJobError is the model-addressable miss for a job_id this run never
// started as a background command.
func unknownBashJobError(callID session.ToolCallID, id string) session.ToolResult {
	return session.NewToolError(callID, fmt.Sprintf(
		"BashStatus: no background command %q in this run; call BashStatus with no arguments for the roster", id))
}

// Compile-time assertions: BashStatusTool is a Tool and receives parent caps.
var (
	_ tool.Tool        = (*BashStatusTool)(nil)
	_ childCapableTool = (*BashStatusTool)(nil)
)
