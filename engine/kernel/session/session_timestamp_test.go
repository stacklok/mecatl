package session

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestSessionRecordTimestampsAndSampling(t *testing.T) {
	h := newSessionHistory(t)
	scope := testSessionID(t)
	calls := 0
	// Include epoch, negative readings and backwards jumps. The commit time is
	// earlier than several records; metadata never imposes temporal ordering.
	readings := []int64{0, -1, 200, 100, math.MinInt64, math.MaxInt64, 23, 24, 255, 256, -10, -20}
	clock := func() time.Time {
		if calls >= len(readings) {
			t.Fatal("unexpected clock sample")
		}
		n := readings[calls]
		calls++
		return time.Unix(0, n)
	}
	s, err := New(h, scope, Config{Policy: sessionPolicy(), InstanceID: 7, Now: clock})
	interactionCheck(t, err)
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	admit := func(add func() error) {
		t.Helper()
		before, count := calls, len(b.records)
		interactionCheck(t, add())
		if calls != before+1 || len(b.records) != count+1 || b.records[count].value.Timestamp != readings[before] {
			t.Fatal("successful admission did not stamp exactly once")
		}
	}
	admit(b.Create)
	if err := b.Create(); !errors.Is(err, ErrTransition) || calls != 1 {
		t.Fatalf("invalid lifecycle sampled clock: %v", err)
	}
	var run RunID
	admit(func() error { var err error; run, err = b.StartRun(); return err })
	admit(func() error { return b.ReplaceRootInstructions(scope, RootInstructions{}) })
	admit(func() error { return b.ReplaceUserInstructions(scope, UserInstructions{}) })
	admit(func() error { return b.ReplaceToolCatalog(scope, ToolCatalog{}) })
	admit(func() error { return b.ReplaceModelBinding(scope, ModelBinding{Route: "r", API: "a", Model: "m"}) })
	admit(func() error { _, err := b.AddUserInput(run, UserInput{}); return err })
	admit(func() error { return b.AddEffectiveInput(run, EffectiveInput{}) })
	capture := captureCommit(t, b)
	if _, err := b.AddModelRequest(run, capture, ModelRequest{}); !errors.Is(err, codec.ErrInvalidData) || calls != 8 {
		t.Fatalf("invalid payload sampled clock: %v", err)
	}
	var request ModelRequestID
	admit(func() error {
		var err error
		request, err = b.AddModelRequest(run, capture, ModelRequest{Recipe: interactionRequest(s, 1).Recipe})
		return err
	})
	var usage UsageID
	admit(func() error {
		var err error
		usage, err = b.AddUsage(nil, &request, UsagePurposeMain, UsageReport{State: UsageReportActive})
		return err
	})
	admit(func() error { return b.ReplaceUsage(usage, UsageReport{State: UsageReportWithdrawn}) })
	before := calls
	proposed := captureCommit(t, b)
	for i := 0; i < proposed.RecordCount(); i++ {
		_ = proposed.RecordAt(i).MutableClone()
	}
	if calls != before {
		t.Fatal("inspection sampled clock")
	}
	ref, err := s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	if calls != before+1 {
		t.Fatalf("submission/reservation sampled %d times", calls-before)
	}
	page, err := h.Read(t.Context(), scope.ResourceID(), history.ReadRequest{Through: ref, MaxCommits: 1})
	interactionCheck(t, err)
	stored := page.Commits[0].Commit
	if stored.Timestamp != -20 || stored.InstanceID != 7 {
		t.Fatalf("commit metadata: %+v", stored)
	}
	for i, r := range stored.Records {
		if r.Timestamp != readings[i] || s.Snapshot().RecordAt(i).MutableClone().Timestamp != readings[i] {
			t.Fatalf("record %d lost timestamp", i)
		}
	}

	loading := true
	newCalls := 0
	loaded, err := Load(t.Context(), h, scope, Config{Policy: sessionPolicy(), InstanceID: 99, Now: func() time.Time {
		if loading {
			t.Fatal("replay sampled clock")
		}
		newCalls++
		return time.Unix(0, -999)
	}})
	interactionCheck(t, err)
	if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
		t.Fatal("loading writer changed historical metadata")
	}
	loading = false
	b, err = loaded.BeginCommit()
	interactionCheck(t, err)
	// Complete every remaining record kind through its public admission path.
	interactionCheck(t, b.AddModelResponse(request, ModelResponse{Terminal: ResponseTerminalNormal, Items: []ResponseItem{{Call: &RequestedCall{Name: "A"}}}}))
	_, err = loaded.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	b, err = loaded.BeginCommit()
	interactionCheck(t, err)
	call := RequestedCallID{requestID: request, ordinal: 1}
	interactionCheck(t, b.AddToolInvocation(call, ToolInvocation{Name: "A"}))
	_, err = loaded.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	b, err = loaded.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.AddToolDisposition(call, toolOutcome("returned")))
	interactionCheck(t, b.EndRun(RunEndedCompleted))
	_, err = b.StartRun()
	interactionCheck(t, err)
	capture = captureCommit(t, b)
	request, err = b.AddModelRequest(b.admission.candidate.activeRun, capture, ModelRequest{Recipe: interactionRequest(loaded, 1).Recipe})
	interactionCheck(t, err)
	_, err = loaded.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	b, err = loaded.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.AddInvocationFailure(request, InvocationFailure{Class: InvocationFailureTransport}))
	ref, err = loaded.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	// Seven newly admitted records plus four first submissions, with no samples
	// for preparation simulations, BeginCommit, capture, or replay.
	if newCalls != 11 {
		t.Fatalf("remaining kinds sampled %d times; want 11", newCalls)
	}
	requestPage := history.ReadRequest{Through: ref, MaxCommits: 1}
	for requestPage.After != ref {
		page, err := h.Read(t.Context(), scope.ResourceID(), requestPage)
		interactionCheck(t, err)
		for _, c := range page.Commits {
			if c.Ref.Sequence == 1 {
				if !c.Commit.Equal(stored) {
					t.Fatal("new writer rewrote old metadata")
				}
				continue
			}
			if c.Commit.Timestamp != -999 || c.Commit.InstanceID != 99 {
				t.Fatal("loaded writer metadata not used")
			}
			for _, r := range c.Commit.Records {
				if r.Timestamp != -999 {
					t.Fatal("record kind bypassed stamping")
				}
			}
		}
		requestPage.After = page.Next
	}
}

func TestSessionValidationFailuresDoNotSample(t *testing.T) {
	s := interactionFixture(t, "bound")
	s.config.Now = func() time.Time { t.Fatal("failed validation sampled clock"); return time.Time{} }
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	copied := *b
	var missing *SessionCommit
	for _, tc := range []struct {
		name string
		add  func() error
		want error
	}{
		{"nil handle", missing.Create, ErrBatch},
		{"copied handle", copied.Create, ErrBatch},
		{"duplicate creation", b.Create, ErrTransition},
		{"active start", func() error { _, err := b.StartRun(); return err }, ErrTransition},
		{"invalid scope", func() error { return b.ReplaceRootInstructions(SessionID{}, RootInstructions{}) }, codec.ErrInvalidData},
		{"synthesized ID", func() error { _, err := b.AddUserInput(s.Snapshot().ActiveRun(), UserInput{ID: 1}); return err }, codec.ErrInvalidData},
		{"payload", func() error { return b.ReplaceModelBinding(s.Snapshot().SessionID(), ModelBinding{}) }, codec.ErrInvalidData},
		{"usage report", func() error { _, err := b.AddUsage(nil, nil, "auxiliary", UsageReport{}); return err }, codec.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := detachedToolAdmission(b.admission)
			if err := tc.add(); !errors.Is(err, tc.want) || len(b.records) != 0 || !reflect.DeepEqual(prior, b.admission) {
				t.Fatalf("failed admission changed batch: %v", err)
			}
		})
	}
	interactionCheck(t, b.Discard())
}

func TestSessionClockFailurePreservesProvisionalSlots(t *testing.T) {
	for _, kind := range []string{"run", "input", "request", "usage", "tool"} {
		t.Run(kind, func(t *testing.T) {
			s := interactionFixture(t, "bound")
			var call RequestedCallID
			switch kind {
			case "run":
				s = interactionFixture(t, "idle")
			case "tool":
				var calls []RequestedCallID
				s, calls = toolAdmissionFixture(t)
				call = calls[0]
			}
			bad := true
			calls := 0
			s.config.Now = func() time.Time {
				calls++
				if bad {
					return time.Unix(0, math.MaxInt64).Add(time.Nanosecond)
				}
				return time.Unix(0, 123)
			}
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			prior := detachedToolAdmission(b.admission)
			pinned := detachedSessionState(s.state)
			capture := captureCommit(t, b)
			add := func() error {
				switch kind {
				case "run":
					id, err := b.StartRun()
					if (bad && id != (RunID{})) || (!bad && id.ordinal != 1) {
						t.Fatal("run ordinal consumed")
					}
					return err
				case "input":
					id, err := b.AddUserInput(s.Snapshot().ActiveRun(), UserInput{})
					if (bad && id != (UserInputID{})) || (!bad && id.ordinal != 1) {
						t.Fatal("input ordinal consumed")
					}
					return err
				case "request":
					id, err := b.AddModelRequest(s.Snapshot().ActiveRun(), capture, ModelRequest{Recipe: interactionRequest(s, 1).Recipe})
					if (bad && id != (ModelRequestID{})) || (!bad && id.requestOrdinal != 1) {
						t.Fatal("request ordinal consumed")
					}
					return err
				case "usage":
					id, err := b.AddUsage(nil, nil, "auxiliary", UsageReport{State: UsageReportActive})
					if (bad && id != (UsageID{})) || (!bad && id.usageOrdinal != 1) {
						t.Fatal("usage ordinal consumed")
					}
					return err
				default:
					return b.AddToolInvocation(call, ToolInvocation{Name: "A"})
				}
			}
			if err := add(); !errors.Is(err, codec.ErrInvalidData) || calls != 1 || len(b.records) != 0 || !reflect.DeepEqual(prior, b.admission) || !reflect.DeepEqual(pinned, detachedSessionState(s.state)) {
				t.Fatalf("clock failure changed admission: %v", err)
			}
			bad = false
			interactionCheck(t, add())
			if calls != 2 || len(b.records) != 1 || b.records[0].value.Timestamp != 123 {
				t.Fatal("valid addition did not reuse provisional slot")
			}
			interactionCheck(t, b.Discard())
		})
	}
}

func TestSessionDeterministicPreparationDoesNotSample(t *testing.T) {
	s := interactionFixture(t, "bound")
	s.config.Now = func() time.Time { t.Fatal("deterministic preparation sampled clock"); return time.Time{} }
	r, err := newRootInstructionsRecord(RootInstructions{}, s.policy.PayloadLimits)
	interactionCheck(t, err)
	r.value.Timestamp = math.MaxInt64
	proposal, err := s.prepare([]*materializedRecord{r}, math.MinInt64, math.MaxUint64)
	interactionCheck(t, err)
	ending, err := s.prepareEnding(RunEndedLimit)
	interactionCheck(t, err)
	if ending.commit.Timestamp != math.MinInt64 || ending.commit.InstanceID != math.MaxUint64 || ending.commit.Records[0].Timestamp != math.MinInt64 {
		t.Fatal("future ending did not reserve maximum-width metadata")
	}
	// Accepted application also decodes the supplied metadata, never this clock.
	interactionCheck(t, s.apply(history.Commit{Ref: proposal.ref, Commit: proposal.commit}))
}

func TestSessionSubmissionClockFailureReleasesSlot(t *testing.T) {
	s := newSession(t, sessionPolicy())
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.Create())
	before := detachedSessionState(s.state)
	calls := 0
	s.config.Now = func() time.Time { calls++; return time.Time{} }
	ref, err := s.SubmitCommit(t.Context(), b)
	if !errors.Is(err, codec.ErrInvalidData) || ref != (history.CommitRef{}) || calls != 1 || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
		t.Fatalf("invalid submission clock: %v", err)
	}
	if s.batch != nil || b.proposal.ref != (history.CommitRef{}) {
		t.Fatal("failed preparation retained slot/proposal")
	}
	s.config = testConfig()
	b, err = s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.Create())
	_, err = s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
}
