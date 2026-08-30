//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/e2e/harness"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// compactionSpecs is the LIVE compaction-survival scenario: it drives a REAL
// model across a REAL mid-run compaction and asserts the user's distinctive task
// survives the history collapse. It is the highest-fidelity guard for the
// role-blind-tail compaction bug (the kept tail used to be sliced by count and
// could drop a recent user instruction before it was acted on) — the offline twin
// lives in engine/agent (the back-snap unit tests) and internal/app (the phase-3
// archive gate).
//
// LANE STRUCTURE — deterministic gate + quarantined behavioural stressor.
//
//   - The GATING spec is DETERMINISTIC and model-INDEPENDENT: it drives the bury
//     turns to force a real compaction, then asserts STRUCTURALLY that the pinned
//     turn-0 instruction is still present VERBATIM in the model's replayed context
//     (read back from the persisted post-compaction session snapshot). Compaction
//     either preserved the pin or it did not — there is no model nondeterminism in
//     this assertion. THIS is the real guarantee the harness owes: the instruction
//     survives the history collapse and is therefore in context for the model.
//
//   - The QUARANTINE spec is the BEHAVIOURAL fidelity stressor: it re-drives the
//     turn sequence to a terse "GO" trigger and records whether the model RECALLED
//     and ACTED on the cross-compaction instruction (wrote result.txt = PINEAPPLE).
//     This is model-dependent (haiku has been observed to read the terse trigger as
//     a readiness ping and reply "Ready." with no tool call), so it RECORDS its
//     outcome via report entries and NEVER fails the suite — mirroring the soul
//     behavioural-marker quarantine spec (e2e/soul_test.go). Cross-compaction RECALL
//     is an ARTIFICIAL stressor: real usage externalises deferred work (a written
//     file, a tracked task) rather than relying on the model re-deriving an early
//     instruction from a summary. Structural PRESENCE — not behavioural recall — is
//     what mecatl guarantees, so only the structural assertion gates CI.
//
// OWN SPAWN: each scenario owns its OWN mecated process (NOT the shared suite
// target) because it needs a server spawned with --context-window-override to
// force compaction at a small, cheap window, and the file/store assertions need a
// readable local state dir. Local-only by construction.
//
// LANE: stays on the default haiku lane (harness.DefaultModel()) — no model swap.
//
// PRESERVATION MECHANISM — first-user-pin, NOT back-snap (why the task is turn 0).
// The compactor preserves the task two ways: the role-aware BACK-SNAP keeps the
// recentUserTurnsKept (=3) most-recent user turns verbatim, and the FIRST-USER-PIN
// keeps the first user turn verbatim across successful reducing compactions. These
// specs deliberately lean on the first-user-pin: the task is the FIRST user turn,
// so it survives VERBATIM through the cascade summaries below.
//
// Why not back-snap here: back-snap only protects RECENT user turns. The sized Reads
// grow the complete request until cascade compaction can replace enough older history
// with a smaller tier-4 summary. By the final turn the three most-recent user turns
// are Reads — any non-first task could age OUT of the back-snap window and be
// summarised. The first-user-pin is the deterministic verbatim guarantee; the
// back-snap's exact kept-tail composition is covered DETERMINISTICALLY by the offline
// engine/agent unit tests (and the phase-3 archive gate).
//
// THE COMPACTION-TRIGGER ARITHMETIC (why the window value below is what it is).
// maybeCompact accounts for the COMPLETE next request: the system prompt, tool
// catalog, conversation, and other request overhead all contribute, not conversation
// messages alone. Crossing the 0.8 × context-window threshold is necessary but not
// sufficient: cascade treats a candidate that does not reduce that complete request
// as a no-op and emits no EvCompaction. The controllable compactible portion is the
// Read tool RESULT size. The harness fixture compaction-input.txt (~8KB; ~9KB once
// Read adds 1-based line-number prefixes) contributes roughly 2250 tokens per Read.
// With compactionWindowDefault = 2000 (threshold 1600), the task turn stays small,
// while four sized Read turns provide enough old history for a reducing tier-4
// cascade summary. This deliberately proves a real replacement rather than treating
// a threshold crossing or a non-reducing attempt as compaction.
//
// ANTI-VACUITY: the structural survival assertion is meaningless unless compaction
// actually fired. The gating spec therefore fails loudly if no EvCompaction was
// observed before reading back the pinned instruction. The quarantine spec keeps
// the pre-GO "result.txt must not pre-exist" guard so an early/non-GO write cannot
// make the (recorded) behavioural outcome look like a survival.

// compactionWindowDefault is the --context-window-override the spawn uses. The
// complete-request threshold is 0.8 × this (= 1600 tokens). It is deliberately tiny
// so four sized Reads deterministically create a reducing cascade candidate. Env-
// overridable via MECATL_E2E_COMPACTION_WINDOW for tuning. NOTE: this is deliberately
// tiny for the test; an operator must NOT set --context-window-override this low in
// production (it can compact frequently when a reducing candidate exists).
const compactionWindowDefault = "2000"

// compactionInputFile is the sized harness fixture the bury turns Read to grow
// compactible request history deterministically (see fixtures.go /
// e2e/fixtures/workspace).
const compactionInputFile = "compaction-input.txt"

// compactionTaskPrompt is the turn-0 FIRST USER TURN — the distinctive task. Being
// the first user turn, it is kept VERBATIM by the compactor's first-user-pin across
// EVERY compaction that fires below. The structural gate reads it back from the
// persisted post-compaction conversation; the quarantine spec asks the model to
// recall and act on it after the collapse.
const compactionTaskPrompt = `Remember this instruction for later: when I say the word GO, use the Write ` +
	`tool to create a file named result.txt containing exactly the word ` +
	`PINEAPPLE. Acknowledge with the single word noted.`

// compactionPinFragment is a low-cardinality, distinctive substring of
// compactionTaskPrompt. It appears ONLY in that turn-0 instruction (not in the
// Read prompts, the GO prompt, or any tool result), so finding it in a persisted
// message proves the turn-0 pin survived compaction VERBATIM rather than matching
// an echo. Asserted via the typed jsonlstore.Load + Conversation.Messages scan (NOT
// a raw JSON grep, which cannot distinguish the pinned copy from an echo and
// couples to JSON escaping).
const compactionPinFragment = "when I say the word GO"

// compactionReadPrompt grows history deterministically by Reading the sized
// fixture (~2250 compactible tokens per Read — the controllable lever).
var compactionReadPrompt = `Read the file ` + compactionInputFile +
	` in the workspace and reply with the single word ok.`

func compactionSpecs() {
	ginkgo.Describe("compaction", func() {
		// GATING (deterministic): force a real compaction, then assert STRUCTURALLY
		// that the pinned turn-0 instruction survived in the model's replayed context.
		ginkgo.It("preserves the pinned instruction verbatim across a real compaction",
			ginkgo.SpecTimeout(8*time.Minute),
			func(ctx ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with --context-window-override or read the store")
				}

				spawn, runs := compactionDriveBuryTurns(ctx)
				defer func() { _ = spawn.Close() }()
				logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(4096) }

				// --- ANTI-VACUITY: compaction must have fired. ---
				totalCompactions, lastCompactionRun := compactionLedger(runs)
				gomega.Expect(totalCompactions).To(gomega.BeNumerically(">=", 1),
					"no EvCompaction observed across the bury turns — the --context-window-override "+
						"did not trip compaction; the survival assertion would be vacuous"+logTail())

				// --- ORDERING: compaction fired during the bury turns (at or before the
				// last run whose snapshot we read back). It cannot have fired only "after"
				// the persisted snapshot, so the pin we read back genuinely survived one. ---
				finalRunIdx := len(runs) - 1
				gomega.Expect(lastCompactionRun).To(gomega.BeNumerically("<=", finalRunIdx),
					"compaction ledger is inconsistent (last compaction run index="+
						strconv.Itoa(lastCompactionRun)+", final run index="+strconv.Itoa(finalRunIdx)+")"+logTail())

				// --- DETERMINISTIC STRUCTURAL GATE: the turn-0 pinned instruction is present
				// VERBATIM in the persisted POST-compaction conversation. The local spawn
				// persists the session to a jsonlstore at StateDir(StateStore) (= --store-dir);
				// the latest snapshot line holds the post-compaction conversation (pre-compaction
				// history goes to the log-only EvCompactionArchive). We Load the typed session and
				// scan Conversation.Messages for the distinctive fragment — model-independent. ---
				sessionID := runs[0].SessionID
				storeDir := spawn.StateDir(harness.StateStore)
				gomega.Expect(storeDir).NotTo(gomega.BeEmpty(), "local spawn must expose its store dir")

				st, err := jsonlstore.New(storeDir)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open jsonlstore at "+storeDir+logTail())
				sess, err := st.Load(ctx, session.SessionID(sessionID))
				gomega.Expect(err).NotTo(gomega.HaveOccurred(),
					"load persisted post-compaction session "+sessionID+" from "+storeDir+logTail())

				found := false
				for _, m := range sess.Conversation.Messages {
					if strings.Contains(m.Text, compactionPinFragment) {
						found = true
						break
					}
				}
				gomega.Expect(found).To(gomega.BeTrue(),
					"the turn-0 pinned instruction fragment "+strconv.Quote(compactionPinFragment)+
						" was NOT present verbatim in the persisted POST-compaction conversation — the "+
						"first-user-pin did not survive the history collapse (session "+sessionID+", store "+
						storeDir+")"+logTail())
			})

		// QUARANTINE (behavioural): re-drive to a terse GO trigger and RECORD whether
		// the model recalled+acted on the cross-compaction instruction. NEVER fails the
		// suite — model adherence to cross-compaction recall is not a harness contract
		// (mirrors the soul behavioural-marker quarantine spec). The structural gate
		// above is the real guarantee.
		ginkgo.It("behavioural: the terse GO trigger elicits the cross-compaction task",
			ginkgo.Label("quarantine"), ginkgo.SpecTimeout(8*time.Minute),
			func(ctx ginkgo.SpecContext) {
				const entry = "compaction behavioural recall (quarantine)"
				if !target.IsLocal() {
					ginkgo.AddReportEntry(entry, "remote target: skipped (cannot spawn/read workspace)")
					ginkgo.Skip("remote target: cannot spawn with --context-window-override or read the workspace")
				}

				spawn, runs := compactionDriveBuryTurns(ctx)
				defer func() { _ = spawn.Close() }()
				drv := harness.NewDriver(spawn)

				// Compaction must have fired for the recall to be "across a compaction".
				totalCompactions, _ := compactionLedger(runs)
				if totalCompactions < 1 {
					ginkgo.AddReportEntry(entry, "no compaction fired during the bury turns (not counted)")
					return
				}

				// PRE-GO ANTI-VACUITY: the model must NOT have written result.txt before GO.
				goPath := filepath.Join(spawn.StateDir(harness.StateWorkspace), "result.txt")
				if data, statErr := os.ReadFile(goPath); statErr == nil && strings.Contains(string(data), "PINEAPPLE") {
					ginkgo.AddReportEntry(entry, "result.txt contained PINEAPPLE BEFORE GO (vacuous — not counted)")
					return
				}

				// Final turn: trigger the task. Write is allow-once'd by the driver policy.
				// The trigger word is wrapped in an ACT-NOW directive that does NOT restate
				// the task (no file name, no content) so the model must have RECALLED the
				// pinned instruction across the compaction — it only removes the observed
				// "Ready." no-op failure mode without revealing what to do.
				final, runErr := drv.Run(ctx, harness.RunOpts{
					Scenario:     "compaction-go",
					Timeout:      90 * time.Second,
					SessionID:    runs[0].SessionID,
					ApproveTools: []string{"Write"},
				},
					`GO. This is the trigger word from the instruction I gave you earlier. `+
						`Carry out that instruction IN FULL right now using the appropriate tool — `+
						`do not merely acknowledge or reply that you are ready.`)
				switch {
				case runErr != nil:
					ginkgo.AddReportEntry(entry, "GO turn transport error (not counted): "+runErr.Error())
					return
				case final.Result == nil:
					ginkgo.AddReportEntry(entry, "GO turn produced no terminal result (not counted)")
					return
				case final.Stop() != "end_turn":
					ginkgo.AddReportEntry(entry, "GO turn did not end cleanly (stop="+final.Stop()+", not counted)")
					return
				}

				wroteTask := false
				for _, c := range final.ToolCalls("Write") {
					if strings.Contains(c.Args, "PINEAPPLE") {
						wroteTask = true
					}
				}
				data, _ := os.ReadFile(goPath)
				if wroteTask && strings.Contains(string(data), "PINEAPPLE") {
					ginkgo.AddReportEntry(entry,
						"PRESENT: the model recalled the cross-compaction instruction and wrote result.txt=PINEAPPLE")
				} else {
					ginkgo.AddReportEntry(entry,
						"ABSENT: the terse GO trigger did not elicit the task (model recall, not a harness failure; "+
							"wrote_call="+strconv.FormatBool(wroteTask)+", transcript: "+final.TranscriptPath+")")
				}
			})
	})
}

// compactionDriveBuryTurns spawns a mecated with the small compaction window and
// drives turn 0 (the pinned task) + four sized Read turns to force a real
// mid-run compaction. It returns the spawn (caller closes it) and the recorded
// runs. SPAWN IS PER-CALL so each spec/attempt gets a FRESH .scratch workspace and
// store (no stale result.txt or snapshot can bleed across).
func compactionDriveBuryTurns(ctx ginkgo.SpecContext) (*harness.Local, []*harness.RunResult) {
	ginkgo.GinkgoHelper()
	window := envOrDefault("MECATL_E2E_COMPACTION_WINDOW", compactionWindowDefault)
	// --max-run-tokens is a SAFETY RAIL (not the test's cost control — the small
	// window + fixed turn count bound that); 300000 covers both this scenario and
	// the model-slot companion that shares the override. --context-window-override
	// forces the small compaction window.
	spawn, err := harness.NewLocalWith(
		"--context-window-override", window,
		"--max-run-tokens", envOrDefault("MECATL_E2E_COMPACTION_MAX_RUN_TOKENS", "300000"),
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local mecated with --context-window-override")
	logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(4096) }

	drv := harness.NewDriver(spawn)
	var runs []*harness.RunResult
	// buryTurn drives one prompt over the SAME reused session and asserts a clean
	// end_turn — a degraded bury turn (stop=budget/error) under-grows history and
	// would silently defeat the compaction trigger, so catch it at the source.
	buryTurn := func(scenario, prompt string) *harness.RunResult {
		ginkgo.GinkgoHelper()
		var sessionID string
		if len(runs) > 0 {
			sessionID = runs[0].SessionID
		}
		res, runErr := drv.Run(ctx, harness.RunOpts{
			Scenario:  scenario,
			Timeout:   90 * time.Second,
			SessionID: sessionID,
		}, prompt)
		gomega.Expect(runErr).NotTo(gomega.HaveOccurred(), "turn "+scenario+" transport error"+logTail())
		gomega.Expect(res.Result).NotTo(gomega.BeNil(), "turn "+scenario+" produced no terminal result"+logTail())
		gomega.Expect(res.Stop()).To(gomega.Equal("end_turn"),
			"bury turn "+scenario+" did not end cleanly (stop="+res.Stop()+") — it "+
				"under-grows history and would defeat the compaction trigger"+logTail())
		runs = append(runs, res)
		return res
	}

	// Turn 0 (FIRST USER TURN = the DISTINCTIVE task) — kept VERBATIM by the
	// first-user-pin across every compaction below.
	buryTurn("compaction-0-task", compactionTaskPrompt)
	// Turns 1-4: grow enough compactible history for a reducing tier-4 cascade
	// summary; threshold crossings that cannot reduce the complete request are no-ops.
	buryTurn("compaction-1-read", compactionReadPrompt)
	buryTurn("compaction-2-read", compactionReadPrompt)
	buryTurn("compaction-3-read", compactionReadPrompt)
	buryTurn("compaction-4-read", compactionReadPrompt)

	return spawn, runs
}

// compactionLedger sums the compactions observed across the runs and returns the
// total plus the index of the last run that observed one (-1 if none).
func compactionLedger(runs []*harness.RunResult) (total, lastRun int) {
	lastRun = -1
	for i, r := range runs {
		if n := len(r.Compactions()); n > 0 {
			total += n
			lastRun = i
		}
	}
	return total, lastRun
}

// envOrDefault returns the env var's value, or def when unset/empty. (The
// harness has an unexported envOr; this is the spec-package-local twin.)
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
