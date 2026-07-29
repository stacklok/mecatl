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
	// The two axes must be stated SEPARATELY, because they are independent and the schema
	// used to fuse them: "it always restarts from a FRESH checkout … whatever mode you pass
	// now" made the where-it-runs claim follow the EARLIER run's mode, which is false for
	// resume + mode:'read-write' (prepareChildSession passes forker=nil for any writable
	// call, so the child runs in the operator's REAL tree) and contradicted the schema's own
	// `mode` property. A model has to arbitrate that, and the wrong branch is the dangerous
	// one — it holds Edit/Write while believing it is in a scratch checkout.
	//
	// Axis 1 — WHAT SURVIVED is mode-independent: a previously read-only child's files are
	// gone whatever mode you pass now.
	gone := strings.Index(desc, "GONE whatever mode you pass now")
	if gone < 0 {
		t.Errorf("the resume schema must state the read-only default's LOST FILES as mode-independent: %q", desc)
	}
	// Axis 2 — WHERE IT RUNS follows THIS call's mode, and the writable cell must say the
	// real workspace, not a checkout.
	for _, want := range []string{"WHERE IT RUNS NOW follows THIS call's mode", "mode:'read-write' runs DIRECTLY in your real workspace"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the resume schema must state where the resumed subagent runs as a function of THIS call's mode (%q): %q", want, desc)
		}
	}
	// And it must not re-fuse them: no unconditional fresh-checkout claim may govern the
	// mode the caller passes.
	for _, forbidden := range []string{"always restarts from a FRESH checkout", "FRESH checkout with its file changes"} {
		if strings.Contains(desc, forbidden) {
			t.Errorf("the resume schema must not claim a fresh checkout regardless of mode (%q): %q", forbidden, desc)
		}
	}
	// Lockstep with the notes the resumed child itself receives — all three cells, so the
	// schema and the runtime cannot disagree about either axis.
	if !strings.Contains(resumeStalenessNote, "FRESH workspace checkout") {
		t.Errorf("resumeStalenessNote must still state the fresh-checkout fact the schema mirrors for a read-only resume: %q", resumeStalenessNote)
	}
	if !strings.Contains(resumeWritableFreshNote, "DIRECTLY in the real workspace") {
		t.Errorf("resumeWritableFreshNote must state the real-workspace fact the schema mirrors for a writable resume: %q", resumeWritableFreshNote)
	}
}

// TestResumeNoteMatrixCoversBothAxes is the SPACE oracle for the resumed child's harness
// note. The note is a function of TWO independent axes — WHERE the child runs (THIS call's
// mode) and WHAT the earlier run left behind (editsSurvived) — and the whole class of bug
// this test exists to catch is a cell nobody enumerated: for a long time two notes covered
// three reachable combinations, and the missing cell (a previously read-only child resumed
// with mode:"read-write") got told it was "running in a FRESH workspace checkout" while
// holding Edit/Write on the operator's real repository.
//
// So it asserts the whole cartesian product, per AXIS, rather than that one cell has the
// right string: every combination must state both facts, and neither fact may be inferred
// from the other axis. A fourth cell (or a third axis) fails here rather than shipping.
//
// The axis phrases are derived from the production constants by the positive controls first:
// each fragment is asserted PRESENT in the note it is taken from, with t.Fatalf, so a
// reworded constant cannot make the per-cell checks below vacuous.
func TestResumeNoteMatrixCoversBothAxes(t *testing.T) {
	const (
		freshWorkspace = "FRESH workspace checkout" // resumeStalenessNote — read-only, forked
		realWorkspace  = "DIRECTLY in the"          // both writable notes — no fork (ADR 0041)
		editsAlive     = "STILL IN PLACE"           // resumeWritableNote
		editsGone      = "GONE"                     // resumeStalenessNote + resumeWritableFreshNote
	)
	// Positive controls: the fragments must exist in the production constants they name, or
	// every "must not contain" below is meaningless.
	for _, c := range []struct {
		what, fragment, in string
	}{
		{"the read-only note's workspace claim", freshWorkspace, resumeStalenessNote},
		{"the edits-survived note's workspace claim", realWorkspace, resumeWritableNote},
		{"the edits-gone writable note's workspace claim", realWorkspace, resumeWritableFreshNote},
		{"the edits-survived claim", editsAlive, resumeWritableNote},
		{"the read-only note's lost-work claim", editsGone, resumeStalenessNote},
		{"the writable note's lost-work claim", editsGone, resumeWritableFreshNote},
	} {
		if !strings.Contains(c.in, c.fragment) {
			t.Fatalf("%s no longer contains %q, so the per-cell axis checks below are vacuous — re-point them at the live wording: %q",
				c.what, c.fragment, c.in)
		}
	}

	for _, writable := range []bool{false, true} {
		for _, editsSurvived := range []bool{false, true} {
			p := resumePosture{writable: writable, editsSurvived: editsSurvived}
			note := p.note()
			if strings.TrimSpace(note) == "" {
				t.Fatalf("resumePosture{writable:%v, editsSurvived:%v} has NO note — an unenumerated cell", writable, editsSurvived)
			}
			// WHERE it runs is a function of THIS call's mode ONLY.
			if writable {
				if !strings.Contains(note, realWorkspace) || strings.Contains(note, freshWorkspace) {
					t.Errorf("writable=%v editsSurvived=%v: a mode:\"read-write\" child runs in the REAL workspace (no fork) and must be told so, never that it is in a fresh checkout:\n%s",
						writable, editsSurvived, note)
				}
			} else if !strings.Contains(note, freshWorkspace) {
				t.Errorf("writable=%v editsSurvived=%v: a read-only child forks, so it must be told it is in a fresh checkout:\n%s",
					writable, editsSurvived, note)
			}
			// WHAT survived is a function of the EARLIER run ONLY.
			if writable && editsSurvived {
				if !strings.Contains(note, editsAlive) {
					t.Errorf("writable=%v editsSurvived=%v: the child's earlier edits ARE on disk and it must be told so, or it redoes or distrusts them:\n%s",
						writable, editsSurvived, note)
				}
			} else if strings.Contains(note, editsAlive) || !strings.Contains(note, editsGone) {
				t.Errorf("writable=%v editsSurvived=%v: nothing from the earlier run survived and the note must say so, never that the edits are still in place:\n%s",
					writable, editsSurvived, note)
			}
		}
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

// TestWritableStructuredOutputTerminalWarnsPartialEdits pins the direct-write note cell a
// structured-output failure lands in. The child exhausted its correction budget without ever
// producing a schema-valid payload, having already edited the operator's real tree in place
// (ADR 0041) — it did NOT finish cleanly, so the benign "review the changes" note the switch
// used to fall through to reads as a clean finish on a mid-task failure.
func TestWritableStructuredOutputTerminalWarnsPartialEdits(t *testing.T) {
	t.Parallel()
	res := renderWritableSubagentResult("p1", "subagent-p1", "", session.StopStructuredOutput, "",
		&submitResultTool{lastValidationError: `"count": expected integer`}, false, true)
	if !strings.Contains(res.Content, writableSubagentPartialNote) {
		t.Errorf("a writable child that never delivered a valid payload must be warned its edits may be PARTIAL:\n%s", res.Content)
	}
	if strings.Contains(res.Content, writableSubagentCleanNote) {
		t.Errorf("the benign clean-finish note must not label a structured-output failure:\n%s", res.Content)
	}
	// The validation error itself still reaches the model (it is the actionable half).
	if !strings.Contains(res.Content, "expected integer") {
		t.Errorf("the last validation error must still be surfaced:\n%s", res.Content)
	}
}

// TestStructuredOutputTerminalNamesANextAction is the oracle for the LAST delegation
// terminal that named a cause and no action. Every neighbouring terminal names one
// (StopError's resume hint, the limit notes, StopNoProgress, the time-budget notes, even the
// no-summary floor), so a bare one reads to the model as "this delegation is simply dead" —
// ADR 0070's model-visible-affordance rule inverted.
//
// It pins BOTH cells of the gate, and the wording facts that make each honest: the
// store-wired cell must name the affordance AND the argument the parent has to re-pass
// (`output_schema`, which resume composes with — validateResume rejects only `agent`/`model`),
// while the store-less cell must name a next action that needs no affordance at all and must
// NOT advertise a resume validateResume would refuse.
func TestStructuredOutputTerminalNamesANextAction(t *testing.T) {
	t.Parallel()
	submit := &submitResultTool{lastValidationError: `"count": expected integer, got string`}

	resumable := renderSubagentResult("p1", "subagent-p1", "", session.StopStructuredOutput, "",
		submit, false, subagentErrorResumeHint)
	// Positive control: the cause is still the actionable lead.
	if !strings.Contains(resumable.Content, "expected integer") {
		t.Fatalf("the validation error must still reach the model:\n%s", resumable.Content)
	}
	for _, want := range []string{"output_schema", "resume it with the agentId above", "delegate again"} {
		if !strings.Contains(resumable.Content, want) {
			t.Errorf("a store-wired structured-output terminal must name %q as part of its next action:\n%s", want, resumable.Content)
		}
	}

	// Store-less: a next action that needs no affordance, and no resume it cannot honour.
	storeless := renderSubagentResult("p1", "subagent-p1", "", session.StopStructuredOutput, "",
		submit, false, "")
	if !strings.Contains(storeless.Content, "delegate again") {
		t.Errorf("a store-less structured-output terminal must still name the fix-and-re-delegate action:\n%s", storeless.Content)
	}
	if strings.Contains(storeless.Content, "resume it with the agentId") {
		t.Errorf("a store-less deployment must not advertise a resume validateResume will refuse:\n%s", storeless.Content)
	}

	// The direct-write arm owns its own PARTIAL-edits warning and reaches this renderer with
	// no hint, so it gets the affordance-free half — two next actions that do not conflict
	// (fix the schema and re-delegate; review or undo what is in the tree), unlike the
	// resume-or-discard pair writableSubagentFailedNote exists to fuse.
	writable := renderWritableSubagentResult("p1", "subagent-p1", "", session.StopStructuredOutput, "",
		submit, false, true)
	if !strings.Contains(writable.Content, writableSubagentPartialNote) {
		t.Fatalf("the writable structured-output arm must keep its PARTIAL-edits warning:\n%s", writable.Content)
	}
	if strings.Contains(writable.Content, "resume it with the agentId") {
		t.Errorf("the writable arm must not add a second, independent resume imperative:\n%s", writable.Content)
	}
}

// TestResumeWritableFreshNoteScopesItsCautionAndClaimsNoMechanism pins the two wording
// properties this note earned the hard way. Both are about a TRUSTED harness instruction
// delivered to a child that holds Edit/Write on the operator's real tree, which is why the
// exact words are behaviour and not style.
//
//  1. The caution names the behaviour it prevents (wiping files the child did not write, to
//     get a clean start), not the whole class "do not delete or rewrite files" — which is a
//     literal instruction not to do the job the parent delegated, and is what a skim of a long
//     bracketed note actually lands on.
//  2. It states no MECHANISM for why the earlier run's work is gone. The selector is
//     `priorWorkspace != ws.Root()`, which is also true when the snapshot persisted no
//     workspace and when the earlier run was direct-write in a DIFFERENT real tree, so
//     "(that run used a throwaway checkout)" can be false while the fact stays true.
func TestResumeWritableFreshNoteScopesItsCautionAndClaimsNoMechanism(t *testing.T) {
	t.Parallel()
	for _, forbidden := range []string{
		"do not delete or rewrite files",
		"throwaway checkout",
	} {
		if strings.Contains(resumeWritableFreshNote, forbidden) {
			t.Errorf("resumeWritableFreshNote must not say %q — see this test's doc-comment for which of the two rules it breaks: %q", forbidden, resumeWritableFreshNote)
		}
	}
	for _, want := range []string{
		"you did not write yourself",     // the scoped caution
		"carries over",                   // the fact, stated without a mechanism
		"DIRECTLY in the real workspace", // axis 1, shared with the matrix oracle
	} {
		if !strings.Contains(resumeWritableFreshNote, want) {
			t.Errorf("resumeWritableFreshNote must still contain %q: %q", want, resumeWritableFreshNote)
		}
	}
}
