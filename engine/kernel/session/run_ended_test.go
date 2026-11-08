package session

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRunEnded(t *testing.T) {
	v := RunEnded{RunID: allocatedRun(t, testHistoryID(t), 1), Reason: RunEndedInterrupted}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := newRunEndedRecord(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[sessionformat.RunEnded](r.format().Payload, testLimits())
	if err != nil || got.RunID != 1 || !v.Equal(v.Clone()) {
		t.Fatalf("interruption roundtrip: %v", err)
	}
	if (RunEnded{}).Validate() == nil {
		t.Fatal("zero interruption validated")
	}
	r, err = newRunEndedRecord(RunEnded{}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || r != nil {
		t.Fatalf("zero interruption record: %v", err)
	}
}

func TestCaptureFailureEvidenceViewScopePresenceAndOwnership(t *testing.T) {
	s := interactionFixture(t, "pending")
	pending := s.state.view.lifecycle.pending
	input, output := int64(0), int64(1)
	data := []byte{0xff, 0}
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddRunEnded(RunEnded{
			RunID:  pending.runID,
			Reason: RunEndedCaptureFailed,
			Capture: &CaptureFailureEvidence{
				RequestID: pending,
				Evidence:  &ObservedEvidence{Detail: new(""), Correlation: new("correlation")},
				Usage:     &InvocationUsage{Input: &input, Output: &output, Raw: &OpaqueProviderData{Producer: "producer", Format: "format", Version: 2, Data: data}},
			},
		}))
	})
	data[0] = 0
	ending, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).RunEnded()
	if !ok {
		t.Fatal("missing ending")
	}
	capture, ok := ending.Capture()
	if !ok || capture.RequestID() != pending || capture.RequestID().RunID() != ending.RunID() {
		t.Fatal("capture scope")
	}
	evidence, ok := capture.Evidence()
	if !ok {
		t.Fatal("missing evidence")
	}
	if detail, ok := evidence.Detail(); !ok || detail != "" {
		t.Fatal("empty detail presence")
	}
	if correlation, ok := evidence.Correlation(); !ok || correlation != "correlation" {
		t.Fatal("correlation")
	}
	usage, ok := capture.Usage()
	if !ok {
		t.Fatal("missing usage")
	}
	if got, ok := usage.Input(); !ok || got != 0 {
		t.Fatal("input presence")
	}
	if got, ok := usage.Output(); !ok || got != 1 {
		t.Fatal("output")
	}
	if _, ok := usage.CacheRead(); ok {
		t.Fatal("absent cache read")
	}
	raw, ok := usage.Raw()
	if !ok || raw.Producer() != "producer" || raw.Format() != "format" || raw.Version() != 2 || !bytes.Equal(raw.Data(), []byte{0xff, 0}) {
		t.Fatal("raw usage")
	}
	exported := raw.Data()
	exported[0] = 0
	if !bytes.Equal(raw.Data(), []byte{0xff, 0}) {
		t.Fatal("raw byte export aliases")
	}

	interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); interactionCheck(t, err) })
	interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedInterrupted)) })
	absentEnding, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).RunEnded()
	if !ok {
		t.Fatal("missing minimal ending")
	}
	if capture, ok := absentEnding.Capture(); ok || capture.RequestID() != (ModelRequestID{}) {
		t.Fatal("absent capture")
	}

	var zero RunEndedView
	if capture, ok := zero.Capture(); ok || capture.RequestID() != (ModelRequestID{}) {
		t.Fatal("zero capture")
	}
}
