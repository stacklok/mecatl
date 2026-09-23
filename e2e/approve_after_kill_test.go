//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// approveAfterKillSpecs is the cloud-native Phase 2 LIVE scenario: raise a real
// permission ask against a real model, SIGKILL the mecated process WITHOUT
// cleanup, restart a SECOND mecated over the SAME --store-dir, POST approve over
// HTTP, and assert the pending tool ran EXACTLY ONCE (real filesystem effect) and
// the resumed run completed (StopEndTurn).
//
// It is the live counterpart of the offline two-Build gate TestApproveAfterRestartE2E
// (internal/app/), which exercises the identical resume-from-awaiting path through
// the composition (app.Build #1 → ask + Persist → Close = process death →
// app.Build #2 over the same store → ApproveRun → ResumeApproval → exactly-once
// Write + StopEndTurn). The two-Build gate stays the CI-green proof; THIS spec adds
// what only a real SIGKILL across two OS processes can confirm: a durable awaiting
// snapshot written by the live gRPC relay (Persist-on-ask), survives the abrupt
// death, and a fresh process reads + resumes it over a separate HTTP listener.
//
// LANE: HARD-PINNED to anthropic/claude-haiku-4.5 (the haiku lane constant
// below), independent of the MECATL_E2E_MODEL env override. This scenario
// REQUIRES a real tool call (Write) to raise the ask, and the OpenAI-family lane
// content-filters tool-bearing mecatl-shaped requests (finding F2; full trail in
// e2e/README.md) — so the Write ask would never fire and the spec would fail at
// the ask deadline instead of running. harness.DefaultModel() honours the env
// override, so it is deliberately NOT used here.
//
// CAVEAT (what even this live spec does NOT catch): a torn final append racing the
// kill (jsonlstore appendLine is not an atomic rename) and OS-crash durability (no
// fsync) — both narrow and out of scope for "disposable process" (process restart,
// not host crash); see docs/adr/0027-cloud-native.md.
// This scenario's hard-pinned lane is the shared haikuLane constant (see
// restart_helpers_test.go): a tool-call-capable Bedrock-routed model that does
// NOT content-filter mecatl-shaped tool-bearing requests, independent of
// MECATL_E2E_MODEL so the env override cannot route this onto the F2-blocked
// OpenAI lane and make the required Write ask never fire.

func approveAfterKillSpecs() {
	ginkgo.Describe("approve-after-kill (cloud-native Phase 2)", func() {
		ginkgo.It("resumes an awaiting session across a SIGKILL+restart and runs the tool exactly once",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(240*time.Second),
			func(ctx ginkgo.SpecContext) {
				// This scenario owns its OWN process pair (it kills + restarts), so it
				// does not reuse the shared suite target. Local-only by construction —
				// a remote target cannot be SIGKILLed + restarted by the harness.
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot SIGKILL + restart the server process")
				}

				// --- Local #1: raise the Write ask, do NOT approve, then SIGKILL. ---
				local1, err := harness.NewLocal()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local #1")
				killed := false
				defer func() {
					// Close is safe after Kill (nil-guarded conn/log, already-reaped
					// process); on an early failure it tears local1 down cleanly.
					if !killed {
						_ = local1.Close()
					}
				}()

				cli1 := local1.Client()
				// ModeDefault: the Write tool resolves to Ask, so the run parks.
				sessionID, _, _, err := cli1.CreateSession(ctx,
					client.ModeFromString("default"),
					client.ModelSelection{ProviderID: harness.ProviderID, ModelID: haikuLane})
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create session on local #1")

				stream1, err := cli1.OpenConverse(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "open converse on local #1")

				const prompt = `Use the Write tool to create a file named note.txt in the workspace ` +
					`with exactly this content and nothing else: survived. Call no other tool.`
				gomega.Expect(stream1.SendPrompt(sessionID, prompt, nil)).To(gomega.Succeed(), "send prompt on local #1")

				// Drive the stream until the FIRST Write permission ask; capture its id
				// AND the Write tool.call id (card-before-the-gate: the EvToolCall is
				// emitted before authorize, so the ToolCallMsg arrives before the ask).
				// The tool.call id is the canonical pending-call id the exactly-once
				// assertion counts results for — captured HERE from local #1's stream so
				// it does not depend on the resumed SSE replaying the pre-restart call.
				// DO NOT approve — the run stays parked awaiting; the live gRPC relay
				// persists a durable awaiting snapshot on the ask (Persist-on-ask).
				runID, askID, writeCallID, driveErr := driveToWriteAsk(ctx, stream1, 90*time.Second)
				gomega.Expect(driveErr).NotTo(gomega.HaveOccurred(),
					"drive local #1 to the Write permission ask\n--- mecated log tail ---\n"+local1.LogTail(4096))
				expectNonEmpty(runID, "the exact run id on local #1", local1.LogTail(4096))
				expectNonEmpty(askID, "a Write permission ask on local #1", local1.LogTail(4096))
				expectNonEmpty(writeCallID, "a Write tool.call on local #1 (card-before-the-gate)", local1.LogTail(4096))

				// The Write must NOT have run yet — it is parked at the ask.
				notePath := filepath.Join(local1.Workspace(), "note.txt")
				_, statErr := os.Stat(notePath)
				gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue(),
					"note.txt exists before approval — the Write must be parked at the ask, not executed")

				// SIGKILL local #1 WITHOUT cleanup: the parked run + ask registry die;
				// the durable awaiting snapshot is the last write to the shared store.
				gomega.Expect(local1.Kill()).To(gomega.Succeed(), "SIGKILL local #1")
				killed = true

				// --- Local #2: restart over the SAME store, approve over HTTP. ---
				local2, err := harness.NewLocalSharingStore(local1)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local #2 sharing local #1 store")
				defer func() { _ = local2.Close() }()

				// Resolve the exact parked run on local #2. This control is an ACK-only
				// request; completion is observed from the finite durable event replay.
				approveCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				_, err = harness.ResolveAskOverHTTP(approveCtx, local2.HTTPAddr(), sessionID, runID, askID, "allow_once")
				gomega.Expect(err).NotTo(gomega.HaveOccurred(),
					"resolve ask over HTTP on local #2\n--- mecated log tail ---\n"+local2.LogTail(4096))

				var sse []byte
				var events []sseEvent
				gomega.Eventually(func() error {
					var replayErr error
					sse, replayErr = harness.ReplaySessionEventsOverHTTP(approveCtx, local2.HTTPAddr(), sessionID)
					if replayErr != nil {
						return replayErr
					}
					events, replayErr = parseSSEEvents(sse)
					if replayErr != nil {
						return replayErr
					}
					if !sseHasEndTurn(events, runID) {
						return fmt.Errorf("run %s has not reached end_turn\n--- durable events ---\n%s", runID, truncate(string(sse), 2048))
					}
					return nil
				}, 60*time.Second, 500*time.Millisecond).Should(gomega.Succeed(),
					"the resumed run did not reach a clean end_turn\n--- mecated log tail ---\n"+local2.LogTail(4096))
				sseDump := "\n--- durable events ---\n" + truncate(string(sse), 2048) + "\n--- mecated log tail ---\n" + local2.LogTail(4096)

				// ASSERT exactly-once AT THE EVENT LAYER — the core of Phase 2. The
				// detached resumed run is appended to the durable event log. Count
				// tool.result frames for the PENDING Write call id (captured from local
				// #1's pre-restart stream): assert EXACTLY ONE, and that the one result
				// is NOT an error. A double-dispatch of the pending Write (the regression
				// Phase 2 prevents) would append TWO tool.result frames for the call id — which content-equality + end_turn
				// alone cannot see (a re-Write writes identical bytes; end_turn rides any
				// clean end). Mirrors the offline twin's EvToolResult==1 count.
				var total, nonError int
				for _, ev := range events {
					if ev.RunID == runID && ev.Type == "tool.result" && ev.ToolResult != nil && ev.ToolResult.CallID == writeCallID {
						total++
						if !ev.ToolResult.IsError {
							nonError++
						}
					}
				}
				gomega.Expect(total).To(gomega.Equal(1),
					"expected EXACTLY ONE tool.result for the pending Write call id "+writeCallID+" (exactly-once); got total="+plural(total)+sseDump)
				gomega.Expect(nonError).To(gomega.Equal(1),
					"expected the one Write tool.result to be non-error; got non-error="+plural(nonError)+sseDump)

				// Soundness backstop: if the durable replay contains Write tool.call
				// frames, none may introduce a SECOND, distinct Write call id (that would
				// be a different double-dispatch shape). Any replayed Write call must be
				// the same pending id.
				for _, ev := range events {
					if ev.RunID == runID && ev.Type == "tool.call" && ev.ToolCall != nil && ev.ToolCall.Name == "Write" {
						gomega.Expect(ev.ToolCall.ID).To(gomega.Equal(writeCallID),
							"the resumed stream introduced a second distinct Write tool.call id "+ev.ToolCall.ID+sseDump)
					}
				}

				// ASSERT the filesystem side effect: note.txt exists with the expected
				// content. (The workspace is the SHARED state tree, so reading via either
				// Local resolves the same path.) This is the CORROBORATING check — the
				// exactly-once guarantee is the event count above. The content match
				// tolerates model-added trailing punctuation/whitespace (e.g. haiku writes
				// "survived." with a period): LLM output is not byte-exact, so trimming
				// trailing punctuation keeps this from being a brittle false-negative.
				gomega.Eventually(func() (string, error) {
					data, readErr := os.ReadFile(filepath.Join(local2.Workspace(), "note.txt"))
					return strings.TrimRight(string(data), " .\n\t\r"), readErr
				}, 15*time.Second, 500*time.Millisecond).Should(gomega.Equal("survived"),
					"the approved Write did not produce note.txt with the expected content\n--- mecated log tail ---\n"+local2.LogTail(4096))
			})
	})
}

// sseEvent is the minimal proto-Event projection the exactly-once assertion needs
// from the relayed SSE frames. It keeps the spec proto-free (std encoding/json
// over the JSON the relay encodes); the field tags mirror the proto Event /
// ToolCall / ToolResult JSON names (contracts/gen mecatl.v1). NOTE: is_error is
// omitempty-elided to absent when false, so the bool field correctly defaults to
// false — never substring-match "is_error".
type sseEvent struct {
	Type     string `json:"type"`
	RunID    string `json:"run_id"`
	ToolCall *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tool_call"`
	ToolResult *struct {
		CallID  string `json:"call_id"`
		IsError bool   `json:"is_error"`
	} `json:"tool_result"`
	Result *struct {
		Stop string `json:"stop"`
	} `json:"result"`
}

// parseSSEEvents extracts decoded proto Events from the `data: ` frames of an
// SSE body. Mirrors the server-test parseSSE helper (internal/adapter/server/
// http_test.go). A malformed data frame or oversized line fails the observation:
// skipping either could hide a duplicate tool.result from the exactly-once oracle.
func parseSSEEvents(body []byte) ([]sseEvent, error) {
	var out []sseEvent
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tool.result content can be large
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, fmt.Errorf("decode SSE data frame: %w", err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan SSE event replay: %w", err)
	}
	return out, nil
}

// sseHasEndTurn reports whether the exact run's durable events carry stop=end_turn.
func sseHasEndTurn(events []sseEvent, runID string) bool {
	for _, ev := range events {
		if ev.RunID == runID && ev.Type == "result" && ev.Result != nil && ev.Result.Stop == "end_turn" {
			return true
		}
	}
	return false
}

// plural renders an int for a failure message (kept tiny to avoid fmt import noise
// at call sites).
func plural(n int) string { return strconv.Itoa(n) }

// truncate caps a diagnostic string for the failure report.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
