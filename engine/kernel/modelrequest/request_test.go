package modelrequest

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func testLimits() codec.Limits {
	return codec.Limits{MaxBytes: 1 << 24, MaxStringBytes: 1 << 22, MaxByteStringBytes: 1 << 22, MaxArrayElements: 1000, MaxMapPairs: 100, MaxDepth: 64, MaxNodes: 100000, MaxCollectionElements: 100000}
}

func requestFixture() format.RequestV1 {
	text := sessionformat.ContentPart{Text: new("")}
	media := sessionformat.ContentPart{Media: &sessionformat.Media{Kind: sessionformat.MediaKindImage, Source: sessionformat.MediaSourceInline, MIMEType: "image/png", InlineData: []byte{1, 2, 3}}}
	block := format.ContentBlock{Parts: []sessionformat.ContentPart{text, media}}
	return format.RequestV1{
		API: "api", Model: "model", Settings: sessionformat.ModelSettings{ReasoningEffort: new(" target effort "), MaxOutputTokens: new(int64(17))},
		RootInstructions: []format.ContentBlock{block.Clone()}, UserInstructions: []format.ContentBlock{block.Clone()},
		Tools: []sessionformat.ToolDefinition{{Name: "tool", Description: "desc", ArgumentSchema: []byte("true")}},
		Conversation: []format.ConversationItem{
			{User: &format.UserMessage{Parts: block.Clone().Parts}},
			{Assistant: &format.AssistantMessage{Items: []format.AssistantItem{{Output: new(block.Clone())}, {Call: &format.ToolCall{ProviderCallID: "original", Arguments: []byte{0xff, '{'}}}, {Refusal: new(block.Clone())}}, Private: &sessionformat.OpaqueProviderData{Producer: "producer", Format: "format", Version: 1, Data: []byte{7, 8, 9}}}},
			{ToolResult: &format.ToolResult{ProviderCallID: "original", Outcome: format.ToolOutcomeIndeterminate, Parts: block.Clone().Parts}},
		},
	}
}

func TestFinalizationOwnershipAndIdentity(t *testing.T) {
	input := requestFixture()
	before := input.Clone()
	parameters := format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{SourceAPI: "api", SourceModel: "model", Producer: "producer", Format: "format", Version: 1}}}
	built, err := finalizeConstruction(input, parameters, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := built.Request()
	encoded, err := codec.Encode(before, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if request.CID() != contentid.Sum(encoded) {
		t.Fatal("CID does not identify canonical bytes")
	}
	recipe := built.Recipe()
	if recipe.Algorithm != "chronological" || recipe.Version != 1 || len(recipe.Check) != 36 {
		t.Fatal("recipe contract")
	}
	check, err := contentid.ParseBinary(recipe.Check)
	if err != nil || check != request.CID() {
		t.Fatalf("check: %v", err)
	}
	decoded, err := codec.Decode[format.ParametersV1](recipe.Parameters, testLimits())
	if err != nil || !decoded.Equal(parameters) {
		t.Fatalf("recipe parameters: %v", err)
	}
	if &request.value.Tools[0] != &input.Tools[0] || request.value.Settings.MaxOutputTokens != input.Settings.MaxOutputTokens {
		t.Fatal("finalization copied transferred backing")
	}
	// MutableClone, not internal finalization, detaches mutable request storage.
	detached := request.MutableClone()
	*detached.Settings.ReasoningEffort = "changed"
	*detached.Settings.MaxOutputTokens = 1
	*detached.RootInstructions[0].Parts[0].Text = "changed"
	detached.RootInstructions[0].Parts[1].Media.InlineData[0] = 0
	detached.UserInstructions[0].Parts = nil
	detached.Tools[0].ArgumentSchema[0] = '!'
	detached.Conversation[0].User.Parts = nil
	detached.Conversation[1].Assistant.Items[0].Output.Parts = nil
	detached.Conversation[1].Assistant.Items[1].Call.Arguments[0] = 0
	detached.Conversation[1].Assistant.Private.Data[0] = 0
	detached.Conversation[2].ToolResult.Parts = nil
	parameters.PrivateReplay[0].SourceAPI = "changed"
	clear(recipe.Check)
	clear(recipe.Parameters)
	if !built.Request().MutableClone().Equal(before) {
		t.Fatal("MutableClone exposes mutable backing")
	}
	if again := built.Recipe(); len(again.Check) != 36 || bytes.Equal(again.Check, recipe.Check) || bytes.Equal(again.Parameters, recipe.Parameters) {
		t.Fatal("Recipe exposes mutable backing")
	}
	adequate := testLimits()
	adequate.MaxBytes = 1 << 20
	same, err := finalizeConstruction(before.Clone(), format.ParametersV1{}, adequate)
	if err != nil || same.Request().CID() != request.CID() {
		t.Fatalf("limits/unused recipe changed CID: %v", err)
	}
	changed := before.Clone()
	*changed.Settings.MaxOutputTokens++
	different, err := finalizeRequest(changed, testLimits())
	if err != nil || different.CID() == request.CID() {
		t.Fatalf("settings omitted from identity: %v", err)
	}
}

func TestRequestNestedViews(t *testing.T) {
	input := requestFixture()
	request, err := finalizeRequest(input, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if request.API() != input.API || request.Model() != input.Model {
		t.Fatal("target")
	}
	if effort, ok := request.Settings().ReasoningEffort(); !ok || effort != *input.Settings.ReasoningEffort {
		t.Fatal("effort")
	}
	if tokens, ok := request.Settings().MaxOutputTokens(); !ok || tokens != 17 {
		t.Fatal("tokens")
	}
	checkParts := func(parts interface{ Len() int }) {
		if parts.Len() != 2 {
			t.Fatal("parts length")
		}
	}
	for _, blocks := range []struct{ block ContentBlockView }{{request.RootInstructions().At(0)}, {request.UserInstructions().At(0)}} {
		checkParts(blocks.block.Parts())
		if text, ok := blocks.block.Parts().At(0).Text(); !ok || text != "" {
			t.Fatal("present empty text")
		}
		media, ok := blocks.block.Parts().At(1).Media()
		if !ok {
			t.Fatal("media presence")
		}
		if media.Kind() != sessionformat.MediaKindImage || media.Source() != sessionformat.MediaSourceInline || media.MIMEType() != "image/png" {
			t.Fatal("media metadata")
		}
		copied := media.InlineData().Copy()
		copied[0] = 0
		if media.InlineData().Copy()[0] != 1 {
			t.Fatal("media copy aliases")
		}
	}
	tool := request.Tools().At(0)
	if tool.Name() != "tool" || tool.Description() != "desc" || string(tool.ArgumentSchema().Copy()) != "true" {
		t.Fatal("tool definition")
	}
	schema := tool.ArgumentSchema().Copy()
	schema[0] = '!'
	if string(tool.ArgumentSchema().Copy()) != "true" {
		t.Fatal("schema copy aliases")
	}
	user, ok := request.Conversation().At(0).User()
	if !ok {
		t.Fatal("user")
	}
	checkParts(user.Parts())
	assistant, ok := request.Conversation().At(1).Assistant()
	if !ok || assistant.Items().Len() != 3 {
		t.Fatal("assistant")
	}
	output, ok := assistant.Items().At(0).Output()
	if !ok {
		t.Fatal("output")
	}
	checkParts(output.Parts())
	refusal, ok := assistant.Items().At(2).Refusal()
	if !ok {
		t.Fatal("refusal")
	}
	checkParts(refusal.Parts())
	call, ok := assistant.Items().At(1).Call()
	if !ok || call.ProviderCallID() != "original" || call.Name() != "" {
		t.Fatal("call")
	}
	args := call.Arguments().Copy()
	args[0] = 0
	if call.Arguments().Copy()[0] != 0xff {
		t.Fatal("arguments copy aliases")
	}
	private, ok := assistant.Private()
	if !ok || private.Producer() != "producer" || private.Format() != "format" || private.Version() != 1 {
		t.Fatal("private metadata")
	}
	data := private.Data().Copy()
	data[0] = 0
	if private.Data().Copy()[0] != 7 {
		t.Fatal("private copy aliases")
	}
	result, ok := request.Conversation().At(2).ToolResult()
	if !ok || result.ProviderCallID() != call.ProviderCallID() || result.Outcome() != format.ToolOutcomeIndeterminate {
		t.Fatal("result")
	}
	checkParts(result.Parts())
	count := 0
	for range request.Conversation().All() {
		count++
	}
	if count != 3 {
		t.Fatal("iteration")
	}
	if !request.MutableClone().Equal(input) {
		t.Fatal("nested traversal mutated backing")
	}
}

func TestRequestZeroViews(t *testing.T) {
	var request Request
	if request.CID() != (contentid.CID{}) || request.API() != "" || request.Model() != "" || !reflect.DeepEqual(request.MutableClone(), format.RequestV1{}) {
		t.Fatal("zero request")
	}
	if request.RootInstructions().Len() != 0 || request.UserInstructions().Len() != 0 || request.Tools().Len() != 0 || request.Conversation().Len() != 0 {
		t.Fatal("zero collections")
	}
	if _, ok := request.Settings().ReasoningEffort(); ok {
		t.Fatal("zero effort")
	}
	if _, ok := request.Settings().MaxOutputTokens(); ok {
		t.Fatal("zero tokens")
	}
	if _, ok := (ConversationItemView{}).User(); ok {
		t.Fatal("zero user")
	}
	if _, ok := (ConversationItemView{}).Assistant(); ok {
		t.Fatal("zero assistant")
	}
	if _, ok := (ConversationItemView{}).ToolResult(); ok {
		t.Fatal("zero result")
	}
	if _, ok := (AssistantItemView{}).Output(); ok {
		t.Fatal("zero output")
	}
	if _, ok := (AssistantItemView{}).Refusal(); ok {
		t.Fatal("zero refusal")
	}
	if _, ok := (AssistantItemView{}).Call(); ok {
		t.Fatal("zero call")
	}
	if _, ok := (AssistantMessageView{}).Private(); ok {
		t.Fatal("zero private")
	}
	if (AssistantMessageView{}).Items().Len() != 0 {
		t.Fatal("zero items")
	}
	if _, ok := (ContentPartView{}).Text(); ok {
		t.Fatal("zero text")
	}
	if _, ok := (ContentPartView{}).Media(); ok {
		t.Fatal("zero media")
	}
	if (ContentBlockView{}).Parts().Len() != 0 || (UserMessageView{}).Parts().Len() != 0 || (ToolResultView{}).Parts().Len() != 0 {
		t.Fatal("zero parts")
	}
	if (ToolCallView{}).Arguments().Len() != 0 || (MediaView{}).InlineData().Len() != 0 || (ToolDefinitionView{}).ArgumentSchema().Len() != 0 || (OpaqueProviderDataView{}).Data().Len() != 0 {
		t.Fatal("zero bytes")
	}
	var built ConstructionResult
	if built.Request().CID() != (contentid.CID{}) || !reflect.DeepEqual(built.Recipe(), sessionformat.ConstructionRecipe{}) {
		t.Fatal("zero result")
	}
}

func TestFinalizationBounds(t *testing.T) {
	v := requestFixture()
	l := testLimits()
	l.MaxByteStringBytes = 1
	got, err := finalizeConstruction(v, format.ParametersV1{}, l)
	if !errors.Is(err, codec.ErrLimitExceeded) || got.Request().value != nil || !reflect.DeepEqual(got.Recipe(), sessionformat.ConstructionRecipe{}) {
		t.Fatalf("request limit: %v", err)
	}
	l = testLimits()
	l.MaxArrayElements = 1
	got, err = finalizeConstruction(v, format.ParametersV1{}, l)
	if !errors.Is(err, codec.ErrLimitExceeded) || got.Request().value != nil {
		t.Fatalf("array limit: %v", err)
	}
	got, err = finalizeConstruction(v, format.ParametersV1{PrivateReplay: []format.PrivateReplaySelector{{}}}, testLimits())
	if !errors.Is(err, codec.ErrInvalidData) || got.Request().value != nil {
		t.Fatalf("invalid parameters: %v", err)
	}
	got, err = finalizeConstruction(v, format.ParametersV1{}, codec.Limits{})
	if !errors.Is(err, codec.ErrInvalidLimits) || got.Request().value != nil {
		t.Fatalf("invalid policy: %v", err)
	}
	// A tiny bound must reject a large leaf before encoded output allocation.
	v.Conversation[1].Assistant.Items[1].Call.Arguments = bytes.Repeat([]byte{'x'}, 1<<20)
	l = testLimits()
	l.MaxBytes = 100
	measured := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			r, err := finalizeRequest(v, l)
			if !errors.Is(err, codec.ErrLimitExceeded) || r.value != nil {
				b.Fatal("limit not enforced")
			}
		}
	})
	if measured.AllocedBytesPerOp() > 1024 {
		t.Fatalf("rejected finalization allocated %d bytes", measured.AllocedBytesPerOp())
	}
}

var scalarSink string
var bytesSink []byte

func TestRequestScalarAndLeafCopies(t *testing.T) {
	v := requestFixture()
	// Large unrelated siblings make accidental whole-request copies observable.
	v.RootInstructions[0].Parts[0].Text = new(strings.Repeat("r", 1<<20))
	v.Conversation[1].Assistant.Private.Data = bytes.Repeat([]byte{'p'}, 1<<20)
	request, err := finalizeRequest(v, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertRequestScalarAndLeafCopies(t, request, 1)
}

func assertRequestScalarAndLeafCopies(t *testing.T, request Request, assistantIndex int) {
	t.Helper()
	a, ok := request.Conversation().At(assistantIndex).Assistant()
	if !ok {
		t.Fatal("expected assistant message")
	}
	call, ok := a.Items().At(1).Call()
	if !ok || call.Arguments().Len() == 0 {
		t.Fatal("expected call with nonempty arguments")
	}
	if allocs := testing.AllocsPerRun(100, func() {
		a, _ := request.Conversation().At(assistantIndex).Assistant()
		c, _ := a.Items().At(1).Call()
		scalarSink = c.ProviderCallID()
		_, _ = request.Settings().MaxOutputTokens()
	}); allocs != 0 {
		t.Fatalf("scalar navigation allocates %.0f", allocs)
	}
	measured := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			bytesSink = call.Arguments().Copy()
		}
	})
	if measured.AllocsPerOp() != 1 || measured.AllocedBytesPerOp() > 16 {
		t.Fatalf("leaf copy: %d allocs, %d bytes", measured.AllocsPerOp(), measured.AllocedBytesPerOp())
	}
	original := call.Arguments().At(0)
	detached := call.Arguments().Copy()
	detached[0] ^= 1
	if call.Arguments().At(0) != original {
		t.Fatal("selected leaf copy aliases request backing")
	}
}
