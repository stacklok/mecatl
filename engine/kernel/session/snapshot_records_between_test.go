package session_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyformat "github.com/stacklok/mecatl/engine/kernel/history/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestSnapshotRecordsBetweenValidation(t *testing.T) {
	s := boundSession(t, newSessionHistory(t), sessionPolicy())
	uncreated := s.Snapshot()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	first := submitBatch(t, s, b)
	pinned := s.Snapshot()
	b = beginBatch(t, s)
	batchRun(t, b)
	second := submitBatch(t, s, b)
	wrong := history.CommitRef{Sequence: first.Sequence, CID: contentid.Sum([]byte("not accepted"))}
	future := history.CommitRef{Sequence: math.MaxInt64, CID: first.CID}
	zero := history.CommitRef{}
	cases := []struct {
		name           string
		after, through history.CommitRef
		want           error
	}{
		{"zero through", zero, zero, codec.ErrInvalidData},
		{"zero through before membership", wrong, zero, codec.ErrInvalidData},
		{"after sequence only", history.CommitRef{Sequence: 1}, first, codec.ErrInvalidData},
		{"after CID only", history.CommitRef{CID: first.CID}, first, codec.ErrInvalidData},
		{"after negative", history.CommitRef{Sequence: -1, CID: first.CID}, first, codec.ErrInvalidData},
		{"after minimum", history.CommitRef{Sequence: math.MinInt64, CID: first.CID}, first, codec.ErrInvalidData},
		{"through sequence only", zero, history.CommitRef{Sequence: 1}, codec.ErrInvalidData},
		{"through CID only", zero, history.CommitRef{CID: first.CID}, codec.ErrInvalidData},
		{"through negative", zero, history.CommitRef{Sequence: -1, CID: first.CID}, codec.ErrInvalidData},
		{"through minimum", zero, history.CommitRef{Sequence: math.MinInt64, CID: first.CID}, codec.ErrInvalidData},
		{"through shape before after membership", wrong, history.CommitRef{Sequence: 1}, codec.ErrInvalidData},
		{"reversed", second, first, codec.ErrInvalidData},
		{"reversed before membership", future, wrong, codec.ErrInvalidData},
		{"wrong through CID", zero, wrong, session.ErrIdentity},
		{"wrong after CID", wrong, second, session.ErrIdentity},
		{"equal sequence wrong after CID", wrong, first, session.ErrIdentity},
		{"equal sequence wrong through CID", first, wrong, session.ErrIdentity},
		{"equal absent", wrong, wrong, session.ErrIdentity},
		{"extreme through", zero, future, session.ErrIdentity},
		{"extreme equal", future, future, session.ErrIdentity},
		{"valid genesis", zero, first, nil},
		{"valid equal", first, first, nil},
		{"future accepted through", zero, second, session.ErrIdentity},
		{"future accepted equal", second, second, session.ErrIdentity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seq, err := pinned.RecordsBetween(tc.after, tc.through)
			if !errors.Is(err, tc.want) || (seq == nil) != (tc.want != nil) {
				t.Fatalf("RecordsBetween = nil:%t, %v; want %v", seq == nil, err, tc.want)
			}
			if tc.want == nil {
				count := 0
				for range seq {
					count++
				}
				want := 1
				if tc.after == first {
					want = 0
				}
				if count != want {
					t.Fatalf("records = %d; want %d", count, want)
				}
			}
		})
	}
	for name, snapshot := range map[string]session.Snapshot{"zero": {}, "uncreated": uncreated} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range cases {
				want := tc.want
				if want == nil || want == session.ErrIdentity {
					want = session.ErrIdentity
				}
				seq, err := snapshot.RecordsBetween(tc.after, tc.through)
				if seq != nil || !errors.Is(err, want) {
					t.Fatalf("%s: RecordsBetween = nil:%t, %v; want %v", tc.name, seq == nil, err, want)
				}
			}
		})
	}
}

func TestSnapshotRecordsBetweenSubmitLoadAndBorrowing(t *testing.T) {
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	s := boundSession(t, h, sessionPolicy())
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input, err := b.AddUserInput(run, session.UserInput{Parts: append(byteParts([]byte{0xff, 0, 'x'}), byteParts([]byte("sibling"))...)})
	mustBatch(t, err)
	first := submitBatch(t, s, b)
	b = beginBatch(t, s)
	instructions := session.Instructions{Blocks: []session.InstructionBlock{{Parts: byteParts([]byte("instructions")), Source: session.ContentSource{Description: "source"}}}}
	mustBatch(t, b.ReplaceInstructions(scope, instructions))
	mustBatch(t, b.ReplaceInstructions(scope, session.Instructions{}))
	mustBatch(t, b.ReplaceToolCatalog(scope, session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "tool", ArgumentSchema: []byte("{}")}, {Name: "sibling", ArgumentSchema: []byte("true")}}}))
	mustBatch(t, b.ReplaceModelBinding(scope, session.ModelBinding{Route: "route", API: "api", Model: "model", Settings: []byte("{}")}))
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	nextRun := batchRun(t, b)
	nextInput := batchInput(t, b, nextRun, []byte("next"))
	second := submitBatch(t, s, b)
	b = beginBatch(t, s)
	batchInput(t, b, nextRun, []byte("last"))
	third := submitBatch(t, s, b)
	want := s.Snapshot()
	expected := make([]historyformat.Record, want.RecordCount())
	for i := range expected {
		expected[i] = want.RecordAt(i).Format()
	}
	reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
	loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), scope, sessionPolicy())
	mustBatch(t, err)
	reads, observes, appends := len(reader.reads), reader.observes, reader.appends
	for name, snapshot := range map[string]session.Snapshot{"submit": want, "load": loaded.Snapshot()} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				after, through history.CommitRef
				start, end     int
			}{
				{history.CommitRef{}, first, 0, 3},
				{first, second, 3, 10},
				{second, third, 10, 11},
				{history.CommitRef{}, third, 0, 11},
				{first, third, 3, 11},
				{second, second, 10, 10},
			} {
				seq, err := snapshot.RecordsBetween(tc.after, tc.through)
				mustBatch(t, err)
				if seq == nil {
					t.Fatal("nil successful iterator")
				}
				// Early stopping must not consume a cursor shared by later traversals.
				seen := 0
				seq(func(session.RecordView) bool { seen++; return false })
				if (tc.start < tc.end && seen != 1) || (tc.start == tc.end && seen != 0) {
					t.Fatal("iterator ignored early stop or yielded an empty range")
				}
				for range 2 {
					i := tc.start
					for record := range seq {
						if i >= tc.end || !record.Format().Equal(expected[i]) || record != snapshot.RecordAt(i) {
							t.Fatalf("record %d: range changed order, multiplicity or backing", i)
						}
						raw := record.Format()
						clear(raw.Payload)
						switch i {
						case 0:
							v, ok := record.Created()
							if !ok || v.HistoryID() != scope {
								t.Fatal("created scope")
							}
						case 1, 8:
							v, ok := record.RunStarted()
							id := run
							if i == 8 {
								id = nextRun
							}
							if !ok || v.ID() != id {
								t.Fatal("run scope")
							}
						case 2, 9:
							v, ok := record.UserInput()
							id := input
							if i == 9 {
								id = nextInput
							}
							if !ok || v.ID() != id {
								t.Fatal("input scope")
							}
							mutateMediaBytes(t, v.Parts().At(0))
							if i == 2 {
								leaf := mediaAt(t, v.Parts().At(0)).InlineData()
								var detached []byte
								if allocations := testing.AllocsPerRun(10, func() { detached = leaf.Copy() }); allocations != 1 || len(detached) != 3 {
									t.Fatalf("leaf copy: allocations=%g length=%d", allocations, len(detached))
								}
								clear(detached)
							}
						case 3, 4:
							v, ok := record.Instructions()
							if !ok || v.HistoryID() != scope || !instructionsMatch(v, []session.Instructions{instructions, {}}[i-3]) {
								t.Fatal("intermediate replacement lost")
							}
						case 5:
							v, ok := record.ToolCatalog()
							if !ok || v.HistoryID() != scope || v.Tools().Len() != 2 {
								t.Fatal("catalog scope")
							}
							clear(v.Tools().At(0).ArgumentSchema().Copy())
						case 6:
							v, ok := record.ModelBinding()
							if !ok || v.HistoryID() != scope || v.Model() != "model" {
								t.Fatal("binding scope")
							}
							clear(v.Settings().Copy())
						case 7:
							v, ok := record.RunEnded()
							if !ok || v.RunID() != run {
								t.Fatal("ended scope")
							}
						}
						i++
					}
					if i != tc.end {
						t.Fatalf("ended at %d; want %d", i, tc.end)
					}
				}
			}
			for i, record := range expected {
				if !snapshot.RecordAt(i).Format().Equal(record) {
					t.Fatalf("record %d changed after detached export mutation", i)
				}
			}
		})
	}
	if len(reader.reads) != reads || reader.observes != observes || reader.appends != appends {
		t.Fatal("range inspection performed backend I/O")
	}
}
