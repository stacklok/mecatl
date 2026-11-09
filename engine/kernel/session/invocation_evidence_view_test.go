package session

import (
	"bytes"
	"testing"
)

func TestInvocationEvidenceViewsOwnEveryMutableField(t *testing.T) {
	for _, kind := range []string{"failure", "capture"} {
		t.Run(kind, func(t *testing.T) {
			s := interactionFixture(t, "pending")
			id := s.state.view.lifecycle.pending
			evidence := &ObservedEvidence{Detail: new(""), Correlation: new("safe")}
			usage := &InvocationUsage{Input: new(int64(0)), Output: new(int64(1)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4)), Raw: &OpaqueProviderData{Producer: "p", Format: "unknown-usage", Version: 19, Data: []byte{0xff, 0}}}
			wantEvidence, wantUsage := evidence.Clone(), usage.Clone()
			explanation := ""
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			if kind == "failure" {
				interactionCheck(t, b.AddInvocationFailure(id, InvocationFailure{Class: InvocationFailureLocal, Explanation: &explanation, Evidence: evidence, Usage: usage}))
			} else {
				interactionCheck(t, b.AddRunEnded(id.runID, RunEnded{Reason: RunEndedCaptureFailed, Explanation: &explanation, Capture: &CaptureFailureEvidence{RequestID: id.RunRelative(), Evidence: evidence, Usage: usage}}))
			}
			explanation = "changed"
			*evidence.Detail = "changed"
			*evidence.Correlation = "changed"
			for _, p := range []*int64{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning} {
				*p = 99
			}
			usage.Raw.Data[0] = 0
			if usage.Equal(wantUsage) || evidence.Equal(wantEvidence) {
				t.Fatal("mutation control failed")
			}
			_, err = s.SubmitCommit(t.Context(), b)
			interactionCheck(t, err)
			record := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1)
			verify := func() {
				if kind == "capture" {
					v, _ := record.RunEnded()
					if detail, ok := v.Explanation(); !ok || detail != "" {
						t.Fatal("empty ending explanation lost")
					}
					capture, _ := v.Capture()
					if capture.RequestID() != id {
						t.Fatal("capture request identity changed")
					}
					captureEvidence, evidencePresent := capture.Evidence()
					captureUsage, usagePresent := capture.Usage()
					detail, detailPresent := captureEvidence.Detail()
					correlation, correlationPresent := captureEvidence.Correlation()
					input, inputPresent := captureUsage.Input()
					output, outputPresent := captureUsage.Output()
					cacheRead, cacheReadPresent := captureUsage.CacheRead()
					cacheWrite, cacheWritePresent := captureUsage.CacheWrite()
					reasoning, reasoningPresent := captureUsage.Reasoning()
					if !evidencePresent || !usagePresent || !detailPresent || detail != "" || !correlationPresent || correlation != "safe" || !inputPresent || input != 0 || !outputPresent || output != 1 || !cacheReadPresent || cacheRead != 2 || !cacheWritePresent || cacheWrite != 3 || !reasoningPresent || reasoning != 4 {
						t.Fatal("capture evidence changed")
					}
					return
				}
				v, _ := record.InvocationFailure()
				if detail, ok := v.Explanation(); !ok || detail != "" {
					t.Fatal("empty failure explanation lost")
				}
				e, ok := v.Evidence()
				if !ok {
					t.Fatal("missing evidence")
				}
				if detail, ok := e.Detail(); !ok || detail != "" {
					t.Fatal("empty evidence detail lost")
				}
				if correlation, ok := e.Correlation(); !ok || correlation != "safe" {
					t.Fatal("evidence correlation changed")
				}
				u, ok := v.Usage()
				if !ok {
					t.Fatal("missing usage")
				}
				for _, counter := range []struct {
					get  func() (int64, bool)
					want int64
				}{
					{u.Input, 0}, {u.Output, 1}, {u.CacheRead, 2}, {u.CacheWrite, 3}, {u.Reasoning, 4},
				} {
					if got, ok := counter.get(); !ok || got != counter.want {
						t.Fatalf("usage counter = %d, %t", got, ok)
					}
				}
				raw, ok := u.Raw()
				if !ok || raw.Producer() != "p" || raw.Format() != "unknown-usage" || raw.Version() != 19 || !bytes.Equal(raw.Data().Copy(), wantUsage.Raw.Data) {
					t.Fatal("raw evidence changed")
				}
				data := raw.Data().Copy()
				clear(data)
			}
			verify()
			dto := record.Format()
			clear(dto.Payload)
			verify()
			before := s.Snapshot()
			if kind == "failure" {
				interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedError)) })
			}
			interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); interactionCheck(t, err) })
			verify()
			if before.RecordAt(before.RecordCount()-1) != record {
				t.Fatal("successor changed pinned evidence")
			}
			interactionReplay(t, s)
		})
	}
}

func TestInvocationEvidenceViewsAbsentAndZero(t *testing.T) {
	var usage InvocationUsageView
	var evidence ObservedEvidenceView
	var raw OpaqueProviderDataView
	for _, counter := range []func() (int64, bool){usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning} {
		if value, ok := counter(); ok || value != 0 {
			t.Fatal("zero usage counter")
		}
	}
	if value, ok := evidence.Detail(); ok || value != "" {
		t.Fatal("zero evidence detail")
	}
	if value, ok := evidence.Correlation(); ok || value != "" {
		t.Fatal("zero evidence correlation")
	}
	if value, ok := usage.Raw(); ok || value != (OpaqueProviderDataView{}) {
		t.Fatal("zero raw")
	}
	if raw.Producer() != "" || raw.Format() != "" || raw.Version() != 0 || raw.Data().Copy() != nil {
		t.Fatal("zero provider data")
	}
	s := interactionFixture(t, "pending")
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddInvocationFailure(s.state.view.lifecycle.pending, InvocationFailure{Class: InvocationFailureLocal}))
	})
	view, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).InvocationFailure()
	if !ok {
		t.Fatal("failure view")
	}
	if value, ok := view.Evidence(); ok || value != (ObservedEvidenceView{}) {
		t.Fatal("absent evidence")
	}
	if value, ok := view.Usage(); ok || value != (InvocationUsageView{}) {
		t.Fatal("absent usage")
	}
}
