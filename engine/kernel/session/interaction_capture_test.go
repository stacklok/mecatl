package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

func TestInteractionOversizeCaptureFallback(t *testing.T) {
	for _, oversized := range []string{"content", "private"} {
		for _, detailedFits := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/detail=%t", oversized, detailedFits), func(t *testing.T) {
				s := interactionFixture(t, "pending")
				pending := s.state.view.lifecycle.pending
				before := detachedSessionState(s.state)
				response := ModelResponse{RequestID: pending, Terminal: ResponseTerminalNormal, Usage: &InvocationUsage{Output: new(int64(0))}}
				if oversized == "content" {
					response.Items = []ResponseItem{{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Text: new(strings.Repeat("x", s.policy.PayloadLimits.MaxStringBytes+1))}}}}}
				} else {
					response.Private = &OpaqueProviderData{Producer: "p", Format: "unknown", Version: 1, Data: bytes.Repeat([]byte{0xff}, s.policy.PayloadLimits.MaxByteStringBytes+1)}
				}
				b, err := s.BeginCommit()
				interactionCheck(t, err)
				if err = b.AddModelResponse(response); !errors.Is(err, codec.ErrLimitExceeded) || len(b.records) != 0 {
					t.Fatalf("whole capture rejection: %v", err)
				}
				interactionCheck(t, b.Discard())
				if !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("capture rejection resolved request")
				}
				ending := RunEnded{RunID: pending.runID, Reason: RunEndedCaptureFailed, Explanation: new("capture exceeded local bounds"), Capture: &CaptureFailureEvidence{RequestID: pending, Evidence: &ObservedEvidence{Correlation: new("observed")}, Usage: response.Usage}}
				if !detailedFits {
					minimal := interactionRecord(t)(newRunEndedRecord(s.Snapshot().HistoryID(), RunEnded{RunID: pending.runID, Reason: RunEndedCaptureFailed}, s.policy.PayloadLimits))
					s.policy.SoftMaxPayloadBytes = s.state.payloadBytes + uint64(len(minimal.value.Payload))
				}
				b, err = s.BeginCommit()
				interactionCheck(t, err)
				interactionCheck(t, b.AddRunEnded(ending))
				_, err = s.SubmitCommit(context.Background(), b)
				if detailedFits {
					interactionCheck(t, err)
				} else {
					if !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, ErrAcceptanceUnknown) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
						t.Fatalf("detail not known-rejected: %v", err)
					}
					// A fresh proposal is legal only after that known local rejection.
					interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedCaptureFailed)) })
				}
				snap := s.Snapshot()
				if snap.RecordCount() != before.records+1 || s.state.view.lifecycle.pending != pending {
					t.Fatal("fallback accepted response subset or resolved pending request")
				}
				v, ok := snap.RecordAt(snap.RecordCount() - 1).RunEnded()
				if !ok {
					t.Fatal("ending missing")
				}
				capture, present := v.Capture()
				if present != detailedFits {
					t.Fatal("capture presence")
				}
				if detailedFits {
					usage, usagePresent := capture.Usage()
					evidence, evidencePresent := capture.Evidence()
					output, outputPresent := usage.Output()
					correlation, correlationPresent := evidence.Correlation()
					if capture.RequestID() != pending || !usagePresent || !evidencePresent || !outputPresent || output != 0 || !correlationPresent || correlation != "observed" {
						t.Fatal("optional evidence lost")
					}
				}
				if _, present := v.Explanation(); present != detailedFits {
					t.Fatal("minimal fallback kept detail")
				}
				for r := range snap.Records() {
					if _, ok := r.ModelResponse(); ok {
						t.Fatal("stripped response accepted")
					}
					if _, ok := r.InvocationFailure(); ok {
						t.Fatal("capture fabricated invocation failure")
					}
				}
				interactionReplay(t, s)
			})
		}
	}
}

func TestInteractionManyCallsDoNotEnlargeMinimalEnding(t *testing.T) {
	var payload []byte
	for _, count := range []int{0, 1, 15, 16} {
		s := interactionFixture(t, "pending")
		pending := s.state.view.lifecycle.pending
		response := ModelResponse{RequestID: pending, Terminal: ResponseTerminalError}
		for range count {
			response.Items = append(response.Items, ResponseItem{Call: &RequestedCall{Arguments: []byte{0xff}}})
		}
		interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddModelResponse(response)) })
		prepared, err := s.prepareEnding(RunEndedInterrupted)
		interactionCheck(t, err)
		got := prepared.commit.Records[0].Payload
		if payload == nil {
			payload = bytes.Clone(got)
		} else if !bytes.Equal(payload, got) {
			t.Fatal("ending encodes call obligations")
		}
		interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedInterrupted)) })
		if s.state.view.lifecycle.calls != uint64(count) {
			t.Fatal("ending erased calls")
		}
		interactionReplay(t, s)
	}
}
