package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp/jq"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// fakeCallProvider is a minimal Provider whose CallTool returns a scripted
// CallResult (or a Go error), so CallMcpWithQuery's filtering paths can be
// exercised without a live MCP server. Only CallTool is meaningful; the other
// Provider methods are stubs that return empty/zero.
type fakeCallProvider struct {
	result CallResult
	err    error
}

func (*fakeCallProvider) ListResources(context.Context, string) ([]Resource, error) {
	return nil, nil
}
func (*fakeCallProvider) ReadResource(context.Context, string, string) (ResourceContents, error) {
	return ResourceContents{}, errors.New("not implemented")
}
func (*fakeCallProvider) ListPrompts(context.Context, string) ([]Prompt, error) { return nil, nil }
func (*fakeCallProvider) GetPrompt(context.Context, string, string, map[string]string) (PromptResult, error) {
	return PromptResult{}, errors.New("not implemented")
}
func (f *fakeCallProvider) CallTool(_ context.Context, _, _ string, _ json.RawMessage) (CallResult, error) {
	return f.result, f.err
}

var _ Provider = (*fakeCallProvider)(nil)

// scriptedCallTool builds a callMcpWithQueryTool over a fakeCallProvider
// configured to return the given scripted CallResult.
func scriptedCallTool(result CallResult) callMcpWithQueryTool {
	return callMcpWithQueryTool{provider: &fakeCallProvider{result: result}}
}

// TestCallMcpWithQueryReadOnly pins the read-only flag so the tool slots into
// read-parallel dispatch and survives the plan-mode catalog filter.
func TestCallMcpWithQueryReadOnly(t *testing.T) {
	tl := callMcpWithQueryTool{}
	if !tl.ReadOnly() {
		t.Fatalf("CallMcpWithQuery must be ReadOnly")
	}
	if got := tl.Spec().Name; got != callMcpWithQueryToolName {
		t.Fatalf("Spec().Name = %q, want %q", got, callMcpWithQueryToolName)
	}
}

// TestCallMcpWithQueryFiltersSubset asserts jq narrows a StructuredContent
// payload: `.items | length` → 2, `.items[] | .id` → [1,2], `.items[0].name`
// → "a".
func TestCallMcpWithQueryFiltersSubset(t *testing.T) {
	structured := json.RawMessage(`{"items":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`)
	tl := scriptedCallTool(CallResult{Server: "fake", Tool: "struct", StructuredContent: structured})

	for _, tc := range []struct {
		filter string
		want   string
	}{
		{filter: `.items | length`, want: `2`},
		{filter: `.items[] | .id`, want: `[1,2]`},
		{filter: `.items[0].name`, want: `"a"`},
	} {
		call := session.NewToolCall("c", callMcpWithQueryToolName, json.RawMessage(`{"server":"fake","tool":"struct","jq_filter":`+quoteJSON(tc.filter)+`}`))
		res, err := tl.Execute(context.Background(), call, nil)
		if err != nil {
			t.Fatalf("filter %q: unexpected Go error: %v", tc.filter, err)
		}
		if res.IsError {
			t.Fatalf("filter %q: unexpected IsError: %q", tc.filter, res.Content)
		}
		if res.Content != tc.want {
			t.Fatalf("filter %q: Content = %q, want %q", tc.filter, res.Content, tc.want)
		}
	}
}

// TestCallMcpWithQueryStructuredContentPrecedence asserts that when BOTH a
// StructuredContent and a TextContent mirror are present, jq runs against the
// StructuredContent (the typed view), not the text.
func TestCallMcpWithQueryStructuredContentPrecedence(t *testing.T) {
	// The two payloads DIFFER; the filter selects a field present only in the
	// structured payload, so a text-first selection would surface the wrong
	// value (or fail).
	structured := json.RawMessage(`{"src":"structured","only_here":42}`)
	textMirror := `{"src":"text"}`
	tl := scriptedCallTool(CallResult{
		Server:            "fake",
		Tool:              "struct",
		StructuredContent: structured,
		Content:           []ResourceContents{{Text: textMirror}},
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"struct","jq_filter":".only_here"}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content)
	}
	if res.Content != `42` {
		t.Fatalf("Content = %q, want 42 (StructuredContent must win over the text mirror)", res.Content)
	}
}

// TestCallMcpWithQueryTextContentJSON asserts a tool returning JSON as a bare
// TextContent (no StructuredContent) is filtered against the first
// JSON-parseable text block.
func TestCallMcpWithQueryTextContentJSON(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server:  "fake",
		Tool:    "jsontext",
		Content: []ResourceContents{{Text: `{"items":[{"id":1},{"id":2}]}`}},
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"jsontext","jq_filter":".items|length"}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content)
	}
	if res.Content != `2` {
		t.Fatalf("Content = %q, want 2", res.Content)
	}
}

// TestCallMcpWithQueryTextContentJSONFirstParseable asserts the first
// JSON-parseable text block wins: a non-JSON text block followed by a JSON
// block is filtered against the JSON block.
func TestCallMcpWithQueryTextContentJSONFirstParseable(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server: "fake",
		Tool:   "mixed",
		Content: []ResourceContents{
			{Text: `this is not json`},
			{Text: `{"n":7}`},
			{Text: `{"n":9}`},
		},
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"mixed","jq_filter":".n"}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content)
	}
	if res.Content != `7` {
		t.Fatalf("Content = %q, want 7 (first JSON-parseable text block)", res.Content)
	}
}

// TestCallMcpWithQueryNonJSONLoudError asserts a tool returning plain text
// (not JSON) fails loud with an "not JSON" message and IsError set on the
// ToolResult (model-facing), not a Go error.
func TestCallMcpWithQueryNonJSONLoudError(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server:  "fake",
		Tool:    "plain",
		Content: []ResourceContents{{Text: `just plain text, not json`}},
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"plain","jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "not JSON") {
		t.Fatalf("Content = %q, want a message mentioning \"not JSON\"", res.Content)
	}
}

// TestCallMcpWithQueryParseError asserts an invalid jq filter fails loud with
// a message mentioning "parse".
func TestCallMcpWithQueryParseError(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server:            "fake",
		Tool:              "struct",
		StructuredContent: json.RawMessage(`{"a":1}`),
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"struct","jq_filter":".a |"}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "parse") {
		t.Fatalf("Content = %q, want a message mentioning \"parse\"", res.Content)
	}
}

// TestCallMcpWithQueryJqDeadline asserts a non-terminating filter with a short
// context deadline fails loud with a message mentioning "timed out".
func TestCallMcpWithQueryJqDeadline(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server:            "fake",
		Tool:              "struct",
		StructuredContent: json.RawMessage(`{"a":1}`),
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"struct","jq_filter":"def f: f; f"}`))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res, err := tl.Execute(ctx, call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("Content = %q, want a message mentioning \"timed out\"", res.Content)
	}
}

// TestCallMcpWithQueryRemoteErrorSurfaced asserts a remote tool-level error
// (IsError on the CallResult) is surfaced verbatim (truncated) WITHOUT running
// jq — the model asked to filter a failed call; tell it the call failed.
func TestCallMcpWithQueryRemoteErrorSurfaced(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server:  "fake",
		Tool:    "boom",
		IsError: true,
		Content: []ResourceContents{{Text: `kaboom: the remote tool failed`}},
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"boom","jq_filter":".items"}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "kaboom") {
		t.Fatalf("Content = %q, want the remote error text", res.Content)
	}
}

// TestCallMcpWithQueryOversizedInput asserts a remote result larger than the
// in-memory filter cap fails loud with a message mentioning the cap and
// pointing the model at narrowing the remote call.
func TestCallMcpWithQueryOversizedInput(t *testing.T) {
	// Build a JSON payload just over jq.MaxInputBytes.
	huge := make([]byte, jq.MaxInputBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	// A single giant JSON string.
	oversized, err := json.Marshal(string(huge))
	if err != nil {
		t.Fatalf("marshal oversized: %v", err)
	}
	tl := scriptedCallTool(CallResult{
		Server:            "fake",
		Tool:              "big",
		StructuredContent: oversized,
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"big","jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "in-memory filter cap") {
		t.Fatalf("Content = %q, want a message mentioning the in-memory filter cap", res.Content)
	}
	if !strings.Contains(res.Content, "narrow") {
		t.Fatalf("Content = %q, want a message pointing at narrowing the call", res.Content)
	}
}

// TestCallMcpWithQueryMissingArgs asserts the required-argument validation is
// model-facing (IsError), not a Go error.
func TestCallMcpWithQueryMissingArgs(t *testing.T) {
	tl := scriptedCallTool(CallResult{})
	for _, args := range []string{
		`{"server":"fake","tool":"x"}`,                // missing jq_filter
		`{"server":"fake","jq_filter":"."}`,           // missing tool
		`{"tool":"x","jq_filter":"."}`,                // missing server
		`{"server":"fake","tool":"x","jq_filter":""}`, // empty jq_filter
	} {
		call := session.NewToolCall("c", callMcpWithQueryToolName, json.RawMessage(args))
		res, err := tl.Execute(context.Background(), call, nil)
		if err != nil {
			t.Fatalf("args %s: unexpected Go error: %v", args, err)
		}
		if !res.IsError {
			t.Fatalf("args %s: expected IsError result, got: %+v", args, res)
		}
	}
}

// TestCallMcpWithQueryCallError asserts a transport-level Go error from
// CallTool surfaces as a model-facing error (not a Go error), mirroring
// readResourceTool's handling.
func TestCallMcpWithQueryCallError(t *testing.T) {
	tl := callMcpWithQueryTool{provider: &fakeCallProvider{err: errors.New("transport boom")}}
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"x","jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "transport boom") {
		t.Fatalf("Content = %q, want the verbatim transport error", res.Content)
	}
}

// TestCallMcpWithQueryCallErrorUnavailable asserts a connection-drop /
// errReconnectFailed maps to the clear "unavailable after reconnect" message.
func TestCallMcpWithQueryCallErrorUnavailable(t *testing.T) {
	tl := callMcpWithQueryTool{provider: &fakeCallProvider{err: fmt.Errorf("%w: dial failed", errReconnectFailed)}}
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"x","jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result, got: %+v", res)
	}
	if !strings.Contains(res.Content, "unavailable after reconnect") {
		t.Fatalf("Content = %q, want the \"unavailable after reconnect\" message", res.Content)
	}
}

// TestCallMcpWithQueryContextCancelled asserts a cancelled context before the
// call propagates as a Go error (not a model-facing error), matching
// readResourceTool.
func TestCallMcpWithQueryContextCancelled(t *testing.T) {
	tl := scriptedCallTool(CallResult{
		Server:            "fake",
		Tool:              "struct",
		StructuredContent: json.RawMessage(`{"a":1}`),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"struct","jq_filter":"."}`))
	_, err := tl.Execute(ctx, call, nil)
	if err == nil {
		t.Fatalf("expected a Go error from a cancelled context, got nil")
	}
}

// TestCallMcpWithQueryOutputFailClosedOversized asserts the FINAL result
// fails closed when the jq filter produces more than toolkit.MaxOutputBytes
// (25 KiB) but less than jq's own 100 KiB cap. A ~30 KiB filtered result is
// always JSON-shaped (jq.Run json-encodes every value), so Truncate()-ing it
// at 25 KiB would hand the model an unparseable fragment — the exact hazard
// the primary path (tool.go's structuredTooLargeError) already fails closed
// on. This used to assert the opposite (truncate rather than error); it now
// guards the fix that closes that gap on the recovery path.
func TestCallMcpWithQueryOutputFailClosedOversized(t *testing.T) {
	// A filter that produces ~30 KiB of JSON (over the 25 KiB output cap, well
	// under jq's 100 KiB output cap): `[range(0;3000) | {id:.}]` yields a 3000
	// element array, ~30 KiB JSON-encoded.
	big := make([]byte, 0, 30000)
	big = append(big, []byte(`{"items":[`)...)
	for i := 0; i < 3000; i++ {
		if i > 0 {
			big = append(big, ',')
		}
		big = append(big, []byte(`{"id":`)...)
		big = strconv.AppendInt(big, int64(i), 10)
		big = append(big, '}')
	}
	big = append(big, []byte(`]}`)...)
	tl := scriptedCallTool(CallResult{
		Server:            "fake",
		Tool:              "struct",
		StructuredContent: big,
	})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"struct","jq_filter":".items"}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError (a >25KiB filtered JSON result must fail closed, not truncate): %q", res.Content)
	}
	if !strings.Contains(res.Content, "narrow") {
		t.Errorf("fail-closed message should guide the model to narrow the jq filter: %q", res.Content)
	}
	if strings.Contains(res.Content, toolkit.TruncationMarker) {
		t.Errorf("fail-closed result must not carry truncated JSON: %q", res.Content)
	}
}

// TestCallMcpWithQueryFiltersArrayStructuredContent asserts jq narrows a
// StructuredContent payload that is a JSON ARRAY (not an object): `length` → 2,
// `.[].id` → [1,2], `.[0].name` → "a". Mirrors TestCallMcpWithQueryFiltersSubset
// over an array-typed structured content.
func TestCallMcpWithQueryFiltersArrayStructuredContent(t *testing.T) {
	structured := json.RawMessage(`[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)
	tl := scriptedCallTool(CallResult{Server: "fake", Tool: "structarray", StructuredContent: structured})

	for _, tc := range []struct {
		filter string
		want   string
	}{
		{filter: `length`, want: `2`},
		{filter: `.[].id`, want: `[1,2]`},
		{filter: `.[0].name`, want: `"a"`},
	} {
		call := session.NewToolCall("c", callMcpWithQueryToolName, json.RawMessage(`{"server":"fake","tool":"structarray","jq_filter":`+quoteJSON(tc.filter)+`}`))
		res, err := tl.Execute(context.Background(), call, nil)
		if err != nil {
			t.Fatalf("filter %q: unexpected Go error: %v", tc.filter, err)
		}
		if res.IsError {
			t.Fatalf("filter %q: unexpected IsError: %q", tc.filter, res.Content)
		}
		if res.Content != tc.want {
			t.Fatalf("filter %q: Content = %q, want %q", tc.filter, res.Content, tc.want)
		}
	}
}

// TestCallMcpWithQueryFiltersPrimitiveStructuredContent asserts jq handles a
// primitive (number) StructuredContent: `.` → 42.
func TestCallMcpWithQueryFiltersPrimitiveStructuredContent(t *testing.T) {
	structured := json.RawMessage(`42`)
	tl := scriptedCallTool(CallResult{Server: "fake", Tool: "structprim", StructuredContent: structured})
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"structprim","jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content)
	}
	if res.Content != `42` {
		t.Fatalf("Content = %q, want 42", res.Content)
	}
}

// recordingCallProvider captures the args json.RawMessage handed to CallTool so
// a test can assert what the remote tool actually received after normalization.
type recordingCallProvider struct {
	fakeCallProvider
	gotArgs json.RawMessage
}

func (r *recordingCallProvider) CallTool(_ context.Context, _, _ string, args json.RawMessage) (CallResult, error) {
	r.gotArgs = args
	return r.result, r.err
}

// TestCallMcpWithQueryArgsAsObject asserts a proper JSON object in "args" is
// forwarded to the remote tool verbatim.
func TestCallMcpWithQueryArgsAsObject(t *testing.T) {
	prov := &recordingCallProvider{fakeCallProvider: fakeCallProvider{result: CallResult{
		Server: "fake", Tool: "t", StructuredContent: json.RawMessage(`{"ok":1}`),
	}}}
	tl := callMcpWithQueryTool{provider: prov}
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"t","args":{"owner":"stacklok","repo":"toolhive"},"jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content)
	}
	var got, want map[string]any
	if e := json.Unmarshal(prov.gotArgs, &got); e != nil {
		t.Fatalf("forwarded args not JSON: %v (%s)", e, prov.gotArgs)
	}
	_ = json.Unmarshal([]byte(`{"owner":"stacklok","repo":"toolhive"}`), &want)
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("forwarded args = %v, want %v", got, want)
	}
}

// TestCallMcpWithQueryArgsStringEncodedObjectRecovered asserts the deser
// tolerance recovers an "args" value that arrived as a JSON STRING which itself
// encodes a JSON object (the double-encoding failure mode) — the remote tool
// receives the parsed object, not the string.
func TestCallMcpWithQueryArgsStringEncodedObjectRecovered(t *testing.T) {
	prov := &recordingCallProvider{fakeCallProvider: fakeCallProvider{result: CallResult{
		Server: "fake", Tool: "t", StructuredContent: json.RawMessage(`{"ok":1}`),
	}}}
	tl := callMcpWithQueryTool{provider: prov}
	// "args" is the JSON string  "{\"owner\":\"stacklok\"}"  (an object-as-string).
	call := session.NewToolCall("c", callMcpWithQueryToolName,
		json.RawMessage(`{"server":"fake","tool":"t","args":"{\"owner\":\"stacklok\"}","jq_filter":"."}`))
	res, err := tl.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError (string-encoded object should be recovered): %q", res.Content)
	}
	var got map[string]any
	if e := json.Unmarshal(prov.gotArgs, &got); e != nil {
		t.Fatalf("forwarded args not a JSON object: %v (%s)", e, prov.gotArgs)
	}
	if got["owner"] != "stacklok" {
		t.Fatalf("forwarded args = %v, want owner=stacklok", got)
	}
}

// TestCallMcpWithQueryArgsUnrecoverableString asserts a garbled/non-object
// string "args" (the GLM <arg_key> / whitespace mangling) yields a clear,
// self-correcting model-facing error naming the expected object shape — NOT the
// opaque remote "cannot unmarshal string into map" and NOT a Go error.
func TestCallMcpWithQueryArgsUnrecoverableString(t *testing.T) {
	tl := scriptedCallTool(CallResult{Server: "fake", Tool: "t"})
	for name, argsJSON := range map[string]string{
		"whitespace": `{"server":"fake","tool":"t","args":"\t\t\t\t","jq_filter":"."}`,
		"glm_xml":    `{"server":"fake","tool":"t","args":"\t\t\t<arg_key>owner</arg_key>","jq_filter":"."}`,
		"array":      `{"server":"fake","tool":"t","args":[1,2,3],"jq_filter":"."}`,
	} {
		t.Run(name, func(t *testing.T) {
			call := session.NewToolCall("c", callMcpWithQueryToolName, json.RawMessage(argsJSON))
			res, err := tl.Execute(context.Background(), call, nil)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected an IsError correction, got: %q", res.Content)
			}
			if !strings.Contains(res.Content, "JSON") || !strings.Contains(res.Content, "args") {
				t.Fatalf("correction should name the expected JSON args shape: %q", res.Content)
			}
		})
	}
}

// TestCallMcpWithQueryArgsAbsentOrEmpty asserts an absent, null, or empty-object
// "args" forwards no/empty arguments without error (tools that take no args).
func TestCallMcpWithQueryArgsAbsentOrEmpty(t *testing.T) {
	for name, argsJSON := range map[string]string{
		"absent":       `{"server":"fake","tool":"t","jq_filter":"."}`,
		"null":         `{"server":"fake","tool":"t","args":null,"jq_filter":"."}`,
		"empty_object": `{"server":"fake","tool":"t","args":{},"jq_filter":"."}`,
	} {
		t.Run(name, func(t *testing.T) {
			prov := &recordingCallProvider{fakeCallProvider: fakeCallProvider{result: CallResult{
				Server: "fake", Tool: "t", StructuredContent: json.RawMessage(`{"ok":1}`),
			}}}
			tl := callMcpWithQueryTool{provider: prov}
			call := session.NewToolCall("c", callMcpWithQueryToolName, json.RawMessage(argsJSON))
			res, err := tl.Execute(context.Background(), call, nil)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if res.IsError {
				t.Fatalf("unexpected IsError: %q", res.Content)
			}
		})
	}
}

// quoteJSON returns the JSON string literal for s.
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestRegisterCallWithQueryGating pins the registration seam: the meta-tool
// registers ONLY when the manager exposes ≥1 tool (the escape hatch is
// meaningless otherwise), and a nil manager is a no-op. Mirrors
// TestRegisterResourceToolsGating's "register only when non-empty" discipline,
// but over TOOLS rather than resources.
func TestRegisterCallWithQueryGating(t *testing.T) {
	// Nil manager → no registration, no error.
	cat := tool.NewCatalog()
	reg, err := RegisterCallWithQuery(cat, nil)
	if err != nil {
		t.Fatalf("RegisterCallWithQuery(nil): %v", err)
	}
	if reg {
		t.Errorf("expected no registration for a nil manager")
	}
	if _, ok := cat.Lookup(callMcpWithQueryToolName); ok {
		t.Errorf("CallMcpWithQuery registered despite a nil manager")
	}

	// Empty manager (no servers) → no registration.
	empty := &Manager{}
	reg2, err := RegisterCallWithQuery(cat, empty)
	if err != nil {
		t.Fatalf("RegisterCallWithQuery(empty): %v", err)
	}
	if reg2 {
		t.Errorf("expected no registration for a manager with no tools")
	}

	// Manager with tools → registered.
	url := newTestServer(t, nil)
	s := connectTest(t, ServerConfig{Name: "fake", URL: url})
	mgr := &Manager{servers: []*Server{s}}
	cat2 := tool.NewCatalog()
	reg3, err := RegisterCallWithQuery(cat2, mgr)
	if err != nil {
		t.Fatalf("RegisterCallWithQuery(mgr): %v", err)
	}
	if !reg3 {
		t.Fatalf("expected registration for a manager with tools")
	}
	tl, ok := cat2.Lookup(callMcpWithQueryToolName)
	if !ok {
		t.Fatalf("CallMcpWithQuery not registered")
	}
	if !tl.ReadOnly() {
		t.Errorf("CallMcpWithQuery must be ReadOnly (read-parallel dispatch + plan-mode survival)")
	}
}
