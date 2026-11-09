package session_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// batchBackend records real byte proposals and fails once, before or after acceptance.
type batchBackend struct {
	history.Backend
	attempts []batchAttempt
	fail     error
	accept   bool
	cancel   context.CancelFunc
}
type batchAttempt struct {
	id       history.HistoryID
	expected history.CommitRef
	commit   history.ByteCommit
}

func (b *batchBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
	b.attempts = append(b.attempts, batchAttempt{id, expected, history.ByteCommit{Ref: c.Ref, Bytes: bytes.Clone(c.Bytes)}})
	fault := b.fail
	b.fail = nil
	if fault != nil && !b.accept {
		return history.Observation{}, fault
	}
	o, err := b.Backend.Append(ctx, id, expected, c)
	if err != nil {
		return history.Observation{}, err
	}
	if fault != nil {
		if b.cancel != nil {
			b.cancel()
		}
		return history.Observation{}, fault
	}
	return o, nil
}
func boundSession(t testing.TB, h *history.History, p session.Policy) *session.Session {
	t.Helper()
	s, err := session.New(h, testHistoryID(t), p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func beginBatch(t testing.TB, s *session.Session) *session.SessionCommit {
	t.Helper()
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func mustBatch(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func batchInput(t testing.TB, b *session.SessionCommit, run session.RunID, content []byte) session.UserInputID {
	t.Helper()
	id, err := b.AddUserInput(run, session.UserInput{Parts: byteParts(content)})
	mustBatch(t, err)
	return id
}
func batchRun(t testing.TB, b *session.SessionCommit) session.RunID {
	t.Helper()
	id, err := b.StartRun()
	mustBatch(t, err)
	return id
}
func submitBatch(t testing.TB, s *session.Session, b *session.SessionCommit) history.CommitRef {
	t.Helper()
	ref, err := s.SubmitCommit(context.Background(), b)
	if err != nil || ref == (history.CommitRef{}) {
		t.Fatalf("SubmitCommit = %v, %v", ref, err)
	}
	return ref
}

func TestSessionCommitHappyPathOwnershipAndNumbering(t *testing.T) {
	backend := &batchBackend{Backend: mem.New()}
	h := newLifecycleHistory(t, backend)
	policy := sessionPolicy()
	s := boundSession(t, h, policy)
	policy.MaxRecordsPerCommit = 1
	b := beginBatch(t, s)
	defer b.Discard()
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	content := []byte{0xff, 0, 'x'}
	input := batchInput(t, b, run, content)
	clear(content)
	if input != allocatedInput(t, run, 1) || run != allocatedRun(t, testHistoryID(t), 1) {
		t.Fatal("provisional numbering/scope")
	}
	if s.Snapshot().Created() || s.Snapshot().RecordCount() != 0 {
		t.Fatal("provisional publication")
	}
	root := submitBatch(t, s, b)
	v := s.Snapshot()
	if v.Head() != root || v.ActiveRun() != run || !bytes.Equal(inputBytes(lookupInput(t, v, run, 1)), []byte{0xff, 0, 'x'}) || lookupInput(t, v, run, 1).ID() != input {
		t.Fatal("accepted facts")
	}
	page, err := h.Read(context.Background(), testHistoryID(t), history.ReadRequest{Through: root, MaxCommits: 1})
	mustBatch(t, err)
	fact := page.Commits[0].Commit.Records[2]
	if !fact.Equal(inputRecord(t, 1).Format()) {
		t.Fatal("stored input aliases caller")
	}
	clear(inputBytes(lookupInput(t, v, run, 1)))
	clear(page.Commits[0].Commit.Records[2].Payload)
	next := beginBatch(t, s)
	mustBatch(t, b.Discard())
	if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("stale cleanup released slot")
	}
	mustBatch(t, next.EndRun(session.RunEndedInterrupted))
	secondRun := batchRun(t, next)
	second := batchInput(t, next, secondRun, []byte("second"))
	if second != allocatedInput(t, secondRun, 1) || secondRun != allocatedRun(t, testHistoryID(t), 2) {
		t.Fatal("run-relative numbering")
	}
	submitBatch(t, s, next)
	if got := s.Snapshot(); got.RunCount() != 2 || got.ActiveRun() != secondRun || !bytes.Equal(inputBytes(lookupInput(t, got, run, 1)), []byte{0xff, 0, 'x'}) {
		t.Fatal("old content or successor lost")
	}
	reuse := beginBatch(t, s)
	mustBatch(t, reuse.EndRun(session.RunEndedInterrupted))
	third := batchRun(t, reuse)
	if _, err := reuse.AddUserInput(run, session.UserInput{}); !errors.Is(err, session.ErrTransition) {
		t.Fatal("closed owner accepted")
	}
	if id := batchInput(t, reuse, third, nil); id != allocatedInput(t, third, 1) {
		t.Fatal("failed owner consumed ID")
	}
	submitBatch(t, s, reuse)
}

func TestSessionCommitOrderedProvisionalInterruption(t *testing.T) {
	backend := &batchBackend{Backend: mem.New()}
	s := boundSession(t, newLifecycleHistory(t, backend), sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	first := batchRun(t, b)
	batchInput(t, b, first, []byte("first"))
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	if err := b.EndRun(session.RunEndedInterrupted); !errors.Is(err, session.ErrTransition) {
		t.Fatal("idle interruption added")
	}
	run := batchRun(t, b)
	second := batchInput(t, b, run, []byte("second"))
	if run != allocatedRun(t, testHistoryID(t), 2) || second != allocatedInput(t, run, 1) {
		t.Fatal("same-batch numbering")
	}
	submitBatch(t, s, b)
	if got := s.Snapshot(); got.ActiveRun() != run || got.RunCount() != 2 || !userInputsMatch(got, got.RunAt(0).ID(), 1) || !userInputsMatch(got, run, 1) || len(backend.attempts) != 1 {
		t.Fatal("ordered interruption")
	}
}

func TestSessionCommitRecordAdmissionFailureIsAtomic(t *testing.T) {
	p := sessionPolicy()
	p.MaxRecordsPerCommit = 2
	backend := &batchBackend{Backend: mem.New()}
	s := boundSession(t, newLifecycleHistory(t, backend), p)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	batchInput(t, b, run, nil)
	before := captureRejectedSnapshot(s.Snapshot())
	if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("submission count rejection: %v, %v", ref, err)
	}
	if len(backend.attempts) != 0 || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
		t.Fatal("rejected aggregate appended or published")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.Create())
	if got := batchRun(t, b); got != run {
		t.Fatal("rejected batch consumed run ordinal")
	}
	submitBatch(t, s, b)
	next := beginBatch(t, s)
	if id := batchInput(t, next, run, nil); id != allocatedInput(t, run, 1) {
		t.Fatal("rejected batch consumed input ordinal")
	}
	submitBatch(t, s, next)
}

func TestSessionCommitHandlesDiscardAndFailedAdditions(t *testing.T) {
	p := sessionPolicy()
	p.MaxRecordsPerCommit = 3
	s := boundSession(t, newSessionHistory(t), p)
	other := boundSession(t, newSessionHistory(t), sessionPolicy())
	var zero session.SessionCommit
	for _, b := range []*session.SessionCommit{nil, &zero} {
		if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrBatch) {
			t.Fatal("zero submit")
		}
		if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
			t.Fatal("zero discard")
		}
		if _, err := b.AddUserInput(session.RunID{}, session.UserInput{}); !errors.Is(err, session.ErrBatch) {
			t.Fatal("zero add")
		}
	}
	b := beginBatch(t, s)
	copyHandle := *b
	if err := copyHandle.Discard(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("copied discard")
	}
	if _, err := copyHandle.AddUserInput(session.RunID{}, session.UserInput{}); !errors.Is(err, session.ErrBatch) {
		t.Fatal("copied add")
	}
	if _, err := s.SubmitCommit(context.Background(), &copyHandle); !errors.Is(err, session.ErrBatch) {
		t.Fatal("copied submit")
	}
	if _, err := other.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrBatch) {
		t.Fatal("foreign submit")
	}
	if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("two slots")
	}
	if _, err := b.StartRun(); !errors.Is(err, session.ErrTransition) {
		t.Fatal("uncreated start")
	}
	if _, err := b.AddUserInput(session.RunID{}, session.UserInput{}); !errors.Is(err, session.ErrTransition) {
		t.Fatal("input without owner")
	}
	mustBatch(t, b.Create())
	if err := b.Create(); !errors.Is(err, session.ErrTransition) {
		t.Fatal("duplicate create")
	}
	run := batchRun(t, b)
	if _, err := b.StartRun(); !errors.Is(err, session.ErrTransition) {
		t.Fatal("duplicate start")
	}
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, owner := range []session.RunID{{}, allocatedRun(t, foreign, 1), allocatedRun(t, testHistoryID(t), 2)} {
		if id, err := b.AddUserInput(owner, session.UserInput{}); id != (session.UserInputID{}) || !errors.Is(err, session.ErrTransition) {
			t.Fatal("invalid owner")
		}
	}
	if id, err := b.AddUserInput(run, session.UserInput{Parts: byteParts(make([]byte, testLimits().MaxByteStringBytes+1))}); id != (session.UserInputID{}) || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("oversize: %v", err)
	}
	input := batchInput(t, b, run, []byte("discarded"))
	mustBatch(t, b.Discard())
	fresh := beginBatch(t, s)
	if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("stale discard")
	}
	if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrBatch) {
		t.Fatal("stale submit")
	}
	mustBatch(t, fresh.Create())
	if got := batchRun(t, fresh); got != run {
		t.Fatal("discard consumed run")
	}
	if got := batchInput(t, fresh, run, []byte("accepted")); got != input {
		t.Fatal("discard consumed input")
	}
	submitBatch(t, s, fresh)
	if _, err := s.SubmitCommit(context.Background(), fresh); !errors.Is(err, session.ErrBatch) {
		t.Fatal("completed submit")
	}
	if _, err := fresh.AddUserInput(session.RunID{}, session.UserInput{}); !errors.Is(err, session.ErrBatch) {
		t.Fatal("completed add")
	}
}

func TestSessionCommitInvalidPreflightNoAppend(t *testing.T) {
	for _, name := range []string{"empty", "reserve", "commit codec"} {
		t.Run(name, func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			p := sessionPolicy()
			if name == "reserve" {
				p.MaxRetainedRecords = 3
				p.SoftMaxRetainedRecords = 3
			}
			if name == "commit codec" {
				p.CommitLimits.MaxBytes = 1
			}
			s := boundSession(t, newLifecycleHistory(t, backend), p)
			b := beginBatch(t, s)
			if name != "empty" {
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				batchInput(t, b, run, nil)
			}
			before := s.Snapshot()
			if ref, err := s.SubmitCommit(context.Background(), b); err == nil || ref != (history.CommitRef{}) {
				t.Fatalf("preflight: %v, %v", ref, err)
			}
			if len(backend.attempts) != 0 || !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("preflight wrote or published")
			}
			if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrBatch) {
				t.Fatal("failed handle reused")
			}
			next := beginBatch(t, s)
			if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("stale discard")
			}
			mustBatch(t, next.Create())
			if name == "reserve" {
				if id := batchRun(t, next); id != allocatedRun(t, testHistoryID(t), 1) {
					t.Fatal("rejection consumed run")
				}
			}
			mustBatch(t, next.Discard())
		})
	}
}

func TestSessionCommitUncertainIdenticalExplicitRetry(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before acceptance", true: "lost acknowledgment"}[accepted], func(t *testing.T) {
			cause := errors.New("untrusted backend payload must not render")
			backend := &batchBackend{Backend: mem.New(), fail: cause, accept: accepted}
			s := boundSession(t, newLifecycleHistory(t, backend), sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			content := []byte("original")
			batchInput(t, b, run, content)
			clear(content)
			before := s.Snapshot()
			ref, err := s.SubmitCommit(context.Background(), b)
			if ref != (history.CommitRef{}) || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
				t.Fatalf("uncertain: %v, %v", ref, err)
			}
			if len(backend.attempts) != 1 || !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("hidden retry/publication")
			}
			if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("uncertain slot lost")
			}
			if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("uncertain discard")
			}
			if _, err := b.AddUserInput(session.RunID{}, session.UserInput{}); !errors.Is(err, session.ErrBatch) {
				t.Fatal("frozen add")
			}
			if err := b.EndRun(session.RunEndedInterrupted); !errors.Is(err, session.ErrBatch) {
				t.Fatal("frozen interruption")
			}
			ref = submitBatch(t, s, b)
			if len(backend.attempts) != 2 || !reflect.DeepEqual(backend.attempts[0], backend.attempts[1]) || ref != backend.attempts[0].commit.Ref {
				t.Fatal("retry changed identity/bytes/parent")
			}
			if got := s.Snapshot(); got.RunCount() != 1 || !userInputsMatch(got, got.RunAt(0).ID(), 1) || string(inputBytes(lookupInput(t, got, run, 1))) != "original" {
				t.Fatal("retry not exactly once")
			}
			if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrBatch) || len(backend.attempts) != 2 {
				t.Fatal("completed retry")
			}
		})
	}
}

func TestSessionCommitUncertainSuccessorIdenticalExplicitRetry(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before acceptance", true: "lost acknowledgment"}[accepted], func(t *testing.T) {
			cause := errors.New("private backend cause")
			backend := &batchBackend{Backend: mem.New()}
			s := boundSession(t, newLifecycleHistory(t, backend), sessionPolicy())
			first := beginBatch(t, s)
			mustBatch(t, first.Create())
			firstRun := batchRun(t, first)
			batchInput(t, first, firstRun, []byte("first"))
			submitBatch(t, s, first)
			closeout := beginBatch(t, s)
			mustBatch(t, closeout.EndRun(session.RunEndedInterrupted))
			submitBatch(t, s, closeout)
			parent := s.Snapshot()
			if parent.Head().Sequence == 0 || parent.ActiveRun() != (session.RunID{}) || parent.RunCount() != 1 {
				t.Fatal("nonzero idle parent")
			}
			backend.fail, backend.accept = cause, accepted
			next := beginBatch(t, s)
			run := batchRun(t, next)
			input := batchInput(t, next, run, []byte("second"))
			if input != allocatedInput(t, run, 1) || run != allocatedRun(t, testHistoryID(t), 2) {
				t.Fatal("successor ordinals")
			}
			ref, err := s.SubmitCommit(context.Background(), next)
			if ref != (history.CommitRef{}) || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
				t.Fatalf("uncertain successor: %v, %v", ref, err)
			}
			if len(backend.attempts) != 3 || !reflect.DeepEqual(parent, s.Snapshot()) {
				t.Fatal("hidden retry/publication")
			}
			original := backend.attempts[2]
			if original.id != testHistoryID(t) || original.expected != parent.Head() || original.commit.Ref.Sequence != parent.Head().Sequence+1 || len(original.commit.Bytes) == 0 {
				t.Fatal("lost nonzero parent")
			}
			if got := submitBatch(t, s, next); got != original.commit.Ref {
				t.Fatal("retry ref")
			}
			if len(backend.attempts) != 4 || !reflect.DeepEqual(original, backend.attempts[3]) {
				t.Fatal("successor retry bytes/parent")
			}
			got := s.Snapshot()
			if got.Head() != original.commit.Ref || got.ActiveRun() != run || got.RunCount() != 2 || !userInputsMatch(got, run, 1) || lookupInput(t, got, input.RunID(), 1).ID() != input || string(inputBytes(lookupInput(t, got, input.RunID(), 1))) != "second" || parent.RunCount() != 1 {
				t.Fatal("successor exactly once or old snapshot")
			}
			if _, err := s.SubmitCommit(context.Background(), next); !errors.Is(err, session.ErrBatch) || len(backend.attempts) != 4 {
				t.Fatal("completed successor retry")
			}
		})
	}
}

func TestSessionCommitPayloadLimitRejectsBeforeAllocation(t *testing.T) {
	content := make([]byte, 128)
	seed := content[:64]
	for i := range seed {
		seed[i] = byte(i)
	}
	policy := sessionPolicy()
	policy.PayloadLimits.MaxByteStringBytes = 64
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "accepted"}[accepted], func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			s := boundSession(t, newLifecycleHistory(t, backend), policy)
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			batchInput(t, b, run, seed)
			if accepted {
				submitBatch(t, s, b)
				b = beginBatch(t, s)
			}
			before := s.Snapshot()
			attempts := len(backend.attempts)
			if id, err := b.AddUserInput(run, session.UserInput{Parts: byteParts(content)}); id != (session.UserInputID{}) || !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("payload-local rejection: %v, %v", id, err)
			}
			if !reflect.DeepEqual(before, s.Snapshot()) || len(backend.attempts) != attempts {
				t.Fatal("early rejection published or appended")
			}
			if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("early rejection released slot")
			}
			if id := batchInput(t, b, run, nil); id != allocatedInput(t, run, 2) {
				t.Fatal("failed add consumed ID")
			}
			submitBatch(t, s, b)
			view := s.Snapshot()
			seedRecord := encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: 1, ID: 1, Parts: formatByteParts(seed)})
			emptyRecord := encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: 1, ID: 2})
			want := []fixtureRecord{createdRecord(t), startRecord(t, 1), seedRecord, emptyRecord}
			if view.RecordCount() != len(want) || !userInputsMatch(view, run, 2) {
				t.Fatal("failed addition lost earlier provisional facts")
			}
			for i, record := range want {
				if !view.RecordAt(i).Format().Equal(record.Format()) {
					t.Fatalf("surviving record %d changed after payload failure", i)
				}
			}
		})
	}
	control := sessionformat.UserInput{RunID: 1, ID: 2, Parts: formatByteParts(content)}
	if n := testing.AllocsPerRun(10, func() {
		_, err := codec.Encode(control, testLimits())
		mustBatch(t, err)
	}); n == 0 {
		t.Fatal("encoding allocation control")
	}
}

func TestSessionCommitCanceledContextRetry(t *testing.T) {
	backend := &batchBackend{Backend: mem.New()}
	s := boundSession(t, newLifecycleHistory(t, backend), sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ref, err := s.SubmitCommit(ctx, b); !errors.Is(err, context.Canceled) || ref != (history.CommitRef{}) || len(backend.attempts) != 0 {
		t.Fatalf("canceled submit: %v, %v", ref, err)
	}
	if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("cancellation assumed rollback")
	}
	submitBatch(t, s, b)
	if len(backend.attempts) != 1 {
		t.Fatal("retry count")
	}
}
func TestSessionCommitLateAcceptanceCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &batchBackend{Backend: mem.New(), fail: context.Canceled, accept: true, cancel: cancel}
	h := newLifecycleHistory(t, backend)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	if ref, err := s.SubmitCommit(ctx, b); ref != (history.CommitRef{}) || !errors.Is(err, context.Canceled) || ctx.Err() == nil {
		t.Fatalf("late cancellation: %v, %v", ref, err)
	}
	assertHistoryHead(t, h, backend.attempts[0].commit.Ref)
	if s.Snapshot().Created() {
		t.Fatal("unconfirmed publication")
	}
	if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("late acceptance discarded")
	}
	ref := submitBatch(t, s, b)
	if ref != backend.attempts[0].commit.Ref || len(backend.attempts) != 2 || !reflect.DeepEqual(backend.attempts[0], backend.attempts[1]) {
		t.Fatal("fresh-context retry identity")
	}
}

func TestSessionCommitFailedByteAdmissionIsAtomic(t *testing.T) {
	p := sessionPolicy()
	empty := encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: 1, ID: 1})
	largestEnding := encodeFixture(t, sessionformat.KindRunEnded, sessionformat.RunEnded{RunID: 1, Reason: sessionformat.RunEndedCaptureFailed})
	p.MaxPayloadBytes = uint64(len(createdRecord(t).Format().Payload) + len(startRecord(t, 1).Format().Payload) + 2*len(empty.Format().Payload) + len(largestEnding.Format().Payload))
	p.SoftMaxPayloadBytes = p.MaxPayloadBytes
	backend := &batchBackend{Backend: mem.New()}
	s := boundSession(t, newLifecycleHistory(t, backend), p)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	batchInput(t, b, run, nil)
	if id := batchInput(t, b, run, make([]byte, 128)); id != allocatedInput(t, run, 2) {
		t.Fatal("aggregate capacity rejected a locally valid addition")
	}
	before := captureRejectedSnapshot(s.Snapshot())
	if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) {
		t.Fatalf("aggregate rejection: %v, %v", ref, err)
	}
	if len(backend.attempts) != 0 || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
		t.Fatal("rejection appended or published")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.Create())
	if got := batchRun(t, b); got != run {
		t.Fatal("rejection consumed run ID")
	}
	batchInput(t, b, run, nil)
	if id := batchInput(t, b, run, nil); id != allocatedInput(t, run, 2) {
		t.Fatal("rejection consumed input ID")
	}
	submitBatch(t, s, b)
	if !userInputsMatch(s.Snapshot(), run, 2) || len(backend.attempts) != 1 {
		t.Fatal("replacement proposal changed counts")
	}
}

func TestSessionCommitConflictExpiryAndNewerHead(t *testing.T) {
	for _, name := range []string{"conflict", "expiry", "accepted newer head"} {
		t.Run(name, func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			hp := history.Policy{CommitLimits: testLimits(), RetryWindow: 1, MaxPageBytes: testLimits().MaxBytes, MaxPageCommits: 2}
			if name == "accepted newer head" {
				hp.RetryWindow = 16
			}
			h, err := history.New(backend, hp)
			mustBatch(t, err)
			s := boundSession(t, h, sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			batchInput(t, b, run, []byte("ours"))
			winner := boundSession(t, h, sessionPolicy())
			if name == "conflict" {
				w := beginBatch(t, winner)
				mustBatch(t, w.Create())
				r := batchRun(t, w)
				batchInput(t, w, r, []byte("winner"))
				submitBatch(t, winner, w)
			} else {
				backend.fail, backend.accept = errors.New("lost ack"), true
				if _, err := s.SubmitCommit(context.Background(), b); err == nil {
					t.Fatal("missing lost ack")
				}
				root := backend.attempts[0].commit.Ref
				winner, err = session.Load(context.Background(), h, testHistoryID(t), sessionPolicy())
				mustBatch(t, err)
				if winner.Snapshot().Head() != root {
					t.Fatal("loaded winner endpoint")
				}
				w := beginBatch(t, winner)
				batchInput(t, w, run, []byte("later"))
				submitBatch(t, winner, w)
			}
			before := s.Snapshot()
			ref, err := s.SubmitCommit(context.Background(), b)
			switch name {
			case "conflict":
				if ref != (history.CommitRef{}) || !errors.Is(err, session.ErrConflict) {
					t.Fatalf("conflict: %v, %v", ref, err)
				}
			case "expiry":
				if ref != (history.CommitRef{}) || !errors.Is(err, session.ErrRetryExpired) || errors.Is(err, session.ErrConflict) {
					t.Fatalf("expiry: %v, %v", ref, err)
				}
			case "accepted newer head":
				if err != nil || ref != backend.attempts[0].commit.Ref || s.Snapshot().Head() != ref || !userInputsMatch(s.Snapshot(), run, 1) {
					t.Fatalf("newer head: %v, %v", ref, err)
				}
			}
			if name != "accepted newer head" && !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatal("nonacceptance published")
			}
			if _, err := s.BeginCommit(); !errors.Is(err, session.ErrReloadRequired) {
				t.Fatal("reload barrier")
			}
			if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, session.ErrBatch) {
				t.Fatal("terminal retry")
			}
		})
	}
}
