package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func testLimits() codec.Limits {
	return codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
}
func testHistoryID(t testing.TB) historyid.HistoryID {
	t.Helper()
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	mustBatch(t, err)
	return id
}
func sessionPolicy() session.Policy {
	return session.Policy{CommitLimits: testLimits(), PayloadLimits: testLimits(), MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxRetainedRecords: 128, MaxPayloadBytes: 32768, MaxDecodedWork: 1 << 24}
}
func newSessionHistory(t testing.TB) *history.History {
	t.Helper()
	return newLifecycleHistory(t, mem.New())
}
func newSession(t testing.TB, p session.Policy) *session.Session {
	t.Helper()
	return boundSession(t, newSessionHistory(t), p)
}

// fixtureRecord contains only an explicit local persistence DTO, not a scoped
// proposal or admission proof. Load must bind it through real History storage.
type fixtureRecord struct{ value format.Record }

func (r fixtureRecord) Format() format.Record { return r.value.Clone() }
func encodeFixture(t testing.TB, kind string, v codec.Marshaler) fixtureRecord {
	t.Helper()
	wire, err := codec.Encode(v, testLimits())
	mustBatch(t, err)
	return fixtureRecord{format.Record{Kind: kind, KindVersion: 1, Payload: wire}}
}
func createdRecord(t testing.TB) fixtureRecord {
	t.Helper()
	return encodeFixture(t, sessionformat.KindCreated, sessionformat.Created{})
}
func startRecord(t testing.TB, n uint64) fixtureRecord {
	t.Helper()
	return encodeFixture(t, sessionformat.KindRunStarted, sessionformat.RunStarted{ID: n})
}
func inputRecord(t testing.TB, n uint64) fixtureRecord { t.Helper(); return runInputRecord(t, 1, n) }
func runInputRecord(t testing.TB, run, n uint64) fixtureRecord {
	t.Helper()
	return encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: run, ID: n, Parts: formatByteParts([]byte{0xff, 0, 'x'})})
}
func closeoutRecord(t testing.TB, n uint64) fixtureRecord {
	t.Helper()
	return encodeFixture(t, sessionformat.KindRunEnded, sessionformat.RunEnded{RunID: n, Reason: sessionformat.RunEndedInterrupted})
}
func effectiveReferenceRecord(t testing.TB, run uint64, ordinals ...uint64) fixtureRecord {
	t.Helper()
	v := sessionformat.EffectiveInput{RunID: run}
	for _, n := range ordinals {
		v.Inputs = append(v.Inputs, sessionformat.InputItem{UserInputID: new(n)})
	}
	return encodeFixture(t, sessionformat.KindEffectiveInput, v)
}
func identify(t testing.TB, dto format.Commit) history.Commit {
	t.Helper()
	wire, err := codec.Encode(dto, testLimits())
	mustBatch(t, err)
	return history.Commit{Ref: history.CommitRef{Sequence: dto.Sequence, CID: contentid.Sum(wire)}, Commit: dto}
}
func proposedCommit(t testing.TB, s *session.Session, records ...fixtureRecord) history.Commit {
	t.Helper()
	v := s.Snapshot()
	dto := format.Commit{FormatVersion: 1, Sequence: v.Head().Sequence + 1}
	if v.Head() == (history.CommitRef{}) {
		dto.HistoryID = new(v.HistoryID())
	} else {
		dto.Parent = new(v.Head().CID)
	}
	for _, r := range records {
		dto.Records = append(dto.Records, r.Format())
	}
	return identify(t, dto)
}

// historyWithPrefix exposes exactly the accepted prefix under test through an
// isolated backend. It never writes the source Session's bound History.
func historyWithPrefix(t testing.TB, source *history.History, v session.Snapshot) *history.History {
	t.Helper()
	h := newSessionHistory(t)
	request := history.ReadRequest{Through: v.Head(), MaxCommits: 1}
	for request.After != request.Through {
		page, err := source.Read(context.Background(), v.HistoryID(), request)
		mustBatch(t, err)
		for _, c := range page.Commits {
			result, err := h.Append(context.Background(), v.HistoryID(), c.Commit)
			mustBatch(t, err)
			if result.Status != history.StatusAccepted {
				t.Fatal("fixture prefix not accepted")
			}
		}
		request.After = page.Next
	}
	return h
}
func loadFixtureCommit(t testing.TB, source *history.History, s *session.Session, c history.Commit, p session.Policy) (*session.Session, error) {
	t.Helper()
	h := historyWithPrefix(t, source, s.Snapshot())
	result, err := h.Append(context.Background(), s.Snapshot().HistoryID(), c.Commit)
	if err != nil {
		return nil, err
	}
	if result.Status != history.StatusAccepted {
		t.Fatal("fixture continuation not accepted")
	}
	return session.Load(context.Background(), h, s.Snapshot().HistoryID(), p)
}
func loadRecords(t testing.TB, h *history.History, s *session.Session, records ...fixtureRecord) *session.Session {
	t.Helper()
	result, err := h.Append(context.Background(), s.Snapshot().HistoryID(), proposedCommit(t, s, records...).Commit)
	mustBatch(t, err)
	if result.Status != history.StatusAccepted {
		t.Fatal("fixture append rejected")
	}
	loaded, err := session.Load(context.Background(), h, s.Snapshot().HistoryID(), sessionPolicy())
	mustBatch(t, err)
	return loaded
}

func lookupInput(t testing.TB, v session.Snapshot, run session.RunID, n uint64) session.UserInputView {
	t.Helper()
	var seen uint64
	for record := range v.Records() {
		input, ok := record.UserInput()
		if !ok || input.ID().RunID() != run {
			continue
		}
		seen++
		if seen == n {
			located, present := v.UserInputByID(input.ID())
			if !present || located != input {
				t.Fatal("input lookup differs from inspection")
			}
			return located
		}
	}
	t.Fatalf("missing input at fixture position %d", n)
	return session.UserInputView{}
}
func userInputsMatch(v session.Snapshot, run session.RunID, count uint64) bool {
	var seen uint64
	for r := range v.Records() {
		in, ok := r.UserInput()
		if !ok || in.ID().RunID() != run {
			continue
		}
		seen++
		got, present := v.UserInputByID(in.ID())
		if !present || got != in {
			return false
		}
	}
	return seen == count
}

type rejectedSnapshotFacts struct {
	historyID historyid.HistoryID
	head      history.CommitRef
	created   bool
	activeRun session.RunID
	records   []format.Record
	runs      []rejectedRunFacts
}
type rejectedRunFacts struct {
	id         session.RunID
	records    []format.Record
	inputs     []session.UserInputID
	partCounts []int
}

func captureRejectedSnapshot(v session.Snapshot) rejectedSnapshotFacts {
	facts := rejectedSnapshotFacts{historyID: v.HistoryID(), head: v.Head(), created: v.Created(), activeRun: v.ActiveRun()}
	for r := range v.Records() {
		facts.records = append(facts.records, r.Format())
	}
	for run := range v.Runs() {
		f := rejectedRunFacts{id: run.ID()}
		for r := range run.Records() {
			f.records = append(f.records, r.Format())
			if input, ok := r.UserInput(); ok {
				located, _ := v.UserInputByID(input.ID())
				f.inputs = append(f.inputs, located.ID())
				f.partCounts = append(f.partCounts, located.PartCount())
			}
		}
		facts.runs = append(facts.runs, f)
	}
	return facts
}
func assertRejected(t testing.TB, h *history.History, s *session.Session, c history.Commit, want error) {
	t.Helper()
	old := s.Snapshot()
	before := captureRejectedSnapshot(old)
	loaded, err := loadFixtureCommit(t, h, s, c, sessionPolicy())
	if loaded != nil || !errors.Is(err, want) {
		t.Fatalf("Load = %v, %v; want %v", loaded, err, want)
	}
	if !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) || !reflect.DeepEqual(before, captureRejectedSnapshot(old)) {
		t.Fatal("failed Load changed source session or old snapshot")
	}
}
