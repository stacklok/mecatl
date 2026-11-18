package session

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func usageReportFixture() UsageReport {
	return UsageReport{State: UsageReportActive, Buckets: []UsageBucket{
		{SelectedRoute: new(" Route "), SelectedModel: new("Model"), Counters: UsageCounters{Input: new(int64(0)), Output: new(int64(9)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4))}},
		{SelectedRoute: new(" Route "), Counters: UsageCounters{}},
	}, Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff, 1, 2}}}
}

func TestUsageAuthoringPrefixAndReplacements(t *testing.T) {
	s, _ := internalFixture(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	sessionID, err := b.AddUsage(nil, nil, " Session Work ", UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	run := batchRun(t, b)
	input := batchInput(t, b, run, []byte("input"))
	mustBatch(t, b.AddEffectiveInput(run, EffectiveInput{Inputs: []InputItem{{UserInputID: new(input.RunRelative())}}}))
	mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), ModelBinding{Route: "r", API: "a", Model: "m"}))
	request, err := b.AddModelRequest(run, captureCommit(t, b), ModelRequest{Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}})
	mustBatch(t, err)
	execution := b.admission.candidate
	report := usageReportFixture()
	wantFirst := report.Clone()
	requestUsage, err := b.AddUsage(nil, &request, UsagePurposeMain, report)
	mustBatch(t, err)
	captured := captureCommit(t, b)
	pinned, ok := captured.RecordAt(captured.RecordCount() - 1).Usage()
	if !ok {
		t.Fatal("proposed usage missing")
	}
	runUsage, err := b.AddUsage(&run, nil, "aux/future", UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	reduced := UsageReport{State: UsageReportActive, Buckets: []UsageBucket{{Counters: UsageCounters{Output: new(int64(1))}}}}
	mustBatch(t, b.ReplaceUsage(requestUsage, reduced))
	mustBatch(t, b.ReplaceUsage(requestUsage, reduced)) // Duplicate reports are replacements, not deduplication errors.
	withdrawn := UsageReport{State: UsageReportWithdrawn}
	mustBatch(t, b.ReplaceUsage(requestUsage, withdrawn))
	mustBatch(t, b.ReplaceUsage(requestUsage, UsageReport{State: UsageReportActive}))
	mustBatch(t, b.ReplaceUsage(requestUsage, wantFirst)) // Re-appending an older observation is legal.
	after := b.admission.candidate
	after.usages = execution.usages
	if after != execution {
		t.Fatal("accounting changed execution admission")
	}
	if err := b.AddModelResponse(request, ModelResponse{Terminal: ResponseTerminalNormal}); !errors.Is(err, ErrTransition) {
		t.Fatalf("same-batch response barrier bypassed: %v", err)
	}
	if sessionID.HistoryRelative() != 1 || requestUsage.HistoryRelative() != 2 || runUsage.HistoryRelative() != 3 {
		t.Fatal("allocation is not history-wide")
	}
	*report.Buckets[0].Counters.Output = 99
	*report.Buckets[0].SelectedRoute = "changed"
	clear(report.Raw.Data)
	if !pinned.Report().MutableClone().Equal(wantFirst) || captured.RecordCount() != 8 {
		t.Fatal("caller mutation or later additions changed captured proposal")
	}
	submitBatch(t, s, b)
	if s.state.view.lifecycle.pending != request {
		t.Fatal("usage resolved pending work")
	}
	old := s.Snapshot()
	interactionReplay(t, s)

	// End A without an outcome; B is active when A's first late report arrives.
	b = beginBatch(t, s)
	mustBatch(t, b.EndRun(RunEndedInterrupted))
	next := batchRun(t, b)
	late, err := b.AddUsage(nil, &request, "late/first", UsageReport{State: UsageReportActive, Raw: wantFirst.Raw})
	mustBatch(t, err)
	_, err = b.AddUsage(&run, nil, "late/run", UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	mustBatch(t, b.ReplaceUsage(requestUsage, reduced))
	submitBatch(t, s, b)
	if late.HistoryRelative() != 4 || s.Snapshot().ActiveRun() != next {
		t.Fatal("late accounting attached to current run or reset allocation")
	}
	interactionReplay(t, s)
	loaded, err := Load(t.Context(), s.history, run.HistoryID(), s.policy)
	mustBatch(t, err)
	for _, snapshot := range []Snapshot{s.Snapshot(), loaded.Snapshot()} {
		var reports []UsageReport
		definitions := 0
		for record := range snapshot.Records() {
			usage, ok := record.Usage()
			if !ok {
				continue
			}
			if usage.ID().HistoryID() != run.HistoryID() {
				t.Fatal("usage scope lost")
			}
			if d, present := usage.Definition(); present {
				definitions++
				if usage.ID() == sessionID {
					if _, ok := d.RunID(); ok {
						t.Fatal("session work invented a run")
					}
				}
				if usage.ID() == runUsage {
					if r, ok := d.RunID(); !ok || r != run {
						t.Fatal("run-only scope lost")
					}
					if _, ok := d.RequestID(); ok {
						t.Fatal("run work invented a request")
					}
				}
				if usage.ID() == requestUsage || usage.ID() == late {
					if r, ok := d.RequestID(); !ok || r != request {
						t.Fatal("request correlation lost")
					}
				}
			}
			if usage.ID() == requestUsage {
				reports = append(reports, usage.Report().MutableClone())
			}
		}
		want := []UsageReport{wantFirst, reduced, reduced, withdrawn, {State: UsageReportActive}, wantFirst, reduced}
		if len(reports) != len(want) || definitions != 5 {
			t.Fatalf("got %d reports and %d definitions; want %d and 5", len(reports), definitions, len(want))
		}
		for i := range want {
			if !reports[i].Equal(want[i]) {
				t.Fatalf("chronological complete report %d changed", i)
			}
		}
	}
	if old.RecordCount() != 14 || !pinned.Report().MutableClone().Equal(wantFirst) {
		t.Fatal("accepted appends changed pinned history")
	}
	clone := pinned.MutableClone()
	*clone.Definition.RequestID = 99
	*clone.Report.Buckets[0].Counters.Input = 99
	clear(clone.Report.Raw.Data)
	raw, _ := pinned.Report().Raw()
	clear(raw.Data().Copy())
	if !pinned.Report().MutableClone().Equal(wantFirst) {
		t.Fatal("clone or leaf Copy aliased retained storage")
	}
}

func TestUsageHasNoExecutionEffects(t *testing.T) {
	for _, stage := range []string{"idle", "unbound", "active", "pending", "calls", "ended"} {
		t.Run(stage, func(t *testing.T) {
			s := interactionFixture(t, stage)
			before := s.state.view.lifecycle
			b := beginBatch(t, s)
			id, err := b.AddUsage(nil, nil, UsagePurposeMain, usageReportFixture())
			mustBatch(t, err)
			mustBatch(t, b.ReplaceUsage(id, UsageReport{State: UsageReportWithdrawn}))
			mustBatch(t, b.ReplaceUsage(id, UsageReport{State: UsageReportActive}))
			submitBatch(t, s, b)
			after := s.state.view.lifecycle
			after.usages = before.usages
			if before != after {
				t.Fatal("usage changed lifecycle, input coverage, pending request or tools")
			}
			interactionReplay(t, s)
		})
	}
}

func TestUsageInvalidAuthoringAndAllocation(t *testing.T) {
	s, _ := internalFixture(t)
	b := beginBatch(t, s)
	if id, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive}); id != (UsageID{}) || !errors.Is(err, ErrTransition) {
		t.Fatalf("before creation: %v", err)
	}
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	foreign := RunID{historyID: historyid.NewHistoryID(), ordinal: 1}
	foreignRequest := requestID(foreign.historyID, 1, 1)
	validRequest := requestID(run.historyID, 1, 1)
	missingRun := RunID{historyID: run.historyID, ordinal: 2}
	malformed := usageReportFixture()
	malformed.Raw.Data = make([]byte, s.policy.PayloadLimits.MaxByteStringBytes+1)
	for _, tc := range []struct {
		name    string
		run     *RunID
		request *ModelRequestID
		want    error
	}{
		{"both", &run, &validRequest, codec.ErrInvalidData},
		{"zero run", new(RunID), nil, codec.ErrInvalidData},
		{"malformed run", &RunID{historyID: run.historyID}, nil, codec.ErrInvalidData},
		{"unscoped run", &RunID{ordinal: 1}, nil, codec.ErrInvalidData},
		{"zero request", nil, new(ModelRequestID), codec.ErrInvalidData},
		{"malformed request", nil, &ModelRequestID{runID: run}, codec.ErrInvalidData},
		{"foreign run", &foreign, nil, ErrIdentity},
		{"foreign request", nil, &foreignRequest, ErrIdentity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := b.admission
			count := len(b.records)
			id, err := b.AddUsage(tc.run, tc.request, "", malformed)
			if id != (UsageID{}) || !errors.Is(err, tc.want) || b.admission != before || len(b.records) != count {
				t.Fatalf("identity-before-payload/atomicity: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		run     *RunID
		request *ModelRequestID
	}{{&missingRun, nil}, {nil, &validRequest}} {
		before := b.admission
		if id, err := b.AddUsage(tc.run, tc.request, UsagePurposeMain, UsageReport{State: UsageReportActive}); id != (UsageID{}) || !errors.Is(err, ErrTransition) || b.admission != before {
			t.Fatalf("missing correlation: %v", err)
		}
	}
	for _, report := range []UsageReport{{}, {State: UsageReportWithdrawn}, {State: UsageReportActive, Buckets: []UsageBucket{{Counters: UsageCounters{Input: new(int64(-1))}}}}} {
		if id, err := b.AddUsage(nil, nil, UsagePurposeMain, report); id != (UsageID{}) || !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid first report: %v", err)
		}
	}
	for _, purpose := range []string{"", "\xff"} {
		if _, err := b.AddUsage(nil, nil, purpose, UsageReport{State: UsageReportActive}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid purpose: %v", err)
		}
	}
	id, err := b.AddUsage(nil, nil, UsagePurposeMain, usageReportFixture())
	mustBatch(t, err)
	if id.HistoryRelative() != 1 {
		t.Fatal("failed additions consumed identity")
	}
	before := b.admission
	for _, tc := range []struct {
		id     UsageID
		report UsageReport
		want   error
	}{
		{UsageID{}, malformed, codec.ErrInvalidData},
		{UsageID{historyID: foreign.historyID, usageOrdinal: 1}, malformed, ErrIdentity},
		{UsageID{historyID: run.historyID, usageOrdinal: 2}, UsageReport{State: UsageReportActive}, ErrTransition},
		{id, UsageReport{}, codec.ErrInvalidData},
		{id, malformed, codec.ErrLimitExceeded},
	} {
		if err := b.ReplaceUsage(tc.id, tc.report); !errors.Is(err, tc.want) || b.admission != before {
			t.Fatalf("invalid replacement: %v", err)
		}
	}
	mustBatch(t, b.Discard())
	b = beginBatch(t, s)
	mustBatch(t, b.Create())
	again, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	if again != id {
		t.Fatal("discard consumed identity")
	}
	submitBatch(t, s, b)
	interactionReplay(t, s)
}

func TestUsageHandlePrecedesIdentityAndPayload(t *testing.T) {
	s := interactionFixture(t, "idle")
	b := beginBatch(t, s)
	copied := *b
	for _, invalid := range []*SessionCommit{nil, &copied} {
		if id, err := invalid.AddUsage(new(RunID), nil, "", UsageReport{}); id != (UsageID{}) || !errors.Is(err, ErrBatch) {
			t.Fatalf("invalid creation handle: %v", err)
		}
		if err := invalid.ReplaceUsage(UsageID{}, UsageReport{}); !errors.Is(err, ErrBatch) {
			t.Fatalf("invalid replacement handle: %v", err)
		}
	}
	id, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	submitBatch(t, s, b)
	if _, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive}); !errors.Is(err, ErrBatch) {
		t.Fatalf("completed creation handle: %v", err)
	}
	if err := b.ReplaceUsage(id, UsageReport{State: UsageReportWithdrawn}); !errors.Is(err, ErrBatch) {
		t.Fatalf("completed replacement handle: %v", err)
	}
}

func TestUsageIdentityExhaustion(t *testing.T) {
	s := interactionFixture(t, "idle")
	b := beginBatch(t, s)
	b.admission.candidate.usages = math.MaxUint64 - 1
	last, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	if last.HistoryRelative() != math.MaxUint64 {
		t.Fatal("last identity")
	}
	before := b.admission
	if id, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive}); id != (UsageID{}) || !errors.Is(err, codec.ErrLimitExceeded) || b.admission != before {
		t.Fatalf("exhaustion consumed state: %v", err)
	}
	mustBatch(t, b.ReplaceUsage(last, UsageReport{State: UsageReportWithdrawn}))
	// A replacement requires no prefix scan, even at the largest internal ID.
	if err := b.admission.admitUsage(sessionformat.Usage{ID: math.MaxUint64, Report: UsageReport{State: UsageReportActive}}, nil); err != nil {
		t.Fatal(err)
	}
	mustBatch(t, b.Discard())
	if s.state.view.lifecycle.usages != 0 {
		t.Fatal("discard published exhausted allocation")
	}
	b = beginBatch(t, s)
	id, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
	mustBatch(t, err)
	if id.HistoryRelative() != 1 {
		t.Fatal("discarded IDs consumed allocation")
	}
	submitBatch(t, s, b)
	interactionReplay(t, s)
}

func TestUsageSharedAdmissionRejectsTamperedReplay(t *testing.T) {
	for _, bad := range []string{"redefinition", "definition gap", "unknown replacement", "future run", "future request", "wrong run request"} {
		for _, path := range []string{"builder", "preflight", "apply", "load"} {
			t.Run(bad+"/"+path, func(t *testing.T) {
				s := interactionFixture(t, "ended")
				b := beginBatch(t, s)
				_, err := b.StartRun()
				mustBatch(t, err) // Run 2 exists, but has no requests.
				_, err = b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
				mustBatch(t, err)
				submitBatch(t, s, b)
				definition := &UsageDefinition{Purpose: UsagePurposeMain}
				invalid := sessionformat.Usage{ID: 3, Definition: definition, Report: UsageReport{State: UsageReportActive}}
				switch bad {
				case "redefinition":
					invalid.ID = 1
				case "definition gap":
					invalid.ID = 4
				case "unknown replacement":
					invalid.ID = 4
					invalid.Definition = nil
				case "future run":
					definition.RunID = new(uint64(3))
				case "future request":
					definition.RunID = new(uint64(1))
					definition.RequestID = new(uint64(2))
				case "wrong run request":
					definition.RunID = new(uint64(2))
					definition.RequestID = new(uint64(1))
				}
				// Valid earlier changes must not publish if a later fact rejects.
				records := []*materializedRecord{
					interactionRecord(t)(newUsageRecord(sessionformat.Usage{ID: 1, Report: UsageReport{State: UsageReportWithdrawn}}, s.policy.PayloadLimits)),
					interactionRecord(t)(newUsageRecord(sessionformat.Usage{ID: 2, Definition: &UsageDefinition{Purpose: "new"}, Report: UsageReport{State: UsageReportActive}}, s.policy.PayloadLimits)),
					interactionRecord(t)(newUsageRecord(invalid, s.policy.PayloadLimits)),
				}
				before := detachedSessionState(s.state)
				switch path {
				case "builder":
					b = beginBatch(t, s)
					mustBatch(t, b.add(records[0], nil))
					mustBatch(t, b.add(records[1], nil))
					admission := b.admission
					err = b.add(records[2], nil)
					if b.admission != admission || len(b.records) != 2 {
						t.Fatal("rejected addition changed staged prefix")
					}
					mustBatch(t, b.Discard())
				case "preflight":
					_, err = s.prepare(records)
				case "apply":
					err = s.apply(interactionCommit(t, s, records))
				case "load":
					forged := interactionCommit(t, s, records)
					result, e := s.history.Append(t.Context(), s.Snapshot().HistoryID(), forged.Commit)
					mustBatch(t, e)
					if result.Status != history.StatusAccepted {
						t.Fatal("History rejected opaque session records")
					}
					var loaded *Session
					loaded, err = Load(t.Context(), s.history, s.Snapshot().HistoryID(), s.policy)
					if loaded != nil {
						t.Fatal("failed replay published partial aggregate")
					}
				}
				if !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatalf("shared rejection/atomicity: %v", err)
				}
			})
		}
	}
}

func TestUsageRejectsForwardSameBatchCorrelation(t *testing.T) {
	for _, correlation := range []string{"run", "request"} {
		s := interactionFixture(t, "idle")
		scope := s.Snapshot().HistoryID()
		run := RunID{historyID: scope, ordinal: 1}
		definition := &UsageDefinition{Purpose: UsagePurposeMain, RunID: new(uint64(1))}
		start := interactionRecord(t)(newRunStartedRecord(RunStarted{ID: 1}, s.policy.PayloadLimits))
		records := []*materializedRecord{}
		if correlation == "request" {
			definition.RequestID = new(uint64(1))
			records = append(records, start, interactionRecord(t)(newModelBindingRecord(ModelBinding{Route: "r", API: "a", Model: "m"}, s.policy.PayloadLimits)))
		}
		records = append(records, interactionRecord(t)(newUsageRecord(sessionformat.Usage{ID: 1, Definition: definition, Report: UsageReport{State: UsageReportActive}}, s.policy.PayloadLimits)))
		if correlation == "run" {
			records = append(records, start)
		} else {
			request := interactionRequest(s, 1)
			request.ID.runID = run
			records = append(records, interactionRecord(t)(makeRequestRecord(request, s.policy.PayloadLimits)))
		}
		if _, err := s.prepare(records); !errors.Is(err, ErrTransition) {
			t.Fatalf("forward %s preflight: %v", correlation, err)
		}
		forged := interactionCommit(t, s, records)
		_, err := s.history.Append(t.Context(), scope, forged.Commit)
		mustBatch(t, err)
		if loaded, err := Load(t.Context(), s.history, scope, s.policy); loaded != nil || !errors.Is(err, ErrTransition) {
			t.Fatalf("forward %s replay: %v", correlation, err)
		}
	}
}

func TestUsageSizeBeforeAllocation(t *testing.T) {
	s, _ := internalFixture(t)
	for _, v := range []sessionformat.Usage{
		{ID: 1, Definition: &UsageDefinition{Purpose: strings.Repeat("p", s.policy.PayloadLimits.MaxStringBytes+1)}, Report: UsageReport{State: UsageReportActive}},
		{ID: 1, Report: UsageReport{State: UsageReportActive, Buckets: make([]UsageBucket, s.policy.PayloadLimits.MaxArrayElements+1)}},
		{ID: 1, Report: UsageReport{State: UsageReportActive, Buckets: []UsageBucket{{SelectedModel: new(strings.Repeat("m", s.policy.PayloadLimits.MaxStringBytes+1))}}}},
		{ID: 1, Report: UsageReport{State: UsageReportActive, Raw: &OpaqueProviderData{Data: make([]byte, s.policy.PayloadLimits.MaxByteStringBytes+1)}}},
	} {
		if r, err := newUsageRecord(v, s.policy.PayloadLimits); r != nil || !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatalf("sizing: %v", err)
		}
		if n := testing.AllocsPerRun(20, func() { _, _ = newUsageRecord(v, s.policy.PayloadLimits) }); n != 0 {
			t.Fatalf("rejected accounting allocated %g", n)
		}
	}
}
