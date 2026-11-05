package session_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContextValuesIntegrationAndPinnedViews(t *testing.T) {
	backend := mem.New()
	p := sessionPolicy()
	// Nested harness content adds an owning prompt level relative to authored input.
	p.PayloadLimits.MaxDepth = 16
	p.PayloadLimits.MaxCollectionElements = 512
	p.PayloadLimits.MaxNodes = 2048
	p.PayloadLimits.MaxBytes = 32768
	// The envelope retains payloads as bytes, so its independent byte ceilings grow too.
	p.CommitLimits.MaxByteStringBytes = 32768
	p.CommitLimits.MaxBytes = 65536
	p.MaxPayloadBytes = 1 << 20
	p.SoftMaxPayloadBytes = 1 << 20
	// History uses the same explicit envelope policy, not ambient defaults.
	h, err := history.New(backend, history.Policy{CommitLimits: p.CommitLimits, RetryWindow: 16, MaxPageBytes: p.CommitLimits.MaxBytes, MaxPageCommits: 2})
	mustBatch(t, err)
	s := boundSession(t, h, p)
	scope := s.Snapshot().HistoryID()
	if _, ok := s.Snapshot().Instructions(); ok {
		t.Fatal("absent instructions present")
	}
	if _, ok := s.Snapshot().ToolCatalog(); ok {
		t.Fatal("absent catalog present")
	}
	if _, ok := s.Snapshot().ModelBinding(); ok {
		t.Fatal("absent binding present")
	}
	source := session.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}}
	instructions := session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Content: session.Content{{Kind: session.ContentText, Text: "root\n"}}, Source: source}}}
	catalog := session.ToolCatalog{HistoryID: scope, Tools: []session.ToolDefinition{{Name: "read", Description: "read file", ArgumentSchema: []byte(" {\"type\": \"object\"} ")}, {Name: "noop", ArgumentSchema: []byte("true")}}}
	binding := session.ModelBinding{HistoryID: scope, Route: "route", API: "dialect/v1", Model: "alias", Settings: []byte(" {\"temperature\": 1.00} ")}
	wantInstructions, wantCatalog, wantBinding := instructions.Clone(), catalog.Clone(), binding.Clone()
	if instructions.Validate() != nil || catalog.Validate() != nil || binding.Validate() != nil || !instructions.Clone().Equal(instructions) || !catalog.Clone().Equal(catalog) || !binding.Clone().Equal(binding) {
		t.Fatal("valid context values")
	}
	b := beginBatch(t, s)
	if err := b.ReplaceInstructions(instructions); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("before creation: %v", err)
	}
	mustBatch(t, b.Create())
	if err := b.ReplaceToolCatalog(catalog); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("idle catalog: %v", err)
	}
	run := batchRun(t, b)
	messages := []session.AuthoredMessage{{Content: richContent()}, {Content: session.Content{{Kind: session.ContentText, Text: "second message"}}}}
	authored := session.UserInput{Messages: messages}
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Messages: messages})
	mustBatch(t, err)
	authored.ID = input
	authored = authored.Clone()
	mustBatch(t, b.ReplaceInstructions(instructions))
	mustBatch(t, b.ReplaceToolCatalog(catalog))
	mustBatch(t, b.ReplaceModelBinding(binding))
	index := uint64(1)
	effective := session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: &index}, {Harness: &session.HarnessPrompt{Purpose: session.PromptInstructions, Content: session.Content{{Kind: session.ContentText, Text: "skill expansion"}}, Source: session.ContentSource{Description: "skill example expansion", WorkspaceFile: &session.WorkspaceFileSource{Path: "skills/example.md"}}}}, {Harness: &session.HarnessPrompt{Purpose: session.PromptContext, Content: session.Content{{Kind: session.ContentText, Text: "context data"}}, Source: session.ContentSource{Description: "workspace design reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}}}}}
	wantEffective := effective.Clone()
	if effective.Validate() != nil || !effective.Clone().Equal(effective) {
		t.Fatal("effective value")
	}
	mustBatch(t, b.AddEffectiveInput(effective))
	// Every caller-owned mutable level may change after addition, before submission.
	messages[0].Content[1].Media.Source.Inline.Data[0] = 0
	messages[0].Content[0].Text = "changed"
	messages[0].Content[2].Media.Source.Remote.URI = "urn:changed"
	messages[0].Content[3].Media.Source.Provider.Scope = "changed"
	messages[0].Content[5].Media.Source.Inline.Data = []byte("not empty")
	instructions.Blocks[0].Source.WorkspaceFile.Path = "changed"
	instructions.Blocks[0].Content[0].Text = "changed"
	clear(catalog.Tools[0].ArgumentSchema)
	catalog.Tools[1].Name = "changed"
	clear(binding.Settings)
	index = math.MaxUint64
	effective.Prompts[1].Harness.Source.WorkspaceFile.Path = "changed"
	effective.Prompts[2].Harness.Content[0].Text = "changed"
	submitBatch(t, s, b)
	old := s.Snapshot()
	iv, ok := old.Instructions()
	if !ok || iv.HistoryID() != scope || !iv.Value().Equal(wantInstructions) {
		t.Fatal("instructions view")
	}
	tv, ok := old.ToolCatalog()
	if !ok || tv.HistoryID() != scope || !tv.Value().Equal(wantCatalog) {
		t.Fatal("catalog view")
	}
	mv, ok := old.ModelBinding()
	if !ok || mv.HistoryID() != scope || !mv.Value().Equal(wantBinding) {
		t.Fatal("binding view")
	}
	ev, ok := old.RecordAt(6).EffectiveInput()
	if !ok || ev.InputID() != input || !ev.Value().Equal(wantEffective) {
		t.Fatal("effective view")
	}
	if !authored.Equal(session.UserInput{ID: old.RunAt(0).InputAt(0).ID(), Messages: old.RunAt(0).InputAt(0).Messages()}) {
		t.Fatal("authored rich data changed")
	}
	// Exports, including DTO exports, never lend accepted mutable storage.
	iv.Value().Blocks[0].Source.WorkspaceFile.Path = "export mutation"
	iv.Format().Blocks[0].Content[0].Text = "export mutation"
	tv.Value().Tools[0].ArgumentSchema[0] = 0
	tv.Format().Tools[0].ArgumentSchema[0] = 0
	mv.Value().Settings[0] = 0
	mv.Format().Settings[0] = 0
	ev.Value().Prompts[0].AuthoredMessage = new(uint64)
	ev.Format().Prompts[1].Harness.Source.WorkspaceFile.Path = "export mutation"
	if !iv.Value().Equal(wantInstructions) || !tv.Value().Equal(wantCatalog) || !mv.Value().Equal(wantBinding) || !ev.Value().Equal(wantEffective) {
		t.Fatal("view export aliases")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope}))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: input}))
	mustBatch(t, b.Interrupt())
	batchRun(t, b)
	submitBatch(t, s, b)
	next := s.Snapshot()
	selected, present := next.Instructions()
	if !present || len(selected.Value().Blocks) != 0 || !iv.Value().Equal(wantInstructions) {
		t.Fatal("explicit empty or old selection lost")
	}
	unchangedCatalog, _ := next.ToolCatalog()
	unchangedBinding, _ := next.ModelBinding()
	if unchangedCatalog != tv || unchangedBinding != mv {
		t.Fatal("unmodified components were copied or lost across runs")
	}
	if old.RunAt(0).RecordCount() != 6 || next.RunAt(0).RecordCount() != 9 {
		t.Fatal("run chronology incorrectly filters session-scoped facts")
	}
	if _, ok := old.RecordAt(2).Instructions(); ok {
		t.Fatal("wrong-kind access")
	}
	if _, ok := old.RecordAt(3).EffectiveInput(); ok {
		t.Fatal("wrong-kind effective access")
	}
	emptyRevision, ok := next.RecordAt(8).EffectiveInput()
	if !ok || len(emptyRevision.Value().Prompts) != 0 || emptyRevision.InputID() != input {
		t.Fatal("explicit empty revision lost")
	}
	loaded, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	if !reflect.DeepEqual(next, loaded.Snapshot()) {
		t.Fatal("replay changed rich data or selected pointers")
	}
	// The earlier history remains pinned, including every source explanation.
	if !ev.Value().Equal(wantEffective) || old.RecordCount() != 7 {
		t.Fatal("old handle changed")
	}
}

func TestContextAdmissionSourceBoundsAndScopes(t *testing.T) {
	h := newLifecycleHistory(t, mem.New())
	s := boundSession(t, h, sessionPolicy())
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	if err := b.ReplaceInstructions(session.Instructions{HistoryID: foreign}); !errors.Is(err, session.ErrIdentity) {
		t.Fatalf("foreign context: %v", err)
	}
	source := session.InputID{RunID: run, Ordinal: 1}
	zero := uint64(0)
	huge := uint64(math.MaxUint64)
	if err := b.AddEffectiveInput(session.EffectiveInput{InputID: source}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("forward source: %v", err)
	}
	for _, tc := range []struct {
		name string
		id   session.InputID
		want error
	}{
		{"nonexistent", session.InputID{RunID: run, Ordinal: 99}, session.ErrTransition},
		{"wrong run", session.InputID{RunID: session.RunID{HistoryID: scope, Ordinal: 2}, Ordinal: 1}, session.ErrTransition},
		{"foreign source", session.InputID{RunID: session.RunID{HistoryID: foreign, Ordinal: 1}, Ordinal: 1}, session.ErrIdentity},
	} {
		if err := b.AddEffectiveInput(session.EffectiveInput{InputID: tc.id}); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Messages: []session.AuthoredMessage{{}}})
	mustBatch(t, err)
	for _, index := range []*uint64{new(uint64(1)), &huge} {
		if err := b.AddEffectiveInput(session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: index}}}); !errors.Is(err, session.ErrTransition) {
			t.Fatalf("message bounds: %v", err)
		}
	}
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: &zero}}}))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: input}))
	submitBatch(t, s, b)
	// Resolve an accepted source using existing locators, not only same-batch scans.
	b = beginBatch(t, s)
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: &zero}}}))
	mustBatch(t, b.Interrupt())
	batchRun(t, b)
	if err := b.AddEffectiveInput(session.EffectiveInput{InputID: input}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("inactive source: %v", err)
	}
	empty, err := b.AddInput(session.AddInputRequest{RunID: session.RunID{HistoryID: scope, Ordinal: 2}})
	mustBatch(t, err)
	if err := b.AddEffectiveInput(session.EffectiveInput{InputID: empty, Prompts: []session.PreparedPrompt{{AuthoredMessage: &zero}}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("missing message synthesized: %v", err)
	}
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: empty}))
	submitBatch(t, s, b)
	loaded, err := session.Load(context.Background(), h, scope, sessionPolicy())
	mustBatch(t, err)
	if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) || len(loaded.Snapshot().RunAt(0).InputAt(0).Messages()) != 1 || len(loaded.Snapshot().RunAt(1).InputAt(0).Messages()) != 0 {
		t.Fatal("empty message boundary collapsed or rejected additions leaked")
	}
}

func TestContextMixedReferenceBoundsBeforeAppend(t *testing.T) {
	for _, acceptedSource := range []bool{false, true} {
		for _, indexes := range [][]uint64{{0, 3}, {3, 0}, {0, math.MaxUint64}, {math.MaxUint64, 0}} {
			t.Run(fmt.Sprintf("accepted=%t/indexes=%v", acceptedSource, indexes), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				submitBatch(t, s, b)
				b = beginBatch(t, s)
				input, err := b.AddInput(session.AddInputRequest{RunID: run, Messages: []session.AuthoredMessage{{}, {}, {}}})
				mustBatch(t, err)
				if acceptedSource {
					submitBatch(t, s, b)
					b = beginBatch(t, s)
				}
				before := captureRejectedSnapshot(s.Snapshot())
				attempts := len(backend.attempts)
				// The greatest reference is neither first nor last. Preserve this
				// addition when the following mixed-reference revision is rejected.
				valid := session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: new(uint64(0))}, {AuthoredMessage: new(uint64(2))}, {AuthoredMessage: new(uint64(1))}}}
				mustBatch(t, b.AddEffectiveInput(valid))
				invalid := session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: &indexes[0]}, {AuthoredMessage: &indexes[1]}}}
				if err := b.AddEffectiveInput(invalid); !errors.Is(err, session.ErrTransition) {
					t.Fatalf("mixed message bounds must fail at Add, before acceptance: %v", err)
				}
				if len(backend.attempts) != attempts || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
					t.Fatal("addition appended or changed accepted state")
				}
				assertHistoryHead(t, h, s.Snapshot().Head())
				mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: input}))
				submitBatch(t, s, b)
				if len(backend.attempts) != attempts+1 || s.Snapshot().RecordCount() != 5 {
					t.Fatal("rejected revision leaked or prior additions were lost")
				}
				first, ok := s.Snapshot().RecordAt(3).EffectiveInput()
				last, lastOK := s.Snapshot().RecordAt(4).EffectiveInput()
				if !ok || !first.Value().Equal(valid) || !lastOK || !last.Value().Equal(session.EffectiveInput{InputID: input}) || len(s.Snapshot().RunAt(0).InputAt(0).Messages()) != 3 {
					t.Fatal("submission did not preserve exact prior additions/source")
				}
				loaded, err := session.Load(context.Background(), h, input.RunID.HistoryID, sessionPolicy())
				mustBatch(t, err)
				if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
					t.Fatal("submission accepted a revision that replay rejects")
				}
			})
		}
	}
}

func TestContextMixedReferenceBoundsExternalApplyAndLoad(t *testing.T) {
	for _, acceptedSource := range []bool{false, true} {
		for _, indexes := range [][]uint64{{0, 2, 1}, {0, 3}, {3, 0}, {0, math.MaxUint64}, {math.MaxUint64, 0}} {
			t.Run(fmt.Sprintf("accepted=%t/indexes=%v", acceptedSource, indexes), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				input := session.UserInput{ID: session.InputID{RunID: run, Ordinal: 1}, Messages: []session.AuthoredMessage{{}, {}, {}}}
				if acceptedSource {
					id, err := b.AddInput(session.AddInputRequest{RunID: run, Messages: input.Messages})
					mustBatch(t, err)
					if id != input.ID {
						t.Fatal("source identity")
					}
				}
				submitBatch(t, s, b)
				var records []session.Record
				if !acceptedSource {
					r, err := session.NewUserInputRecord(input, testLimits())
					mustBatch(t, err)
					records = append(records, r)
				}
				prior := session.EffectiveInput{InputID: input.ID, Prompts: []session.PreparedPrompt{{AuthoredMessage: new(uint64(2))}}}
				r, err := session.NewEffectiveInputRecord(prior, testLimits())
				mustBatch(t, err)
				records = append(records, r)
				effective := session.EffectiveInput{InputID: input.ID}
				valid := true
				for _, index := range indexes {
					effective.Prompts = append(effective.Prompts, session.PreparedPrompt{AuthoredMessage: new(index)})
					valid = valid && index < uint64(len(input.Messages))
				}
				r, err = session.NewEffectiveInputRecord(effective, testLimits())
				mustBatch(t, err)
				records = append(records, r)
				c := proposedCommit(t, s, records...)
				before := captureRejectedSnapshot(s.Snapshot())
				attempts := len(backend.attempts)
				err = s.Apply(c)
				if valid {
					mustBatch(t, err)
					if s.Snapshot().RecordCount() != 5 {
						t.Fatal("valid mixed references lost records")
					}
					got, ok := s.Snapshot().RecordAt(4).EffectiveInput()
					if !ok || !got.Value().Equal(effective) {
						t.Fatal("valid mixed references changed")
					}
				} else if !errors.Is(err, session.ErrTransition) || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
					t.Fatalf("invalid external Apply published partial state or wrong error: %v", err)
				}
				if len(backend.attempts) != attempts {
					t.Fatal("Apply appended")
				}
				// History accepts opaque session payloads; fresh Load must enforce
				// every reference independently of the local constructor's metadata.
				result, err := h.Append(context.Background(), run.HistoryID, c.Commit)
				mustBatch(t, err)
				if result.Status != history.StatusAccepted || result.Proposal != c.Ref {
					t.Fatal("external append")
				}
				reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
				loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), run.HistoryID, sessionPolicy())
				if valid {
					mustBatch(t, err)
					if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
						t.Fatal("valid external replay differs")
					}
				} else if loaded != nil || !errors.Is(err, session.ErrTransition) {
					t.Fatalf("invalid external Load = %v, %v", loaded, err)
				}
				if reader.appends != 0 || len(backend.attempts) != attempts+1 {
					t.Fatal("Load attempted to repair history")
				}
			})
		}
	}
}

func TestNewRecordExactVersionsAndZeroViews(t *testing.T) {
	scope := testHistoryID(t)
	input := session.InputID{RunID: session.RunID{HistoryID: scope, Ordinal: 1}, Ordinal: 1}
	for _, makeRecord := range []func() (session.Record, error){
		func() (session.Record, error) {
			return session.NewInstructionsRecord(session.Instructions{HistoryID: scope}, testLimits())
		},
		func() (session.Record, error) {
			return session.NewToolCatalogRecord(session.ToolCatalog{HistoryID: scope}, testLimits())
		},
		func() (session.Record, error) {
			return session.NewModelBindingRecord(session.ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte("{}")}, testLimits())
		},
		func() (session.Record, error) {
			return session.NewEffectiveInputRecord(session.EffectiveInput{InputID: input}, testLimits())
		},
		func() (session.Record, error) {
			return session.NewUserInputRecord(session.UserInput{ID: input}, testLimits())
		},
	} {
		r, err := makeRecord()
		mustBatch(t, err)
		got, err := session.DecodeRecord(scope, r.Format(), testLimits())
		mustBatch(t, err)
		if !got.Format().Equal(r.Format()) {
			t.Fatal("record roundtrip")
		}
		dto := r.Format()
		dto.KindVersion++
		if got, err := session.DecodeRecord(scope, dto, testLimits()); !errors.Is(err, session.ErrUnsupportedRecord) || !reflect.DeepEqual(got, session.Record{}) {
			t.Fatalf("unsupported version: %v", err)
		}
		if dto.Kind == sessionformat.KindUserInput {
			dto.KindVersion = 1
			if _, err := session.DecodeRecord(scope, dto, testLimits()); !errors.Is(err, session.ErrUnsupportedRecord) {
				t.Fatalf("input v1 accepted: %v", err)
			}
		}
	}
	var r session.RecordView
	if v, ok := r.Instructions(); ok || v.HistoryID() != (historyid.HistoryID{}) || !reflect.DeepEqual(v.Value(), session.Instructions{}) || !reflect.DeepEqual(v.Format(), sessionformat.Instructions{}) {
		t.Fatal("zero instructions")
	}
	if v, ok := r.ToolCatalog(); ok || v.HistoryID() != (historyid.HistoryID{}) || !reflect.DeepEqual(v.Value(), session.ToolCatalog{}) || !reflect.DeepEqual(v.Format(), sessionformat.ToolCatalog{}) {
		t.Fatal("zero catalog")
	}
	if v, ok := r.ModelBinding(); ok || v.HistoryID() != (historyid.HistoryID{}) || !reflect.DeepEqual(v.Value(), session.ModelBinding{}) || !reflect.DeepEqual(v.Format(), sessionformat.ModelBinding{}) {
		t.Fatal("zero binding")
	}
	if v, ok := r.EffectiveInput(); ok || v.InputID() != (session.InputID{}) || !reflect.DeepEqual(v.Value(), session.EffectiveInput{}) || !reflect.DeepEqual(v.Format(), sessionformat.EffectiveInput{}) {
		t.Fatal("zero effective input")
	}
}
