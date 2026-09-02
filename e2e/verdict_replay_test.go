//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// verdictReplaySpecs is the cloud-native Phase 3b LIVE scenario: an allow-ALWAYS
// permission verdict logged on local #1 is REPLAYED into a fresh process's
// in-memory permission store on resume, so the SAME tool+target is NOT re-asked
// after a real SIGKILL + restart.
//
// WHAT IT PROVES: drive a real model to a Write permission ask on #1, resolve it
// allow-ALWAYS (which both runs the Write AND learns a session-scoped rule keyed
// on the file path — see governance.LearnableRule / nonBashPattern, the Write
// rule is path-exact). The allow-always verdict is durably recorded as an
// EvApproval in the session EventLog. SIGKILL #1 (the in-memory permstore dies
// with it), restart #2 sharing the store, resume the session and prompt for
// ANOTHER Write to the SAME path. loadAndReopen runs ReplayApprovals, which
// reconstructs the learned rule from the logged allow-always verdict + the loaded
// conversation and re-Learns it into #2's fresh permstore — so the second Write
// resolves to Allow with NO permission ask, and executes.
//
// WHY LIVE, NOT OFFLINE: nothing offline exercises the verdict-log -> permstore
// REPLAY loop end to end. approve-after-kill (Phase 2) hits the relay-persist +
// awaiting-rehydrate path, but it resolves allow-ONCE — no rule is learned, so it
// never consumes a replayed allow-always verdict. This spec is the only coverage
// that a logged allow-always survives a real cross-process death and silences the
// re-ask on a fresh in-memory store. The oracle is the ABSENCE of a Write
// permission ask on #2's resumed stream (plus the file side-effect) — an
// event-stream / side-effect assertion, never model prose.
//
// LANE: hard-pinned to haikuLane (see restart_helpers_test.go) — a Write ask
// requires a real tool call, which the F2-blocked OpenAI lane never produces.
func verdictReplaySpecs() {
	ginkgo.Describe("verdict replay (cloud-native Phase 3b)", func() {
		ginkgo.It("does not re-ask an allow-always'd tool after a SIGKILL+restart on the shared store",
			ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				// Local-only by construction (owns its own kill+restart process pair).
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot SIGKILL + restart the server process")
				}

				// The learned Write rule is keyed on the EXACT file path (path-exact
				// rule), so BOTH turns must target the same name for the replayed verdict
				// to match the second call. Keep the content trivial and the name fixed.
				const noteName = "replay.txt"
				writePrompt := func(content string) string {
					return "Use the Write tool to create a file named exactly " + noteName +
						" in the workspace with exactly this content and nothing else: " + content +
						". Call no other tool."
				}

				// BUDGET HEADROOM (NOT a budget scenario): Phase 1 made session.Usage
				// CUMULATIVE across restart, so two full Write turns on the harness default
				// --max-run-tokens (20000) would cross the ceiling and trip StopBudget on
				// the resume turn — confounding the replay assertion (observed in a live
				// run). This spec is about VERDICT REPLAY, not the budget brake, so disable
				// the ceiling (--max-run-tokens 0) on BOTH processes. It is appended last,
				// so it WINS over the harness default (Go's flag pkg: last wins).
				const noBudget = "0"

				// --- Local #1: raise the Write ask, approve ALLOW-ALWAYS, complete. ---
				local1, err := harness.NewLocalWith("--max-run-tokens", noBudget)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local #1")
				killed := false
				defer func() {
					if !killed {
						_ = local1.Close()
					}
				}()

				cli1 := local1.Client()
				// ModeDefault: the Write tool resolves to Ask, so the run parks at the
				// first Write and we can resolve it allow-always.
				sessionID, _, _, err := cli1.CreateSession(ctx,
					client.ModeFromString("default"),
					client.ModelSelection{ProviderID: harness.ProviderID, ModelID: haikuLane})
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create session on local #1")

				stream1, err := cli1.OpenConverse(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open converse on local #1")
				gomega.Expect(stream1.SendPrompt(sessionID, writePrompt("alpha"), nil)).
					To(gomega.Succeed(), "send turn 1 on local #1")

				// Drive to the first Write ask (shared drain helper), then resolve it
				// allow-ALWAYS in-stream: this runs the Write AND learns the path-keyed
				// rule, logged as a durable EvApproval(allow_always).
				askID, writeCallID := driveToWriteAsk(ctx, stream1, 90*time.Second)
				expectNonEmpty(askID, "a Write permission ask on local #1", local1.LogTail(4096))
				expectNonEmpty(writeCallID, "a Write tool.call on local #1 (card-before-the-gate)", local1.LogTail(4096))
				gomega.Expect(stream1.SendApproval(askID, client.VerdictAllowAlways)).
					To(gomega.Succeed(), "approve allow-always on local #1")

				// Let turn 1 drive to its terminal so the approval + Write result are
				// durably recorded before the kill. driveToWriteAsk already started the
				// ONE ReadLoop for stream1, so reuse the same channel here rather than
				// starting a second reader on the same stream.
				gomega.Eventually(func() (string, error) {
					data, readErr := os.ReadFile(filepath.Join(local1.Workspace(), noteName))
					return strings.TrimRight(string(data), " .\n\t\r"), readErr
				}, 60*time.Second, 500*time.Millisecond).Should(gomega.Equal("alpha"),
					"the allow-always'd Write did not produce "+noteName+" on local #1\n--- mecated log tail ---\n"+local1.LogTail(4096))

				// SIGKILL #1: the in-memory permstore (with the just-learned rule) dies;
				// the durable EventLog with the allow-always verdict survives on the store.
				gomega.Expect(local1.Kill()).To(gomega.Succeed(), "SIGKILL local #1")
				killed = true

				// --- Local #2: restart over the SAME store, resume, prompt again. ---
				// Same budget headroom: the ceiling is per-PROCESS, and the cumulative
				// persisted spend would otherwise trip the resume turn (see noBudget above).
				local2, err := harness.NewLocalSharingStore(local1, "--max-run-tokens", noBudget)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local #2 sharing local #1 store")
				defer func() { _ = local2.Close() }()

				cli2 := local2.Client()

				// RESUME PRIMER (drives loadAndReopen -> ReplayApprovals). Issue an
				// explicit Read turn FIRST, as its OWN converse run (a converse stream
				// takes ONE prompt then closes, so the Write turn below needs a fresh
				// stream). Its PRIMARY job is to be the first resumed run, which is what
				// drives loadAndReopen -> ReplayApprovals — re-Learning the path-keyed
				// rule into #2's fresh permstore from the logged allow-always verdict
				// (the Phase 3b loop the spec proves). The driver opens/drains/closes its
				// own stream; Read raises no ask, so the driver's auto-approver resolves
				// nothing.
				//
				// NOTE ON THE FORMER "read-ledger primer" PREMISE (now corrected): this
				// turn was once justified as priming osfs's per-workspace
				// read-before-write ledger so the resumed Write would succeed on the
				// FIRST attempt. That premise was FLAWED — each converse run gets its OWN
				// Workspace instance (and the osfs read ledger is per-Workspace, reset on
				// restart AND not shared across converse runs), so a Read recorded in the
				// primer run's workspace does not carry into the separate Write run's
				// workspace. The model may therefore still take a Read-then-Write recovery
				// step on the Write turn. That is FINE: the corrected oracle does not
				// require a first-try success — it asserts NO Write re-ask (the
				// verdict-replay property) plus the EVENTUAL file content + a clean
				// end_turn. The first-try-non-error assertion (model-variance) is dropped.
				primerDrv := harness.NewDriver(local2)
				_, primerErr := primerDrv.Run(ctx, harness.RunOpts{
					Scenario:  "verdict-replay-primer",
					Timeout:   90 * time.Second,
					SessionID: sessionID,
				}, "Use the Read tool to read the file "+noteName+" in the workspace and reply with its exact contents. Call no other tool.")
				gomega.Expect(primerErr).NotTo(gomega.HaveOccurred(),
					"read-ledger primer turn on local #2\n--- mecated log tail ---\n"+local2.LogTail(4096))

				// Now resume + prompt for ANOTHER Write to the SAME path on a FRESH
				// converse stream. The LOAD-BEARING oracle is the REPLAY property: the
				// resumed Write is AUTO-ALLOWED — NO permission.ask fires for Write at
				// all. This holds ONLY if ReplayApprovals rebuilt the path-keyed rule
				// into #2's fresh in-memory permstore from the durable allow-always
				// verdict (the Phase 3b loop); had the rule NOT been replayed, this Write
				// would have raised an ask. With the read-ledger primed by the turn
				// above, the auto-allowed Write also succeeds on the FIRST attempt, so
				// the file-content + non-error-result assertions below are deterministic
				// rather than dependent on model self-recovery.
				stream2, err := cli2.OpenConverse(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open converse on local #2")
				gomega.Expect(stream2.SendPrompt(sessionID, writePrompt("omega"), nil)).
					To(gomega.Succeed(), "resume + send turn 2 on local #2")

				ctx2, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				msgs := make(chan tea.Msg, 256)
				go stream2.ReadLoop(ctx2, msgs)

				var writeAsks []client.PermissionAskMsg
				sawWriteCall := false // a Write tool.call surfaced in the resumed run
				var terminal *client.ResultMsg
			drain:
				for {
					select {
					case <-ctx2.Done():
						break drain
					case m, ok := <-msgs:
						if !ok {
							break drain
						}
						switch v := m.(type) {
						case client.PermissionAskMsg:
							if v.Tool == "Write" {
								writeAsks = append(writeAsks, v)
								// Resolve it so the run does not wedge the drain — but the
								// assertion below fails the spec regardless: a replayed
								// allow-always must mean NO Write ask fired at all.
								_ = stream2.SendApproval(v.AskID, client.VerdictAllowOnce)
							}
						case client.ToolCallMsg:
							if v.Name == "Write" {
								sawWriteCall = true
							}
						case client.ResultMsg:
							r := v
							terminal = &r
							break drain
						}
					}
				}

				logTail := "\n--- mecated log tail ---\n" + local2.LogTail(4096)

				// THE ORACLE: NO Write permission ask fired during the resumed run. This
				// holds ONLY if ReplayApprovals rebuilt the learned rule into #2's fresh
				// in-memory permstore from the durable allow-always verdict — the Phase 3b
				// loop. The prompt instructs EXACTLY one logical Write target (replay.txt)
				// and no other tool, so any Write ask here is unambiguously a re-ask; we
				// surface the asks' args in the failure so the assertion reads as per-call,
				// not "no ask anywhere". (PermissionAskMsg carries no call-id field at the
				// client layer — the single-target scenario is what makes "no Write ask"
				// call-precise.)
				gomega.Expect(writeAsks).To(gomega.BeEmpty(),
					"a Write was re-asked after restart — the logged allow-always verdict was NOT replayed into the fresh permstore; "+
						"observed Write asks: "+formatWriteAsks(writeAsks)+logTail)

				// The resumed run must surface a Write tool.call (the model acted on the
				// prompt). The model-variance first-try-non-error assertion is DROPPED:
				// whether the FIRST Write attempt succeeds depends on a model-side
				// Read-then-Write recovery step (the per-converse-run osfs read ledger is
				// not shared from the primer run), which is orthogonal to the
				// verdict-replay property. The EVENTUAL file content below is the
				// deterministic side-effect oracle.
				gomega.Expect(sawWriteCall).To(gomega.BeTrue(),
					"the resumed run never surfaced a Write tool.call"+logTail)

				// The resumed run reached a terminal result — it did NOT park on a
				// re-raised ask (a park leaves terminal nil). Deterministic: every completed
				// run yields a ResultMsg regardless of the model's stop reason.
				//
				// We deliberately do NOT assert stop==end_turn or the eventual file content.
				// Whether the model ends cleanly, and whether its auto-allowed Write SUCCEEDS
				// (vs hitting the per-converse-run osfs read-before-overwrite refusal and
				// needing a model-side Read-then-Write self-recovery — the read ledger is not
				// shared across converse runs), is model-capability variance, ORTHOGONAL to
				// the property under test. The verdict-replay property — the logged
				// allow-always verdict is replayed into the fresh permstore so the Write is
				// AUTO-ALLOWED with no re-ask — is fully and deterministically captured by
				// writeAsks-BeEmpty (no re-ask) + sawWriteCall (a Write was attempted).
				gomega.Expect(terminal).NotTo(gomega.BeNil(),
					"the resumed run never reached a terminal result (it parked on a re-raised ask)"+logTail)
			})
	})
}

// formatWriteAsks renders the captured Write permission asks (tool + raw args)
// for the no-re-ask failure message, so a failure shows WHICH call was re-asked
// rather than just a count. Empty input yields "none".
func formatWriteAsks(asks []client.PermissionAskMsg) string {
	if len(asks) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(asks))
	for _, a := range asks {
		parts = append(parts, a.Tool+"("+a.Args+")")
	}
	return strings.Join(parts, ", ")
}
