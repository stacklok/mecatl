//go:build e2e

package harness

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Driver runs one prompt at a time against the target over the SAME path
// mecatui uses: CreateSession → OpenConverse → SendPrompt → ReadLoop, collecting
// the full translated event transcript (tea.Msg structs are plain data — types
// + payloads). Permission asks are resolved by policy: tools in ApproveTools
// get an allow-once; everything else is DENIED and recorded — a live scenario
// must never park on a human.
type Driver struct {
	target Target
}

// NewDriver builds a driver over the target.
func NewDriver(t Target) *Driver { return &Driver{target: t} }

// RunOpts configures one scenario run.
type RunOpts struct {
	// Scenario names the artifact directory (and the transcript file).
	Scenario string
	// Model is the model id for the session ("" → DefaultModel()). The provider
	// is always ProviderID.
	Model string
	// ApproveTools are tool names whose permission asks get allow-once.
	ApproveTools []string
	// Timeout bounds the whole run (0 → 4 minutes). On expiry the driver sends
	// Cancel and drains.
	Timeout time.Duration
	// SessionID reuses an existing session instead of creating one.
	SessionID string
}

// RunResult is the collected outcome of one run: every translated event in
// arrival order, the terminal result, and the ask/approval ledger.
type RunResult struct {
	Scenario  string
	SessionID string
	Resolved  client.ResolvedModel
	Caps      client.Capabilities

	Prompt string
	Msgs   []tea.Msg
	Result *client.ResultMsg

	Asks     []client.PermissionAskMsg
	Approved []string // tool names approved (allow-once)
	Denied   []string // tool names denied by the harness policy

	TranscriptPath string
	StreamErr      error
	TimedOut       bool

	// approve is the ask-approval policy for this run (from RunOpts).
	approve []string
}

// Run executes one prompt and collects the transcript. A transport-level
// failure returns an error; a completed-but-failed run returns the RunResult
// (the specs assert on it).
func (d *Driver) Run(ctx context.Context, opts RunOpts, prompt string) (*RunResult, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 4 * time.Minute
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel()
	}

	res := &RunResult{Scenario: opts.Scenario, Prompt: prompt, approve: opts.ApproveTools}

	tr, err := NewTranscript(d.target.StateDir(StateArtifacts), opts.Scenario)
	if err != nil {
		return nil, err
	}
	defer tr.Close()
	res.TranscriptPath = tr.Path()

	cli := d.target.Client()
	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	res.SessionID = opts.SessionID
	if res.SessionID == "" {
		id, caps, resolved, err := cli.CreateSession(runCtx, client.ModeFromString("default"), client.ModelSelection{
			ProviderID: ProviderID,
			ModelID:    model,
		})
		if err != nil {
			tr.Record("error", map[string]string{"stage": "create_session", "err": err.Error()})
			return nil, fmt.Errorf("create session: %w", err)
		}
		res.SessionID, res.Caps, res.Resolved = id, caps, resolved
	}
	tr.Record("meta", map[string]any{
		"session_id": res.SessionID,
		"provider":   ProviderID,
		"model":      model,
		"resolved":   res.Resolved,
		"approve":    opts.ApproveTools,
	})

	stream, err := cli.OpenConverse(runCtx)
	if err != nil {
		tr.Record("error", map[string]string{"stage": "open_converse", "err": err.Error()})
		return nil, fmt.Errorf("open converse: %w", err)
	}
	tr.Record("prompt", map[string]string{"text": prompt})
	if err := stream.SendPrompt(res.SessionID, prompt, nil); err != nil {
		tr.Record("error", map[string]string{"stage": "send_prompt", "err": err.Error()})
		return nil, fmt.Errorf("send prompt: %w", err)
	}

	msgs := make(chan tea.Msg, 256)
	go stream.ReadLoop(runCtx, msgs)

	cancelSent := false
	for {
		select {
		case <-runCtx.Done():
			// Timeout: try to cancel the run once, then keep draining until the
			// reader closes (it selects on the same ctx, so it cannot wedge).
			if !cancelSent {
				res.TimedOut = true
				tr.Record("timeout", map[string]string{"after": opts.Timeout.String()})
				_ = stream.SendCancel()
				cancelSent = true
			}
			// Bounded final drain so a post-cancel terminal result is captured.
			deadline := time.After(5 * time.Second)
			for {
				select {
				case m, ok := <-msgs:
					if !ok {
						return res, nil
					}
					d.absorb(res, tr, stream, m)
				case <-deadline:
					return res, nil
				}
			}
		case m, ok := <-msgs:
			if !ok {
				return res, nil // reader closed: stream ended
			}
			d.absorb(res, tr, stream, m)
		}
	}
}

// absorb records one msg into the result + transcript and answers asks.
func (d *Driver) absorb(res *RunResult, tr *Transcript, stream *client.Stream, m tea.Msg) {
	res.Msgs = append(res.Msgs, m)
	tr.Record("event", m)
	switch v := m.(type) {
	case client.PermissionAskMsg:
		res.Asks = append(res.Asks, v)
		if slices.Contains(res.approve, v.Tool) {
			res.Approved = append(res.Approved, v.Tool)
			tr.Record("approval", map[string]string{"ask_id": v.AskID, "tool": v.Tool, "verdict": "allow_once"})
			_ = stream.SendApproval(v.AskID, client.VerdictAllowOnce)
		} else {
			res.Denied = append(res.Denied, v.Tool)
			tr.Record("approval", map[string]string{"ask_id": v.AskID, "tool": v.Tool, "verdict": "deny"})
			_ = stream.SendApproval(v.AskID, client.VerdictDeny)
		}
	case client.ResultMsg:
		r := v
		res.Result = &r
	case client.StreamErrMsg:
		res.StreamErr = v.Err
	}
}

// --- result helpers the specs assert with ---

// ToolCalls returns every ToolCallMsg with the given tool name.
func (r *RunResult) ToolCalls(name string) []client.ToolCallMsg {
	var out []client.ToolCallMsg
	for _, m := range r.Msgs {
		if tc, ok := m.(client.ToolCallMsg); ok && tc.Name == name {
			out = append(out, tc)
		}
	}
	return out
}

// ToolResult returns the ToolResultMsg matching a call id (nil when absent).
func (r *RunResult) ToolResult(callID string) *client.ToolResultMsg {
	for _, m := range r.Msgs {
		if tr, ok := m.(client.ToolResultMsg); ok && tr.CallID == callID {
			return &tr
		}
	}
	return nil
}

// Compactions returns every CompactionMsg observed in the run (the "history
// compacted" notice the relay emits when maybeCompact fires).
func (r *RunResult) Compactions() []client.CompactionMsg {
	var out []client.CompactionMsg
	for _, m := range r.Msgs {
		if c, ok := m.(client.CompactionMsg); ok {
			out = append(out, c)
		}
	}
	return out
}

// SubagentMsgs returns every SubagentMsg of the given kind.
func (r *RunResult) SubagentMsgs(kind client.SubagentKind) []client.SubagentMsg {
	var out []client.SubagentMsg
	for _, m := range r.Msgs {
		if s, ok := m.(client.SubagentMsg); ok && s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// ParallelMsgs returns every ParallelMsg of the given kind.
func (r *RunResult) ParallelMsgs(kind client.ParallelKind) []client.ParallelMsg {
	var out []client.ParallelMsg
	for _, m := range r.Msgs {
		if p, ok := m.(client.ParallelMsg); ok && p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// TeamMsgs returns every TeamMsg of the given kind.
func (r *RunResult) TeamMsgs(kind client.TeamKind) []client.TeamMsg {
	var out []client.TeamMsg
	for _, m := range r.Msgs {
		if t, ok := m.(client.TeamMsg); ok && t.Kind == kind {
			out = append(out, t)
		}
	}
	return out
}

// AssistantText concatenates the streamed assistant deltas (the model-visible
// reply text across all turns).
func (r *RunResult) AssistantText() string {
	var b strings.Builder
	for _, m := range r.Msgs {
		if d, ok := m.(client.AssistantDeltaMsg); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

// Stop returns the terminal stop reason ("" when no result arrived).
func (r *RunResult) Stop() string {
	if r.Result == nil {
		return ""
	}
	return r.Result.Stop
}

// Usage returns the terminal result's usage (zero when no result arrived).
func (r *RunResult) Usage() client.Usage {
	if r.Result == nil {
		return client.Usage{}
	}
	return r.Result.Usage
}
