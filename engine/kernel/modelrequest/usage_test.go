package modelrequest

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestUsageExcludedFromConstructionAndReconstruction(t *testing.T) {
	for _, sameBatch := range []bool{true, false} {
		t.Run(map[bool]string{true: "same-batch", false: "accepted-prefix"}[sameBatch], func(t *testing.T) {
			f := newConstructionFixture(t)
			defer func() { requireConstruction(t, f.b.Discard()) }()
			requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "question")}}))
			first := f.build(format.ParametersV1{})
			report := session.UsageReport{State: session.UsageReportActive, Raw: &session.OpaqueProviderData{
				Producer: "producer", Format: "format", Version: 1,
				Data: bytes.Repeat([]byte("usage-only"), 1024), // Exceeds the request-output limits below.
			}}
			_, err := f.b.AddUsage(&f.run, nil, "aux/source", report)
			requireConstruction(t, err)
			// The first accepted request's own same-batch source includes raw
			// accounting, but its recipe still commits to the pre-accounting request.
			firstCapture := f.view()
			firstID, err := f.b.AddModelRequest(f.run, firstCapture, session.ModelRequest{Recipe: first.Recipe()})
			requireConstruction(t, err)
			f.submit()
			// Usage deliberately matches the response's replay selector. Only the
			// response's small private payload may enter the logical request.
			private := &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 1, Data: []byte("response-private")}
			requireConstruction(t, f.b.AddModelResponse(firstID, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Private: private}))
			if !sameBatch {
				f.submit()
			}
			parameters := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 1}}}
			limits := testLimits()
			limits.MaxBytes, limits.MaxByteStringBytes = 4096, 128
			baseline, err := Construct(f.view(), parameters, limits)
			requireConstruction(t, err)
			conversation := baseline.Request().MutableClone().Conversation
			if len(conversation) != 2 || conversation[1].Assistant == nil || !conversation[1].Assistant.Private.Equal(*private) {
				t.Fatal("positive control: response private replay missing")
			}
			wantBytes, err := codec.Encode(baseline.Request().MutableClone(), limits)
			requireConstruction(t, err)
			check := func(source Source) {
				t.Helper()
				built, err := Construct(source, parameters, limits)
				requireConstruction(t, err)
				got, err := codec.Encode(built.Request().MutableClone(), limits)
				requireConstruction(t, err)
				if !bytes.Equal(got, wantBytes) || built.Request().CID() != baseline.Request().CID() {
					t.Fatal("accounting changed logical bytes, CID or private replay")
				}
			}
			before := f.view()
			usage, err := f.b.AddUsage(nil, &firstID, session.UsagePurposeMain, report)
			requireConstruction(t, err)
			if f.view().RecordCount() != before.RecordCount()+1 {
				t.Fatal("accounting did not change the source prefix")
			}
			check(f.view())
			f.submit()
			previous := f.s.Snapshot().Head()
			for _, replacement := range []session.UsageReport{
				{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Input: new(int64(3)), Output: new(int64(0))}}}},
				{State: session.UsageReportWithdrawn},
				report,
			} {
				requireConstruction(t, f.b.ReplaceUsage(usage, replacement))
				check(f.view())
				f.submit()
				loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), testConfigFor(f.policy))
				requireConstruction(t, err)
				if loaded.Snapshot().Head() == previous {
					t.Fatal("usage-only commit did not change history anchor")
				}
				previous = loaded.Snapshot().Head()
				check(f.s.Snapshot())
				check(loaded.Snapshot())
				assertReconstructed(t, loaded.Snapshot(), firstID, first.Request())
			}
			// The new accepted commitment anchors after usage-only commits while
			// retaining exactly the pre-accounting logical bytes and recipe.
			capture := f.view()
			secondID, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: baseline.Recipe()})
			requireConstruction(t, err)
			f.submit()
			committed, _ := f.s.Snapshot().RecordAt(f.s.Snapshot().RecordCount() - 1).ModelRequest()
			anchor, ok := committed.Source()
			if !ok || anchor.CID() != previous.CID || anchor.Sequence() != previous.Sequence {
				t.Fatal("request did not retain its changed history anchor")
			}
			requireConstruction(t, f.b.EndRun(session.RunEndedInterrupted))
			f.run, err = f.b.StartRun()
			requireConstruction(t, err)
			f.submit()
			next := f.build(parameters).Request()
			// Both first reports and corrections for A may arrive during B.
			_, err = f.b.AddUsage(nil, &secondID, "late/producer", report)
			requireConstruction(t, err)
			requireConstruction(t, f.b.ReplaceUsage(usage, session.UsageReport{State: session.UsageReportWithdrawn}))
			f.submit()
			loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), testConfigFor(f.policy))
			requireConstruction(t, err)
			for _, snapshot := range []session.Snapshot{f.s.Snapshot(), loaded.Snapshot()} {
				built, err := Construct(snapshot, parameters, limits)
				requireConstruction(t, err)
				if built.Request().CID() != next.CID() || !built.Request().MutableClone().Equal(next.MutableClone()) {
					t.Fatal("late A accounting entered B's request")
				}
				for id, want := range map[session.ModelRequestID]Request{firstID: first.Request(), secondID: baseline.Request()} {
					got, err := Reconstruct(snapshot, id, limits)
					requireConstruction(t, err)
					encoded, err := codec.Encode(got.MutableClone(), limits)
					requireConstruction(t, err)
					wantEncoded, err := codec.Encode(want.MutableClone(), limits)
					requireConstruction(t, err)
					if got.CID() != want.CID() || !bytes.Equal(encoded, wantEncoded) {
						t.Fatal("later accounting changed an earlier accepted request")
					}
				}
			}
		})
	}
}

func TestKnownUsageTokens(t *testing.T) {
	full := func(input, output int64) session.UsageReport {
		return session.UsageReport{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Input: new(input), Output: new(output), CacheRead: new(int64(7)), CacheWrite: new(int64(8)), Reasoning: new(int64(9))}}}}
	}
	for _, tc := range []struct {
		name       string
		reports    []session.UsageReport
		withdraw   bool
		total      int64
		incomplete int
		overflow   bool
	}{
		{name: "absent"},
		{name: "unknown report", reports: []session.UsageReport{{State: session.UsageReportActive}}, incomplete: 1},
		{name: "unknown bucket", reports: []session.UsageReport{{State: session.UsageReportActive, Buckets: []session.UsageBucket{{}}}}, incomplete: 1},
		{name: "partial", reports: []session.UsageReport{{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Output: new(int64(2))}}}}}, total: 2, incomplete: 1},
		{name: "reported zero", reports: []session.UsageReport{full(0, 0)}},
		{name: "inclusive counters", reports: []session.UsageReport{full(10, 3)}, total: 13},
		{name: "withdrawn unknown", reports: []session.UsageReport{{State: session.UsageReportActive}}, withdraw: true},
		{name: "multiple buckets", reports: []session.UsageReport{{State: session.UsageReportActive, Buckets: append(full(10, 3).Buckets, full(2, 1).Buckets...)}}, total: 16},
		{name: "largest subtotal", reports: []session.UsageReport{full(math.MaxInt64, 0)}, total: math.MaxInt64},
		{name: "within bucket overflow", reports: []session.UsageReport{full(math.MaxInt64, 1)}, overflow: true},
		{name: "across reports overflow", reports: []session.UsageReport{full(math.MaxInt64, 0), full(0, 1)}, overflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructionFixture(t)
			defer func() { requireConstruction(t, f.b.Discard()) }()
			for _, report := range tc.reports {
				id, err := f.b.AddUsage(nil, nil, "example/subtotal", report)
				requireConstruction(t, err)
				if tc.withdraw {
					requireConstruction(t, f.b.ReplaceUsage(id, session.UsageReport{State: session.UsageReportWithdrawn}))
				}
			}
			f.submit()
			loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), testConfigFor(f.policy))
			requireConstruction(t, err)
			for _, snapshot := range []session.Snapshot{f.s.Snapshot(), loaded.Snapshot()} {
				total, incomplete, err := knownUsageTokens(snapshot.Usages())
				if tc.overflow {
					if err == nil || total != 0 || incomplete != 0 {
						t.Fatalf("overflow returned usable subtotal: %d, %d, %v", total, incomplete, err)
					}
				} else if err != nil || total != tc.total || incomplete != tc.incomplete {
					t.Fatalf("subtotal = %d, %d, %v; want %d, %d", total, incomplete, err, tc.total, tc.incomplete)
				}
			}
		})
	}
}

func TestUsageSourceFreshnessForModelRequest(t *testing.T) {
	for _, operation := range []string{"add", "replace"} {
		for _, valid := range []bool{true, false} {
			t.Run(operation+map[bool]string{true: "/success", false: "/failure"}[valid], func(t *testing.T) {
				f := newConstructionFixture(t)
				defer func() { requireConstruction(t, f.b.Discard()) }()
				usage, err := f.b.AddUsage(&f.run, nil, "aux", session.UsageReport{State: session.UsageReportActive})
				requireConstruction(t, err)
				capture := f.view()
				built := f.build(format.ParametersV1{})
				report := session.UsageReport{State: session.UsageReportActive}
				if !valid {
					report.Buckets = []session.UsageBucket{{Counters: session.UsageCounters{Input: new(int64(-1))}}}
				}
				if operation == "add" {
					_, err = f.b.AddUsage(&f.run, nil, "aux", report)
				} else {
					err = f.b.ReplaceUsage(usage, report)
				}
				if valid {
					requireConstruction(t, err)
				} else if !errors.Is(err, codec.ErrInvalidData) {
					t.Fatalf("invalid accounting: %v", err)
				}
				id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
				if valid {
					if !errors.Is(err, session.ErrIdentity) || id != (session.ModelRequestID{}) {
						t.Fatalf("successful accounting did not stale source: %v", err)
					}
					fresh := f.view()
					id, err = f.b.AddModelRequest(f.run, fresh, session.ModelRequest{Recipe: built.Recipe()})
				}
				requireConstruction(t, err)
				f.submit()
				loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), testConfigFor(f.policy))
				requireConstruction(t, err)
				assertReconstructed(t, loaded.Snapshot(), id, built.Request())
			})
		}
	}
}
