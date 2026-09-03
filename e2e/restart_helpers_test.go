//go:build e2e

package e2e_test

import (
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// restart_helpers_test.go holds the small, genuinely-shared building blocks of
// the THREE cloud-native restart specs (approve-after-kill, snapshot-fidelity,
// verdict-replay). The restart DANCE itself (NewLocal → drive → Kill →
// NewLocalSharingStore) is NOT abstracted: each spec's spawn/kill/respawn
// sequence is short, and what varies between them (what is driven, what is
// approved, what is asserted) is the WHOLE point of the spec — folding it behind
// one helper would hide the distinct assertions, not clarify them (the wrong
// abstraction is worse than the duplication; Rule of Three applies to the
// stream-drain loops below, which all three specs genuinely share, NOT to the
// process lifecycle).
//
// What IS shared and extracted here:
//   - haikuLane: the hard-pinned tool-calling lane every restart spec uses.
//   - driveToWriteAsk: drain a Converse stream until the first Write permission
//     ask, returning the ask id AND the Write tool.call id (card-before-the-gate).
//   - driveToResult: drain a Converse stream until the terminal ResultMsg.

// haikuLane is the model every cloud-native restart spec HARD-PINS, independent
// of the MECATL_E2E_MODEL env override. These scenarios REQUIRE a real tool call
// (Write) and/or deterministic token accounting; the OpenAI-family lane
// content-filters tool-bearing mecatl-shaped requests (finding F2, full trail in
// e2e/README.md), so a Write ask would never fire and the budget turns would not
// run. harness.DefaultModel() honours the env override, so it is deliberately NOT
// used by these specs. (Same value + rationale as approveAfterKillModel, shared
// here so the three restart specs cannot drift on the lane.) The name is historic
// — the lane was anthropic/claude-3.5-haiku until it EOL'd on AWS Bedrock and was
// repointed to anthropic/claude-haiku-4.5 (same family, same lane rationale).
const haikuLane = "anthropic/claude-haiku-4.5"

// driveToWriteAsk drains the stream msgs channel until the FIRST Write permission
// ask, returning that ask's id AND the Write tool.call id. The tool.call id
// arrives BEFORE the ask (card-before-the-gate: the EvToolCall is emitted before
// authorize), so a single drain captures both. It does NOT answer the ask — the
// caller decides the verdict (approve-after-kill leaves it parked; verdict-replay
// approves allow-always). Stream errors, premature closure, cancellation, and the
// local deadline are returned explicitly so admission failures cannot degrade
// into misleading missing-event assertions.
//
// Shared by approve-after-kill (Phase 2) and verdict-replay (Phase 3): both must
// drive a real model to a real Write ask before they diverge on the verdict.
func driveToWriteAsk(ctx ginkgo.SpecContext, stream *client.Stream, deadline time.Duration) (askID, writeCallID string, err error) {
	ginkgo.GinkgoHelper()
	msgs := make(chan tea.Msg, 256)
	go stream.ReadLoop(ctx, msgs)

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return askID, writeCallID, fmt.Errorf("waiting for Write permission ask: %w", ctx.Err())
		case <-timer.C:
			return askID, writeCallID, fmt.Errorf("waiting for Write permission ask: deadline exceeded after %s", deadline)
		case m, ok := <-msgs:
			if !ok {
				return askID, writeCallID, errors.New("waiting for Write permission ask: stream closed prematurely")
			}
			switch v := m.(type) {
			case client.ToolCallMsg:
				if v.Name == "Write" && writeCallID == "" {
					writeCallID = v.ID
				}
			case client.PermissionAskMsg:
				if v.Tool == "Write" {
					return v.AskID, writeCallID, nil
				}
			case client.StreamErrMsg:
				return askID, writeCallID, fmt.Errorf("waiting for Write permission ask: stream error: %w", v.Err)
			case client.StreamClosedMsg:
				return askID, writeCallID, errors.New("waiting for Write permission ask: stream closed prematurely")
			}
		}
	}
}

// driveToResult drains the stream msgs channel until the terminal ResultMsg.
// Stream errors, premature closure, cancellation, and the local deadline are
// returned explicitly. The ResultMsg carries the run's stop reason and per-run
// usage — the event-layer oracle the snapshot-fidelity spec asserts the budget
// terminal on.
//
// Shared by both turns of the snapshot-fidelity spec (a budget-PASSING turn on
// #1 and a budget-TRIPPING turn on #2); kept here next to driveToWriteAsk so the
// two stream-drain idioms live together.
func driveToResult(ctx ginkgo.SpecContext, stream *client.Stream, deadline time.Duration) (client.ResultMsg, error) {
	ginkgo.GinkgoHelper()
	msgs := make(chan tea.Msg, 256)
	go stream.ReadLoop(ctx, msgs)

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return client.ResultMsg{}, fmt.Errorf("waiting for terminal result: %w", ctx.Err())
		case <-timer.C:
			return client.ResultMsg{}, fmt.Errorf("waiting for terminal result: deadline exceeded after %s", deadline)
		case m, ok := <-msgs:
			if !ok {
				return client.ResultMsg{}, errors.New("waiting for terminal result: stream closed prematurely")
			}
			switch v := m.(type) {
			case client.ResultMsg:
				return v, nil
			case client.StreamErrMsg:
				return client.ResultMsg{}, fmt.Errorf("waiting for terminal result: stream error: %w", v.Err)
			case client.StreamClosedMsg:
				return client.ResultMsg{}, errors.New("waiting for terminal result: stream closed prematurely")
			}
		}
	}
}

// expectNonEmpty is a tiny shared assertion guard: a captured id must be present,
// or the spec failed at the drive step (with the server log tail for diagnosis).
func expectNonEmpty(got, what string, logTail string) {
	ginkgo.GinkgoHelper()
	gomega.Expect(got).NotTo(gomega.BeEmpty(),
		"never observed "+what+" within the deadline\n--- mecated log tail ---\n"+logTail)
}
