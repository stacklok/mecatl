package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// benchToolSpecs is a small, fixed set of tool specs with realistic JSON schemas
// — the steady-state main loop carries a fixed catalogue; the conversation is what
// grows per turn, so the tool count is held constant across sizes.
func benchToolSpecs() []tool.ToolSpec {
	return []tool.ToolSpec{
		{Name: "Read", Description: "Read a file from the workspace.", Schema: json.RawMessage(
			`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`)},
		{Name: "Edit", Description: "Edit a file.", Schema: json.RawMessage(
			`{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old","new"]}`)},
		{Name: "Bash", Description: "Run a shell command.", Schema: json.RawMessage(
			`{"type":"object","properties":{"command":{"type":"string"},"timeout_ms":{"type":"integer"}},"required":["command"]}`)},
	}
}

// These benchmarks close the measurement gap called out in issue #157: every
// offline engine benchmark drives the loop through engine/adapter/mockllm, which
// short-circuits the provider request-assembly path entirely. The per-turn,
// grows-with-history re-marshal that live monitoring fingered as the dominant
// main-turn allocator (store:false replays the WHOLE accumulated conversation each
// turn) therefore had ZERO offline coverage and could never be ranked against the
// hypotheses. These benches drive buildParams/buildMessages (our glue) and the SDK
// re-marshal over synthetic conversations of 50/200/500 messages, plus the decode
// translate() glue over a recorded SSE fixture. They are fully offline (no client,
// no network) and follow engine/agent/bench_test.go conventions: b.Loop(), fixtures
// built outside the loop, results parked in a package-level sink.

// Package-level sinks defeat dead-code elimination of the benchmarked results.
var (
	sinkParams sdkParamsSink
	sinkBytes  []byte
	sinkChunks []port.Chunk
)

// sdkParamsSink is an alias so the assignment site reads clearly; the SDK params
// type is large, so we park the whole value.
type sdkParamsSink = any

// benchConversation builds a synthetic conversation of n messages modelling a
// realistic long-session replay: a leading genuine user prompt, then repeating
// (assistant-with-reasoning-and-tool-call, tool-result, assistant-text, user)
// cycles. It is deterministic and allocation-stable across calls (built once,
// outside the measured loop).
func benchConversation(n int) []session.Message {
	msgs := make([]session.Message, 0, n)
	msgs = append(msgs, session.NewUserMessage(
		"Please analyse the repository and summarise the architecture across the engine and adapter layers."))

	// A representative Anthropic reasoning replay blob (one thinking block with a
	// signature), packed the way the real turn does.
	reasoning := packReasoning([]reasoningBlock{{
		Kind:      reasoningKindThinking,
		Thinking:  "I should inspect the layering rules and the port interfaces before answering, then cross-check the adapters.",
		Signature: "c2lnbmF0dXJlLWJsb2ItZm9yLWJlbmNobWFyay1vbmx5",
	}})

	i := 0
	for len(msgs) < n {
		callID := session.ToolCallID(fmt.Sprintf("call_%d", i))
		switch i % 4 {
		case 0:
			// Assistant turn: reasoning + a tool call (the thinking-before-tool_use shape).
			msgs = append(msgs, session.Message{
				Role:      session.RoleAssistant,
				Reasoning: reasoning,
				ToolCalls: []session.ToolCall{{
					ID:   callID,
					Name: "Read",
					Args: json.RawMessage(fmt.Sprintf(`{"path":"engine/agent/loop_%d.go","offset":0,"limit":200}`, i)),
				}},
			})
		case 1:
			// Tool result (rides a user-role tool_result block).
			res := session.NewToolResult(session.ToolCallID(fmt.Sprintf("call_%d", i-1)),
				"package agent\n\n// a representative chunk of file content returned by the Read tool;\n"+
					"// real results are token-shaped and can run to several KB on a large file.\n"+
					"func run() error { return nil }\n")
			msgs = append(msgs, session.Message{Role: session.RoleTool, ToolResult: &res})
		case 2:
			// Assistant visible text turn.
			msgs = append(msgs, session.Message{
				Role: session.RoleAssistant,
				Text: "I read the file; it defines the engine loop entry point. Continuing the survey.",
			})
		default:
			// A follow-up user instruction.
			msgs = append(msgs, session.NewUserMessage(
				"Good. Now also check the governance evaluator and how permissions resolve."))
		}
		i++
	}
	return msgs
}

// benchRequest assembles a synthetic LLMRequest with an n-message conversation, a
// two-layer system prompt, and a small set of tool specs (the steady-state main
// loop carries a fixed tool catalogue; the conversation is what grows per turn).
func benchRequest(n int) port.LLMRequest {
	return port.LLMRequest{
		Model: "claude-sonnet-4-6",
		System: prompt.Layered{
			StablePrefix:   "You are a careful coding agent. Follow the repository's layering rules.",
			VolatileSuffix: "Current working directory: /repo. Today is a benchmark day.",
		},
		Tools:    benchToolSpecs(),
		Messages: benchConversation(n),
	}
}

var benchSizes = []int{50, 200, 500}

// BenchmarkBuildParams measures OUR glue only: buildParams -> buildMessages +
// buildTools + buildSystem, constructing the SDK param structs WITHOUT marshalling
// them. This isolates the slice/struct-building cost the adapter owns.
func BenchmarkBuildParams(b *testing.B) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000))
	for _, n := range benchSizes {
		req := benchRequest(n)
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var params sdkParamsSink
			for b.Loop() {
				pr, err := p.buildParams(req)
				if err != nil {
					b.Fatalf("buildParams: %v", err)
				}
				params = pr
			}
			sinkParams = params
		})
	}
}

// BenchmarkBuildMessages isolates buildMessages alone (the per-turn conversation
// translation that grows with history) from tools/system assembly.
func BenchmarkBuildMessages(b *testing.B) {
	for _, n := range benchSizes {
		msgs := benchConversation(n)
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var params sdkParamsSink
			for b.Loop() {
				out, err := buildMessages(msgs, port.ProviderCapabilities{Image: true, EmbeddedContext: true})
				if err != nil {
					b.Fatalf("buildMessages: %v", err)
				}
				params = out
			}
			sinkParams = params
		})
	}
}

// BenchmarkBuildParamsAndMarshal measures the FULL per-turn re-marshal the issue
// fingered: build the SDK params, then json.Marshal them (the SDK's MarshalJSON is
// what the real Stream call drives inside the client). This is the path that grows
// with conversation length under store:false. The split between this and
// BenchmarkBuildParams attributes the cost to OUR glue vs the SDK's marshal.
func BenchmarkBuildParamsAndMarshal(b *testing.B) {
	p := New(WithAPIKey("sk-test"), WithMaxTokens(16000))
	for _, n := range benchSizes {
		req := benchRequest(n)
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var raw []byte
			for b.Loop() {
				params, err := p.buildParams(req)
				if err != nil {
					b.Fatalf("buildParams: %v", err)
				}
				data, merr := json.Marshal(params)
				if merr != nil {
					b.Fatalf("marshal: %v", merr)
				}
				raw = data
			}
			sinkBytes = raw
		})
	}
}

// BenchmarkDecodeSSE measures the decode glue we own — translate() driven over a
// recorded SSE fixture by the test-only decodeSSE scanner. The live SDK decode is
// not benchmarkable offline; this is the only decode code path we author.
func BenchmarkDecodeSSE(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "thinking_tool_turn.sse"))
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	b.ReportAllocs()
	var chunks []port.Chunk
	for b.Loop() {
		c, derr := decodeSSE(bytes.NewReader(data))
		if derr != nil {
			b.Fatalf("decodeSSE: %v", derr)
		}
		chunks = c
	}
	sinkChunks = chunks
}
