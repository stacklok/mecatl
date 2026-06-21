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
)

// compactionSpecs is the LIVE compaction-survival scenario: it drives a REAL
// model across a REAL mid-run compaction and asserts the user's distinctive task
// survives the history collapse. It is the highest-fidelity guard for the
// role-blind-tail compaction bug (the kept tail used to be sliced by count and
// could drop a recent user instruction before it was acted on) — the offline twin
// lives in engine/agent (the back-snap unit tests) and internal/app (the phase-3
// archive gate); THIS spec adds what only a real model across a real compaction
// can show: the task issued early in the conversation still gets executed after
// the summariser has replaced the head.
//
// OWN SPAWN: the scenario owns its OWN mecated process (NOT the shared suite
// target) because it needs a server spawned with --context-window-override to
// force compaction at a small, cheap window, and the result.txt side-effect
// assertion needs a readable local workspace. Local-only by construction.
//
// LANE: stays on the default haiku lane (harness.DefaultModel()) — the scenario
// uses the Write tool only on the FINAL turn (allow-once), and haiku does not
// content-filter mecatl-shaped tool-bearing requests.
//
// PRESERVATION MECHANISM — first-user-pin, NOT back-snap (why the task is turn 0).
// The compactor preserves the task two ways: the role-aware BACK-SNAP keeps the
// recentUserTurnsKept (=3) most-recent user turns verbatim, and the FIRST-USER-PIN
// keeps the first user turn verbatim across ANY number of compactions. This LIVE
// spec deliberately leans on the first-user-pin: the task is the FIRST user turn,
// so it survives VERBATIM no matter how many times compaction re-fires before GO.
//
// Why not back-snap here: back-snap only protects RECENT user turns, but the Reads
// that grow history re-trip the threshold every turn, so compaction RE-FIRES at
// each Read turn. By the GO turn the three most-recent user turns are the Reads —
// any non-first task would have aged OUT of the back-snap window and been
// summarised, leaving its survival to the (model-driven) summariser chain. That is
// structurally un-deterministic for a live test: an EARLY task old enough for a real
// collapse to summarise it is, by definition, NOT recent, so back-snap cannot keep
// it verbatim at GO. The first-user-pin is the only verbatim guarantee that holds
// across multiple compactions, so it is what a deterministic LIVE survival test must
// use. The back-snap's exact kept-tail composition is covered DETERMINISTICALLY by
// the offline engine/agent unit tests (and the phase-3 archive gate) — this spec adds
// only what offline cannot: a REAL model executing a REAL-compaction-preserved
// instruction.
//
// THE COMPACTION-TRIGGER ARITHMETIC (why the window value below is what it is).
// The trigger counts CONVERSATION MESSAGES ONLY — engine/agent/loop.go
// (maybeCompact) calls TokenCounter.CountMessages(sess.Conversation.Messages),
// and the spawn runs the default --tokenizer heuristic, so the denominator is
// HeuristicTokenCounter.CountMessages: ~len(text+toolresult bodies)/4 plus a fixed
// ~4 tokens/message and ~4/tool-call. The system prompt and tool catalog are NOT
// in it (those are billed INPUT, not the trigger denominator) — so a tiny window
// like 10000 would NEVER fire here (a few hundred conversation-tokens of short
// prompts/replies never reach 0.8 × 10000 = 8000). The controllable lever is the
// Read tool RESULT size, which is deterministic: the harness fixture
// compaction-input.txt (~8KB; ~9KB once Read adds 1-based line-number prefixes)
// contributes ~2250 conversation-tokens per Read. With the window below
// (compactionWindowDefault = 2000 → threshold 0.8 × 2000 = 1600):
//   - turn 0 (the TASK) accumulates only ~50 conversation-tokens — WELL under 1600,
//     so the task is recorded (and pinned) before any compaction;
//   - the first sized Read (turn 1) pushes history to ~2300 conversation-tokens;
//   - maybeCompact runs at the TOP of each turn, so the threshold is first crossed
//     at the TOP of turn 2 (history from turns 0-1 ≈ 2300 > 1600) — compaction
//     fires before the GO turn (index 4) and re-fires on each later Read turn. The
//     task (turn 0) is the first-user-pin, so it is kept VERBATIM through every one.
//
// ANTI-VACUITY: the survival assertion is meaningless unless compaction actually
// fired AND fired before the final GO turn. The spec therefore (A) fails loudly if
// no EvCompaction was observed, (C) asserts a compaction was seen in a run PRIOR to
// the final GO run, and (pre-GO) asserts result.txt does NOT yet exist before GO —
// so an early/non-GO write cannot make the survival check pass vacuously.

// compactionWindowDefault is the --context-window-override the spawn uses. The
// trigger fires at 0.8 × this (= 1600 conversation-tokens). It is sized against the
// per-Read accumulation documented above (one ~2250-token sized Read crosses it),
// NOT against billed input. Env-overridable via MECATL_E2E_COMPACTION_WINDOW for
// tuning. NOTE: this is deliberately tiny for the test; an operator must NOT set
// --context-window-override this low in production (it would compact every turn).
const compactionWindowDefault = "2000"

// compactionInputFile is the sized harness fixture the bury turns Read to grow
// conversation history deterministically (see fixtures.go / e2e/fixtures/workspace).
const compactionInputFile = "compaction-input.txt"

func compactionSpecs() {
	ginkgo.Describe("compaction", func() {
		ginkgo.It("survives a real mid-run compaction (the distinctive task is still executed)",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(8*time.Minute),
			func(ctx ginkgo.SpecContext) {
				// OWN process: spawned with the override flag, so it does not reuse the
				// shared suite target. Local-only — a remote target cannot be spawned with
				// scenario-specific flags and its workspace is not locally readable.
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with --context-window-override or read the workspace")
				}

				window := envOrDefault("MECATL_E2E_COMPACTION_WINDOW", compactionWindowDefault)
				// SPAWN IS PER-ATTEMPT (created inside the spec func): FlakeAttempts(2)
				// re-runs this body, so each attempt gets a FRESH .scratch workspace and no
				// stale result.txt can bleed across attempts. Do NOT hoist this spawn out of
				// the func — a prior attempt's result.txt would then make assertion B pass
				// vacuously.
				//
				// extraArgs append AFTER the standard args (Go flag pkg: last wins), so
				// --max-run-tokens here overrides the harness default (20000) — a 6-turn
				// reused session accumulates ~5-6k billed input per turn on the cumulative
				// session usage the budget reads, so a tight budget would trip stop=budget
				// before the scenario completes. This budget is a SAFETY RAIL, not the
				// test's cost/length control (the small window + fixed turn count bound
				// that) — it only has to sit above the scenario's natural usage. 150000
				// leaves comfortable headroom; raised from 100000 after live haiku drifted
				// verbose enough that the ~110k 6-turn session crossed the old rail at the
				// final GO turn (stop=budget). This spec does not test the budget (assertion
				// B's end_turn check guards a budget stop). --context-window-override forces
				// the small compaction window.
				spawn, err := harness.NewLocalWith(
					"--context-window-override", window,
					"--max-run-tokens", envOrDefault("MECATL_E2E_COMPACTION_MAX_RUN_TOKENS", "150000"),
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local mecated with --context-window-override")
				defer func() { _ = spawn.Close() }()

				drv := harness.NewDriver(spawn)
				logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(4096) }

				// runTurn drives one prompt over the SAME reused session (the driver skips
				// CreateSession when SessionID is set) and records the run for the
				// anti-vacuity ledger. It fails the spec on a transport error.
				var runs []*harness.RunResult
				runTurn := func(scenario, prompt string, approve []string) *harness.RunResult {
					ginkgo.GinkgoHelper()
					var sessionID string
					if len(runs) > 0 {
						sessionID = runs[0].SessionID
					}
					res, runErr := drv.Run(ctx, harness.RunOpts{
						Scenario:     scenario,
						Timeout:      90 * time.Second,
						SessionID:    sessionID,
						ApproveTools: approve,
					}, prompt)
					gomega.Expect(runErr).NotTo(gomega.HaveOccurred(), "turn "+scenario+" transport error"+logTail())
					gomega.Expect(res.Result).NotTo(gomega.BeNil(), "turn "+scenario+" produced no terminal result"+logTail())
					runs = append(runs, res)
					return res
				}

				// buryTurn is a runTurn that ALSO asserts the turn ended cleanly (end_turn) —
				// a degraded bury turn (stop=budget/error) would under-grow history and
				// silently defeat the compaction trigger, so catch it at the source.
				buryTurn := func(scenario, prompt string) *harness.RunResult {
					ginkgo.GinkgoHelper()
					res := runTurn(scenario, prompt, nil)
					gomega.Expect(res.Stop()).To(gomega.Equal("end_turn"),
						"bury turn "+scenario+" did not end cleanly (stop="+res.Stop()+") — it "+
							"under-grows history and would defeat the compaction trigger"+logTail())
					return res
				}

				// Turn 0 (FIRST USER TURN = the DISTINCTIVE task). Being the first user turn,
				// it is kept VERBATIM by the compactor's first-user-pin across EVERY compaction
				// that fires below — the deterministic preservation this live spec relies on
				// (see the PRESERVATION MECHANISM note above). The offline engine/agent unit
				// tests cover the back-snap path for non-first recent turns.
				buryTurn("compaction-0-task",
					`Remember this instruction for later: when I say the word GO, use the Write `+
						`tool to create a file named result.txt containing exactly the word `+
						`PINEAPPLE. Acknowledge with the single word noted.`)

				// Turns 1-4: grow history deterministically by Reading the sized fixture
				// (~2250 conversation-tokens per Read — the controllable lever; see the
				// arithmetic block above). The first Read (turn 1) crosses the threshold so
				// compaction first fires at the TOP of turn 2; later Reads re-fire it, and by
				// the GO turn the earlier Reads have collapsed into the summary while the
				// pinned task (turn 0) is kept verbatim. Four Reads (vs the framing turn the
				// task used to occupy) ensure a NON-vacuous collapse — at least one Read
				// summarised into the head — fires BEFORE the GO turn.
				readPrompt := `Read the file ` + compactionInputFile +
					` in the workspace and reply with the single word ok.`
				buryTurn("compaction-1-read", readPrompt)
				buryTurn("compaction-2-read", readPrompt)
				buryTurn("compaction-3-read", readPrompt)
				buryTurn("compaction-4-read", readPrompt)

				// PRE-GO ANTI-VACUITY (assertion B guard): the model must NOT have written
				// result.txt before the GO turn. If it did, the file would contain PINEAPPLE
				// regardless of survival and B would pass vacuously — so fail loudly here. (The
				// deny policy already withholds Write approval until GO; this makes the
				// invariant explicit rather than accidental.)
				goPath := filepath.Join(spawn.StateDir(harness.StateWorkspace), "result.txt")
				if data, statErr := os.ReadFile(goPath); statErr == nil {
					gomega.Expect(string(data)).NotTo(gomega.ContainSubstring("PINEAPPLE"),
						"result.txt already contained PINEAPPLE BEFORE the GO turn — an early/non-GO "+
							"write would make the survival assertion vacuous"+logTail())
				}

				// Final turn: trigger the task. Write is allow-once'd by the driver policy.
				//
				// The trigger word is wrapped in an ACT-NOW directive that does NOT restate
				// the task: a bare "GO" is ambiguous to a live model — observed in a failing
				// run where haiku read it as a readiness ping and replied "Ready." with NO
				// tool call (while a passing run on the same code wrote the file). The
				// directive removes that no-op failure mode WITHOUT revealing what to do:
				// the file name (result.txt) and content (PINEAPPLE) are never mentioned
				// here, so the survival assertion still requires the model to have RECALLED
				// the pinned instruction across the compaction — only now it reliably ACTS
				// on it instead of merely acknowledging. (Compliance ≠ preservation: the
				// first-user-pin guarantees the task is present; this guarantees the terse
				// trigger elicits the action.)
				final := runTurn("compaction-5-go",
					`GO. This is the trigger word from the instruction I gave you earlier. `+
						`Carry out that instruction IN FULL right now using the appropriate tool — `+
						`do not merely acknowledge or reply that you are ready.`,
					[]string{"Write"})

				// --- Assertion A (ANTI-VACUITY): compaction must have fired. ---
				totalCompactions := 0
				lastCompactionRun := -1
				for i, r := range runs {
					if n := len(r.Compactions()); n > 0 {
						totalCompactions += n
						lastCompactionRun = i
					}
				}
				gomega.Expect(totalCompactions).To(gomega.BeNumerically(">=", 1),
					"no EvCompaction observed across all turns — the --context-window-override "+window+
						" did not trip compaction; the survival assertion would be vacuous"+logTail())

				// --- Assertion C (ORDERING): a compaction was seen at or before the turn
				// BEFORE the final GO run, so PINEAPPLE genuinely survived a compaction (not
				// "never compacted"). Relaxed to <= finalRunIdx-1 (not strict <) so a
				// compaction landing one turn earlier than the model's reply-length tuning
				// predicts still satisfies it. ---
				finalRunIdx := len(runs) - 1
				gomega.Expect(lastCompactionRun).To(gomega.BeNumerically("<=", finalRunIdx-1),
					"compaction only fired on the final GO turn (or never before it) — survival is "+
						"not demonstrably across a compaction (last compaction run index="+
						strconv.Itoa(lastCompactionRun)+", final run index="+strconv.Itoa(finalRunIdx)+")"+logTail())

				// --- Assertion B (TASK SURVIVED). ---
				// A budget-truncated final run is not success: assert a clean end_turn so a
				// stop=budget run is never mistaken for the task completing.
				gomega.Expect(final.Stop()).To(gomega.Equal("end_turn"),
					"the final GO turn did not end cleanly (stop="+final.Stop()+") — a "+
						"budget/error stop must not be read as task survival"+logTail())

				// Primary: the side-effect file in the spawn's workspace contains PINEAPPLE.
				gomega.Eventually(func() string {
					data, _ := os.ReadFile(goPath)
					return string(data)
				}, 15*time.Second, 500*time.Millisecond).Should(
					gomega.ContainSubstring("PINEAPPLE"),
					"result.txt never contained PINEAPPLE after the GO turn — the task did NOT "+
						"survive compaction (path "+goPath+")"+logTail())

				// Secondary corroboration: a Write tool.call whose args carry PINEAPPLE. This
				// is robust (the literal token, never model prose) and proves the model acted
				// on the remembered instruction rather than the file pre-existing.
				wroteTask := false
				for _, c := range final.ToolCalls("Write") {
					if strings.Contains(c.Args, "PINEAPPLE") {
						wroteTask = true
					}
				}
				gomega.Expect(wroteTask).To(gomega.BeTrue(),
					"the final GO turn issued no Write call containing PINEAPPLE"+logTail())
			})
	})
}

// envOrDefault returns the env var's value, or def when unset/empty. (The
// harness has an unexported envOr; this is the spec-package-local twin.)
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
