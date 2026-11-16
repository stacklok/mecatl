package modelrequest

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func assertReconstructed(t *testing.T, snapshot session.Snapshot, id session.ModelRequestID, want Request) {
	t.Helper()
	got, err := Reconstruct(snapshot, id, testLimits())
	requireConstruction(t, err)
	if got.CID() != want.CID() || !got.MutableClone().Equal(want.MutableClone()) {
		t.Fatal("reconstruction changed the original logical request")
	}
}

func assertReconstructionFailure(t *testing.T, snapshot session.Snapshot, id session.ModelRequestID, limits codec.Limits, want error) {
	t.Helper()
	got, err := Reconstruct(snapshot, id, limits)
	if err == nil || (want != nil && !errors.Is(err, want)) || !reflect.DeepEqual(got, Request{}) {
		t.Fatalf("expected zero request and %v; got error %v", want, err)
	}
}

func TestReconstructOriginalPrefixAndRun(t *testing.T) {
	for _, position := range []string{"genesis", "beginning", "middle"} {
		t.Run(position, func(t *testing.T) {
			f := newConstructionFixture(t)
			scope := f.run.HistoryID()
			requireConstruction(t, f.b.ReplaceRootInstructions(scope, session.RootInstructions{Blocks: []session.InstructionBlock{instruction("original root")}}))
			requireConstruction(t, f.b.ReplaceUserInstructions(scope, session.UserInstructions{Blocks: []session.InstructionBlock{instruction("original prefix")}}))
			requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "original input")}}))
			if position != "genesis" {
				f.submit()
			}
			if position == "middle" {
				requireConstruction(t, f.b.ReplaceRootInstructions(scope, session.RootInstructions{Blocks: []session.InstructionBlock{instruction("same-commit root")}}))
				requireConstruction(t, f.b.ReplaceUserInstructions(scope, session.UserInstructions{Blocks: []session.InstructionBlock{instruction("same-commit prefix")}}))
				requireConstruction(t, f.b.ReplaceModelBinding(scope, session.ModelBinding{Route: "original", API: "same-commit API", Model: "same-commit model"}))
				requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "same-commit input")}}))
			}
			before := f.s.Snapshot()
			capture := f.view()
			built, err := Construct(capture, format.ParametersV1{}, testLimits())
			requireConstruction(t, err)
			id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
			requireConstruction(t, err)
			assertReconstructionFailure(t, before, id, testLimits(), session.ErrIdentity)
			// Later facts in this very commit must not enter the original prefix.
			requireConstruction(t, f.b.ReplaceRootInstructions(scope, session.RootInstructions{Blocks: []session.InstructionBlock{instruction("later root")}}))
			requireConstruction(t, f.b.ReplaceUserInstructions(scope, session.UserInstructions{}))
			requireConstruction(t, f.b.ReplaceModelBinding(scope, session.ModelBinding{Route: "later", API: "later API", Model: "later model"}))
			f.submit()
			accepted := f.s.Snapshot()
			assertReconstructed(t, accepted, id, built.Request())
			found := false
			for record := range accepted.Records() {
				if request, ok := record.ModelRequest(); ok && request.ID() == id {
					_, hasAnchor := request.Source()
					if hasAnchor != (position != "genesis") {
						t.Fatal("incorrect original accepted anchor presence")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("accepted target not inspectable")
			}
			requireConstruction(t, f.b.AddModelResponse(id, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: textParts("later response")}}}}))
			requireConstruction(t, f.b.EndRun(session.RunEndedInterrupted))
			f.run, err = f.b.StartRun()
			requireConstruction(t, err)
			requireConstruction(t, f.b.ReplaceRootInstructions(scope, session.RootInstructions{}))
			requireConstruction(t, f.b.ReplaceUserInstructions(scope, session.UserInstructions{Blocks: []session.InstructionBlock{instruction("next run prefix")}}))
			requireConstruction(t, f.b.ReplaceModelBinding(scope, session.ModelBinding{Route: "next run", API: "next API", Model: "next model"}))
			requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "next run input")}}))
			f.submit()
			if f.s.Snapshot().ActiveRun() == id.RunID() {
				t.Fatal("fixture did not advance the active run")
			}
			assertReconstructed(t, f.s.Snapshot(), id, built.Request())
			requireConstruction(t, f.b.EndRun(session.RunEndedInterrupted))
			f.submit()
			if f.s.Snapshot().ActiveRun() != (session.RunID{}) {
				t.Fatal("fixture is not idle")
			}
			loaded, err := session.Load(t.Context(), f.h, scope, f.policy)
			requireConstruction(t, err)
			for _, snapshot := range []session.Snapshot{accepted, f.s.Snapshot(), loaded.Snapshot()} {
				assertReconstructed(t, snapshot, id, built.Request())
			}
		})
	}
}

func TestResumeUserInstructionsSelectiveRefresh(t *testing.T) {
	f := newConstructionFixture(t)
	scope := f.run.HistoryID()
	root := session.RootInstructions{Blocks: []session.InstructionBlock{instruction("root contract")}}
	project := session.InstructionBlock{
		Key: "workspace/root-guidance", Parts: append(textParts("retained project guidance"), session.ContentPart{Media: &session.Media{
			Kind: session.MediaKindDocument, Source: session.MediaSourceInline, MIMEType: "application/octet-stream", InlineData: []byte{0xff, 0, 3},
		}}),
		Source: session.ContentSource{Description: "same provenance is not identity", WorkspaceFile: &session.WorkspaceFileSource{Path: "unavailable-on-resume/AGENTS.md"}},
	}
	persona := session.InstructionBlock{
		Key: "operator/persona", Parts: textParts("original persona"),
		Source: session.ContentSource{Description: project.Source.Description, WorkspaceFile: &session.WorkspaceFileSource{Path: "unavailable-on-resume/SOUL.md"}},
	}
	original := session.UserInstructions{Blocks: []session.InstructionBlock{project, persona, {
		Key: "operator/preferences", Source: session.ContentSource{Description: "explicitly empty contribution"},
	}}}
	requireConstruction(t, f.b.ReplaceRootInstructions(scope, root))
	// The latest accepted replacement, not the first occurrence, is the resume baseline.
	requireConstruction(t, f.b.ReplaceUserInstructions(scope, session.UserInstructions{}))
	requireConstruction(t, f.b.ReplaceUserInstructions(scope, original))
	requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "initial input")}}))
	capture := f.view()
	built, err := Construct(capture, format.ParametersV1{}, testLimits())
	requireConstruction(t, err)
	requestID, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
	requireConstruction(t, err)
	f.submit()
	requireConstruction(t, f.b.AddModelResponse(requestID, session.ModelResponse{Terminal: session.ResponseTerminalNormal}))
	requireConstruction(t, f.b.EndRun(session.RunEndedCompleted))
	f.submit()
	requireConstruction(t, f.b.Discard())

	// Neither source file exists in this fixture. Load has only retained History,
	// not a filesystem resolver, and must preserve both provenance labels exactly.
	loaded, err := session.Load(t.Context(), f.h, scope, f.policy)
	requireConstruction(t, err)
	oldSnapshot := loaded.Snapshot()
	standing, present := oldSnapshot.UserInstructions()
	if !present || !standing.MutableClone().Equal(original) {
		t.Fatal("Load did not select the latest retained instructions")
	}
	updated := standing.MutableClone()
	found := false
	for i := range updated.Blocks {
		if updated.Blocks[i].Key == "operator/persona" {
			updated.Blocks[i].Parts = textParts("refreshed persona")
			updated.Blocks[i].Source = session.ContentSource{Description: "prepared from a non-file source on resume"}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing persona contribution")
	}
	want := updated.Clone()
	b, err := loaded.BeginCommit()
	requireConstruction(t, err)
	requireConstruction(t, b.ReplaceUserInstructions(scope, updated))
	// Authoring owns a detached replacement, including unchanged siblings.
	updated.Blocks[0].Key = "caller-mutated"
	updated.Blocks[0].Parts[1].Media.InlineData[0] = 7
	updated.Blocks[0].Source.WorkspaceFile.Path = "CLAUDE.md"
	*updated.Blocks[1].Parts[0].Text = "caller-mutated"
	_, err = loaded.SubmitCommit(t.Context(), b)
	requireConstruction(t, err)
	reloaded, err := session.Load(t.Context(), f.h, scope, f.policy)
	requireConstruction(t, err)
	for _, snapshot := range []session.Snapshot{loaded.Snapshot(), reloaded.Snapshot()} {
		got, ok := snapshot.UserInstructions()
		gotRoot, rootPresent := snapshot.RootInstructions()
		if !ok || !got.MutableClone().Equal(want) || !rootPresent || !gotRoot.MutableClone().Equal(root) {
			t.Fatal("refresh changed order, unrelated contributions, root contract, or new persona")
		}
		assertReconstructed(t, snapshot, requestID, built.Request())
	}
	old, _ := oldSnapshot.UserInstructions()
	if !standing.MutableClone().Equal(original) || !old.MutableClone().Equal(original) {
		t.Fatal("refresh or clone mutation changed old snapshot backing")
	}
	assertReconstructed(t, oldSnapshot, requestID, built.Request())
}

func TestReconstructSameCommitOutcomeAndPrivateReplay(t *testing.T) {
	f := newConstructionFixture(t)
	capture := f.view()
	first := f.build(format.ParametersV1{})
	firstID, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: first.Recipe()})
	requireConstruction(t, err)
	f.submit()
	requireConstruction(t, f.b.AddModelResponse(firstID, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Private: &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 1, Data: []byte{0xff, 0, 7}}}))
	// Private matching uses the first request's binding, not this new one.
	requireConstruction(t, f.b.ReplaceModelBinding(f.run.HistoryID(), session.ModelBinding{Route: "next", API: "next API", Model: "next model"}))
	parameters := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 1}}}
	capture = f.view()
	built, err := Construct(capture, parameters, testLimits())
	requireConstruction(t, err)
	id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
	requireConstruction(t, err)
	f.submit()
	if built.Request().Conversation().Len() != 1 {
		t.Fatal("selected private-only assistant message was lost")
	}
	loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), f.policy)
	requireConstruction(t, err)
	assertReconstructed(t, loaded.Snapshot(), id, built.Request())
}

func TestReconstructRejectsRetainedRecipe(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*session.ConstructionRecipe)
		want   error
	}{
		{"algorithm", func(v *session.ConstructionRecipe) { v.Algorithm = "unknown" }, nil},
		{"version", func(v *session.ConstructionRecipe) { v.Version = 2 }, nil},
		{"algorithm-case", func(v *session.ConstructionRecipe) { v.Algorithm = "Chronological" }, nil},
		{"empty-parameters", func(v *session.ConstructionRecipe) { v.Parameters = nil }, codec.ErrInvalidData},
		{"missing-parameter-field", func(v *session.ConstructionRecipe) { v.Parameters = []byte{0xa0} }, codec.ErrInvalidData},
		{"unknown-parameter-field", func(v *session.ConstructionRecipe) { v.Parameters = []byte{0xa1, 0x61, 'x', 0x80} }, codec.ErrInvalidData},
		{"extra-parameter-field", func(v *session.ConstructionRecipe) {
			v.Parameters = []byte{0xa2, 0x61, 'p', 0x80, 0x61, 'x', 0x00}
		}, codec.ErrInvalidData},
		{"extra-selector-field", func(v *session.ConstructionRecipe) {
			v.Parameters = []byte{0xa1, 0x61, 'p', 0x81, 0xa6,
				0x61, 'a', 0x61, 'a', 0x61, 'f', 0x61, 'f', 0x61, 'm', 0x61, 'm',
				0x61, 'p', 0x61, 'p', 0x61, 'v', 0x01, 0x61, 'x', 0x00}
		}, codec.ErrInvalidData},
		{"wrong-parameter-type", func(v *session.ConstructionRecipe) { v.Parameters = []byte{0xa1, 0x61, 'p', 0x01} }, codec.ErrInvalidData},
		{"truncated-parameters", func(v *session.ConstructionRecipe) { v.Parameters = []byte{0xa1, 0x61, 'p'} }, codec.ErrInvalidData},
		{"trailing-parameters", func(v *session.ConstructionRecipe) { v.Parameters = append(v.Parameters, 0) }, codec.ErrInvalidData},
		{"invalid-selector", func(v *session.ConstructionRecipe) { v.Parameters = []byte{0xa1, 0x61, 'p', 0x81, 0xa0} }, codec.ErrInvalidData},
		{"short-check", func(v *session.ConstructionRecipe) { v.Check = v.Check[:35] }, codec.ErrInvalidData},
		{"long-check", func(v *session.ConstructionRecipe) { v.Check = append(v.Check, 0) }, codec.ErrInvalidData},
		{"check-text", func(v *session.ConstructionRecipe) { v.Check = []byte(contentid.Sum(nil).String()) }, codec.ErrInvalidData},
		{"check-digest", func(v *session.ConstructionRecipe) { v.Check = v.Check[4:] }, codec.ErrInvalidData},
		{"check-profile", func(v *session.ConstructionRecipe) { v.Check[1] = 0x55 }, nil},
		{"mismatch", func(v *session.ConstructionRecipe) { v.Check[35] ^= 1 }, contentid.ErrMismatch},
		{"changed-experimental-v1", func(v *session.ConstructionRecipe) {
			// A valid CID for older v1 rendering is still a mismatch, not waived.
			old, err := codec.Encode(format.RequestV1{API: "api", Model: "old v1 rendering"}, testLimits())
			requireConstruction(t, err)
			check := contentid.Sum(old).Binary()
			v.Check = check[:]
		}, contentid.ErrMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newConstructionFixture(t)
			capture := f.view()
			recipe := f.build(format.ParametersV1{}).Recipe()
			tc.mutate(&recipe)
			id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: recipe})
			requireConstruction(t, err)
			f.submit()
			loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), f.policy)
			requireConstruction(t, err)
			for _, snapshot := range []session.Snapshot{f.s.Snapshot(), loaded.Snapshot()} {
				assertReconstructionFailure(t, snapshot, id, testLimits(), tc.want)
				request, ok := snapshot.RecordAt(snapshot.RecordCount() - 1).ModelRequest()
				if !ok || request.ID() != id || request.Recipe().Algorithm() != recipe.Algorithm {
					t.Fatal("unsupported or mismatched recipe impaired session inspection")
				}
			}
		})
	}
}

func TestReconstructIdentityAndLimits(t *testing.T) {
	f := newConstructionFixture(t)
	capture := f.view()
	built := f.build(format.ParametersV1{})
	id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
	requireConstruction(t, err)
	f.submit()
	assertReconstructionFailure(t, session.Snapshot{}, id, testLimits(), session.ErrIdentity)
	assertReconstructionFailure(t, f.s.Snapshot(), session.ModelRequestID{}, testLimits(), session.ErrIdentity)
	missing, err := session.NewModelRequestID(f.run, 999)
	requireConstruction(t, err)
	assertReconstructionFailure(t, f.s.Snapshot(), missing, testLimits(), session.ErrIdentity)
	foreignHistory, err := historyid.ParseHistoryID("HST2222222222222222222222")
	requireConstruction(t, err)
	foreignRun, err := session.NewRunID(foreignHistory, 1)
	requireConstruction(t, err)
	foreign, err := session.NewModelRequestID(foreignRun, 1)
	requireConstruction(t, err)
	assertReconstructionFailure(t, f.s.Snapshot(), foreign, testLimits(), session.ErrIdentity)
	assertReconstructionFailure(t, f.s.Snapshot(), id, codec.Limits{}, codec.ErrInvalidLimits)
	low := testLimits()
	low.MaxBytes = 1
	assertReconstructionFailure(t, f.s.Snapshot(), id, low, codec.ErrLimitExceeded)
	low = testLimits()
	low.MaxStringBytes = 1
	assertReconstructionFailure(t, f.s.Snapshot(), id, low, codec.ErrLimitExceeded)
	high := testLimits()
	high.MaxBytes *= 2
	got, err := Reconstruct(f.s.Snapshot(), id, high)
	requireConstruction(t, err)
	if got.CID() != built.Request().CID() {
		t.Fatal("successful limits changed bytes")
	}
}

func TestReconstructCIDIgnoresHistoryRouteAndIrrelevantSelectors(t *testing.T) {
	var requests []Request
	var recipes []session.ConstructionRecipe
	for i, textID := range []string{"HST1111111111111111111111", "HST2222222222222222222222"} {
		f := newConstructionFixture(t)
		if i != 0 {
			// Independent accepted history, not a relabeled Source or request DTO.
			requireConstruction(t, f.b.Discard())
			scope, err := historyid.ParseHistoryID(textID)
			requireConstruction(t, err)
			f.s, err = session.New(f.h, scope, f.policy)
			requireConstruction(t, err)
			f.b, err = f.s.BeginCommit()
			requireConstruction(t, err)
			requireConstruction(t, f.b.Create())
			f.run, err = f.b.StartRun()
			requireConstruction(t, err)
			requireConstruction(t, f.b.ReplaceModelBinding(scope, session.ModelBinding{Route: "different route", API: "api", Model: "model"}))
		}
		parameters := format.ParametersV1{}
		if i != 0 {
			parameters.PrivateReplay = []format.PrivateReplaySelector{{SourceAPI: "unused", SourceModel: "unused", Producer: "unused", Format: "unused", Version: 1}}
		}
		capture := f.view()
		built, err := Construct(capture, parameters, testLimits())
		requireConstruction(t, err)
		recipe := built.Recipe()
		id, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: recipe})
		requireConstruction(t, err)
		f.submit()
		loaded, err := session.Load(t.Context(), f.h, f.run.HistoryID(), f.policy)
		requireConstruction(t, err)
		got, err := Reconstruct(loaded.Snapshot(), id, testLimits())
		requireConstruction(t, err)
		requests = append(requests, got)
		recipes = append(recipes, recipe)
	}
	one, err := codec.Encode(requests[0].MutableClone(), testLimits())
	requireConstruction(t, err)
	two, err := codec.Encode(requests[1].MutableClone(), testLimits())
	requireConstruction(t, err)
	if !bytes.Equal(one, two) || requests[0].CID() != requests[1].CID() || bytes.Equal(recipes[0].Parameters, recipes[1].Parameters) {
		t.Fatal("history, route or irrelevant selectors contaminated request identity")
	}
}
