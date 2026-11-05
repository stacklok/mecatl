package session_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContextValuesIntegrationAndPinnedViews(t *testing.T) {
	backend := mem.New()
	p := sessionPolicy()
	p.PayloadLimits.MaxDepth = 16
	p.PayloadLimits.MaxCollectionElements = 512
	p.PayloadLimits.MaxNodes = 2048
	p.PayloadLimits.MaxBytes = 32768
	p.CommitLimits.MaxByteStringBytes = 32768
	p.CommitLimits.MaxBytes = 65536
	p.MaxPayloadBytes, p.SoftMaxPayloadBytes = 1<<20, 1<<20
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
	instructions := session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Content: []session.ContentPart{{Kind: session.ContentText, Text: "root\n"}}, Source: source}}}
	catalog := session.ToolCatalog{HistoryID: scope, Tools: []session.ToolDefinition{{Name: "read", Description: "read file", ArgumentSchema: []byte(" {\"type\": \"object\"} ")}, {Name: "noop", ArgumentSchema: []byte("true")}}}
	binding := session.ModelBinding{HistoryID: scope, Route: "route", API: "dialect/v1", Model: "alias", Settings: []byte(" {\"temperature\": 1.00} ")}
	wantI, wantT, wantM := instructions.Clone(), catalog.Clone(), binding.Clone()
	if instructions.Validate() != nil || catalog.Validate() != nil || binding.Validate() != nil {
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
	parts := richContent()
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Parts: parts})
	mustBatch(t, err)
	second, err := b.AddInput(session.AddInputRequest{RunID: run, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "second submission"}}})
	mustBatch(t, err)
	authored := session.UserInput{ID: input, Parts: parts}.Clone()
	mustBatch(t, b.ReplaceInstructions(instructions))
	mustBatch(t, b.ReplaceToolCatalog(catalog))
	mustBatch(t, b.ReplaceModelBinding(binding))
	effective := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{
		{UserInput: &input},
		{Inline: &session.AttributedInput{Actor: "skill/example", Purpose: session.PromptInstructions, Parts: richContent(), Source: session.ContentSource{Description: "skill expansion", WorkspaceFile: &session.WorkspaceFileSource{Path: "skills/example.md"}}}},
		{UserInput: &second}, {UserInput: &input},
		{Inline: &session.AttributedInput{Actor: " \textension/尾 e\u0301\r\n ", Purpose: session.PromptContext, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "context data"}}, Source: session.ContentSource{Description: "workspace reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}}},
	}}
	wantE := effective.Clone()
	if effective.Validate() != nil || !effective.Clone().Equal(effective) {
		t.Fatal("effective value")
	}
	mustBatch(t, b.AddEffectiveInput(effective))
	parts[1].Media.Data[0] = 0
	parts[1].Media.Format = "changed"
	parts[1].Media.Filename = "changed"
	parts[1].Media.Title = "changed"
	parts[0].Text = "changed"
	parts[2].Media.URI = "urn:changed"
	parts[3].Media.Scope = "changed"
	parts[5].Media.Data = []byte("not empty")
	instructions.Blocks[0].Source.WorkspaceFile.Path = "changed"
	instructions.Blocks[0].Content[0].Text = "changed"
	clear(catalog.Tools[0].ArgumentSchema)
	catalog.Tools[1].Name = "changed"
	clear(binding.Settings)
	effective.Inputs[0].UserInput.Ordinal = math.MaxUint64
	effective.Inputs[1].Inline.Parts[1].Media.Data[0] = 0
	effective.Inputs[1].Inline.Source.WorkspaceFile.Path = "changed"
	effective.Inputs[4].Inline.Parts[0].Text = "changed"
	effective.Inputs[4].Inline.Actor = "changed"
	submitBatch(t, s, b)
	old := s.Snapshot()
	iv, ok := old.Instructions()
	if !ok || iv.HistoryID() != scope || !iv.Value().Equal(wantI) {
		t.Fatal("instructions view")
	}
	tv, ok := old.ToolCatalog()
	if !ok || tv.HistoryID() != scope || !tv.Value().Equal(wantT) {
		t.Fatal("catalog view")
	}
	mv, ok := old.ModelBinding()
	if !ok || mv.HistoryID() != scope || !mv.Value().Equal(wantM) {
		t.Fatal("binding view")
	}
	ev, ok := old.RecordAt(7).EffectiveInput()
	if !ok || ev.RunID() != run || !ev.Value().Equal(wantE) {
		t.Fatal("effective view")
	}
	uv := old.RunAt(0).InputAt(0)
	if !authored.Equal(session.UserInput{ID: uv.ID(), Parts: uv.Parts()}) {
		t.Fatal("authored rich data changed")
	}
	// Independent literals catch lossy conversion even when Clone and Equal use
	// that same conversion. Recheck public exports at each real acceptance seam.
	assertExactContent := func(snapshot session.Snapshot) {
		t.Helper()
		input := snapshot.RunAt(0).InputAt(0)
		increment, ok := snapshot.RecordAt(7).EffectiveInput()
		if !ok {
			t.Fatal("missing effective input")
		}
		items := increment.Value().Inputs
		if len(items) != 5 || items[1].Inline == nil || items[4].Inline == nil {
			t.Fatal("lost attributed item boundaries")
		}
		if items[1].Inline.Actor != "skill/example" || items[4].Inline.Actor != " \textension/尾 e\u0301\r\n " {
			t.Fatalf("actors changed: %q, %q", items[1].Inline.Actor, items[4].Inline.Actor)
		}
		for _, parts := range [][]session.ContentPart{input.Parts(), items[1].Inline.Parts} {
			if len(parts) != 6 {
				t.Fatalf("part count = %d; want 6", len(parts))
			}
			for _, expected := range []struct {
				index int
				media session.Media
			}{
				{1, session.Media{MediaType: "application/octet-stream", Format: "raw", Filename: "notes.bin", Title: "Notes", Source: "inline", Data: []byte{0xff, 0, 'x'}}},
				{2, session.Media{Source: "remote", URI: "https://example.invalid/a%2Fb?q=x"}},
				{3, session.Media{Format: "wav", Source: "provider", Provider: "provider", Scope: "tenant", ReferenceID: "reference"}},
				{5, session.Media{MediaType: "image/empty", Source: "inline"}},
			} {
				if parts[expected.index].Media == nil {
					t.Fatalf("part %d lost media", expected.index)
				}
				got, want := *parts[expected.index].Media, expected.media
				if !bytes.Equal(got.Data, want.Data) {
					t.Fatalf("part %d media bytes changed", expected.index)
				}
				got.Data, want.Data = nil, nil // Nil and empty bytes are equivalent.
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("part %d media = %#v; want %#v", expected.index, got, want)
				}
			}
		}
	}
	assertExactContent(old)
	iv.Value().Blocks[0].Source.WorkspaceFile.Path = "export mutation"
	iv.Format().Blocks[0].Content[0].Text = "export mutation"
	tv.Value().Tools[0].ArgumentSchema[0] = 0
	tv.Format().Tools[0].ArgumentSchema[0] = 0
	mv.Value().Settings[0] = 0
	mv.Format().Settings[0] = 0
	ev.Value().Inputs[0].UserInput.Ordinal = 99
	ev.Value().Inputs[1].Inline.Parts[1].Media.Data[0] = 0
	ev.Value().Inputs[1].Inline.Source.WorkspaceFile.Path = "export mutation"
	ev.Format().Inputs[0].UserInput = new(uint64(99))
	ev.Format().Inputs[1].Inline.Parts[1].Media.Data[0] = 0
	ev.Format().Inputs[1].Inline.Source.WorkspaceFile.Path = "export mutation"
	uv.Parts()[1].Media.Data[0] = 0
	uv.Format().Parts[1].Media.Data[0] = 0
	exportedParts := uv.Parts()
	exportedParts[1].Media.Format, exportedParts[1].Media.Filename, exportedParts[1].Media.Title = "changed", "changed", "changed"
	exportedParts[2].Media.URI = "urn:changed"
	exportedParts[3].Media.Provider, exportedParts[3].Media.Scope, exportedParts[3].Media.ReferenceID = "changed", "changed", "changed"
	exportedInput := uv.Format()
	exportedInput.Parts[1].Media.Format, exportedInput.Parts[1].Media.Filename, exportedInput.Parts[1].Media.Title = "changed", "changed", "changed"
	exportedEffective := ev.Value()
	exportedEffective.Inputs[1].Inline.Parts[1].Media.Format, exportedEffective.Inputs[1].Inline.Parts[1].Media.Filename, exportedEffective.Inputs[1].Inline.Parts[1].Media.Title = "changed", "changed", "changed"
	exportedEffective.Inputs[4].Inline.Actor = "changed"
	exportedDTO := ev.Format()
	exportedDTO.Inputs[1].Inline.Parts[1].Media.Format, exportedDTO.Inputs[1].Inline.Parts[1].Media.Filename, exportedDTO.Inputs[1].Inline.Parts[1].Media.Title = "changed", "changed", "changed"
	exportedDTO.Inputs[4].Inline.Actor = "changed"
	assertExactContent(old)
	if !iv.Value().Equal(wantI) || !tv.Value().Equal(wantT) || !mv.Value().Equal(wantM) || !ev.Value().Equal(wantE) || !authored.Equal(session.UserInput{ID: uv.ID(), Parts: uv.Parts()}) {
		t.Fatal("view export aliases")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope}))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run}))
	mustBatch(t, b.Interrupt())
	batchRun(t, b)
	submitBatch(t, s, b)
	next := s.Snapshot()
	selected, present := next.Instructions()
	if !present || len(selected.Value().Blocks) != 0 || !iv.Value().Equal(wantI) {
		t.Fatal("explicit empty or old selection lost")
	}
	nt, _ := next.ToolCatalog()
	nm, _ := next.ModelBinding()
	if nt != tv || nm != mv {
		t.Fatal("unmodified context changed across runs")
	}
	if old.RunAt(0).RecordCount() != 7 || next.RunAt(0).RecordCount() != 10 {
		t.Fatal("run chronology filtered context")
	}
	if _, ok := old.RecordAt(2).Instructions(); ok {
		t.Fatal("wrong-kind instructions")
	}
	if _, ok := old.RecordAt(4).EffectiveInput(); ok {
		t.Fatal("wrong-kind effective")
	}
	empty, ok := next.RecordAt(9).EffectiveInput()
	if !ok || len(empty.Value().Inputs) != 0 || empty.RunID() != run {
		t.Fatal("empty increment lost")
	}
	loaded, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	assertExactContent(loaded.Snapshot())
	loadedInput := loaded.Snapshot().RunAt(0).InputAt(0)
	loadedEffective, ok := loaded.Snapshot().RecordAt(7).EffectiveInput()
	if !ok {
		t.Fatal("missing loaded effective input")
	}
	loadedInput.Parts()[1].Media.Format = "changed"
	loadedInput.Format().Parts[1].Media.Title = "changed"
	loadedEffective.Value().Inputs[1].Inline.Parts[1].Media.Filename = "changed"
	loadedEffective.Format().Inputs[4].Inline.Actor = "changed"
	assertExactContent(loaded.Snapshot())
	if !reflect.DeepEqual(next, loaded.Snapshot()) || !ev.Value().Equal(wantE) || old.RecordCount() != 8 {
		t.Fatal("replay or pinned history changed")
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
	forward := session.InputID{RunID: run, Ordinal: 1}
	if err := b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &forward}}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("forward source: %v", err)
	}
	// Generic inline-only contributions need neither a submission nor a special actor.
	inline := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{
		{Inline: &session.AttributedInput{Actor: "messages/arbitrary origin", Purpose: session.PromptContext, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "retained"}}, Source: session.ContentSource{Description: "prepared elsewhere"}}},
		{Inline: &session.AttributedInput{Actor: "harness", Purpose: session.PromptInstructions, Parts: []session.ContentPart{}, Source: session.ContentSource{Description: "empty contribution"}}},
	}}
	mustBatch(t, b.AddEffectiveInput(inline))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{}}))
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Parts: []session.ContentPart{}})
	mustBatch(t, err)
	refs := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &input}, {UserInput: &input}}}
	mustBatch(t, b.AddEffectiveInput(refs))
	mustBatch(t, b.AddEffectiveInput(refs))
	for _, ordinal := range []uint64{2, 99, math.MaxUint64} {
		id := session.InputID{RunID: run, Ordinal: ordinal}
		if err := b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &input}, {UserInput: &id}}}); !errors.Is(err, session.ErrTransition) {
			t.Fatalf("nonexistent source: %v", err)
		}
	}
	foreignRun := session.RunID{HistoryID: foreign, Ordinal: 1}
	if err := b.AddEffectiveInput(session.EffectiveInput{RunID: foreignRun}); !errors.Is(err, session.ErrIdentity) {
		t.Fatalf("foreign batch: %v", err)
	}
	wrongRun := session.RunID{HistoryID: scope, Ordinal: 2}
	if err := b.AddEffectiveInput(session.EffectiveInput{RunID: wrongRun}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("wrong run: %v", err)
	}
	submitBatch(t, s, b)
	old := s.Snapshot()
	b = beginBatch(t, s)
	mustBatch(t, b.AddEffectiveInput(refs)) // Repeated across commits is not consumed.
	mustBatch(t, b.Interrupt())
	if err := b.AddEffectiveInput(inline); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("inactive batch: %v", err)
	}
	successor := batchRun(t, b)
	if err := b.AddEffectiveInput(refs); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("old run: %v", err)
	}
	if err := b.AddEffectiveInput(session.EffectiveInput{RunID: successor, Inputs: []session.InputItem{{UserInput: &input}}}); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("old source in new run: %v", err)
	}
	empty, err := b.AddInput(session.AddInputRequest{RunID: successor})
	mustBatch(t, err)
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: successor, Inputs: []session.InputItem{{UserInput: &empty}}}))
	submitBatch(t, s, b)
	loaded, err := session.Load(context.Background(), h, scope, sessionPolicy())
	mustBatch(t, err)
	if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) || old.RecordCount() != 7 || len(loaded.Snapshot().RunAt(0).InputAt(0).Parts()) != 0 || len(loaded.Snapshot().RunAt(1).InputAt(0).Parts()) != 0 {
		t.Fatal("empty/duplicate facts lost or rejection leaked")
	}
	got, _ := loaded.Snapshot().RecordAt(2).EffectiveInput()
	if !got.Value().Equal(inline) {
		t.Fatal("inline-only actor/item boundaries changed")
	}
}

func TestContextMixedReferenceBoundsBeforeAppend(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, ordinals := range [][]uint64{{1, 2}, {2, 1}, {1, math.MaxUint64}, {math.MaxUint64, 1}} {
			t.Run(fmt.Sprintf("accepted=%t/refs=%v", accepted, ordinals), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				submitBatch(t, s, b)
				b = beginBatch(t, s)
				input, err := b.AddInput(session.AddInputRequest{RunID: run})
				mustBatch(t, err)
				if accepted {
					submitBatch(t, s, b)
					b = beginBatch(t, s)
				}
				before := captureRejectedSnapshot(s.Snapshot())
				attempts := len(backend.attempts)
				valid := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &input}, {UserInput: &input}}}
				mustBatch(t, b.AddEffectiveInput(valid))
				invalid := session.EffectiveInput{RunID: run}
				for _, n := range ordinals {
					invalid.Inputs = append(invalid.Inputs, session.InputItem{UserInput: &session.InputID{RunID: run, Ordinal: n}})
				}
				if err := b.AddEffectiveInput(invalid); !errors.Is(err, session.ErrTransition) {
					t.Fatalf("late reference: %v", err)
				}
				if len(backend.attempts) != attempts || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
					t.Fatal("addition appended or published")
				}
				mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run}))
				submitBatch(t, s, b)
				if len(backend.attempts) != attempts+1 || s.Snapshot().RecordCount() != 5 {
					t.Fatal("failed addition leaked or prior additions lost")
				}
				got, _ := s.Snapshot().RecordAt(3).EffectiveInput()
				if !got.Value().Equal(valid) {
					t.Fatal("prior contribution changed")
				}
				loaded, err := session.Load(context.Background(), h, run.HistoryID, sessionPolicy())
				mustBatch(t, err)
				if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
					t.Fatal("replay rejects submitted contribution")
				}
			})
		}
	}
}

func TestContextMixedReferenceBoundsExternalApplyAndLoad(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, ordinals := range [][]uint64{{1, 1}, {1, 2}, {2, 1}, {1, math.MaxUint64}, {math.MaxUint64, 1}} {
			t.Run(fmt.Sprintf("accepted=%t/refs=%v", accepted, ordinals), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				input := session.UserInput{ID: session.InputID{RunID: run, Ordinal: 1}}
				if accepted {
					id, err := b.AddInput(session.AddInputRequest{RunID: run})
					mustBatch(t, err)
					if id != input.ID {
						t.Fatal("source identity")
					}
				}
				submitBatch(t, s, b)
				var records []session.Record
				if !accepted {
					r, err := session.NewUserInputRecord(input, testLimits())
					mustBatch(t, err)
					records = append(records, r)
				}
				prior := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &input.ID}}}
				r, err := session.NewEffectiveInputRecord(prior, testLimits())
				mustBatch(t, err)
				records = append(records, r)
				effective := session.EffectiveInput{RunID: run}
				valid := true
				for _, n := range ordinals {
					effective.Inputs = append(effective.Inputs, session.InputItem{UserInput: &session.InputID{RunID: run, Ordinal: n}})
					valid = valid && n == 1
				}
				r, err = session.NewEffectiveInputRecord(effective, testLimits())
				mustBatch(t, err)
				// Exercise externally decoded metadata as well as the independent Apply path.
				r, err = session.DecodeRecord(run.HistoryID, r.Format(), testLimits())
				mustBatch(t, err)
				records = append(records, r)
				c := proposedCommit(t, s, records...)
				before := captureRejectedSnapshot(s.Snapshot())
				attempts := len(backend.attempts)
				err = s.Apply(c)
				if valid {
					mustBatch(t, err)
					got, _ := s.Snapshot().RecordAt(4).EffectiveInput()
					if !got.Value().Equal(effective) {
						t.Fatal("valid references changed")
					}
				} else if !errors.Is(err, session.ErrTransition) || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
					t.Fatalf("invalid Apply published: %v", err)
				}
				if len(backend.attempts) != attempts {
					t.Fatal("Apply appended")
				}
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
						t.Fatal("valid replay differs")
					}
				} else if loaded != nil || !errors.Is(err, session.ErrTransition) {
					t.Fatalf("invalid Load: %v, %v", loaded, err)
				}
				if reader.appends != 0 || len(backend.attempts) != attempts+1 {
					t.Fatal("Load repaired history")
				}
			})
		}
	}
}

func TestContextForwardReferenceExternalApplyAndLoad(t *testing.T) {
	for _, path := range []string{"Apply", "Load"} {
		t.Run(path, func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: run.HistoryID, Blocks: []session.InstructionBlock{{Content: []session.ContentPart{{Kind: session.ContentText, Text: "accepted root"}}, Source: session.ContentSource{Description: "accepted"}}}}))
			submitBatch(t, s, b)
			old := s.Snapshot()
			before := captureRejectedSnapshot(old)
			selected, ok := old.Instructions()
			if !ok {
				t.Fatal("missing initial instructions")
			}

			prefix, err := session.NewInstructionsRecord(session.Instructions{HistoryID: run.HistoryID}, testLimits())
			mustBatch(t, err)
			input := session.InputID{RunID: run, Ordinal: 1}
			effective, err := session.NewEffectiveInputRecord(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &input}}}, testLimits())
			mustBatch(t, err)
			user, err := session.NewUserInputRecord(session.UserInput{ID: input, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "later source"}}}, testLimits())
			mustBatch(t, err)
			// Reordering exactly these facts is valid: rejection must depend on
			// the source being later, not on an invalid prefix or source payload.
			control, err := session.Load(context.Background(), h, run.HistoryID, sessionPolicy())
			mustBatch(t, err)
			mustBatch(t, control.Apply(proposedCommit(t, control, prefix, user, effective)))
			if control.Snapshot().RunAt(0).InputCount() != 1 {
				t.Fatal("ordered control lost the source")
			}
			c := proposedCommit(t, s, prefix, effective, user)
			attempts := len(backend.attempts)
			if path == "Apply" {
				if err := s.Apply(c); !errors.Is(err, session.ErrTransition) {
					t.Fatalf("forward reference Apply = %v; want ErrTransition", err)
				}
				if len(backend.attempts) != attempts {
					t.Fatal("Apply appended")
				}
				assertHistoryHead(t, h, old.Head())
			} else {
				// History accepts the complete external commit opaquely. Load must
				// reject its ordering even though the source exists by commit end.
				result, err := h.Append(context.Background(), run.HistoryID, c.Commit)
				mustBatch(t, err)
				if result.Status != history.StatusAccepted || result.Proposal != c.Ref {
					t.Fatal("external append")
				}
				reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
				loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), run.HistoryID, sessionPolicy())
				if loaded != nil || !errors.Is(err, session.ErrTransition) {
					t.Fatalf("forward reference Load = %v, %v; want nil, ErrTransition", loaded, err)
				}
				if reader.appends != 0 || len(backend.attempts) != attempts+1 {
					t.Fatal("Load repaired or appended history")
				}
				assertHistoryHead(t, h, c.Ref)
			}
			current, ok := s.Snapshot().Instructions()
			if !ok || current != selected || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) || !reflect.DeepEqual(before, captureRejectedSnapshot(old)) {
				t.Fatal("forward reference published a prefix or changed accepted/pinned state")
			}
		})
	}
}

func TestNewRecordExactVersionsAndZeroViews(t *testing.T) {
	scope := testHistoryID(t)
	run := session.RunID{HistoryID: scope, Ordinal: 1}
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
			return session.NewEffectiveInputRecord(session.EffectiveInput{RunID: run}, testLimits())
		},
		func() (session.Record, error) {
			return session.NewUserInputRecord(session.UserInput{ID: session.InputID{RunID: run, Ordinal: 1}}, testLimits())
		},
	} {
		r, err := makeRecord()
		mustBatch(t, err)
		got, err := session.DecodeRecord(scope, r.Format(), testLimits())
		mustBatch(t, err)
		if r.Format().KindVersion != 1 || !got.Format().Equal(r.Format()) {
			t.Fatal("record roundtrip/version")
		}
		dto := r.Format()
		dto.KindVersion++
		if got, err := session.DecodeRecord(scope, dto, testLimits()); !errors.Is(err, session.ErrUnsupportedRecord) || !reflect.DeepEqual(got, session.Record{}) {
			t.Fatalf("unsupported version: %v", err)
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
	if v, ok := r.EffectiveInput(); ok || v.RunID() != (session.RunID{}) || !reflect.DeepEqual(v.Value(), session.EffectiveInput{}) || !reflect.DeepEqual(v.Format(), sessionformat.EffectiveInput{}) {
		t.Fatal("zero effective")
	}
}
