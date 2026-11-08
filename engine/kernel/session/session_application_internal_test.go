package session

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func lookupInput(t testing.TB, v Snapshot, run RunID, ordinal uint64) UserInputView {
	t.Helper()
	var seen uint64
	for record := range v.Records() {
		input, ok := record.UserInput()
		if !ok || input.ID().RunID() != run {
			continue
		}
		seen++
		if seen == ordinal {
			located, present := v.UserInputByID(input.ID())
			if !present || located != input {
				t.Fatal("input lookup differs from inspection")
			}
			return located
		}
	}
	t.Fatalf("missing input at fixture position %d", ordinal)
	return UserInputView{}
}

// userInputsMatch checks expected membership through chronological facts and lookup.
func userInputsMatch(v Snapshot, run RunID, count uint64) bool {
	var seen uint64
	for i := 0; i < v.RecordCount(); i++ {
		input, ok := v.RecordAt(i).UserInput()
		if !ok || input.ID().RunID() != run {
			continue
		}
		seen++
		got, present := v.UserInputByID(input.ID())
		if !present || got.ID() != input.ID() || got != input {
			return false
		}
	}
	return seen == count
}

func sessionPolicy() Policy {
	return Policy{CommitLimits: testLimits(), PayloadLimits: testLimits(), MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxRetainedRecords: 128, MaxPayloadBytes: 32768, MaxDecodedWork: 1 << 24}
}
func newSessionHistory(t testing.TB) *history.History {
	t.Helper()
	return newLifecycleHistory(t, mem.New())
}
func newSession(t testing.TB, p Policy) *Session {
	t.Helper()
	s, err := New(newSessionHistory(t), testHistoryID(t), p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func createdRecord(t testing.TB) *materializedRecord {
	t.Helper()
	r, err := newCreatedRecord(Created{HistoryID: testHistoryID(t)}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func inputRecord(t testing.TB, ordinal uint64) *materializedRecord {
	t.Helper()
	return runInputRecord(t, 1, ordinal)
}
func runInputRecord(t testing.TB, run, ordinal uint64) *materializedRecord {
	t.Helper()
	r, err := makeInputRecord(identifiedInput{ID: allocatedInput(t, allocatedRun(t, testHistoryID(t), run), ordinal), Parts: internalByteParts([]byte{0xff, 0, 'x'})}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func startRecord(t testing.TB, ordinal uint64) *materializedRecord {
	t.Helper()
	r, err := newRunStartedRecord(RunStarted{ID: allocatedRun(t, testHistoryID(t), ordinal)}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func closeoutRecord(t testing.TB, ordinal uint64) *materializedRecord {
	t.Helper()
	r, err := newRunEndedRecord(RunEnded{RunID: allocatedRun(t, testHistoryID(t), ordinal), Reason: RunEndedInterrupted}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// proposedCommit constructs a canonical assertion independently of the reducer.
// It intentionally does not validate lifecycle legality or claim storage acceptance.
func proposedCommit(t testing.TB, s *Session, records ...*materializedRecord) history.Commit {
	t.Helper()
	v := s.Snapshot()
	dto := format.Commit{FormatVersion: 1, Sequence: v.Head().Sequence + 1}
	if v.Head() == (history.CommitRef{}) {
		dto.HistoryID = new(v.HistoryID())
	} else {
		dto.Parent = new(v.Head().CID)
	}
	for _, r := range records {
		dto.Records = append(dto.Records, r.format())
	}
	return identify(t, dto)
}
func applyRecords(t testing.TB, s *Session, records ...*materializedRecord) history.Commit {
	t.Helper()
	c := proposedCommit(t, s, records...)
	// This low-level caller asserts acceptance. Real storage is exercised separately.
	if err := s.apply(c); err != nil {
		t.Fatal(err)
	}
	return c
}
func identify(t testing.TB, dto format.Commit) history.Commit {
	t.Helper()
	wire, err := codec.Encode(dto, sessionPolicy().CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	return history.Commit{Ref: history.CommitRef{Sequence: dto.Sequence, CID: contentid.Sum(wire)}, Commit: dto}
}

// rejectedSnapshotFacts owns every mutable export; unlike Snapshot it cannot
// share an oracle with the state whose immutability is under test.
type rejectedSnapshotFacts struct {
	historyID historyid.HistoryID
	head      history.CommitRef
	created   bool
	activeRun RunID
	records   []format.Record
	runs      []rejectedRunFacts
}

type rejectedRunFacts struct {
	id         RunID
	records    []format.Record
	inputs     []UserInputID
	partCounts []int
}

func captureRejectedSnapshot(v Snapshot) rejectedSnapshotFacts {
	facts := rejectedSnapshotFacts{historyID: v.HistoryID(), head: v.Head(), created: v.Created(), activeRun: v.ActiveRun()}
	for i := 0; i < v.RecordCount(); i++ {
		facts.records = append(facts.records, v.RecordAt(i).Format().Clone())
	}
	for i := 0; i < v.RunCount(); i++ {
		run := v.RunAt(i)
		f := rejectedRunFacts{id: run.ID()}
		for j := 0; j < run.RecordCount(); j++ {
			f.records = append(f.records, run.RecordAt(j).Format().Clone())
			if input, ok := run.RecordAt(j).UserInput(); ok {
				located, _ := v.UserInputByID(input.ID())
				f.inputs = append(f.inputs, located.ID())
				f.partCounts = append(f.partCounts, located.PartCount())
			}
		}
		facts.runs = append(facts.runs, f)
	}
	return facts
}

func assertRejected(t testing.TB, s *Session, c history.Commit, want error) {
	t.Helper()
	old := s.Snapshot()
	before := captureRejectedSnapshot(old)
	if err := s.apply(c); !errors.Is(err, want) {
		t.Fatalf("Apply = %v; want %v", err, want)
	}
	if !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) || !reflect.DeepEqual(before, captureRejectedSnapshot(old)) {
		t.Fatal("failed Apply changed accepted state or an old snapshot")
	}
}

func TestSessionLifecycleAndReplay(t *testing.T) {
	s := newSession(t, sessionPolicy())
	if v := s.Snapshot(); v.Created() || v.Head() != (history.CommitRef{}) || v.HistoryID() != testHistoryID(t) {
		t.Fatal("new scope")
	}
	commits := []history.Commit{applyRecords(t, s, createdRecord(t)), applyRecords(t, s, startRecord(t, 1)), applyRecords(t, s, inputRecord(t, 1), inputRecord(t, 2))}
	active := newSession(t, sessionPolicy())
	for _, c := range commits {
		mustBatch(t, active.apply(c))
	}
	if !reflect.DeepEqual(active.Snapshot(), s.Snapshot()) {
		t.Fatal("active replay differs")
	}
	commits = append(commits, applyRecords(t, s, closeoutRecord(t, 1)), applyRecords(t, s, startRecord(t, 2), runInputRecord(t, 2, 1)))
	replay := newSession(t, sessionPolicy())
	for _, c := range commits {
		mustBatch(t, replay.apply(c))
	}
	if !reflect.DeepEqual(replay.Snapshot(), s.Snapshot()) || replay.Snapshot().ActiveRun() != allocatedRun(t, testHistoryID(t), 2) {
		t.Fatal("successor replay differs")
	}
}

func TestSessionCompetingStartsAndStaleProposals(t *testing.T) {
	s := newSession(t, sessionPolicy())
	applyRecords(t, s, createdRecord(t))
	first := proposedCommit(t, s, startRecord(t, 1), inputRecord(t, 1))
	second := proposedCommit(t, s, startRecord(t, 1))
	if first.Ref == second.Ref {
		t.Fatal("different facts have same identity")
	}
	mustBatch(t, s.apply(first))
	assertRejected(t, s, second, ErrIdentity)
	assertRejected(t, s, first, ErrIdentity)
	assertRejected(t, s, proposedCommit(t, s, startRecord(t, 2)), ErrTransition)
	closeout := proposedCommit(t, s, closeoutRecord(t, 1))
	applyRecords(t, s, closeoutRecord(t, 1), startRecord(t, 2))
	assertRejected(t, s, closeout, ErrIdentity)
}

func TestSessionAtomicRejection(t *testing.T) {
	maxInput, err := codec.Encode(sessionformat.UserInput{RunID: 3, ID: math.MaxUint64}, testLimits())
	mustBatch(t, err)
	maxRun, err := codec.Encode(sessionformat.RunStarted{ID: math.MaxUint64}, testLimits())
	mustBatch(t, err)
	for _, tc := range []struct {
		name string
		last format.Record
		want error
	}{
		{"duplicate creation", createdRecord(t).format(), ErrTransition},
		{"skipped input", runInputRecord(t, 3, 3).format(), ErrTransition},
		{"maximum input ordinal", format.Record{Kind: sessionformat.KindUserInput, KindVersion: 1, Payload: maxInput}, ErrTransition},
		{"maximum run ordinal", format.Record{Kind: sessionformat.KindRunStarted, KindVersion: 1, Payload: maxRun}, ErrTransition},
		{"repeated input", runInputRecord(t, 3, 1).format(), ErrTransition},
		{"duplicate active start", startRecord(t, 4).format(), ErrTransition},
		{"wrong owner", runInputRecord(t, 2, 2).format(), ErrTransition},
		{"wrong interruption", closeoutRecord(t, 2).format(), ErrTransition},
		{"unsupported kind", format.Record{Kind: "session.future_request", KindVersion: 1, Payload: []byte{0xa0}}, ErrUnsupportedRecord},
		{"unsupported version", format.Record{Kind: "session.user_input", KindVersion: 2, Payload: []byte{0xa0}}, ErrUnsupportedRecord},
		{"malformed payload", format.Record{Kind: "session.user_input", KindVersion: 1, Payload: []byte{0xff}}, codec.ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := []*materializedRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1), inputRecord(t, 2), closeoutRecord(t, 1), startRecord(t, 2), runInputRecord(t, 2, 1), closeoutRecord(t, 2)}
			continuation := []*materializedRecord{startRecord(t, 3), runInputRecord(t, 3, 1), closeoutRecord(t, 3)}
			s := newSession(t, sessionPolicy())
			applyRecords(t, s, prefix...)
			good := proposedCommit(t, s, continuation[:2]...)
			dto := good.Commit.Clone()
			dto.Records = append(dto.Records, tc.last)
			assertRejected(t, s, identify(t, dto), tc.want)
			mustBatch(t, s.apply(good))
			applyRecords(t, s, continuation[2])
			if v := s.Snapshot(); v.RunCount() != 3 || !userInputsMatch(v, v.RunAt(0).ID(), 2) || !userInputsMatch(v, v.RunAt(1).ID(), 1) || !userInputsMatch(v, v.RunAt(2).ID(), 1) || v.ActiveRun() != (RunID{}) {
				t.Fatal("failed batch leaked locators or admission budget")
			}
		})
	}
	for _, records := range [][]*materializedRecord{{inputRecord(t, 1)}, {startRecord(t, 1)}, {closeoutRecord(t, 1)}} {
		s := newSession(t, sessionPolicy())
		assertRejected(t, s, proposedCommit(t, s, records...), ErrTransition)
	}
	s := newSession(t, sessionPolicy())
	applyRecords(t, s, createdRecord(t))
	for _, r := range []*materializedRecord{inputRecord(t, 1), startRecord(t, 2), closeoutRecord(t, 1)} {
		assertRejected(t, s, proposedCommit(t, s, r), ErrTransition)
	}
	applyRecords(t, s, startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1))
	assertRejected(t, s, proposedCommit(t, s, inputRecord(t, 2)), ErrTransition)
	assertRejected(t, s, proposedCommit(t, s, startRecord(t, 2), inputRecord(t, 2)), ErrTransition)
	applyRecords(t, s, startRecord(t, 2), runInputRecord(t, 2, 1))
}

func TestSessionIdentityVerification(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, tc := range []struct {
		name             string
		root, reidentify bool
		edit             func(*history.Commit)
	}{
		{"root scope", true, true, func(c *history.Commit) { c.Commit.HistoryID = new(foreign) }},
		{"skipped sequence", false, true, func(c *history.Commit) { c.Commit.Sequence++ }},
		{"reference sequence", false, false, func(c *history.Commit) { c.Ref.Sequence++ }},
		{"wrong parent", false, true, func(c *history.Commit) { c.Commit.Parent = new(contentid.Sum(nil)) }},
		{"reference CID", false, false, func(c *history.Commit) { c.Ref.CID = contentid.Sum(nil) }},
		{"unset CID", false, false, func(c *history.Commit) { c.Ref.CID = contentid.CID{} }},
		{"zero reference", false, false, func(c *history.Commit) { c.Ref = history.CommitRef{} }},
		{"mutated DTO", false, false, func(c *history.Commit) { c.Commit.Records[0].Payload = startRecord(t, 2).format().Payload }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(t, sessionPolicy())
			r := createdRecord(t)
			if !tc.root {
				applyRecords(t, s, createdRecord(t))
				r = startRecord(t, 1)
			}
			c := proposedCommit(t, s, r)
			tc.edit(&c)
			if tc.reidentify {
				c = identify(t, c.Commit)
			}
			assertRejected(t, s, c, ErrIdentity)
		})
	}
	s := newSession(t, sessionPolicy())
	c := proposedCommit(t, s, createdRecord(t))
	c.Commit.FormatVersion = 2
	assertRejected(t, s, c, codec.ErrInvalidData)
}

func TestSessionApplyOwnership(t *testing.T) {
	policy := sessionPolicy()
	s := newSession(t, policy)
	policy.MaxRecordsPerCommit = 1
	policy.PayloadLimits.MaxBytes = 1
	c := applyRecords(t, s, createdRecord(t), startRecord(t, 1), inputRecord(t, 1))
	clear(c.Commit.Records[2].Payload)
	*c.Commit.HistoryID = historyid.HistoryID{}
	before := s.Snapshot()
	old := lookupInput(t, before, before.RunAt(0).ID(), 1)
	successor := applyRecords(t, s, inputRecord(t, 2))
	clear(successor.Commit.Records[0].Payload)
	*successor.Commit.Parent = contentid.CID{}
	content := internalInputBytes(old)
	clear(content)
	raw := before.RecordAt(2).Format()
	clear(raw.Payload)
	if got := internalInputBytes(old); !reflect.DeepEqual(got, []byte{0xff, 0, 'x'}) || !userInputsMatch(before, old.ID().RunID(), 1) {
		t.Fatal("accepted DTO/export aliases")
	}
}

func TestSessionPolicyAndZero(t *testing.T) {
	p := sessionPolicy()
	for i := 0; i < reflect.TypeOf(p).NumField(); i++ {
		bad := p
		reflect.ValueOf(&bad).Elem().Field(i).SetZero()
		if s, err := New(newSessionHistory(t), testHistoryID(t), bad); !errors.Is(err, ErrInvalidPolicy) || s != nil {
			t.Fatalf("zero policy field %d: %v", i, err)
		}
	}
	for _, commit := range []bool{false, true} {
		for i := 0; i < reflect.TypeOf(p.PayloadLimits).NumField(); i++ {
			bad := p
			limits := &bad.PayloadLimits
			if commit {
				limits = &bad.CommitLimits
			}
			reflect.ValueOf(limits).Elem().Field(i).SetInt(-1)
			if err := bad.Validate(); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("negative codec field %d: %v", i, err)
			}
		}
	}
	if s, err := New(nil, testHistoryID(t), p); !errors.Is(err, ErrIdentity) || s != nil {
		t.Fatalf("nil History: %v", err)
	}
	if s, err := New(newSessionHistory(t), historyid.HistoryID{}, p); !errors.Is(err, ErrIdentity) || s != nil {
		t.Fatalf("zero scope: %v", err)
	}
	var zero Session
	if _, err := zero.BeginCommit(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("zero BeginCommit: %v", err)
	}
	if err := zero.apply(history.Commit{}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("zero Apply: %v", err)
	}
	if !reflect.DeepEqual(zero.Snapshot(), Snapshot{}) {
		t.Fatal("zero Snapshot")
	}
}

func TestSessionAdmissionAndCloseoutBounds(t *testing.T) {
	initial := []*materializedRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1)}
	var initialBytes uint64
	for _, r := range initial {
		initialBytes += uint64(len(r.format().Payload))
	}
	closeBytes := uint64(len(closeoutRecord(t, 1).format().Payload))
	p := sessionPolicy()
	work := uint64(p.PayloadLimits.MaxBytes + p.PayloadLimits.MaxNodes + p.PayloadLimits.MaxCollectionElements)
	for _, tc := range []struct {
		name string
		edit func(*Policy)
	}{
		{"batch count", func(p *Policy) { p.MaxRecordsPerCommit = 2 }},
		{"retained closeout", func(p *Policy) { p.MaxRetainedRecords = 3 }},
		{"payload closeout", func(p *Policy) { p.MaxPayloadBytes = initialBytes + closeBytes - 1 }},
		{"work closeout", func(p *Policy) { p.MaxDecodedWork = 4*work - 1 }},
		{"payload codec", func(p *Policy) { p.PayloadLimits.MaxNodes = 1 }},
		{"commit codec", func(p *Policy) { p.CommitLimits.MaxBytes = 1 }},
		{"per payload bytes", func(p *Policy) { p.PayloadLimits.MaxBytes = 1 }},
		{"overflow work", func(p *Policy) {
			p.PayloadLimits.MaxBytes = math.MaxInt
			p.PayloadLimits.MaxNodes = math.MaxInt
			p.PayloadLimits.MaxCollectionElements = math.MaxInt
			p.MaxDecodedWork = math.MaxUint64
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := p
			tc.edit(&policy)
			if tc.name == "overflow work" && uint64(math.MaxInt) < math.MaxUint32 {
				policy.MaxDecodedWork = uint64(math.MaxInt)
			}
			policy.SoftMaxRetainedRecords = policy.MaxRetainedRecords
			policy.SoftMaxPayloadBytes = policy.MaxPayloadBytes
			policy.SoftMaxDecodedWork = policy.MaxDecodedWork
			s := newSession(t, policy)
			if tc.name == "retained closeout" || tc.name == "payload closeout" || tc.name == "work closeout" {
				// Accepted replay needs only its own hard capacity, not future write room.
				applyRecords(t, s, initial...)
				assertRejected(t, s, proposedCommit(t, s, closeoutRecord(t, 1)), codec.ErrLimitExceeded)
			} else {
				assertRejected(t, s, proposedCommit(t, s, initial...), codec.ErrLimitExceeded)
			}
		})
	}
	p.MaxRetainedRecords = 4
	p.MaxPayloadBytes = initialBytes + closeBytes
	p.MaxDecodedWork = 4 * work
	p.SoftMaxRetainedRecords, p.SoftMaxPayloadBytes, p.SoftMaxDecodedWork = p.MaxRetainedRecords, p.MaxPayloadBytes, p.MaxDecodedWork
	s := newSession(t, p)
	applyRecords(t, s, initial...)
	applyRecords(t, s, closeoutRecord(t, 1))
	assertRejected(t, s, proposedCommit(t, s, startRecord(t, 2)), codec.ErrLimitExceeded)
}

func TestSessionRun24CloseoutCapacity(t *testing.T) {
	all := []*materializedRecord{createdRecord(t)}
	for n := uint64(1); n <= 24; n++ {
		all = append(all, startRecord(t, n), runInputRecord(t, n, 1), closeoutRecord(t, n))
	}
	// The final active prefix must reserve the largest supported minimal ending.
	lastEnding, err := newRunEndedRecord(RunEnded{RunID: allocatedRun(t, testHistoryID(t), 24), Reason: RunEndedCaptureFailed}, testLimits())
	mustBatch(t, err)
	all[len(all)-1] = lastEnding
	var exactBytes uint64
	for _, r := range all {
		exactBytes += uint64(len(r.format().Payload))
	}
	prefix := func(t *testing.T, s *Session) {
		t.Helper()
		b := beginBatch(t, s)
		mustBatch(t, b.Create())
		submitBatch(t, s, b)
		for n := uint64(1); n <= 23; n++ {
			b = beginBatch(t, s)
			run := batchRun(t, b)
			if run != allocatedRun(t, testHistoryID(t), n) {
				t.Fatal("prefix run ordinal")
			}
			batchInput(t, b, run, []byte{0xff, 0, 'x'})
			mustBatch(t, b.EndRun(RunEndedInterrupted))
			submitBatch(t, s, b)
		}
	}
	shortPolicy := sessionPolicy()
	shortPolicy.MaxPayloadBytes = exactBytes - 1
	shortPolicy.SoftMaxPayloadBytes = shortPolicy.MaxPayloadBytes
	short := newSession(t, shortPolicy)
	prefix(t, short)
	before := captureRejectedSnapshot(short.Snapshot())
	b := beginBatch(t, short)
	run := batchRun(t, b)
	batchInput(t, b, run, []byte{0xff, 0, 'x'})
	if run != allocatedRun(t, testHistoryID(t), 24) {
		t.Fatal("width-boundary provisional run")
	}
	if ref, err := short.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, captureRejectedSnapshot(short.Snapshot())) {
		t.Fatalf("width-boundary writer reservation: %v, %v", ref, err)
	}
	applyRecords(t, short, all[70:72]...)
	assertRejected(t, short, proposedCommit(t, short, all[72]), codec.ErrLimitExceeded)
	exactPolicy := sessionPolicy()
	exactPolicy.MaxPayloadBytes = exactBytes
	exactPolicy.SoftMaxPayloadBytes = exactBytes
	backend := &batchBackend{Backend: mem.New()}
	h := newLifecycleHistory(t, backend)
	exact := boundSession(t, h, exactPolicy)
	prefix(t, exact)
	attempts := len(backend.attempts)
	wantStart := proposedCommit(t, exact, all[70:72]...)
	b = beginBatch(t, exact)
	run = batchRun(t, b)
	batchInput(t, b, run, []byte{0xff, 0, 'x'})
	if ref := submitBatch(t, exact, b); ref != wantStart.Ref || len(backend.attempts) != attempts+1 {
		t.Fatal("exact-fit boundary writer did not append canonical start/input")
	}
	wantClose := proposedCommit(t, exact, all[72])
	b = beginBatch(t, exact)
	mustBatch(t, b.EndRun(RunEndedCaptureFailed))
	if ref := submitBatch(t, exact, b); ref != wantClose.Ref || len(backend.attempts) != attempts+2 {
		t.Fatal("exact-fit boundary writer did not append canonical interruption")
	}
	assertHistoryHead(t, h, wantClose.Ref)
	if exact.Snapshot().RunCount() != 24 || exact.Snapshot().ActiveRun() != (RunID{}) {
		t.Fatal("width-boundary closeout")
	}
}

func TestSessionAdmissionBeforeDecodeOrClone(t *testing.T) {
	records := []*materializedRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1)}
	wide := sessionPolicy()
	source := newSession(t, wide)
	accepted := proposedCommit(t, source, records...)
	var payloadBytes uint64
	for _, r := range records {
		payloadBytes += uint64(len(r.format().Payload))
	}
	work := uint64(wide.PayloadLimits.MaxBytes + wide.PayloadLimits.MaxNodes + wide.PayloadLimits.MaxCollectionElements)
	for _, tc := range []struct {
		name string
		edit func(*Policy)
	}{
		{"summed payload", func(p *Policy) { p.MaxPayloadBytes = payloadBytes - 1 }},
		{"decoded work", func(p *Policy) { p.MaxDecodedWork = 3*work - 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := wide
			tc.edit(&p)
			p.SoftMaxPayloadBytes, p.SoftMaxDecodedWork = p.MaxPayloadBytes, p.MaxDecodedWork
			s := newSession(t, p)
			assertRejected(t, s, accepted, codec.ErrLimitExceeded)
			if n := testing.AllocsPerRun(10, func() {
				if err := s.apply(accepted); !errors.Is(err, codec.ErrLimitExceeded) {
					panic(err)
				}
			}); n != 0 {
				t.Fatalf("early admission allocations: %g", n)
			}
		})
	}
	s := newSession(t, wide)
	oversized := history.Commit{Commit: format.Commit{Records: make([]format.Record, 4096)}}
	for i := range oversized.Commit.Records {
		oversized.Commit.Records[i] = format.Record{Kind: "unknown", KindVersion: 1, Payload: []byte{0xff}}
	}
	assertRejected(t, s, oversized, codec.ErrLimitExceeded)
	if n := testing.AllocsPerRun(10, func() {
		if err := s.apply(oversized); err != codec.ErrLimitExceeded {
			panic(err)
		}
	}); n != 0 {
		t.Fatalf("over-count allocations: %g", n)
	}
}

func FuzzSessionApplyMalformed(f *testing.F) {
	f.Add([]byte{0xff}, uint8(0))
	f.Add([]byte{0xa0}, uint8(1))
	f.Add([]byte{}, uint8(2))
	f.Fuzz(func(t *testing.T, payload []byte, variant uint8) {
		if len(payload) > 1024 {
			t.Skip()
		}
		s := newSession(t, sessionPolicy())
		applyRecords(t, s, createdRecord(t), startRecord(t, 1))
		dto := proposedCommit(t, s, inputRecord(t, 1)).Commit
		kind := []string{"session.user_input", "session.run_started", "session.run_ended", "session.created", "unknown"}[int(variant)%5]
		dto.Records = append(dto.Records, format.Record{Kind: kind, KindVersion: 1, Payload: payload})
		c := identify(t, dto)
		before := s.Snapshot()
		if err := s.apply(c); err != nil && !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("malformed application partly published")
		}
	})
}
