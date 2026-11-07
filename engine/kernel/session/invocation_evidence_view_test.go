package session

import (
	"reflect"
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
				interactionCheck(t, b.AddInvocationFailure(InvocationFailure{RequestID: id, Class: InvocationFailureLocal, Explanation: &explanation, Evidence: evidence, Usage: usage}))
			} else {
				interactionCheck(t, b.AddRunEnded(RunEnded{RunID: id.RunID, Reason: RunEndedCaptureFailed, Explanation: &explanation, Capture: &CaptureFailureEvidence{RequestID: id, Evidence: evidence, Usage: usage}}))
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
			read := func() (ObservedEvidence, InvocationUsage) {
				if kind == "failure" {
					v, _ := record.InvocationFailure()
					detail, ok := v.Explanation()
					if !ok || detail != "" {
						t.Fatal("empty failure explanation lost")
					}
					e, _ := v.Evidence()
					u, _ := v.Usage()
					return e, u
				}
				v, _ := record.RunEnded()
				detail, ok := v.Explanation()
				if !ok || detail != "" {
					t.Fatal("empty ending explanation lost")
				}
				c, _ := v.Capture()
				if c.RequestID != id {
					t.Fatal("capture scope")
				}
				return *c.Evidence, *c.Usage
			}
			exportedEvidence, exportedUsage := read()
			if !exportedEvidence.Equal(wantEvidence) || !exportedUsage.Equal(wantUsage) {
				t.Fatal("input mutation reached accepted evidence")
			}
			*exportedEvidence.Detail = "changed"
			*exportedEvidence.Correlation = "changed"
			for _, p := range []*int64{exportedUsage.Input, exportedUsage.Output, exportedUsage.CacheRead, exportedUsage.CacheWrite, exportedUsage.Reasoning} {
				*p = 99
			}
			clear(exportedUsage.Raw.Data)
			dto := record.Format()
			clear(dto.Payload)
			e, u := read()
			if !e.Equal(wantEvidence) || !u.Equal(wantUsage) {
				t.Fatal("export mutation reached accepted evidence")
			}
			before := s.Snapshot()
			if kind == "failure" {
				interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedError)) })
			}
			interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); interactionCheck(t, err) })
			e, u = read()
			if !reflect.DeepEqual(e, wantEvidence) || !u.Equal(wantUsage) || before.RecordAt(before.RecordCount()-1) != record {
				t.Fatal("successor changed pinned evidence")
			}
			interactionReplay(t, s)
		})
	}
}
