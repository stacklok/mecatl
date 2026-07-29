package agent

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestFailureResumeHintMatchesTheResumedChildsRealWorkspacePosture keeps the StopError
// resume hint HONEST about what a resume actually restores. The hint used to promise
// "continue where it left off", but resumeStalenessNote greets the resumed child with
// "file changes … from your earlier run are GONE" — its git worktree was torn down and
// this run gets a fresh fork. A parent model reading only "where it left off" can
// re-delegate a follow-up that assumes half-written files survived, which is the same
// harness-asserts-a-falsehood class as the two messages issue #319 fixed.
//
// The oracle is the PAIRING, not one string: the hint must state the same workspace fact
// resumeStalenessNote states, so the two cannot drift apart into a contradiction the model
// has to arbitrate.
func TestFailureResumeHintMatchesTheResumedChildsRealWorkspacePosture(t *testing.T) {
	// The over-promise is gone.
	if strings.Contains(subagentErrorResumeHint, "continue where it left off") {
		t.Errorf("the hint must not promise \"continue where it left off\" — the workspace does not carry over: %q", subagentErrorResumeHint)
	}
	// The conversation half is still advertised (that IS what survives).
	for _, want := range []string{"conversation is preserved", "resume it with the agentId above"} {
		if !strings.Contains(subagentErrorResumeHint, want) {
			t.Errorf("the hint must still advertise the resume path (%q): %q", want, subagentErrorResumeHint)
		}
	}
	// The workspace half must be stated, and stated the SAME way resumeStalenessNote
	// states it — the note the resumed child itself receives.
	if !strings.Contains(subagentErrorResumeHint, "WORKSPACE does not carry over") {
		t.Errorf("the hint must say the workspace does not carry over: %q", subagentErrorResumeHint)
	}
	if !strings.Contains(subagentErrorResumeHint, "GONE") || !strings.Contains(resumeStalenessNote, "GONE") {
		t.Errorf("the hint and resumeStalenessNote must agree that the child's files are GONE:\nhint: %q\nnote: %q",
			subagentErrorResumeHint, resumeStalenessNote)
	}
	// It must not silently drop the alternative (start fresh), or a model facing an
	// obviously permanent cause retries forever.
	if !strings.Contains(strings.ToLower(subagentErrorResumeHint), "fresh subagent") {
		t.Errorf("the hint must still name the start-a-fresh-subagent alternative: %q", subagentErrorResumeHint)
	}
}

// TestSubagentTimeoutNoteGate is the ORACLE for the per-call time-budget terminal's
// next action (the one remaining dead-end path: a timed-out child lands StateCancelled,
// which resume has always recovered, but the terminal named no action at all while every
// neighbouring terminal did).
//
// It pins all four cells of the gate, mirroring subagentResumeHint's two honest-silence
// cases so the timeout path cannot drift from the StopError path's policy.
func TestSubagentTimeoutNoteGate(t *testing.T) {
	for _, tc := range []struct {
		name                string
		writable, resumable bool
		want                string
	}{
		{"read-only, store wired → the generic timeout resume hint", false, true, subagentTimeoutResumeHint},
		{"read-only, no store → silence (validateResume would refuse)", false, false, ""},
		{"writable, store wired → ONE combined resume-or-discard decision", true, true, writableSubagentTimeoutNote},
		{"writable, no store → review-or-undo only (no resume to offer)", true, false, writableSubagentPartialNote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := subagentTimeoutNote(tc.writable, tc.resumable); got != tc.want {
				t.Fatalf("subagentTimeoutNote(%v, %v) =\n%q\nwant\n%q", tc.writable, tc.resumable, got, tc.want)
			}
		})
	}

	// Both timeout notes name the knob that actually fixes a genuine overrun — the
	// distinguishing content versus the StopError wording, which describes a failure.
	for name, note := range map[string]string{
		"subagentTimeoutResumeHint":   subagentTimeoutResumeHint,
		"writableSubagentTimeoutNote": writableSubagentTimeoutNote,
	} {
		if !strings.Contains(note, "timeout_ms") {
			t.Errorf("%s must name `timeout_ms` as the knob to raise: %q", name, note)
		}
		if !strings.Contains(note, "resume it with the agentId above") {
			t.Errorf("%s must advertise the resume path with the agentId trailer above it: %q", name, note)
		}
	}
	// The writable note is ONE exclusive decision, not two independent imperatives —
	// the same reasoning writableSubagentFailedNote carries (a model handed "discard the
	// edits" and "resume" separately can do both, then meet resumeWritableNote's "your
	// edits are STILL IN PLACE", which the discard just falsified).
	if !strings.Contains(writableSubagentTimeoutNote, "Do not do both") {
		t.Errorf("the writable timeout note must state the two options as mutually exclusive: %q", writableSubagentTimeoutNote)
	}
	// And it must be honest that a mid-task kill can leave half-finished work behind.
	if !strings.Contains(writableSubagentTimeoutNote, "PARTIAL") {
		t.Errorf("the writable timeout note must warn the edits may be PARTIAL: %q", writableSubagentTimeoutNote)
	}
}

// TestRecoveredDigestStatesTheNextActionOnce is the DE-DUPLICATION oracle for the
// StopNoProgress terminal, driven through the REAL render path.
//
// The digest prefix and the stop-reason note are rendered one after the other on that
// terminal, and they used to duplicate the clause "treat as partial; resume it with the
// agentId above to continue" BYTE-FOR-BYTE. The repo's rule is that the stop reason is
// stated in exactly ONE place; the same applies to the next action, which is worse to
// duplicate — a model reading the same imperative twice, in two framings, has no way to
// tell whether that is one instruction or two.
//
// The note owns the next action; the prefix owns provenance + partial-ness only.
func TestRecoveredDigestStatesTheNextActionOnce(t *testing.T) {
	body := recoveredDigestPrefix + "\n\nPARTIAL FINDINGS FROM THE CHILD"
	res := renderSubagentResult("p1", "subagent-p1", body, session.StopNoProgress, "", nil, false, "")

	if n := strings.Count(res.Content, "resume it with the agentId above"); n != 1 {
		t.Fatalf("the next action must be stated exactly once on a StopNoProgress digest, got %d occurrences:\n%s", n, res.Content)
	}
	// The stop reason likewise stays in one place (the pre-existing half of the rule).
	if n := strings.Count(res.Content, "ended without a final summary"); n != 1 {
		t.Fatalf("the stop reason must be stated exactly once, got %d occurrences:\n%s", n, res.Content)
	}
	// The prefix still carries what it owes: provenance + partial-ness, so it reads
	// coherently standalone on the note-less empty-StopEndTurn path.
	for _, want := range []string{"recovered", "partial"} {
		if !strings.Contains(recoveredDigestPrefix, want) {
			t.Errorf("recoveredDigestPrefix %q must still mention %q", recoveredDigestPrefix, want)
		}
	}
	// Standalone coherence, asserted on the path that has no note at all: the recovered
	// content is still framed as recovered and partial.
	clean := renderSubagentResult("p2", "subagent-p2", body, session.StopEndTurn, "", nil, false, "")
	if !strings.Contains(clean.Content, "recovered the subagent's last output") || !strings.Contains(clean.Content, "partial") {
		t.Fatalf("the digest prefix must read coherently standalone on the note-less empty-StopEndTurn path:\n%s", clean.Content)
	}
}
