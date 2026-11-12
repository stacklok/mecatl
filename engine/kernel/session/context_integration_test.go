package session_test

import (
	"context"
	"errors"
	"fmt"
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
	instructions := session.Instructions{Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new("root\n")}}, Source: source}}}
	catalog := session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "read", Description: "read file", ArgumentSchema: []byte(" {\"type\": \"object\"} ")}, {Name: "noop", ArgumentSchema: []byte("true")}}}
	binding := session.ModelBinding{Route: "route", API: "dialect/v1", Model: "alias", Settings: []byte(" {\"temperature\": 1.00} ")}
	wantI, wantT, wantM := instructions.Clone(), catalog.Clone(), binding.Clone()
	if instructions.Validate() != nil || catalog.Validate() != nil || binding.Validate() != nil {
		t.Fatal("valid context values")
	}
	b := beginBatch(t, s)
	if err := b.ReplaceInstructions(scope, instructions); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("before creation: %v", err)
	}
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	parts := richContent()
	input, err := b.AddUserInput(run, session.UserInput{Parts: parts})
	mustBatch(t, err)
	second, err := b.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{{Text: new("second submission")}}})
	mustBatch(t, err)
	authoredID := input
	authored := session.UserInput{Parts: parts}.Clone()
	mustBatch(t, b.ReplaceInstructions(scope, instructions))
	mustBatch(t, b.ReplaceToolCatalog(scope, catalog))
	mustBatch(t, b.ReplaceModelBinding(scope, binding))
	effective := session.EffectiveInput{Inputs: []session.InputItem{
		{UserInputID: new(input.RunRelative())},
		{Inline: &session.AttributedInput{Actor: "skill/example", Purpose: session.PromptInstructions, Parts: richContent(), Source: session.ContentSource{Description: "skill expansion", WorkspaceFile: &session.WorkspaceFileSource{Path: "skills/example.md"}}}},
		{UserInputID: new(second.RunRelative())}, {UserInputID: new(input.RunRelative())},
		{Inline: &session.AttributedInput{Actor: " \textension/尾 e\u0301\r\n ", Purpose: session.PromptContext, Parts: []session.ContentPart{{Text: new("context data")}}, Source: session.ContentSource{Description: "workspace reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}}},
	}}
	wantE := effective.Clone()
	storedEffective := effective
	storedEffective.RunID = run.HistoryRelative()
	if storedEffective.Validate() != nil || !effective.Clone().Equal(effective) {
		t.Fatal("effective value")
	}
	mustBatch(t, b.AddEffectiveInput(run, effective))
	parts[1].Media.InlineData[0] = 0
	parts[1].Media.FormatHint = "changed"
	parts[1].Media.Filename = "changed"
	parts[1].Media.Title = "changed"
	*parts[0].Text = "changed"
	*parts[4].Text = "no longer empty"
	parts[2].Media.RemoteURI = "urn:changed"
	parts[3].Media.ProviderScope = "changed"
	parts[5].Media.InlineData = []byte("not empty")
	instructions.Blocks[0].Source.WorkspaceFile.Path = "changed"
	*instructions.Blocks[0].Parts[0].Text = "changed"
	clear(catalog.Tools[0].ArgumentSchema)
	catalog.Tools[1].Name = "changed"
	clear(binding.Settings)
	*effective.Inputs[0].UserInputID = 0
	effective.Inputs[1].Inline.Parts[1].Media.InlineData[0] = 0
	effective.Inputs[1].Inline.Source.WorkspaceFile.Path = "changed"
	*effective.Inputs[4].Inline.Parts[0].Text = "changed"
	effective.Inputs[4].Inline.Actor = "changed"
	submitBatch(t, s, b)
	old := s.Snapshot()
	iv, ok := old.Instructions()
	if !ok || iv.HistoryID() != scope || !instructionsMatch(iv, wantI) {
		t.Fatal("instructions view")
	}
	tv, ok := old.ToolCatalog()
	if !ok || tv.HistoryID() != scope || !catalogMatches(tv, wantT) {
		t.Fatal("catalog view")
	}
	mv, ok := old.ModelBinding()
	if !ok || mv.HistoryID() != scope || !bindingMatches(mv, wantM) {
		t.Fatal("binding view")
	}
	ev, ok := old.RecordAt(7).EffectiveInput()
	if !ok || ev.RunID() != run || !effectiveMatches(ev, wantE) {
		t.Fatal("effective view")
	}
	uv := lookupInput(t, old, authoredID.RunID(), 1)
	if !userInputMatches(uv, authored) {
		t.Fatal("authored rich data changed")
	}
	// Independent literals catch lossy conversion even when Clone and Equal use
	// that same conversion. Recheck public exports at each real acceptance seam.
	assertExactContent := func(snapshot session.Snapshot) {
		t.Helper()
		input := lookupInput(t, snapshot, authoredID.RunID(), 1)
		increment, ok := snapshot.RecordAt(7).EffectiveInput()
		if !ok {
			t.Fatal("missing effective input")
		}
		if increment.Inputs().Len() != 5 {
			t.Fatal("lost attributed item boundaries")
		}
		skill, contextInput := inlineAt(t, increment, 1), inlineAt(t, increment, 4)
		if skill.Actor() != "skill/example" || contextInput.Actor() != " \textension/尾 e\u0301\r\n " {
			t.Fatalf("actors changed: %q, %q", skill.Actor(), contextInput.Actor())
		}
		ref, present := increment.Inputs().At(0).UserInputID()
		resolved, found := snapshot.UserInputByID(ref)
		if !present || !found || resolved.ID() != input.ID() {
			t.Fatal("reference did not resolve in pinned snapshot")
		}
		for _, parts := range []struct {
			count int
			at    func(int) session.ContentPartView
		}{{resolved.Parts().Len(), resolved.Parts().At}, {skill.Parts().Len(), skill.Parts().At}} {
			if parts.count != 6 {
				t.Fatalf("part count = %d; want 6", parts.count)
			}
			text, textOK := parts.at(0).Text()
			if !textOK || text != " /skill example\r\ne\u0301 " {
				t.Fatalf("exact text = %q", text)
			}
			if text, ok := parts.at(4).Text(); !ok || text != "" {
				t.Fatal("explicit empty text lost")
			}
			for _, expected := range []struct {
				index int
				media session.Media
			}{
				{1, session.Media{Kind: session.MediaKindDocument, MIMEType: "application/octet-stream", FormatHint: "raw", Filename: "notes.bin", Title: "Notes", Source: "inline", InlineData: []byte{0xff, 0, 'x'}}},
				{2, session.Media{Kind: session.MediaKindImage, Source: "remote", RemoteURI: "https://example.invalid/a%2Fb?q=x"}},
				{3, session.Media{Kind: session.MediaKindAudio, FormatHint: "wav", Source: "provider", ProviderName: "provider", ProviderScope: "tenant", ProviderReferenceID: "reference"}},
				{5, session.Media{Kind: session.MediaKindImage, MIMEType: "image/empty", Source: "inline"}},
			} {
				if !mediaMatches(mediaAt(t, parts.at(expected.index)), expected.media) {
					t.Fatalf("part %d media changed", expected.index)
				}
			}
		}
	}
	assertExactContent(old)
	page, err := h.Read(context.Background(), scope, history.ReadRequest{Through: old.Head(), MaxCommits: 1})
	mustBatch(t, err)
	if len(page.Commits) != 1 {
		t.Fatal("missing accepted fixture commit")
	}
	applied, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	assertExactContent(applied.Snapshot())
	for _, record := range page.Commits[0].Commit.Records {
		clear(record.Payload)
	}
	assertExactContent(applied.Snapshot())
	clear(tv.Tools().At(0).ArgumentSchema().Copy())
	clear(mv.Settings().Copy())
	ref, ok := ev.Inputs().At(0).UserInputID()
	if !ok || ref != authoredID {
		t.Fatal("missing authored identity")
	}
	ref = session.UserInputID{} // Replacing a returned identity cannot change the view.
	if ref == authoredID {
		t.Fatal("identity replacement")
	}
	mutateMediaBytes(t, inlineAt(t, ev, 1).Parts().At(1))
	mutateMediaBytes(t, uv.Parts().At(1))
	// Encoded transport remains an explicit independently owned export.
	raw := old.RecordAt(7).Format()
	clear(raw.Payload)
	assertExactContent(old)
	if !instructionsMatch(iv, wantI) || !catalogMatches(tv, wantT) || !bindingMatches(mv, wantM) || !effectiveMatches(ev, wantE) || !userInputMatches(uv, authored) {
		t.Fatal("view export aliases")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.ReplaceInstructions(scope, session.Instructions{}))
	mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{}))
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	batchRun(t, b)
	submitBatch(t, s, b)
	next := s.Snapshot()
	selected, present := next.Instructions()
	if !present || selected.Blocks().Len() != 0 || !instructionsMatch(iv, wantI) {
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
	if !ok || empty.Inputs().Len() != 0 || empty.RunID() != run {
		t.Fatal("empty increment lost")
	}
	loaded, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	assertExactContent(loaded.Snapshot())
	loadedInput := lookupInput(t, loaded.Snapshot(), authoredID.RunID(), 1)
	loadedEffective, ok := loaded.Snapshot().RecordAt(7).EffectiveInput()
	if !ok {
		t.Fatal("missing loaded effective input")
	}
	mutateMediaBytes(t, loadedInput.Parts().At(1))
	mutateMediaBytes(t, inlineAt(t, loadedEffective, 1).Parts().At(1))
	assertExactContent(loaded.Snapshot())
	if !reflect.DeepEqual(next, loaded.Snapshot()) || !effectiveMatches(ev, wantE) || old.RecordCount() != 8 {
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
	if err := b.ReplaceInstructions(foreign, session.Instructions{}); !errors.Is(err, session.ErrIdentity) {
		t.Fatalf("foreign context: %v", err)
	}
	forward := allocatedInput(t, run, 1)
	if err := b.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(forward.RunRelative())}}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("forward source: %v", err)
	}
	// Generic inline-only contributions need neither a submission nor a special actor.
	inline := session.EffectiveInput{Inputs: []session.InputItem{
		{Inline: &session.AttributedInput{Actor: "messages/arbitrary origin", Purpose: session.PromptContext, Parts: []session.ContentPart{{Text: new("retained")}}, Source: session.ContentSource{Description: "prepared elsewhere"}}},
		{Inline: &session.AttributedInput{Actor: "harness", Purpose: session.PromptInstructions, Parts: []session.ContentPart{}, Source: session.ContentSource{Description: "empty contribution"}}},
	}}
	mustBatch(t, b.AddEffectiveInput(run, inline))
	mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{}}))
	input, err := b.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{}})
	mustBatch(t, err)
	refs := session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}, {UserInputID: new(input.RunRelative())}}}
	mustBatch(t, b.AddEffectiveInput(run, refs))
	mustBatch(t, b.AddEffectiveInput(run, refs))
	for _, ordinal := range []uint64{2, 99} {
		id := allocatedInput(t, run, ordinal)
		if err := b.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}, {UserInputID: new(id.RunRelative())}}}); !errors.Is(err, session.ErrTransition) {
			t.Fatalf("nonexistent source: %v", err)
		}
	}
	foreignRun := allocatedRun(t, foreign, 1)
	if err := b.AddEffectiveInput(foreignRun, session.EffectiveInput{}); !errors.Is(err, session.ErrIdentity) {
		t.Fatalf("foreign batch: %v", err)
	}
	wrongRun := allocatedRun(t, scope, 2)
	if err := b.AddEffectiveInput(wrongRun, session.EffectiveInput{}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("wrong run: %v", err)
	}
	submitBatch(t, s, b)
	old := s.Snapshot()
	b = beginBatch(t, s)
	mustBatch(t, b.AddEffectiveInput(run, refs)) // Repeated across commits is not consumed.
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	if err := b.AddEffectiveInput(run, inline); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("inactive batch: %v", err)
	}
	successor := batchRun(t, b)
	if err := b.AddEffectiveInput(run, refs); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("old run: %v", err)
	}
	if err := b.AddEffectiveInput(successor, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("old source in new run: %v", err)
	}
	empty, err := b.AddUserInput(successor, session.UserInput{})
	mustBatch(t, err)
	mustBatch(t, b.AddEffectiveInput(successor, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(empty.RunRelative())}}}))
	submitBatch(t, s, b)
	loaded, err := session.Load(context.Background(), h, scope, sessionPolicy())
	mustBatch(t, err)
	if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) || old.RecordCount() != 7 || lookupInput(t, loaded.Snapshot(), input.RunID(), 1).Parts().Len() != 0 || lookupInput(t, loaded.Snapshot(), empty.RunID(), 1).Parts().Len() != 0 {
		t.Fatal("empty/duplicate facts lost or rejection leaked")
	}
	got, _ := loaded.Snapshot().RecordAt(2).EffectiveInput()
	if !effectiveMatches(got, inline) {
		t.Fatal("inline-only actor/item boundaries changed")
	}
}

func TestContextMixedReferenceBoundsBeforeAppend(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, ordinals := range [][]uint64{{1, 2}, {2, 1}} {
			t.Run(fmt.Sprintf("accepted=%t/refs=%v", accepted, ordinals), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				submitBatch(t, s, b)
				b = beginBatch(t, s)
				input, err := b.AddUserInput(run, session.UserInput{})
				mustBatch(t, err)
				if accepted {
					submitBatch(t, s, b)
					b = beginBatch(t, s)
				}
				before := captureRejectedSnapshot(s.Snapshot())
				attempts := len(backend.attempts)
				valid := session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}, {UserInputID: new(input.RunRelative())}}}
				mustBatch(t, b.AddEffectiveInput(run, valid))
				invalid := session.EffectiveInput{}
				for _, n := range ordinals {
					invalid.Inputs = append(invalid.Inputs, session.InputItem{UserInputID: new(allocatedInput(t, run, n).RunRelative())})
				}
				if err := b.AddEffectiveInput(run, invalid); !errors.Is(err, session.ErrTransition) {
					t.Fatalf("late reference: %v", err)
				}
				if len(backend.attempts) != attempts || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
					t.Fatal("addition appended or published")
				}
				mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{}))
				submitBatch(t, s, b)
				if len(backend.attempts) != attempts+1 || s.Snapshot().RecordCount() != 5 {
					t.Fatal("failed addition leaked or prior additions lost")
				}
				got, _ := s.Snapshot().RecordAt(3).EffectiveInput()
				if !effectiveMatches(got, valid) {
					t.Fatal("prior contribution changed")
				}
				loaded, err := session.Load(context.Background(), h, run.HistoryID(), sessionPolicy())
				mustBatch(t, err)
				if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
					t.Fatal("replay rejects submitted contribution")
				}
			})
		}
	}
}

func TestContextMixedReferenceBoundsExternalLoad(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, ordinals := range [][]uint64{{1, 1}, {1, 2}, {2, 1}} {
			t.Run(fmt.Sprintf("accepted=%t/refs=%v", accepted, ordinals), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				input := allocatedInput(t, run, 1)
				if accepted {
					id, err := b.AddUserInput(run, session.UserInput{})
					mustBatch(t, err)
					if id != input {
						t.Fatal("source identity")
					}
				}
				submitBatch(t, s, b)
				var records []fixtureRecord
				if !accepted {
					records = append(records, encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: 1, ID: 1}))
				}
				records = append(records, effectiveReferenceRecord(t, 1, 1))
				effective := session.EffectiveInput{}
				valid := true
				for _, n := range ordinals {
					effective.Inputs = append(effective.Inputs, session.InputItem{UserInputID: new(allocatedInput(t, run, n).RunRelative())})
					valid = valid && n == 1
				}
				records = append(records, effectiveReferenceRecord(t, 1, ordinals...))
				c := proposedCommit(t, s, records...)
				before := captureRejectedSnapshot(s.Snapshot())
				attempts := len(backend.attempts)
				independent, err := loadFixtureCommit(t, h, s, c, sessionPolicy())
				if valid {
					mustBatch(t, err)
					got, _ := independent.Snapshot().RecordAt(4).EffectiveInput()
					if !effectiveMatches(got, effective) {
						t.Fatal("valid references changed")
					}
				} else if independent != nil || !errors.Is(err, session.ErrTransition) {
					t.Fatalf("invalid prefix Load: %v", err)
				}
				if !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) || len(backend.attempts) != attempts {
					t.Fatal("independent Load changed the source session or storage")
				}
				result, err := h.Append(context.Background(), run.HistoryID(), c.Commit)
				mustBatch(t, err)
				if result.Status != history.StatusAccepted || result.Proposal != c.Ref {
					t.Fatal("external append")
				}
				reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
				loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), run.HistoryID(), sessionPolicy())
				if valid {
					mustBatch(t, err)
					if !reflect.DeepEqual(independent.Snapshot(), loaded.Snapshot()) {
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

func TestContextForwardReferenceExternalLoad(t *testing.T) {
	for _, path := range []string{"independent prefix", "read-only storage"} {
		t.Run(path, func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			mustBatch(t, b.ReplaceInstructions(run.HistoryID(), session.Instructions{Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new("accepted root")}}, Source: session.ContentSource{Description: "accepted"}}}}))
			submitBatch(t, s, b)
			old := s.Snapshot()
			before := captureRejectedSnapshot(old)
			selected, ok := old.Instructions()
			if !ok {
				t.Fatal("missing initial instructions")
			}

			prefix := encodeFixture(t, sessionformat.KindInstructions, sessionformat.Instructions{})
			input := allocatedInput(t, run, 1)
			effective := effectiveReferenceRecord(t, 1, 1)
			user := encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: 1, ID: 1, Parts: []sessionformat.ContentPart{{Text: new("later source")}}})
			// Reordering exactly these facts is valid: rejection must depend on
			// the source being later, not on an invalid prefix or source payload.
			control, err := loadFixtureCommit(t, h, s, proposedCommit(t, s, prefix, user, effective), sessionPolicy())
			mustBatch(t, err)
			if !userInputsMatch(control.Snapshot(), input.RunID(), 1) {
				t.Fatal("ordered control lost the source")
			}
			c := proposedCommit(t, s, prefix, effective, user)
			attempts := len(backend.attempts)
			if path == "independent prefix" {
				loaded, err := loadFixtureCommit(t, h, s, c, sessionPolicy())
				if loaded != nil || !errors.Is(err, session.ErrTransition) {
					t.Fatalf("forward reference Load = %v, %v; want nil, ErrTransition", loaded, err)
				}
				if len(backend.attempts) != attempts {
					t.Fatal("independent replay changed source storage")
				}
				assertHistoryHead(t, h, old.Head())
			} else {
				// History accepts the complete external commit opaquely. Load must
				// reject its ordering even though the source exists by commit end.
				result, err := h.Append(context.Background(), run.HistoryID(), c.Commit)
				mustBatch(t, err)
				if result.Status != history.StatusAccepted || result.Proposal != c.Ref {
					t.Fatal("external append")
				}
				reader := &loadBackend{Backend: readOnlyHistoryBackend{Backend: backend, t: t}}
				loaded, err := session.Load(context.Background(), newLifecycleHistory(t, reader), run.HistoryID(), sessionPolicy())
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
	h := newSessionHistory(t)
	s := loadRecords(t, h, boundSession(t, h, sessionPolicy()), createdRecord(t), startRecord(t, 1))
	for _, r := range []fixtureRecord{
		encodeFixture(t, sessionformat.KindInstructions, sessionformat.Instructions{}),
		encodeFixture(t, sessionformat.KindToolCatalog, sessionformat.ToolCatalog{}),
		encodeFixture(t, sessionformat.KindModelBinding, sessionformat.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}),
		effectiveReferenceRecord(t, 1),
		encodeFixture(t, sessionformat.KindUserInput, sessionformat.UserInput{RunID: 1, ID: 1}),
	} {
		loaded, err := loadFixtureCommit(t, h, s, proposedCommit(t, s, r), sessionPolicy())
		mustBatch(t, err)
		got := loaded.Snapshot().RecordAt(2).Format()
		if got.KindVersion != sessionformat.Version || !got.Equal(r.Format()) {
			t.Fatal("record roundtrip/version")
		}
		dto := r.Format()
		dto.KindVersion++
		assertRejected(t, h, s, proposedCommit(t, s, fixtureRecord{dto}), session.ErrUnsupportedRecord)
	}
	var r session.RecordView
	if v, ok := r.Instructions(); ok || v.HistoryID() != (historyid.HistoryID{}) || v.Blocks().Len() != 0 {
		t.Fatal("zero instructions")
	}
	if v, ok := r.ToolCatalog(); ok || v.HistoryID() != (historyid.HistoryID{}) || v.Tools().Len() != 0 {
		t.Fatal("zero catalog")
	}
	if v, ok := r.ModelBinding(); ok || v.HistoryID() != (historyid.HistoryID{}) || v.Route() != "" || v.API() != "" || v.Model() != "" || v.Settings().Copy() != nil {
		t.Fatal("zero binding")
	}
	if v, ok := r.EffectiveInput(); ok || v.RunID() != (session.RunID{}) || v.Inputs().Len() != 0 {
		t.Fatal("zero effective")
	}
}
