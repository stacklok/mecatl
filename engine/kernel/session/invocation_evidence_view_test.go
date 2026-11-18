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
			report := UsageReport{State: UsageReportActive, Buckets: []UsageBucket{{Counters: UsageCounters{Input: new(int64(0)), Output: new(int64(1)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4))}}}, Raw: &OpaqueProviderData{Producer: "p", Format: "unknown-usage", Version: 19, Data: []byte{0xff, 0}}}
			want := report.Clone()
			explanation := ""
			b := beginBatch(t, s)
			_, err := b.AddUsage(nil, &id, UsagePurposeMain, report)
			mustBatch(t, err)
			if kind == "failure" {
				mustBatch(t, b.AddInvocationFailure(id, InvocationFailure{Class: InvocationFailureLocal, Explanation: &explanation, Evidence: evidence}))
			} else {
				mustBatch(t, b.AddRunEnded(id.runID, RunEnded{Reason: RunEndedCaptureFailed, Explanation: &explanation, Capture: &CaptureFailureEvidence{RequestID: id.RunRelative(), Evidence: evidence}}))
			}
			explanation = "changed"
			*evidence.Detail, *evidence.Correlation = "changed", "changed"
			c := report.Buckets[0].Counters
			for _, p := range []*int64{c.Input, c.Output, c.CacheRead, c.CacheWrite, c.Reasoning} {
				*p = 99
			}
			clear(report.Raw.Data)
			submitBatch(t, s, b)
			pinned := s.Snapshot()
			verify := func(v Snapshot) {
				record := v.RecordAt(pinned.RecordCount() - 1)
				var e ObservedEvidenceView
				if kind == "capture" {
					ending, _ := record.RunEnded()
					if text, ok := ending.Explanation(); !ok || text != "" {
						t.Fatal("ending explanation lost")
					}
					capture, ok := ending.Capture()
					if !ok || capture.RequestID() != id {
						t.Fatal("capture scope lost")
					}
					e, _ = capture.Evidence()
				} else {
					failure, _ := record.InvocationFailure()
					if text, ok := failure.Explanation(); !ok || text != "" {
						t.Fatal("failure explanation lost")
					}
					e, _ = failure.Evidence()
				}
				if text, ok := e.Detail(); !ok || text != "" {
					t.Fatal("empty diagnostic lost")
				}
				if text, ok := e.Correlation(); !ok || text != "safe" {
					t.Fatal("correlation changed")
				}
				usage, ok := v.RecordAt(pinned.RecordCount() - 2).Usage()
				if !ok || !usage.Report().MutableClone().Equal(want) {
					t.Fatal("accounting changed")
				}
				definition, _ := usage.Definition()
				if request, ok := definition.RequestID(); !ok || request != id {
					t.Fatal("usage scope lost")
				}
				counters := usage.Report().Buckets().At(0).Counters()
				for n, get := range []func() (int64, bool){counters.Input, counters.Output, counters.CacheRead, counters.CacheWrite, counters.Reasoning} {
					if got, ok := get(); !ok || got != int64(n) {
						t.Fatal("counter presence changed")
					}
				}
				raw, ok := usage.Report().Raw()
				if !ok || raw.Producer() != "p" || raw.Format() != "unknown-usage" || raw.Version() != 19 || !bytes.Equal(raw.Data().Copy(), want.Raw.Data) {
					t.Fatal("raw usage changed")
				}
				clear(raw.Data().Copy())
			}
			verify(pinned)
			clear(pinned.RecordAt(pinned.RecordCount() - 1).MutableClone().Payload)
			verify(pinned)
			if kind == "failure" {
				interactionSubmit(t, s, func(b *SessionCommit) { mustBatch(t, b.EndRun(RunEndedError)) })
			}
			interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); mustBatch(t, err) })
			verify(pinned)
			loaded, err := Load(t.Context(), s.history, pinned.HistoryID(), s.policy, testConfig())
			mustBatch(t, err)
			verify(loaded.Snapshot())
		})
	}
}

func TestInvocationEvidenceViewsAbsentAndZero(t *testing.T) {
	var evidence ObservedEvidenceView
	var raw OpaqueProviderDataView
	if text, ok := evidence.Detail(); ok || text != "" {
		t.Fatal("zero detail")
	}
	if text, ok := evidence.Correlation(); ok || text != "" {
		t.Fatal("zero correlation")
	}
	if raw.Producer() != "" || raw.Format() != "" || raw.Version() != 0 || raw.Data().Copy() != nil {
		t.Fatal("zero opaque")
	}
	s := interactionFixture(t, "pending")
	interactionSubmit(t, s, func(b *SessionCommit) {
		mustBatch(t, b.AddInvocationFailure(s.state.view.lifecycle.pending, InvocationFailure{Class: InvocationFailureLocal}))
	})
	view, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).InvocationFailure()
	if !ok {
		t.Fatal("missing failure")
	}
	if e, ok := view.Evidence(); ok || e != (ObservedEvidenceView{}) {
		t.Fatal("absent evidence")
	}
}
