package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// positivePNG is a valid 1x1 transparent PNG (raw bytes; the SDK base64-encodes
// it on the wire). Used to prove an image block round-trips as typed bytes, NOT
// a base64 dump in the model-facing string.
var positivePNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
	0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// The embedded-text body and the structured-output payload are fixed so the
// assertions can match them verbatim.
const (
	positiveEmbeddedText = "hello from an embedded resource"
	positiveStructured   = `{"ok":true,"count":3,"items":["a","b","c"]}`
)

// newTypedResultsMCPServer stands up the POSITIVE counterpart to
// newHostileMCPServer: benign tools that exercise the capability PR #226 shipped
// — every non-text MCP content kind now flows through as a typed session.Content
// block on ToolResult.Parts (replacing the old "[non-text content]" placeholder)
// with a bounded model-facing summary string. echo_args additionally proves a
// nested STRUCTURED INPUT argument round-trips to the remote tool intact.
func newTypedResultsMCPServer(t *testing.T) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "typed", Version: "v1"}, nil)
	obj := map[string]any{"type": "object"}

	// resource_link: a REFERENCE (URI + rich metadata), never dereferenced.
	srv.AddTool(&mcpsdk.Tool{Name: "good_resource_link", Description: "benign resource link", InputSchema: obj},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.ResourceLink{
				URI: "https://example.com/doc.md", Name: "doc.md",
				Title: "Design doc", Description: "the design", MIMEType: "text/markdown",
			}}}, nil
		})

	// image/png: valid inline image the provider can render natively.
	srv.AddTool(&mcpsdk.Tool{Name: "good_image", Description: "valid 1x1 PNG", InputSchema: obj},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.ImageContent{
				MIMEType: "image/png", Data: positivePNG}}}, nil
		})

	// embedded text resource: inline text carried through verbatim.
	srv.AddTool(&mcpsdk.Tool{Name: "embedded_text", Description: "embedded text resource", InputSchema: obj},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.EmbeddedResource{
				Resource: &mcpsdk.ResourceContents{URI: "file:///notes.txt", MIMEType: "text/plain", Text: positiveEmbeddedText}}}}, nil
		})

	// structured (under-cap): the positive twin of the fail-closed path — a small
	// StructuredContent result is NOT fail-closed; it rides through as a typed
	// BlockStructuredContent AND the SHOULD text mirror.
	srv.AddTool(&mcpsdk.Tool{Name: "structured_small", Description: "small structured JSON", InputSchema: obj, OutputSchema: obj},
		func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var structured any
			if err := json.Unmarshal([]byte(positiveStructured), &structured); err != nil {
				return nil, err
			}
			return &mcpsdk.CallToolResult{
				StructuredContent: structured,
				Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: positiveStructured}},
			}, nil
		})

	// echo_args: STRUCTURED INPUT round-trip. The handler unmarshals the raw
	// arguments the model sent and echoes them straight back as StructuredContent,
	// so the assertion proves a nested object/array argument reached the remote
	// tool intact (argsFor → SDK marshal → server unmarshal).
	srv.AddTool(&mcpsdk.Tool{Name: "echo_args", Description: "echoes its structured arguments back", InputSchema: obj, OutputSchema: obj},
		func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var in any
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
					return nil, err
				}
			}
			return &mcpsdk.CallToolResult{StructuredContent: in}, nil
		})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// echoArgs is the nested structured argument echo_args round-trips.
const echoArgs = `{"nested":{"k":"v"},"list":[1,2,3],"n":42}`

// TestMCPTypedResultsPositiveE2E drives the benign typed-results tools through the
// REAL Engine.Run and asserts the capability PR #226 shipped actually WORKS: each
// non-text content kind arrives as a typed block on ToolResult.Parts with a
// bounded (never base64-dumped) model-facing summary, an under-cap structured
// result is delivered (not fail-closed), and a nested structured input argument
// round-trips. These are the positive mirror of TestMCPHostileFindingsE2E — a
// regression that silently dropped Parts or mis-mapped a block turns them red.
func TestMCPTypedResultsPositiveE2E(t *testing.T) {
	url := newTypedResultsMCPServer(t)
	globalMgr := connectMainManager(t, "typed", url)

	call := func(id, name, args string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
	}
	prov := mockllm.New(
		mockllm.ToolCallTurn(call("p1", "mcp__typed__good_resource_link", `{}`)),
		mockllm.ToolCallTurn(call("p2", "mcp__typed__good_image", `{}`)),
		mockllm.ToolCallTurn(call("p3", "mcp__typed__embedded_text", `{}`)),
		mockllm.ToolCallTurn(call("p4", "mcp__typed__structured_small", `{}`)),
		mockllm.ToolCallTurn(call("p5", "mcp__typed__echo_args", echoArgs)),
		mockllm.TextTurn("done"),
	)

	reg := regForTest(prov, providerOpenAI, "test-model")
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(Config{Model: "test-model"}, reg, prov, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{globalMgr: globalMgr}, nil)
	res, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 20}, time.Now())
	run := res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "go", Parts: nil})

	results := make(map[string]session.ToolResult)
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			continue
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results[string(ev.ToolResult.CallID)] = *ev.ToolResult
		}
	}
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5"} {
		if _, ok := results[id]; !ok {
			t.Fatalf("missing EvToolResult for %s; got %v", id, keysOfResults(results))
		}
	}

	// findBlock returns the first Parts block of the given kind, or fails.
	findBlock := func(t *testing.T, r session.ToolResult, kind session.BlockKind) session.Content {
		t.Helper()
		for _, p := range r.Parts {
			if p.BlockKind == kind {
				return p
			}
		}
		t.Fatalf("no %q block in Parts; Parts=%+v", kind, r.Parts)
		return session.Content{}
	}

	// resource_link: full metadata rides in Parts; the model string is a bounded
	// URI+Name handle (Title/Description are NOT in .Content — the F-INJ axis, here
	// benign) and nothing is base64-dumped.
	t.Run("resource_link_typed_block", func(t *testing.T) {
		r := results["p1"]
		if r.IsError {
			t.Fatalf("unexpected error result: %q", r.Content)
		}
		if want := "[resource link: https://example.com/doc.md (doc.md)]"; !strings.Contains(r.Content, want) {
			t.Fatalf("model string missing the summary %q: %q", want, r.Content)
		}
		b := findBlock(t, r, session.BlockResourceLink)
		if b.URL != "https://example.com/doc.md" || b.Name != "doc.md" || b.Title != "Design doc" || b.Description != "the design" || b.MIMEType != "text/markdown" {
			t.Fatalf("resource_link block fields wrong: %+v", b)
		}
	})

	// image: typed BlockImage carries the raw bytes; the model string is the
	// bounded "[image content: image/png]" summary — the PNG is NEVER base64-dumped.
	t.Run("image_typed_block_no_base64_dump", func(t *testing.T) {
		r := results["p2"]
		if r.IsError {
			t.Fatalf("unexpected error result: %q", r.Content)
		}
		if want := "[image content: image/png]"; !strings.Contains(r.Content, want) {
			t.Fatalf("model string missing the image summary %q: %q", want, r.Content)
		}
		if strings.Contains(r.Content, "iVBOR") || strings.Contains(r.Content, "IHDR") {
			t.Fatalf("model string appears to carry a base64/raw PNG dump: %q", r.Content)
		}
		b := findBlock(t, r, session.BlockImage)
		if b.MIMEType != "image/png" || len(b.Data) != len(positivePNG) {
			t.Fatalf("image block wrong: mime=%q dataLen=%d want image/png len=%d", b.MIMEType, len(b.Data), len(positivePNG))
		}
	})

	// embedded text: verbatim in both the model string and a typed block.
	t.Run("embedded_text_typed_block", func(t *testing.T) {
		r := results["p3"]
		if r.IsError {
			t.Fatalf("unexpected error result: %q", r.Content)
		}
		if !strings.Contains(r.Content, positiveEmbeddedText) {
			t.Fatalf("model string missing the embedded text %q: %q", positiveEmbeddedText, r.Content)
		}
		b := findBlock(t, r, session.BlockEmbeddedResource)
		if b.Text != positiveEmbeddedText || b.URL != "file:///notes.txt" || b.MIMEType != "text/plain" {
			t.Fatalf("embedded_resource block fields wrong: %+v", b)
		}
	})

	// structured (under-cap): NOT fail-closed — delivered as a typed block whose
	// Text is the valid JSON payload, plus the SHOULD text mirror in .Content.
	t.Run("structured_undercap_delivered", func(t *testing.T) {
		r := results["p4"]
		if r.IsError {
			t.Fatalf("under-cap structured result must NOT fail-closed; got error: %q", r.Content)
		}
		if !strings.Contains(r.Content, positiveStructured) {
			t.Fatalf("model string missing the structured JSON mirror: %q", r.Content)
		}
		b := findBlock(t, r, session.BlockStructuredContent)
		if !json.Valid([]byte(b.Text)) {
			t.Fatalf("structured block is not valid JSON: %q", b.Text)
		}
		var got, want any
		_ = json.Unmarshal([]byte(b.Text), &got)
		_ = json.Unmarshal([]byte(positiveStructured), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("structured block JSON = %v, want %v", got, want)
		}
	})

	// STRUCTURED INPUT round-trip: the nested object/array argument the model sent
	// reached the remote tool intact and came back — proving argsFor passes
	// structured arguments (not just flat strings) faithfully.
	t.Run("structured_input_roundtrips", func(t *testing.T) {
		r := results["p5"]
		if r.IsError {
			t.Fatalf("echo_args must not error: %q", r.Content)
		}
		b := findBlock(t, r, session.BlockStructuredContent)
		var got, want any
		if err := json.Unmarshal([]byte(b.Text), &got); err != nil {
			t.Fatalf("echoed structured content not JSON: %q", b.Text)
		}
		_ = json.Unmarshal([]byte(echoArgs), &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("echoed args = %v, want %v (structured input did not round-trip)", got, want)
		}
		t.Logf("CONFIRMED: nested structured input %s round-tripped through the remote tool", echoArgs)
	})
}
