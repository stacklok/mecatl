package session_test

import (
	"bytes"
	"context"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// newIteratorSession provides explicit headroom for both large histories and
// large individual payloads. Conservative decoded-work accounting is per record.
func newIteratorSession(t testing.TB) (*history.History, *session.Session, session.Policy) {
	t.Helper()
	limits := codec.Limits{MaxBytes: 8 << 20, MaxStringBytes: 1 << 20, MaxByteStringBytes: 1 << 20,
		MaxArrayElements: 8192, MaxMapPairs: 32, MaxDepth: 32, MaxNodes: 1 << 20, MaxCollectionElements: 1 << 20}
	p := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 8192,
		SoftMaxRetainedRecords: 8192, MaxRetainedRecords: 8192, SoftMaxPayloadBytes: 32 << 20, MaxPayloadBytes: 32 << 20,
		SoftMaxDecodedWork: 1 << 38, MaxDecodedWork: 1 << 38}
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	mustBatch(t, err)
	return h, boundSession(t, h, p), p
}

// iteratorFixture grows content collections and histories linearly, not as a
// blocks-by-parts product. The first block/inline has N parts; the others have
// one. Runs have N entries (at least one); active/snapshot records have N/3N
// entries plus lifecycle/context overhead. Payload size is a separate axis.
func iteratorFixture(t testing.TB, count, payloadBytes int) map[string]*session.Session {
	t.Helper()
	h, s, p := newIteratorSession(t)
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	for i := 1; i < count; i++ {
		batchRun(t, b)
		mustBatch(t, b.Interrupt())
	}
	run := batchRun(t, b)
	parts := make([]session.ContentPart, count)
	blocks := make([]session.InstructionBlock, count)
	items := make([]session.InputItem, count)
	tools := make([]session.ToolDefinition, count)
	for i := range count {
		parts[i] = session.ContentPart{Media: &session.Media{Kind: session.MediaKindDocument,
			MIMEType: "application/octet-stream", Filename: "part-" + strconv.Itoa(i), Source: session.MediaSourceInline,
			InlineData: bytes.Repeat([]byte{0xa5}, payloadBytes)}}
		source := session.ContentSource{Description: "source-" + strconv.Itoa(i)}
		blocks[i] = session.InstructionBlock{Parts: parts[i : i+1], Source: source}
		items[i] = session.InputItem{Inline: &session.AttributedInput{Actor: session.Actor("actor-" + strconv.Itoa(i)),
			Purpose: session.PromptContext, Parts: parts[i : i+1], Source: source}}
		tools[i] = session.ToolDefinition{Name: "tool-" + strconv.Itoa(i), ArgumentSchema: []byte(`"` + string(bytes.Repeat([]byte{'s'}, payloadBytes)) + `"`)}
	}
	if count > 0 {
		blocks[0].Parts = parts
		items[0].Inline.Parts = parts
	}
	_, err := b.AddUserInput(session.AddUserInputRequest{RunID: run, Parts: parts})
	mustBatch(t, err)
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope, Blocks: blocks}))
	mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: scope, Tools: tools}))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: items}))
	for i := 1; i < count; i++ {
		mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run}))
	}
	if count == 0 {
		// Retain present empty children before clearing the selected root again.
		source := session.ContentSource{Description: "empty"}
		mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Source: source}}}))
		mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{Inline: &session.AttributedInput{Actor: "empty", Purpose: session.PromptContext, Source: source}}}}))
		mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope}))
	}
	submitBatch(t, s, b)
	return iteratorSessions(t, h, s, p)
}

// iteratorSessions independently reconstructs the current head at the public
// admission seams. These fixtures contain at most two commits.
func iteratorSessions(t testing.TB, h *history.History, s *session.Session, p session.Policy) map[string]*session.Session {
	t.Helper()
	scope := s.Snapshot().HistoryID()
	applied := boundSession(t, h, p)
	page, err := h.Read(context.Background(), scope, history.ReadRequest{Through: s.Snapshot().Head(), MaxCommits: 2})
	mustBatch(t, err)
	if len(page.Commits) == 0 || len(page.Commits) != int(s.Snapshot().Head().Sequence) {
		t.Fatal("missing iterator fixture commits")
	}
	for _, commit := range page.Commits {
		mustBatch(t, applied.Apply(commit))
	}
	loaded, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	return map[string]*session.Session{"Submit": s, "Apply": applied, "Load": loaded}
}

// iteratorRecordFixture isolates record counts from run/context collection sizes.
// Snapshot is captured at exactly count records, then one more fact makes its
// active run exactly count records. Both bounds are reconstructed at every seam.
func iteratorRecordFixture(t testing.TB, count int) map[string]iteratorViews {
	t.Helper()
	h, s, p := newIteratorSession(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	if count > 1 {
		run := batchRun(t, b)
		for i := 2; i < count; i++ {
			mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run}))
		}
	}
	submitBatch(t, s, b)
	views := make(map[string]iteratorViews, 3)
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		views[seam] = iteratorViews{snapshot: candidate.Snapshot()}
	}
	b = beginBatch(t, s)
	if count == 1 {
		batchRun(t, b)
	} else {
		mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: s.Snapshot().ActiveRun()}))
	}
	submitBatch(t, s, b)
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		v := views[seam]
		v.run = candidate.Snapshot().RunAt(0)
		if v.snapshot.RecordCount() != count || v.run.RecordCount() != count {
			t.Fatal("record fixture has wrong bounds")
		}
		views[seam] = v
	}
	return views
}

type iteratorViews struct {
	snapshot     session.Snapshot
	run          session.RunView
	user         session.UserInputView
	instructions session.InstructionsView
	block        session.InstructionBlockView
	catalog      session.ToolCatalogView
	effective    session.EffectiveInputView
	inline       session.AttributedInputView
}

func iteratorViewSet(t testing.TB, s *session.Session) iteratorViews {
	t.Helper()
	v := iteratorViews{snapshot: s.Snapshot()}
	v.run = v.snapshot.RunAt(v.snapshot.RunCount() - 1)
	v.user = lookupInput(t, v.snapshot, v.run.ID(), 1)
	v.instructions, _ = v.snapshot.Instructions()
	v.catalog, _ = v.snapshot.ToolCatalog()
	// The active run starts with start, authored input, instructions, catalog,
	// and the populated effective input; later effective inputs are empty.
	v.effective, _ = v.run.RecordAt(4).EffectiveInput()
	for i := 0; i < v.run.RecordCount(); i++ {
		record := v.run.RecordAt(i)
		if instructions, ok := record.Instructions(); ok && instructions.BlockCount() > 0 {
			v.block = instructions.BlockAt(0)
		}
		if effective, ok := record.EffectiveInput(); ok && effective.InputCount() > 0 {
			v.inline, _ = effective.InputAt(0).Inline()
		}
	}
	return v
}

// sequenceContract captures once, so all checks also exercise stored sequences.
// Comparing full handles checks scope and identity as well as order. RunView's
// slice-backed bounds are compared too, without requiring public equality APIs.
func sequenceContract[T any](t testing.TB, seq iter.Seq[T], count int, at func(int) T) func() {
	t.Helper()
	want := make([]T, count)
	for i := range count {
		want[i] = at(i)
	}
	equal := func(a, b T) bool {
		if reflect.TypeFor[T]().Comparable() {
			return reflect.ValueOf(a).Equal(reflect.ValueOf(b))
		}
		return reflect.DeepEqual(a, b) // RunView additionally carries slice bounds.
	}
	return func() {
		t.Helper()
		for range 2 {
			got := slices.Collect(seq)
			if !slices.EqualFunc(got, want, equal) {
				t.Errorf("sequence differs from indexed access: length %d, want %d", len(got), count)
			}
		}
		calls := 0
		seq(func(value T) bool {
			calls++
			if calls > 1 || count == 0 || !equal(value, want[0]) {
				t.Error("yield continued after false or returned wrong first value")
			}
			return false
		})
		if calls != min(count, 1) {
			t.Errorf("false yield called %d times", calls)
		}
		calls = 0
		for range seq {
			calls++
			break
		}
		if calls != min(count, 1) {
			t.Errorf("early break yielded %d times", calls)
		}
		outer := 0
		for a := range seq {
			inner := 0
			for b := range seq {
				if inner >= count || !equal(b, want[inner]) {
					t.Error("nested traversal changed order")
				}
				inner++
			}
			if inner != count || outer >= count || !equal(a, want[outer]) {
				t.Error("nested traversal changed outer state")
			}
			outer++
		}
		if outer != count {
			t.Error("nested traversal changed count")
		}
	}
}

func iteratorContracts(t testing.TB, v iteratorViews) []func() {
	t.Helper()
	return []func(){
		sequenceContract(t, v.user.Parts(), v.user.PartCount(), v.user.PartAt),
		sequenceContract(t, v.inline.Parts(), v.inline.PartCount(), v.inline.PartAt),
		sequenceContract(t, v.block.Parts(), v.block.PartCount(), v.block.PartAt),
		sequenceContract(t, v.instructions.Blocks(), v.instructions.BlockCount(), v.instructions.BlockAt),
		sequenceContract(t, v.effective.Inputs(), v.effective.InputCount(), v.effective.InputAt),
		sequenceContract(t, v.catalog.Tools(), v.catalog.ToolCount(), v.catalog.ToolAt),
		sequenceContract(t, v.run.Records(), v.run.RecordCount(), v.run.RecordAt),
		sequenceContract(t, v.snapshot.Records(), v.snapshot.RecordCount(), v.snapshot.RecordAt),
		sequenceContract(t, v.snapshot.Runs(), v.snapshot.RunCount(), v.snapshot.RunAt),
	}
}

func TestViewIteratorsContract(t *testing.T) {
	for _, check := range iteratorContracts(t, iteratorViews{}) {
		check()
	}
	for seam, v := range iteratorRecordFixture(t, 1) {
		t.Run("minimal-history/"+seam, func(t *testing.T) {
			sequenceContract(t, v.snapshot.Records(), 1, v.snapshot.RecordAt)()
			sequenceContract(t, v.snapshot.Runs(), 0, v.snapshot.RunAt)()
			sequenceContract(t, v.run.Records(), 1, v.run.RecordAt)()
		})
	}
	uncreated := newSession(t, sessionPolicy()).Snapshot()
	sequenceContract(t, uncreated.Records(), 0, uncreated.RecordAt)()
	sequenceContract(t, uncreated.Runs(), 0, uncreated.RunAt)()
	for _, count := range []int{0, 3} {
		for seam, s := range iteratorFixture(t, count, 32) {
			t.Run(strconv.Itoa(count)+"/"+seam, func(t *testing.T) {
				v := iteratorViewSet(t, s)
				if v.instructions.HistoryID() != v.snapshot.HistoryID() || v.catalog.HistoryID() != v.snapshot.HistoryID() || v.effective.RunID() != v.run.ID() {
					t.Fatal("fixture must retain accepted roots, including explicit emptiness")
				}
				if count == 0 && (v.block.Source().Description() != "empty" || v.inline.Actor() != "empty" || v.user.ID() == (session.UserInputID{})) {
					t.Fatal("empty fixture must contain accepted, present empty children")
				}
				for _, check := range iteratorContracts(t, v) {
					check()
				}
			})
		}
	}
}

func TestViewIteratorsPinnedConcurrentReaders(t *testing.T) {
	for _, seam := range []string{"Submit", "Apply", "Load"} {
		t.Run(seam, func(t *testing.T) {
			s := iteratorFixture(t, 3, 32)[seam]
			old := iteratorViewSet(t, s)
			checks := iteratorContracts(t, old)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for range 4 {
				wg.Go(func() {
					<-start
					for range 20 {
						for _, check := range checks {
							check()
						}
					}
				})
			}
			close(start)
			for range 16 {
				b := beginBatch(t, s)
				_, err := b.AddUserInput(session.AddUserInputRequest{RunID: old.run.ID(), Parts: byteParts([]byte("later"))})
				mustBatch(t, err)
				mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: old.snapshot.HistoryID()}))
				mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: old.snapshot.HistoryID()}))
				mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: old.run.ID()}))
				submitBatch(t, s, b)
			}
			b := beginBatch(t, s)
			mustBatch(t, b.Interrupt())
			newRun := batchRun(t, b)
			_, err := b.AddUserInput(session.AddUserInputRequest{RunID: newRun})
			mustBatch(t, err)
			submitBatch(t, s, b)
			wg.Wait()
			for _, check := range checks {
				check()
			}
			current := s.Snapshot()
			instructions, _ := current.Instructions()
			catalog, _ := current.ToolCatalog()
			media, _ := old.user.PartAt(0).Media()
			if current.RunCount() != old.snapshot.RunCount()+1 || current.RunAt(2).RecordCount() <= old.run.RecordCount() || instructions.BlockCount() != 0 || catalog.ToolCount() != 0 || media.Filename() != "part-0" || old.inline.Actor() != "actor-0" || old.catalog.ToolAt(0).Name() != "tool-0" || old.block.Source().Description() != "source-0" {
				t.Fatal("replacement/append did not preserve captured content and bounds")
			}
		})
	}
}
