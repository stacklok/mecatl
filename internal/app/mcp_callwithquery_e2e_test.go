package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// bigJSONItemsCount is the number of elements in the bigjson tool's items array
// — enough that the serialized StructuredContent comfortably exceeds
// toolkit.MaxOutputBytes (25_000) so the fail-closed path fires.
const bigJSONItemsCount = 1100

// newMCPTestServerBigJSON stands up an in-process MCP server exposing a single
// "bigjson" tool that returns a StructuredContent payload OVER toolkit.MaxOutputBytes:
// an object with one "items" array of bigJSONItemsCount small objects. This is the
// fail-closed trigger (a structured/JSON result over the cap must NOT be truncated —
// it surfaces an actionable error pointing at the two escape hatches). It also
// serializes the same JSON as a TextContent mirror, matching the spec's SHOULD.
func newMCPTestServerBigJSON(t *testing.T) string {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "v1"}, nil)

	// Build the over-cap JSON once: {"items":[{"id":N,"name":"item-N","flag":true},...]}.
	bigJSON := func() string {
		var b strings.Builder
		b.WriteString(`{"items":[`)
		for i := 0; i < bigJSONItemsCount; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"name":"item-%d","flag":true}`, i, i)
		}
		b.WriteString(`]}`)
		return b.String()
	}()
	payload := []byte(bigJSON)
	if len(payload) <= toolkit.MaxOutputBytes {
		t.Fatalf("test fixture too small: %d bytes, want > %d", len(payload), toolkit.MaxOutputBytes)
	}

	srv.AddTool(&mcpsdk.Tool{
		Name:         "bigjson",
		Description:  "returns an over-cap structured JSON payload",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var structured any
		if err := json.Unmarshal(payload, &structured); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{
			StructuredContent: structured,
			Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: string(payload)}},
		}, nil
	})

	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

// TestCallMcpWithQueryE2EFailClosedThenRecovery is the end-to-end acceptance test
// (T7) for the fail-closed + CallMcpWithQuery recovery work (issue #223). The unit
// tests in internal/adapter/mcp cover the adapter paths in isolation; this drives
// BOTH through the REAL Engine.Run over a REAL in-process MCP server, proving the two
// paths compose in the real loop:
//
//  1. FAIL-CLOSED: the model calls mcp__fake__bigjson (returns >25KiB JSON
//     StructuredContent). The loop's EvToolResult must be an IsError result whose
//     content is the fail-closed message naming "exceeded the", "CallMcpWithQuery",
//     and "narrow/paginate" — NOT a truncated JSON fragment.
//  2. RECOVERY: the model then calls CallMcpWithQuery with server "fake", tool
//     "bigjson", and a jq_filter ".items | length". The loop's EvToolResult must
//     NOT be an error, and its content must be the small filtered subset
//     (bigJSONItemsCount as a bare number — under the cap, parseable).
//
// It uses sessionEngineFactory over a shared globalMgr (the same wiring
// session_engine_globalmcp_test.go exercises) so the catalog assembled by
// assembleCatalog registers BOTH mcp__fake__bigjson AND CallMcpWithQuery.
func TestCallMcpWithQueryE2EFailClosedThenRecovery(t *testing.T) {
	url := newMCPTestServerBigJSON(t)
	globalMgr := connectMainManager(t, "fake", url)

	// Sanity: the shared manager exposes the namespaced bigjson tool (so the direct
	// call path is wired), and the gate (≥1 tool) holds.
	if _, ok := toolNameSet(globalMgr.Tools())["mcp__fake__bigjson"]; !ok {
		t.Fatalf("global manager should expose mcp__fake__bigjson, got %v", toolNames(globalMgr.Tools()))
	}

	// The model's two-turn script: (1) call the over-cap tool directly → fail-closed
	// error; (2) recover via CallMcpWithQuery → small filtered subset; (3) end.
	bigJSONCall := session.NewToolCall("c1", "mcp__fake__bigjson", json.RawMessage(`{}`))
	queryArgs := `{"server":"fake","tool":"bigjson","jq_filter":".items | length"}`
	queryCall := session.NewToolCall("c2", "CallMcpWithQuery", json.RawMessage(queryArgs))
	prov := mockllm.New(
		mockllm.ToolCallTurn(bigJSONCall),
		mockllm.ToolCallTurn(queryCall),
		mockllm.TextTurn("done"),
	)

	// Single-provider registry + factory over the shared globalMgr, exactly the
	// session_engine_globalmcp_test.go wiring (catalogAssets.globalMgr is the seam
	// mountGlobalMCP reads to register both the namespaced tools and CallMcpWithQuery).
	reg := regForTest(prov, providerOpenAI, "test-model")
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(Config{Model: "test-model"}, reg, prov, store, policy, hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{globalMgr: globalMgr}, nil)

	res, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer func() { _ = res.Close() }()

	// Both tools must be in the assembled catalog (the composition seam).
	if !res.Engine.HasTool("mcp__fake__bigjson") {
		t.Fatal("engine catalog missing mcp__fake__bigjson (mountGlobalMCP did not register the namespaced remote tool)")
	}
	if !res.Engine.HasTool("CallMcpWithQuery") {
		t.Fatal("engine catalog missing CallMcpWithQuery (mountGlobalMCP did not register the recovery meta-tool)")
	}

	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{MaxTurns: 10}, time.Now())
	run := res.Engine.RunContent(context.Background(), sess, memfs.NewWorkspace("/ws"), "go", nil)

	// Collect the two tool results by CallID; approve any permission ask (the direct
	// mcp__fake__bigjson call is not a floor-Allow tool, so it asks; CallMcpWithQuery
	// is floor-Allow and does not).
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

	// (1) FAIL-CLOSED: the direct bigjson call surfaced the actionable error, not a
	// truncated JSON blob.
	big, ok := results["c1"]
	if !ok {
		t.Fatalf("no EvToolResult for the direct bigjson call c1; saw %v", keysOfResults(results))
	}
	if !big.IsError {
		t.Fatalf("direct bigjson call must fail-closed as an IsError result; got non-error: %q", big.Content)
	}
	for _, want := range []string{"exceeded the", "CallMcpWithQuery", "narrow/paginate"} {
		if !strings.Contains(big.Content, want) {
			t.Errorf("fail-closed error missing %q:\n%s", want, big.Content)
		}
	}
	// The truncated JSON must NOT be present — the whole point of fail-closed.
	if strings.Contains(big.Content, toolkit.TruncationMarker) {
		t.Errorf("fail-closed error carried a truncation marker (should not have truncated JSON): ...%q", tail40(big.Content))
	}

	// (2) RECOVERY: CallMcpWithQuery returned the small filtered subset, not an error.
	q, ok := results["c2"]
	if !ok {
		t.Fatalf("no EvToolResult for the CallMcpWithQuery call c2; saw %v", keysOfResults(results))
	}
	if q.IsError {
		t.Fatalf("CallMcpWithQuery result must not be an error; got: %q", q.Content)
	}
	want := itoa(bigJSONItemsCount)
	if q.Content != want {
		t.Fatalf("CallMcpWithQuery Content = %q, want %q (the .items | length of the over-cap payload)", q.Content, want)
	}
}

// toolNames returns the Spec().Name list of the given tools (a stable diagnostic).
func toolNames(tools []tool.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tl := range tools {
		out = append(out, tl.Spec().Name)
	}
	return out
}

// keysOfResults returns the CallID keys of the collected tool results.
func keysOfResults(m map[string]session.ToolResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// tail40 returns the last 40 bytes of s (a short diagnostic suffix).
func tail40(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[len(s)-40:]
}

// itoa is a stdlib-free strconv.Itoa stand-in to keep imports lean.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
