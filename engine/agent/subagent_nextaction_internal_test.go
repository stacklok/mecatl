package agent

import (
	"encoding/json"
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

// resumeSchemaDescription extracts the `resume` property's description from the LIVE
// subagentSchema, so the oracle below reads the string a model actually receives rather
// than a copy of it.
func resumeSchemaDescription(t *testing.T) string {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(subagentSchema, &doc); err != nil {
		t.Fatalf("subagentSchema is not valid JSON: %v", err)
	}
	desc := doc.Properties["resume"].Description
	if desc == "" {
		t.Fatal("subagentSchema has no `resume` property description")
	}
	return desc
}

// TestResumeSchemaDescriptionConditionsSurvivingEditsOnTheEarlierRun extends the
// resume-honesty lockstep to the SCHEMA — the string a model reads BEFORE it delegates,
// and therefore the one that decides whether it passes mode:"read-write" at all.
//
// prepareChildSession deliberately refuses to key editsSurvived on the CURRENT call's mode
// (it compares the persisted Workspace against the real parent root) precisely because a
// previously READ-ONLY child's worktree was torn down: telling such a child its edits are
// still in place is, in resumeWritableNote's own words, the exact falsehood inverted. The
// engine got that right while the schema still promised it unconditionally.
func TestResumeSchemaDescriptionConditionsSurvivingEditsOnTheEarlierRun(t *testing.T) {
	desc := resumeSchemaDescription(t)

	// The over-promise the resume-honesty fix removed from the hints must not survive here.
	if strings.Contains(desc, "continue where it left off") {
		t.Errorf("the resume schema must not promise \"continue where it left off\" — the workspace does not carry over: %q", desc)
	}
	// The surviving-edits claim must be QUALIFIED by the EARLIER run's mode, and the
	// qualifier must come first so the claim cannot be read on its own.
	inPlace := strings.Index(desc, "still in place")
	if inPlace < 0 {
		t.Fatalf("the resume schema must still describe the direct-write case (edits in place): %q", desc)
	}
	const qualifier = "Only when the earlier run was itself mode:'read-write'"
	q := strings.Index(desc, qualifier)
	if q < 0 || q > inPlace {
		t.Errorf("the surviving-edits claim must be qualified by %q BEFORE it is stated (qualifier at %d, claim at %d): %q", qualifier, q, inPlace, desc)
	}
	// And the read-only default must be stated as MODE-INDEPENDENT: passing
	// mode:'read-write' on the resume of a previously read-only child does NOT bring its
	// dead worktree back (resumeStalenessNote is what that child actually receives).
	for _, want := range []string{"FRESH checkout", "whatever mode you pass now"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the resume schema must state the read-only default as mode-independent (%q): %q", want, desc)
		}
	}
	// Lockstep with the note the resumed child itself receives.
	if !strings.Contains(resumeStalenessNote, "FRESH workspace checkout") {
		t.Errorf("resumeStalenessNote must still state the fresh-checkout fact the schema mirrors: %q", resumeStalenessNote)
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

// TestWritableResumeNotesNameTheReadWriteMode pins the ONE argument that makes the
// writable notes' "resume it to finish on top of them" actually true.
//
// `writable` is derived only from the CURRENT call's mode (validateMode); run() never
// infers it from the loaded session. So the obvious follow-up — Subagent{resume: id,
// prompt: "finish it"} — comes back as the READ-ONLY explorer: no Edit/Write, a fresh
// worktree fork off committed HEAD, and resumeStalenessNote greeting the child with "your
// file changes … are GONE", while the operator's real tree still holds the half-finished
// edits. Naming mode:"read-write" in the note is what closes that dead end.
func TestWritableResumeNotesNameTheReadWriteMode(t *testing.T) {
	for name, note := range map[string]string{
		"writableSubagentFailedNote":  writableSubagentFailedNote,
		"writableSubagentTimeoutNote": writableSubagentTimeoutNote,
	} {
		if !strings.Contains(note, `mode:"read-write"`) {
			t.Errorf(`%s advertises a resume but never names mode:"read-write", so the obvious resume silently returns the read-only explorer and loses the partial edits: %q`, name, note)
		}
		// The argument must be named as part of the RESUME clause, not somewhere else in
		// the sentence — otherwise a model can read "resume it with the agentId" and stop.
		resume := strings.Index(note, "resume it with the agentId")
		mode := strings.Index(note, `mode:"read-write"`)
		if resume < 0 || mode < resume {
			t.Errorf("%s must name mode:\"read-write\" as part of the resume clause (resume at %d, mode at %d): %q", name, resume, mode, note)
		}
	}
	// The read-only counterparts must NOT name it: their child ran in a throwaway
	// checkout, so there are no in-place edits to finish on top of and telling the model
	// to resume writable would invite it to write over an unrelated tree state.
	for name, note := range map[string]string{
		"subagentErrorResumeHint":   subagentErrorResumeHint,
		"subagentTimeoutResumeHint": subagentTimeoutResumeHint,
	} {
		if strings.Contains(note, `mode:"read-write"`) {
			t.Errorf("%s is the READ-ONLY child's hint and must not advertise mode:\"read-write\": %q", name, note)
		}
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
