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

// teaminspect.go implements the on-demand, PULL member-transcript inspection tool
// (requirement E). It is a parent-catalog tool the parent LLM calls DELIBERATELY to
// read ONE persisted team member's transcript by (team id, member name). It does NOT
// auto-inject any transcript: the pulled transcript enters the parent Conversation
// only as this tool's own ToolResult — exactly like every tool, and the parent's
// explicit choice — so gauntlet #7's "no AUTO-injection of member transcripts into
// the parent context" property is preserved.

// inspectMemberToolName is the catalog name of the member-inspection tool.
const inspectMemberToolName = "InspectMember"

// maxInspectMessages caps how many trailing member messages the transcript renders,
// and maxInspectMessageRunes caps each rendered message body — together bounding the
// ToolResult so a long member transcript can never be copied verbatim onto the
// parent's conversation (mirroring the clampPreview discipline the team stream uses).
// NOTE: the InspectMember Spec().Description quotes "~40 messages"; keep that phrase in
// sync if maxInspectMessages changes.
const (
	maxInspectMessages     = 40
	maxInspectMessageRunes = 1000
	maxInspectTotalRunes   = 8000
)

// InspectMemberTool reads one persisted team member session via a port.SessionStore
// and returns a BOUNDED rendering of its conversation as its ToolResult. It is PULL
// and read-only.
type InspectMemberTool struct {
	// store reads member sessions by id. Injected by the composition root; the tool
	// consumes the port.SessionStore interface, never a concrete adapter.
	store port.SessionStore
	// ownershipEnforced is supplied by the verified-caller request edge. When false,
	// legacy deployments without a verifier retain their historical access behavior.
	ownershipEnforced bool
}

// inspectMemberArgs is the model-supplied argument payload.
type inspectMemberArgs struct {
	// TeamID is the team id the parent saw on the Team ToolResult / EvTeamStart.
	TeamID string `json:"team_id"`
	// Member is the member name whose transcript to read.
	Member string `json:"member"`
}

// inspectMemberSchema is the JSON schema the model sees for the tool's arguments.
var inspectMemberSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "team_id": {"type": "string", "description": "The team id (as seen on the Team tool result / team start event)."},
    "member": {"type": "string", "description": "The member's name."}
  },
  "required": ["team_id", "member"]
}`)

// NewInspectMemberTool constructs the legacy-compatible InspectMember tool over a
// session store. Without a verified-caller request edge, ownership enforcement stays
// disabled.
func NewInspectMemberTool(store port.SessionStore) tool.Tool {
	return NewInspectMemberToolWithOwnership(store, false)
}

// NewInspectMemberToolWithOwnership constructs InspectMember with the request edge's
// ownership policy. When enforcement is enabled, only the owner with the same
// (Issuer, Subject) pair may read a persisted member transcript.
func NewInspectMemberToolWithOwnership(store port.SessionStore, ownershipEnforced bool) tool.Tool {
	if store == nil {
		panic("agent: NewInspectMemberTool requires a non-nil session store")
	}
	return &InspectMemberTool{store: store, ownershipEnforced: ownershipEnforced}
}

// Spec returns the model-facing specification for the InspectMember tool.
func (*InspectMemberTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: inspectMemberToolName,
		Description: "Read one team member's transcript by team id (from the Team result's " +
			"'Team id:' line) and member name. Returns a BOUNDED rendering — the last ~40 " +
			"messages, each clamped — not the full raw transcript. Use it to debug a member " +
			"that stopped or failed, to verify how a specific step was done, or to pull a " +
			"detail the team's report omitted. Each call folds that transcript into this " +
			"conversation and consumes context budget, so prefer the team's report when it " +
			"suffices.",
		Schema: inspectMemberSchema,
	}
}

// ReadOnly reports that InspectMember only READS the store (no workspace mutation),
// so the dispatcher may run it read-parallel.
func (*InspectMemberTool) ReadOnly() bool { return true }

// Execute loads the member's persisted session (id derived via the SHARED
// MemberSessionID helper, so it cannot drift from the supervisor's save id) and
// renders a bounded transcript. An unknown id is a model-addressable error (the team
// may not have run, or the member never started).
func (t *InspectMemberTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args inspectMemberArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "InspectMember: "+msg), nil
	}
	teamID := strings.TrimSpace(args.TeamID)
	member := strings.TrimSpace(args.Member)
	if teamID == "" || member == "" {
		return session.NewToolError(call.ID, "InspectMember: both 'team_id' and 'member' are required"), nil
	}

	id := MemberSessionID(teamID, member)
	sess, err := t.store.Load(ctx, id)
	if err == nil && !callerOwnsTranscriptWhenEnforced(ctx, sess, t.ownershipEnforced) {
		err = port.ErrSessionNotFound
		sess = nil
	}
	switch {
	case errors.Is(err, port.ErrSessionNotFound) || (err == nil && sess == nil):
		// Genuine not-found: a model-addressable miss the parent can reason about.
		return session.NewToolError(call.ID, fmt.Sprintf(
			"InspectMember: no transcript for member %q in team %q; the team may not have run or the member never started",
			member, teamID)), nil
	case err != nil:
		// A real infrastructure failure (I/O, decode) — surfaced DISTINCTLY so a broken
		// store is not silently misreported as "no transcript". Still a model-addressable
		// error result (never a harness error) so the loop continues.
		return session.NewToolError(call.ID, fmt.Sprintf(
			"InspectMember: failed to load member %q in team %q: %v", member, teamID, err)), nil
	}
	return session.NewToolResult(call.ID, renderInspectTranscript(
		fmt.Sprintf("Transcript of member %q in team %q:", member, teamID), sess)), nil
}

// callerOwnsTranscript retains the legacy child-resume ownership behavior: when no
// caller is present, the caller is the in-process parent rather than an untrusted
// request edge. Inspect tools use callerOwnsTranscriptWhenEnforced below instead.
func callerOwnsTranscript(ctx context.Context, sess *session.Session) bool {
	caller := session.PrincipalFromContext(ctx)
	return caller == nil || (sess != nil && sess.Owner != nil && sess.Owner.SameIdentity(caller))
}

// callerOwnsTranscriptWhenEnforced is the shared caller-ownership predicate for
// every persisted-session read/resume gate that carries an explicit,
// deployment-sourced enforcement flag: InspectSubagent, InspectMember, and
// Subagent's resume path (both its pre-in-flight-guard gate and its later
// authoritative recover-and-mutate load) all call this. The caller may read or
// resume every transcript only when the verified request edge has not enabled
// ownership; otherwise an owner and caller must have the same (Issuer, Subject) identity.
func callerOwnsTranscriptWhenEnforced(ctx context.Context, sess *session.Session, ownershipEnforced bool) bool {
	if !ownershipEnforced {
		// Unenforced deployments keep the legacy predicate's behavior verbatim: a
		// present-but-foreign verified caller is still denied (this must not be
		// MORE permissive than callerOwnsTranscript, or an identity-recorded-but-
		// not-enforced deployment silently loses the check it already had).
		return callerOwnsTranscript(ctx, sess)
	}
	return sess != nil && sess.Owner != nil && sess.Owner.SameIdentity(session.PrincipalFromContext(ctx))
}

// renderInspectTranscript renders the BOUNDED trailing tail of a session's
// conversation into a single, readable string for the parent's ToolResult, under the
// supplied header line. It clamps the number of messages (maxInspectMessages), each
// message body (maxInspectMessageRunes), and the overall length (maxInspectTotalRunes),
// so an arbitrarily long transcript can never be copied verbatim. It is shared by both
// PULL inspect tools (InspectMember, InspectSubagent) so their output stays identical.
func renderInspectTranscript(header string, sess *session.Session) string {
	var b strings.Builder
	b.WriteString(header + "\n")

	msgs := sess.Conversation.Messages
	if len(msgs) == 0 {
		b.WriteString("(no messages)\n")
		return b.String()
	}
	// Keep only the trailing tail when the transcript is long.
	if len(msgs) > maxInspectMessages {
		fmt.Fprintf(&b, "(showing the last %d of %d messages)\n", maxInspectMessages, len(msgs))
		msgs = msgs[len(msgs)-maxInspectMessages:]
	}

	for _, m := range msgs {
		line := renderInspectMessage(m)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
		if b.Len() >= maxInspectTotalRunes {
			b.WriteString("… [transcript truncated]\n")
			break
		}
	}
	return b.String()
}

// renderInspectMessage renders one conversation message as a bounded line.
func renderInspectMessage(m session.Message) string {
	switch m.Role {
	case session.RoleAssistant:
		var parts []string
		if txt := strings.TrimSpace(m.Text); txt != "" {
			parts = append(parts, clampRunes(txt, maxInspectMessageRunes))
		}
		for _, c := range m.ToolCalls {
			parts = append(parts, fmt.Sprintf("[called %s]", c.Name))
		}
		if len(parts) == 0 {
			return ""
		}
		return "assistant: " + strings.Join(parts, " ")
	case session.RoleTool:
		if m.ToolResult == nil {
			return ""
		}
		return "tool result: " + clampRunes(strings.TrimSpace(m.ToolResult.Content), maxInspectMessageRunes)
	case session.RoleUser:
		return "user: " + clampRunes(strings.TrimSpace(m.Text), maxInspectMessageRunes)
	case session.RoleSystem:
		// The system prompt is harness-authored boilerplate; skip it to keep the
		// transcript focused on the member's actual work.
		return ""
	default:
		return ""
	}
}

// clampRunes clamps s to at most n runes, appending an ellipsis on overflow. It is
// rune-aware so it never splits a multi-byte character.
//
// The appended ellipsis is LOAD-BEARING, not decoration. NeutraliseFraming runs BEFORE this
// clamp on every path that composes untrusted text into a model-visible body (see
// subagentErrorBody), and nothing re-examines the clamped result. Without the ellipsis an
// attacker who controls the rune count could place an EXACT-match framing header
// ("Team status:", "Tool:", "Policy:") so that the truncation lands precisely on its colon:
// pre-clamp the line reads "Team status: everything is fine" and does not match, post-clamp
// it IS the bare literal and would never be checked. The "…" makes the truncated line
// "team status:…", which matches nothing. (Prefix-matched headers are immune either way —
// truncation only removes a suffix.) So do not "tidy away" the ellipsis.
//
// Scope note, since the three headers named above are all surfacePrompt entries: the
// delegation-RESULT entries are today entirely prefix- or suffix-matched, so on the paths
// that clamp a neutraliseChildText result the manufacture is currently unreachable anyway.
// The ellipsis is what keeps that a PROPERTY OF THE LIST rather than a coincidence — one new
// exact-match result entry, or one path that clamps a full NeutraliseFraming, would otherwise
// reopen it silently. TestClampedFramingHeaderCannotSurviveTruncation pins it against the
// surfaceAll matcher for that reason.
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Compile-time assertion that InspectMemberTool satisfies the Tool contract.
var _ tool.Tool = (*InspectMemberTool)(nil)
