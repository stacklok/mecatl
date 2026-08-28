package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func failedStepRetryableResult() client.ResultMsg {
	return client.ResultMsg{
		Stop: stopError, Error: "backend unavailable",
		RetryDispositionPresent: true, RetryDisposition: client.RetryDispositionRetryable,
		StreamProgressPresent: true, StreamProgress: client.StreamProgressPrecommit,
	}
}

func retryFrameCount(frames []*mecatlv1.ConverseRequest) int {
	n := 0
	for _, frame := range frames {
		if frame.GetRetry() != nil {
			n++
		}
	}
	return n
}

func userBlockCount(c conversation) int {
	n := 0
	for _, b := range c.blocks {
		if b.kind == blockUser {
			n++
		}
	}
	return n
}

func TestFailedStepRetryKeepsQueueAndComposeStateThenHealthyDrain(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")
	m.prompt.Rewrite("unfinished draft")
	usersBefore := userBlockCount(m.conv)

	mm, cmd := m.Update(failedStepRetryableResult())
	m = mm.(Model)
	runBatchLeaves(cmd)

	if got := m.queued; len(got) != 2 || got[0] != "second" || got[1] != "third" {
		t.Fatalf("failed-step retry changed FIFO: %v", got)
	}
	if got := m.prompt.Value(); got != "unfinished draft" {
		t.Fatalf("failed-step retry changed textarea: %q", got)
	}
	if got := userBlockCount(m.conv); got != usersBefore {
		t.Fatalf("failed-step retry added a user card: %d -> %d", usersBefore, got)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "retrying failed model step") {
		t.Fatalf("status = %q", stripANSIstr(m.statusMsg))
	}
	frames := conv.send.frames()
	if retryFrameCount(frames) != 1 || len(promptTexts(conv.send)) != 1 {
		t.Fatalf("before retry success frames=%#v, want initial Prompt plus one RetryStart", frames)
	}

	// A healthy retry becomes authoritative at turn.start, then resumes the queue.
	m = applyAll(m, client.ModelRetryMsg{})
	m = applyAll(m, client.TurnStartMsg{})
	m.prompt.Reset() // the draft above is only a mutation sentinel; normal running input is empty.
	mm, cmd = m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if len(m.queued) != 0 {
		t.Fatalf("healthy retry did not drain queue: %v", m.queued)
	}
	prompts := promptTexts(conv.send)
	wantQueued := "second" + queueMergeSep + "third"
	if len(prompts) != 2 || prompts[1] != wantQueued {
		t.Fatalf("prompts = %v, want initial then %q", prompts, wantQueued)
	}
	if retryFrameCount(conv.send.frames()) != 1 {
		t.Fatalf("healthy drain launched another retry: %#v", conv.send.frames())
	}
}

func TestRetryPreTurnBudgetStopKeepsQueuePausedAndManualRetryAvailable(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(failedStepRetryableResult())
	m = mm.(Model)
	runBatchLeaves(cmd)
	m = applyAll(m, client.ModelRetryMsg{})
	mm, cmd = m.Update(client.ResultMsg{Stop: "budget"})
	m = mm.(Model)
	runBatchLeaves(cmd)

	if len(m.queued) != 1 || m.queued[0] != "second" || m.queuePaused != "retry_pending" || m.phase != phaseIdle {
		t.Fatalf("pre-turn budget stop changed queue state: queued=%v paused=%q phase=%v", m.queued, m.queuePaused, m.phase)
	}
	if m.failedStepRetryAuthoritative {
		t.Fatal("pre-turn budget stop became authoritative")
	}
	if got := promptTexts(conv.send); len(got) != 1 || got[0] != "first" {
		t.Fatalf("queued prompt sent before an authoritative retry turn: %v", got)
	}
	if retryFrameCount(conv.send.frames()) != 1 {
		t.Fatalf("automatic retry frames = %#v, want one", conv.send.frames())
	}

	mm, cmd = m.runFailedStepRetry()
	m = mm.(Model)
	runBatchLeaves(cmd)
	if retryFrameCount(conv.send.frames()) != 2 || len(m.queued) != 1 || len(promptTexts(conv.send)) != 1 {
		t.Fatalf("manual /retry unavailable or changed queue: frames=%#v queued=%v", conv.send.frames(), m.queued)
	}
}

func TestFailedStepRetrySecondFailurePausesWithoutLoop(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "second")

	mm, cmd := m.Update(failedStepRetryableResult())
	m = mm.(Model)
	runBatchLeaves(cmd)
	m = applyAll(m, client.ModelRetryMsg{})
	m = applyAll(m, client.TurnStartMsg{})
	mm, cmd = m.Update(failedStepRetryableResult())
	m = mm.(Model)
	runBatchLeaves(cmd)

	if retryFrameCount(conv.send.frames()) != 1 {
		t.Fatalf("automatic failed-step retry must be bounded to one: %#v", conv.send.frames())
	}
	if len(m.queued) != 1 || m.queuePaused != stopError || m.phase != phaseIdle {
		t.Fatalf("second failure must pause intact queue: queued=%v paused=%q phase=%v", m.queued, m.queuePaused, m.phase)
	}
	if len(promptTexts(conv.send)) != 1 {
		t.Fatalf("queued prompt sent before retry success: %v", promptTexts(conv.send))
	}
}

func TestIneligibleResultsNeverAutoRetry(t *testing.T) {
	base := failedStepRetryableResult()
	tests := map[string]client.ResultMsg{
		"visible":            func() client.ResultMsg { r := base; r.StreamProgress = client.StreamProgressVisible; return r }(),
		"complete":           func() client.ResultMsg { r := base; r.StreamProgress = client.StreamProgressComplete; return r }(),
		"permanent":          func() client.ResultMsg { r := base; r.RetryDisposition = client.RetryDispositionPermanent; return r }(),
		"unknown":            func() client.ResultMsg { r := base; r.RetryDisposition = client.RetryDispositionUnknown; return r }(),
		"disposition absent": func() client.ResultMsg { r := base; r.RetryDispositionPresent = false; r.Transient = true; return r }(),
		"progress absent":    func() client.ResultMsg { r := base; r.StreamProgressPresent = false; return r }(),
	}
	for name, result := range tests {
		t.Run(name, func(t *testing.T) {
			m, conv := newQueueModel(t)
			m = startRunning(t, m, "first")
			m = enqueue(t, m, "next")
			mm, cmd := m.Update(result)
			m = mm.(Model)
			runBatchLeaves(cmd)
			if retryFrameCount(conv.send.frames()) != 0 || len(promptTexts(conv.send)) != 1 {
				t.Fatalf("ineligible result sent work: %#v", conv.send.frames())
			}
			if len(m.queued) != 1 || m.queuePaused != stopError {
				t.Fatalf("ineligible result did not pause FIFO: queued=%v paused=%q", m.queued, m.queuePaused)
			}
		})
	}
}

func TestManualFailedStepRetryVisibleAndSecondPrecommit(t *testing.T) {
	tests := map[string]func(Model) Model{
		"visible": func(m Model) Model {
			r := failedStepRetryableResult()
			r.StreamProgress = client.StreamProgressVisible
			return applyAll(m, r)
		},
		"second precommit failure": func(m Model) Model {
			m = applyAll(m, failedStepRetryableResult())
			return applyAll(m, failedStepRetryableResult())
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			m, conv := newQueueModel(t)
			m = startRunning(t, m, "first")
			m = enqueue(t, m, "queued")
			m = prepare(m)
			m.prompt.Rewrite("/retry")
			mm, cmd, handled := m.dispatchBareBuiltin(m.prompt.Value())
			if !handled {
				t.Fatal("/retry was not handled")
			}
			m = mm.(Model)
			runBatchLeaves(cmd)
			if retryFrameCount(conv.send.frames()) < 1 || len(m.queued) != 1 {
				t.Fatalf("frames=%#v queued=%v", conv.send.frames(), m.queued)
			}
			if m.prompt.Value() != "" || userBlockCount(m.conv) != 1 {
				t.Fatalf("manual retry did not consume command or changed transcript: input=%q users=%d", m.prompt.Value(), userBlockCount(m.conv))
			}
		})
	}
}

func TestManualFailedStepRetryDelegatesEligibilityToServerWithoutLocalCandidate(t *testing.T) {
	m, conv := newQueueModel(t)
	m.prompt.Rewrite("draft")
	users := userBlockCount(m.conv)
	mm, cmd := m.runFailedStepRetry()
	m = mm.(Model)
	runBatchLeaves(cmd)
	if retryFrameCount(conv.send.frames()) != 1 || m.prompt.Value() != "draft" || userBlockCount(m.conv) != users {
		t.Fatalf("manual retry did not preserve compose/transcript or send RetryStart: frames=%#v input=%q", conv.send.frames(), m.prompt.Value())
	}
}

func TestRetryStartTransportFailurePreservesManualAffordanceAndQueue(t *testing.T) {
	m, conv := newQueueModel(t)
	m.queued = []string{"later"}
	m.queuePaused = stopError
	m.prompt.Rewrite("draft")
	users := userBlockCount(m.conv)
	mm, cmd := m.runFailedStepRetry()
	m = mm.(Model)
	runBatchLeaves(cmd)
	m = applyAll(m, client.StreamErrMsg{Err: errors.New("server rejected retry")})
	if m.phase != phaseIdle || len(m.queued) != 1 || m.prompt.Value() != "draft" || userBlockCount(m.conv) != users {
		t.Fatalf("rejection mutated state: phase=%v queue=%v input=%q users=%d", m.phase, m.queued, m.prompt.Value(), userBlockCount(m.conv))
	}
	mm, cmd = m.runFailedStepRetry()
	m = mm.(Model)
	runBatchLeaves(cmd)
	if retryFrameCount(conv.send.frames()) != 2 {
		t.Fatalf("manual retry affordance was lost: frames=%#v", conv.send.frames())
	}
}

func TestHistoricalRetryableResultDoesNotAutomaticallyRetry(t *testing.T) {
	m, conv := newQueueModel(t)
	m.liveReconGen = 7
	result := failedStepRetryableResult()
	result.StreamProgress = client.StreamProgressVisible
	mm, cmd := m.updateReconnectMsg(reconnectMsg{gen: 7, msg: result})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if retryFrameCount(conv.send.frames()) != 0 {
		t.Fatalf("historical replay auto-ran: frames=%#v", conv.send.frames())
	}
	mm, cmd = m.runFailedStepRetry()
	m = mm.(Model)
	runBatchLeaves(cmd)
	if retryFrameCount(conv.send.frames()) != 1 {
		t.Fatalf("manual retry after replay frames=%#v", conv.send.frames())
	}
}

func TestModelRetryNoticeRendersInScrollbackAndStatus(t *testing.T) {
	m, _ := newQueueModel(t)
	m = applyAll(m, client.ModelRetryMsg{Text: "The prior partial model output failed and is superseded."})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "model retry") || !strings.Contains(got, "superseded") {
		t.Fatalf("view=%q", got)
	}
	if !strings.Contains(stripANSIstr(m.statusMsg), "retrying failed model step") {
		t.Fatalf("status=%q", stripANSIstr(m.statusMsg))
	}
}

func TestStreamErrorAlwaysPausesQueue(t *testing.T) {
	m, conv := newQueueModel(t)
	m = startRunning(t, m, "first")
	m = enqueue(t, m, "next")
	mm, cmd := m.Update(client.StreamErrMsg{Err: errors.New("deadline exceeded"), Transient: true})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if len(m.queued) != 1 || m.queuePaused != stopError || len(promptTexts(conv.send)) != 1 || retryFrameCount(conv.send.frames()) != 0 {
		t.Fatalf("stream error must pause without replay: queued=%v paused=%q frames=%#v", m.queued, m.queuePaused, conv.send.frames())
	}
}

func TestGenuinePromptAndSessionReplacementResetFailedStepRetryAttempt(t *testing.T) {
	m, _ := newQueueModel(t)
	m.failedStepRetryTried = true
	m = startRunning(t, m, "new prompt")
	if m.failedStepRetryTried {
		t.Fatal("genuine prompt did not reset failed-step retry attempt state")
	}

	m = m.endRun("end_turn")
	m.failedStepRetryTried = true
	mm, _, _ := m.applySessionReady(client.SessionReadyMsg{SessionID: "replacement"})
	if mm.(Model).failedStepRetryTried {
		t.Fatal("session replacement did not reset failed-step retry attempt state")
	}
}
