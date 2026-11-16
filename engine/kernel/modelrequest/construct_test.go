package modelrequest

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

// constructionFixture uses real authoring, admission and memory history storage.
// Open captures are deliberately used before Submit, not fabricated RecordViews.
type constructionFixture struct {
	t      *testing.T
	h      *history.History
	s      *session.Session
	b      *session.SessionCommit
	run    session.RunID
	policy session.Policy
}

func requireConstruction(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newConstructionFixture(t *testing.T) *constructionFixture {
	t.Helper()
	limits := testLimits()
	limits.MaxArrayElements = 16384
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	requireConstruction(t, err)
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	requireConstruction(t, err)
	policy := session.Policy{PayloadLimits: limits, CommitLimits: limits, MaxRecordsPerCommit: 1024, SoftMaxRetainedRecords: 32768, MaxRetainedRecords: 32768, SoftMaxPayloadBytes: 1 << 30, MaxPayloadBytes: 1 << 30, SoftMaxDecodedWork: 1 << 40, MaxDecodedWork: 1 << 40}
	s, err := session.New(h, id, policy)
	requireConstruction(t, err)
	b, err := s.BeginCommit()
	requireConstruction(t, err)
	requireConstruction(t, b.Create())
	run, err := b.StartRun()
	requireConstruction(t, err)
	requireConstruction(t, b.ReplaceModelBinding(id, session.ModelBinding{Route: "route", API: "api", Model: "model"}))
	return &constructionFixture{t: t, h: h, s: s, b: b, run: run, policy: policy}
}

func (f *constructionFixture) view() session.CommitView {
	f.t.Helper()
	v, err := f.b.View()
	requireConstruction(f.t, err)
	return v
}
func (f *constructionFixture) submit() {
	f.t.Helper()
	_, err := f.s.SubmitCommit(f.t.Context(), f.b)
	requireConstruction(f.t, err)
	f.b, err = f.s.BeginCommit()
	requireConstruction(f.t, err)
}
func (f *constructionFixture) build(parameters format.ParametersV1) ConstructionResult {
	f.t.Helper()
	built, err := Construct(f.view(), parameters, testLimits())
	requireConstruction(f.t, err)
	return built
}
func (f *constructionFixture) response(items []session.ResponseItem, private *session.OpaqueProviderData) session.ModelResponseView {
	f.t.Helper()
	v := f.view()
	built, err := Construct(v, format.ParametersV1{}, testLimits())
	requireConstruction(f.t, err)
	request, err := f.b.AddModelRequest(f.run, v, session.ModelRequest{Recipe: built.Recipe()})
	requireConstruction(f.t, err)
	f.submit()
	assertConstructionFailure(f.t, f.view(), testLimits(), nil)
	requireConstruction(f.t, f.b.AddModelResponse(request, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: items, Private: private}))
	f.submit()
	response, _ := f.s.Snapshot().RecordAt(f.s.Snapshot().RecordCount() - 1).ModelResponse()
	return response
}

func textParts(values ...string) []session.ContentPart {
	var out []session.ContentPart
	for _, v := range values {
		out = append(out, session.ContentPart{Text: new(v)})
	}
	return out
}
func instruction(text string) session.InstructionBlock {
	return session.InstructionBlock{Key: "harness/core", Parts: textParts(text), Source: session.ContentSource{Description: "private provenance"}}
}
func inline(purpose session.PromptPurpose, values ...string) session.InputItem {
	return session.InputItem{Inline: &session.AttributedInput{Actor: "not a role", Purpose: purpose, Source: session.ContentSource{Description: "private provenance"}, Parts: textParts(values...)}}
}
func assertConstructionFailure(t testing.TB, source Source, limits codec.Limits, want error) {
	t.Helper()
	result, err := Construct(source, format.ParametersV1{}, limits)
	if err == nil || (want != nil && !errors.Is(err, want)) || !reflect.DeepEqual(result, ConstructionResult{}) {
		t.Fatalf("expected zero construction and %v; got error %v", want, err)
	}
}

func TestInstructionKeysDoNotAffectLogicalRequest(t *testing.T) {
	for _, collection := range []string{"root", "user"} {
		t.Run(collection, func(t *testing.T) {
			f := newConstructionFixture(t)
			scope := f.run.HistoryID()
			root := session.RootInstructions{Blocks: []session.InstructionBlock{instruction("root contract")}}
			user := session.UserInstructions{Blocks: []session.InstructionBlock{instruction("user guidance")}}
			requireConstruction(t, f.b.ReplaceRootInstructions(scope, root))
			requireConstruction(t, f.b.ReplaceUserInstructions(scope, user))
			f.submit()
			before := f.s.Snapshot()
			original, err := Construct(before, format.ParametersV1{}, testLimits())
			requireConstruction(t, err)
			if collection == "root" {
				root.Blocks[0].Key = "different/root-key"
				requireConstruction(t, f.b.ReplaceRootInstructions(scope, root))
			} else {
				user.Blocks[0].Key = "different/user-key"
				requireConstruction(t, f.b.ReplaceUserInstructions(scope, user))
			}
			f.submit()
			loaded, err := session.Load(t.Context(), f.h, scope, f.policy)
			requireConstruction(t, err)
			for _, after := range []session.Snapshot{f.s.Snapshot(), loaded.Snapshot()} {
				gotRoot, _ := after.RootInstructions()
				gotUser, _ := after.UserInstructions()
				if after.Head() == before.Head() || !gotRoot.MutableClone().Equal(root) || !gotUser.MutableClone().Equal(user) {
					t.Fatal("session did not retain the key-only change")
				}
				rebuilt, err := Construct(after, format.ParametersV1{}, testLimits())
				requireConstruction(t, err)
				if rebuilt.Request().CID() != original.Request().CID() || !rebuilt.Request().MutableClone().Equal(original.Request().MutableClone()) {
					t.Fatal("bookkeeping key leaked into logical request content or CID")
				}
			}
			// Positive control: this is not a constructor that ignores instructions.
			if collection == "root" {
				*root.Blocks[0].Parts[0].Text = "changed root contract"
				requireConstruction(t, f.b.ReplaceRootInstructions(scope, root))
			} else {
				*user.Blocks[0].Parts[0].Text = "changed user guidance"
				requireConstruction(t, f.b.ReplaceUserInstructions(scope, user))
			}
			if f.build(format.ParametersV1{}).Request().CID() == original.Request().CID() {
				t.Fatal("changed instruction content did not change logical request CID")
			}
		})
	}
}

func TestConstructChronologicalCaptureSubmitLoad(t *testing.T) {
	const inputSentinel = "selected-input-sentinel"
	const privateSentinel = "opaque-private-sentinel"
	const evidenceSentinel = "observed-evidence-sentinel"
	const diagnosticSentinel = "tool-diagnostic-sentinel"
	privateData := append([]byte{0xff, 0, 7}, []byte(privateSentinel)...)
	privateData = append(privateData, bytes.Repeat([]byte{'p'}, 1<<20)...)
	rootText := strings.Repeat("r", 1<<20)
	checkErrorPrivacy := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected failure")
		}
		for _, sentinel := range []string{inputSentinel, privateSentinel, evidenceSentinel, diagnosticSentinel} {
			if strings.Contains(err.Error(), sentinel) {
				t.Fatal("error disclosed retained content")
			}
		}
	}
	f := newConstructionFixture(t)
	id := f.run.HistoryID()
	requireConstruction(t, f.b.ReplaceRootInstructions(id, session.RootInstructions{Blocks: []session.InstructionBlock{instruction("old root")}}))
	requireConstruction(t, f.b.ReplaceUserInstructions(id, session.UserInstructions{Blocks: []session.InstructionBlock{instruction("prefix")}}))
	requireConstruction(t, f.b.ReplaceToolCatalog(id, session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "tool", Description: "description", ArgumentSchema: []byte(" true ")}}}))
	media := session.ContentPart{Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, InlineData: []byte{1, 2, 3}, MIMEType: "image/png"}}
	selected := append(textParts("", " \n", inputSentinel), media)
	u, err := f.b.AddUserInput(f.run, session.UserInput{Parts: selected})
	requireConstruction(t, err)
	_, err = f.b.AddUserInput(f.run, session.UserInput{Parts: textParts("unselected MUST NOT RENDER")})
	requireConstruction(t, err)
	requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{
		{UserInputID: new(u.RunRelative())}, inline(session.PromptInstructions, "", "inline"), inline(session.PromptContext, ""), {UserInputID: new(u.RunRelative())}, inline(session.PromptContext, " "),
	}}))
	first := f.build(format.ParametersV1{})
	wantUsers := []format.ConversationItem{
		{User: &format.UserMessage{Parts: append(textParts(" \n", inputSentinel), media)}},
		{User: &format.UserMessage{Parts: textParts("<supplemental-instructions>\n", "inline", "\n</supplemental-instructions>")}},
		{User: &format.UserMessage{Parts: append(textParts(" \n", inputSentinel), media)}},
		{User: &format.UserMessage{Parts: textParts("<supplemental-context>\n", " ", "\n</supplemental-context>")}},
	}
	initial := format.RequestV1{API: "api", Model: "model", RootInstructions: []format.ContentBlock{{Parts: textParts("old root")}}, UserInstructions: []format.ContentBlock{{Parts: textParts("prefix")}}, Tools: []session.ToolDefinition{{Name: "tool", Description: "description", ArgumentSchema: []byte(" true ")}}, Conversation: wantUsers}
	if !first.Request().MutableClone().Equal(initial) {
		t.Fatalf("initial projection mismatch: %#v", first.Request().MutableClone())
	}
	f.submit()
	origin := f.view()
	originBuilt := f.build(format.ParametersV1{})
	originID, err := f.b.AddModelRequest(f.run, origin, session.ModelRequest{Recipe: originBuilt.Recipe()})
	requireConstruction(t, err)
	f.submit()
	requireConstruction(t, f.b.AddModelResponse(originID, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{
		{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: textParts("", "output")}},
		{Call: &session.RequestedCall{ProviderCallID: "provider/一", Name: "", Arguments: []byte{0xff, '{', 0}}},
		{Content: &session.ResponseContent{Kind: session.ResponseContentReasoning, Parts: textParts("private reasoning")}},
		{Content: &session.ResponseContent{Kind: session.ResponseContentRefusal, Parts: textParts("refusal")}},
		{Call: &session.RequestedCall{ProviderCallID: "provider/two", Name: "unknown", Arguments: []byte("not JSON")}},
	}, Private: &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 3, Data: privateData}, Evidence: &session.ObservedEvidence{Detail: new(evidenceSentinel), Correlation: new(evidenceSentinel)}}))
	f.submit()
	response, _ := f.s.Snapshot().RecordAt(f.s.Snapshot().RecordCount() - 1).ModelResponse()
	assertConstructionFailure(t, f.view(), testLimits(), nil)
	for call := range response.Calls() {
		requireConstruction(t, f.b.AddToolInvocation(call.ID(), session.ToolInvocation{Name: "effective substitute", Arguments: value.Map{}}))
	}
	f.submit()
	// Settlement arrival order differs from requested order. Context and route
	// replacements must not change the originating request's private replay match.
	requireConstruction(t, f.b.AddToolDisposition(response.CallAt(1).ID(), session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: session.ToolNotInvokedUserDenied}, Diagnostic: new(diagnosticSentinel)}))
	requireConstruction(t, f.b.AddToolDisposition(response.CallAt(0).ID(), session.ToolDisposition{Returned: &session.ToolReturned{Status: session.ToolReturnError, Output: session.ToolOutput{Capture: session.ToolCaptureTruncated, Parts: []session.ToolOutputPart{{JSON: new([]byte(" {\"x\": 1} \n"))}}}}, Diagnostic: new(diagnosticSentinel)}))
	requireConstruction(t, f.b.ReplaceModelBinding(id, session.ModelBinding{Route: "different route", API: "next API", Model: "next model", Settings: session.ModelSettings{ReasoningEffort: new("native effort"), MaxOutputTokens: new(int64(123))}}))
	emptyInstruction := instruction("")
	emptyInstruction.Key = "harness/empty"
	requireConstruction(t, f.b.ReplaceRootInstructions(id, session.RootInstructions{Blocks: []session.InstructionBlock{emptyInstruction, instruction(rootText)}}))
	requireConstruction(t, f.b.ReplaceUserInstructions(id, session.UserInstructions{}))
	parameters := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 3}, {SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 3}}}
	capture := f.view()
	built, err := Construct(capture, parameters, testLimits())
	requireConstruction(t, err)
	want := initial.Clone()
	want.API, want.Model = "next API", "next model"
	want.Settings = session.ModelSettings{ReasoningEffort: new("native effort"), MaxOutputTokens: new(int64(123))}
	want.RootInstructions = []format.ContentBlock{{Parts: textParts(rootText)}}
	want.UserInstructions = nil
	want.Conversation = append(want.Conversation,
		format.ConversationItem{Assistant: &format.AssistantMessage{Items: []format.AssistantItem{
			{Output: &format.ContentBlock{Parts: textParts("output")}}, {Call: &format.ToolCall{ProviderCallID: "provider/一", Arguments: []byte{0xff, '{', 0}}}, {Refusal: &format.ContentBlock{Parts: textParts("refusal")}}, {Call: &format.ToolCall{ProviderCallID: "provider/two", Name: "unknown", Arguments: []byte("not JSON")}},
		}, Private: &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 3, Data: privateData}}},
		format.ConversationItem{ToolResult: &format.ToolResult{ProviderCallID: "provider/一", Outcome: format.ToolOutcomeReturnedError, Parts: textParts("Tool returned an error.", "Tool output is incomplete.", " {\"x\": 1} \n")}},
		format.ConversationItem{ToolResult: &format.ToolResult{ProviderCallID: "provider/two", Outcome: format.ToolOutcomeNotInvoked, Parts: textParts("User denied tool execution")}},
	)
	if !built.Request().MutableClone().Equal(want) {
		t.Fatalf("complete projection mismatch: %#v", built.Request().MutableClone())
	}
	wire, err := codec.Encode(want, testLimits())
	requireConstruction(t, err)
	check := built.Request().CID().Binary()
	decoded, err := codec.Decode[format.ParametersV1](built.Recipe().Parameters, testLimits())
	requireConstruction(t, err)
	if built.Request().CID() != contentid.Sum(wire) || !bytes.Equal(built.Recipe().Check, check[:]) || !decoded.Equal(parameters) || built.Recipe().Algorithm != "chronological" || built.Recipe().Version != 1 {
		t.Fatal("canonical recipe/CID")
	}
	actualWire, err := codec.Encode(built.Request().MutableClone(), testLimits())
	requireConstruction(t, err)
	for _, sentinel := range []string{evidenceSentinel, diagnosticSentinel} {
		if bytes.Contains(actualWire, []byte(sentinel)) {
			t.Fatal("logical request disclosed evidence or diagnostics")
		}
	}
	omitted, err := Construct(capture, format.ParametersV1{}, testLimits())
	requireConstruction(t, err)
	omittedWire, err := codec.Encode(omitted.Request().MutableClone(), testLimits())
	requireConstruction(t, err)
	if bytes.Contains(omittedWire, []byte(privateSentinel)) || !bytes.Contains(omittedWire, []byte(inputSentinel)) {
		t.Fatal("private omission discarded input or disclosed an unselected envelope")
	}
	low := testLimits()
	low.MaxByteStringBytes = 1
	failed, err := Construct(capture, parameters, low)
	checkErrorPrivacy(err)
	if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(failed, ConstructionResult{}) {
		t.Fatal("limited construction did not return zero")
	}
	assertRequestScalarAndLeafCopies(t, built.Request(), len(wantUsers))
	// Both reversed settlements are still proposed immediately before this target.
	target, err := f.b.AddModelRequest(f.run, capture, session.ModelRequest{Recipe: built.Recipe()})
	requireConstruction(t, err)
	f.submit()
	capturedAgain, err := Construct(capture, parameters, testLimits())
	requireConstruction(t, err)
	if !capturedAgain.Request().MutableClone().Equal(want) || capturedAgain.Request().CID() != contentid.Sum(wire) {
		t.Fatal("submission changed the pinned source")
	}
	loaded, err := session.Load(t.Context(), f.h, id, f.policy)
	requireConstruction(t, err)
	for _, snapshot := range []session.Snapshot{f.s.Snapshot(), loaded.Snapshot()} {
		got, err := Reconstruct(snapshot, target, testLimits())
		requireConstruction(t, err)
		gotWire, err := codec.Encode(got.MutableClone(), testLimits())
		requireConstruction(t, err)
		if !got.MutableClone().Equal(want) || !bytes.Equal(gotWire, wire) || got.CID() != contentid.Sum(wire) {
			t.Fatal("Submit/Load/Reconstruct changed independently expected request")
		}
		failedRequest, err := Reconstruct(snapshot, target, low)
		checkErrorPrivacy(err)
		if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(failedRequest, Request{}) {
			t.Fatal("limited reconstruction did not return zero")
		}
	}
	reconstructed, err := Reconstruct(loaded.Snapshot(), target, testLimits())
	requireConstruction(t, err)
	assertRequestScalarAndLeafCopies(t, reconstructed, len(wantUsers))
	failed, err = Construct(loaded.Snapshot(), parameters, testLimits())
	checkErrorPrivacy(err)
	if !reflect.DeepEqual(failed, ConstructionResult{}) {
		t.Fatal("pending source returned a partial construction")
	}
	// Explicit run boundaries never withdraw earlier selected conversation or
	// reset independently replaced preparation. Unselected submissions stay absent.
	requireConstruction(t, f.b.EndRun(session.RunEndedInterrupted))
	f.run, err = f.b.StartRun()
	requireConstruction(t, err)
	u, err = f.b.AddUserInput(f.run, session.UserInput{Parts: textParts("next run")})
	requireConstruction(t, err)
	requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(u.RunRelative())}, {UserInputID: new(u.RunRelative())}}}))
	want.Conversation = append(want.Conversation, format.ConversationItem{User: &format.UserMessage{Parts: textParts("next run")}}, format.ConversationItem{User: &format.UserMessage{Parts: textParts("next run")}})
	if got := f.build(parameters).Request().MutableClone(); !got.Equal(want) {
		t.Fatal("cross-run history was reset")
	}
	requireConstruction(t, f.b.Discard())
	if !built.Request().MutableClone().Equal(wantWithoutLastTwo(want)) {
		t.Fatal("source closure mutated owned construction")
	}
	if !first.Request().MutableClone().Equal(initial) {
		t.Fatal("later preparation changed previous result")
	}
}

func wantWithoutLastTwo(v format.RequestV1) format.RequestV1 {
	v.Conversation = v.Conversation[:len(v.Conversation)-2]
	return v
}

func TestConstructPrivateReplaySelectionAndEmptyMessages(t *testing.T) {
	f := newConstructionFixture(t)
	f.response([]session.ResponseItem{
		{Content: &session.ResponseContent{Kind: session.ResponseContentReasoning, Parts: textParts("reasoning")}},
		{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: textParts("")}},
		{Content: &session.ResponseContent{Kind: session.ResponseContentRefusal}},
	}, &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 1, Data: []byte{}})
	selector := format.PrivateReplaySelector{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 1}
	for _, field := range []string{"none", "all", "api", "model", "producer", "format", "version"} {
		t.Run(field, func(t *testing.T) {
			selected := selector
			switch field {
			case "api":
				selected.SourceAPI = "*"
			case "model":
				selected.SourceModel = "other"
			case "producer":
				selected.Producer = "other"
			case "format":
				selected.Format = "other"
			case "version":
				selected.Version++
			}
			parameters := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{selected}}
			if field == "none" {
				parameters.PrivateReplay = nil
			}
			got := f.build(parameters).Request().MutableClone()
			if field == "all" {
				if len(got.Conversation) != 1 || got.Conversation[0].Assistant.Private == nil || len(got.Conversation[0].Assistant.Items) != 0 {
					t.Fatal("private-only present-empty envelope dropped")
				}
			} else if len(got.Conversation) != 0 {
				t.Fatal("nonmatching or empty content rendered")
			}
		})
	}
	first := f.build(format.ParametersV1{})
	second := f.build(format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "irrelevant", SourceModel: "model", Producer: "producer", Format: "format", Version: 1}}})
	if first.Request().CID() != second.Request().CID() || bytes.Equal(first.Recipe().Parameters, second.Recipe().Parameters) {
		t.Fatal("irrelevant parameters affect request identity")
	}
	requireConstruction(t, f.b.Discard())
}

func TestConstructProviderIDValidationIndependentOfOpaqueArguments(t *testing.T) {
	for _, ids := range [][]string{{""}, {"same", "same"}, {"valid one", "valid two"}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			f := newConstructionFixture(t)
			var items []session.ResponseItem
			for _, id := range ids {
				items = append(items, session.ResponseItem{Call: &session.RequestedCall{ProviderCallID: id, Name: "", Arguments: []byte{0xff, '{'}}})
			}
			response := f.response(items, nil)
			for call := range response.Calls() {
				requireConstruction(t, f.b.AddToolDisposition(call.ID(), session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: session.ToolNotInvokedOther}}))
			}
			if ids[0] == "" || ids[0] == ids[1] {
				assertConstructionFailure(t, f.view(), testLimits(), nil)
			} else {
				got := f.build(format.ParametersV1{}).Request().MutableClone()
				if !bytes.Equal(got.Conversation[0].Assistant.Items[0].Call.Arguments, []byte{0xff, '{'}) || got.Conversation[0].Assistant.Items[0].Call.Name != "" {
					t.Fatal("opaque call was normalized")
				}
				f.submit()
				response = f.response([]session.ResponseItem{{Call: &session.RequestedCall{ProviderCallID: ids[0]}}}, nil)
				requireConstruction(t, f.b.AddToolDisposition(response.CallAt(0).ID(), session.ToolDisposition{NotInvoked: &session.ToolNotInvoked{Reason: session.ToolNotInvokedOther}}))
				if len(f.build(format.ParametersV1{}).Request().MutableClone().Conversation) != 5 {
					t.Fatal("cross-response provider ID reuse rejected")
				}
			}
			requireConstruction(t, f.b.Discard())
		})
	}
}

func TestConstructInactiveAndClosedCapture(t *testing.T) {
	assertConstructionFailure(t, nil, testLimits(), nil)
	assertConstructionFailure(t, session.CommitView{}, testLimits(), nil)
	f := newConstructionFixture(t)
	v := f.view()
	built := f.build(format.ParametersV1{})
	requireConstruction(t, f.b.EndRun(session.RunEndedInterrupted))
	assertConstructionFailure(t, f.view(), testLimits(), nil)
	requireConstruction(t, f.b.Discard())
	again, err := Construct(v, format.ParametersV1{}, testLimits())
	requireConstruction(t, err)
	if again.Request().CID() != built.Request().CID() {
		t.Fatal("captured active run read mutable session state")
	}
}

func TestConstructUnconsumedContributionsAndLifecycleOmission(t *testing.T) {
	f := newConstructionFixture(t)
	requireConstruction(t, f.b.ReplaceRootInstructions(f.run.HistoryID(), session.RootInstructions{Blocks: []session.InstructionBlock{instruction("root")}}))
	requireConstruction(t, f.b.ReplaceUserInstructions(f.run.HistoryID(), session.UserInstructions{Blocks: []session.InstructionBlock{instruction("prefix")}}))
	requireConstruction(t, f.b.AddEffectiveInput(f.run, session.EffectiveInput{Inputs: []session.InputItem{inline(session.PromptContext, "unconsumed")}}))
	requireConstruction(t, f.b.EndRun(session.RunEndedInterrupted))
	var err error
	f.run, err = f.b.StartRun()
	requireConstruction(t, err)
	requireConstruction(t, f.b.ReplaceRootInstructions(f.run.HistoryID(), session.RootInstructions{}))
	before := f.build(format.ParametersV1{}).Request().MutableClone()
	if len(before.Conversation) != 1 || len(before.RootInstructions) != 0 || len(before.UserInstructions) != 1 {
		t.Fatal("run ending withdrew unconsumed input or coupled independent replacements")
	}
	view := f.view()
	built, err := Construct(view, format.ParametersV1{}, testLimits())
	requireConstruction(t, err)
	request, err := f.b.AddModelRequest(f.run, view, session.ModelRequest{Recipe: built.Recipe()})
	requireConstruction(t, err)
	f.submit()
	requireConstruction(t, f.b.AddInvocationFailure(request, session.InvocationFailure{Class: session.InvocationFailureTransport, Explanation: new("private failure")}))
	if !f.build(format.ParametersV1{}).Request().MutableClone().Equal(before) {
		t.Fatal("request commitment or infrastructure failure invented model prose")
	}
	f.submit()
	view = f.view()
	built, err = Construct(view, format.ParametersV1{}, testLimits())
	requireConstruction(t, err)
	retry, err := f.b.AddModelRequest(f.run, view, session.ModelRequest{Recipe: built.Recipe(), RetryOf: new(request.RunRelative())})
	requireConstruction(t, err)
	f.submit()
	requireConstruction(t, f.b.AddInvocationFailure(retry, session.InvocationFailure{Class: session.InvocationFailureTimeout}))
	if !f.build(format.ParametersV1{}).Request().MutableClone().Equal(before) {
		t.Fatal("retry invented model prose")
	}
	requireConstruction(t, f.b.Discard())
}

func TestConstructInvalidParametersAndLimits(t *testing.T) {
	f := newConstructionFixture(t)
	assertConstructionFailure(t, f.view(), codec.Limits{}, codec.ErrInvalidLimits)
	for _, parameters := range []format.ParametersV1{
		{PrivateReplay: []format.PrivateReplaySelector{{}}},
		{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: -1}}},
	} {
		result, err := Construct(f.view(), parameters, testLimits())
		if err == nil || !reflect.DeepEqual(result, ConstructionResult{}) {
			t.Fatal("invalid parameters returned usable construction")
		}
	}
	requireConstruction(t, f.b.Discard())
}

func TestQuotedMetadataLength(t *testing.T) {
	values := []string{"", "quotes\" and \\ slash", "\a\b\f\n\r\t\v\x00\x01\x1f\x7f", "尾\u2028\u0080\u00ad\U000e0001"}
	for r := rune(0); r < 0x10000; r++ {
		if r < 0xd800 || r > 0xdfff {
			values = append(values, string(r))
		}
	}
	for _, text := range values {
		want := len(strconv.Quote(text))
		if got, ok := quotedLength(text, want); !ok || got != want {
			t.Fatalf("quoted length %q: %d/%t != %d", text, got, ok, want)
		}
		if _, ok := quotedLength(text, want-1); ok {
			t.Fatal("quoted expansion accepted below bound")
		}
	}
}
