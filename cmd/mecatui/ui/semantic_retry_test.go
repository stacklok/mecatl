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
