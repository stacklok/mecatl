package session

import (
	"reflect"
	"testing"
)

// TestTeamPayloadHasReviewedContentFields is the structural twin of the agent
// package's ParallelPayload guard. TeamPayload intentionally carries a
// fuller-than-metadata observability projection, but every content-shaped field
// must stay explicitly reviewed against the TeamPayload redaction contract
// (bounded previews only; permission asks never projected; parent conversation
// still receives only the joined Team result).
func TestTeamPayloadHasReviewedContentFields(t *testing.T) {
	assertStructFieldsAllowed(t, reflect.TypeOf(TeamPayload{}), map[string]string{
		"ParentCallID":    "parent Team tool-call id",
		"TeamID":          "team id",
		"Roster":          "member metadata; nested shape guarded separately",
		"Member":          "member name",
		"MemberSessionID": "member child-session id",
		"InnerKind":       "projected event kind",
		"Text":            "bounded member message/result preview",
		"ToolName":        "member tool name only",
		"Detail":          "bounded member tool args/result preview",
		"IsError":         "tool-result error flag",
		"Rounds":          "terminal round count",
		"Stop":            "terminal stop reason",
		"Usage":           "token accounting metadata",
		"ContextUsed":     "context-meter numerator",
		"ContextWindow":   "context-meter denominator",
		"Tasks":           "bounded task snapshots; nested shape guarded separately",
		"Findings":        "bounded finding snapshots; nested shape guarded separately",
		"Dispositions":    "closed-enum terminal member verdicts; nested shape guarded separately",
		// issue #331: per-round harness/provider failure detail on EvTeamMember
		// (InnerKind=EvResult, Stop==StopError) — the mirror of SubagentPayload.Cause.
		// Harness metadata, never member-authored output; normalised through
		// subagentCausePayload (clamped to maxSubagentCausePreview) at the one emit
		// site. Empty on every other kind and on the terminal disposition snapshot.
		"Cause": "per-round harness failure detail (StopError result only)",
	})
}

func TestTeamMemberSpecHasNoContentFields(t *testing.T) {
	assertStructFieldsAllowed(t, reflect.TypeOf(TeamMemberSpec{}), map[string]string{
		"Name":     "member handle",
		"Role":     "short role label",
		"Mutating": "workspace mode flag",
		"Lead":     "lead flag",
		// OPT-IN model router (ADR 0034): a CATEGORY label (operator taxonomy name) and a
		// concrete MODEL id the member's engine was minted on — bare metadata, never the
		// member's role/prompt or the classifier's reasoning.
		"RoutedCategory": "router category label",
		"RoutedModel":    "routed concrete model id",
		// ISSUE #397: the bare-metadata REASON the member was not routed (a
		// session.RoutingReason* gate const or a bounded harness/composition miss code),
		// EMPTY on a routed hit — never the member's role/prompt or classifier output.
		"RoutingReason": "routing miss/gate reason label (bare metadata, empty on a hit)",
		// ISSUE #112 / ADR 0035: the concrete MODEL id the member's engine ACTUALLY runs
		// on, regardless of how it was chosen — bare metadata, never member content.
		"Model": "resolved concrete model id (bare metadata)",
	})
}

func TestTeamTaskSnapshotHasReviewedContentFields(t *testing.T) {
	assertStructFieldsAllowed(t, reflect.TypeOf(TeamTaskSnapshot{}), map[string]string{
		"ID":          "task id",
		"Description": "bounded task-description preview",
		"State":       "task state enum string",
		"Assignee":    "member name",
		"Deps":        "task-id list",
	})
}

func TestTeamFindingSnapshotHasReviewedContentFields(t *testing.T) {
	assertStructFieldsAllowed(t, reflect.TypeOf(TeamFindingSnapshot{}), map[string]string{
		"Member": "member name",
		"Body":   "bounded finding-body preview",
	})
}

func TestTeamMemberDispositionHasNoContentFields(t *testing.T) {
	assertStructFieldsAllowed(t, reflect.TypeOf(TeamMemberDisposition{}), map[string]string{
		"Name":        "member name",
		"Disposition": "closed-enum member terminal disposition",
		"Reason":      "closed-enum stop reason",
		// A COUNT of supervisor verdicts (how many rounds ended in a run-level error),
		// issue #318. Nothing member-authored, so no preview cap applies; it exists because
		// a bounded retry lets a member fail a round and still finish "done", and a client
		// that could not see the count would render such a run as silently clean.
		"ErrorRounds": "count of run-level failed rounds (supervisor verdict)",
	})
}

func assertStructFieldsAllowed(t *testing.T, rt reflect.Type, allowed map[string]string) {
	t.Helper()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if _, ok := allowed[f.Name]; !ok {
			t.Fatalf("%s grew an unexpected field %q (%s): review it against the team observability redaction contract before adding it to the allow-list", rt.Name(), f.Name, f.Type)
		}
	}
	for _, banned := range []string{"Args", "Content", "Message", "Prompt", "Transcript", "PermissionAsk", "Ask"} {
		if _, ok := rt.FieldByName(banned); ok {
			t.Fatalf("%s must not carry unreviewed content-shaped field %q", rt.Name(), banned)
		}
	}
}
