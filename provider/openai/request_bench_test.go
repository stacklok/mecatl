package openai

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

// These benchmarks close the measurement gap called out in issue #157: every
// offline engine benchmark drives the loop through engine/adapter/mockllm, which
// short-circuits the provider request-assembly path entirely. The per-turn,
// grows-with-history re-marshal that live monitoring fingered as the dominant
// main-turn allocator (store:false replays the WHOLE accumulated conversation each
// turn) therefore had ZERO offline coverage and could never be ranked against the
// hypotheses. These benches drive buildParams -> buildInput/buildTools (our glue)
// and the SDK re-marshal over synthetic conversations of 50/200/500 messages, plus
// the decode translate() glue over a recorded SSE fixture. They are fully offline
// (no client, no network) and follow engine/agent/bench_test.go conventions:
// b.Loop(), fixtures built outside the loop, results parked in a package-level
// sink.

// Package-level sinks defeat dead-code elimination of the benchmarked results.
var (
	sinkParams paramsSink
	sinkBytes  []byte
	sinkChunks []port.Chunk
)

// paramsSink parks the (large) SDK param value so it is not optimised away.
type paramsSink = any

// benchToolSpecs is a small, fixed set of tool specs with realistic JSON schemas —
// the steady-state main loop carries a fixed catalogue; the conversation is what
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

// benchConversation builds a synthetic conversation of n messages modelling a
// realistic long-session replay: a leading genuine user prompt, then repeating
// (assistant-with-reasoning-and-tool-call, tool-result, assistant-text, user)
// cycles. It is deterministic and allocation-stable across calls (built once,
// outside the measured loop). The reasoning blob is the opaque encrypted_content
// the OpenAI Responses adapter replays verbatim.
func benchConversation(n int) []session.Message {
	msgs := make([]session.Message, 0, n)
	msgs = append(msgs, session.NewUserMessage(
		"Please analyse the repository and summarise the architecture across the engine and adapter layers."))

	const reasoning = "Z25jcnlwdGVkLXJlYXNvbmluZy1ibG9iLWZvci1iZW5jaG1hcmstb25seS1ub3QtcmVhbA=="

	i := 0
	for len(msgs) < n {
		callID := session.ToolCallID(fmt.Sprintf("call_%d", i))
		switch i % 4 {
		case 0:
			// Assistant turn: reasoning replay blob + a function call.
			msgs = append(msgs, session.Message{
				Role:      session.RoleAssistant,
				Reasoning: reasoning,
				ToolCalls: []session.ToolCall{{
					ID:     callID,
					ItemID: fmt.Sprintf("fc_%d", i),
					Name:   "Read",
					Args:   json.RawMessage(fmt.Sprintf(`{"path":"engine/agent/loop_%d.go","offset":0,"limit":200}`, i)),
				}},
			})
		case 1:
			res := session.NewToolResult(session.ToolCallID(fmt.Sprintf("call_%d", i-1)),
				"package agent\n\n// a representative chunk of file content returned by the Read tool;\n"+
					"// real results are token-shaped and can run to several KB on a large file.\n"+
					"func run() error { return nil }\n")
			msgs = append(msgs, session.Message{Role: session.RoleTool, ToolResult: &res})
		case 2:
			msgs = append(msgs, session.Message{
				Role:          session.RoleAssistant,
				Text:          "I read the file; it defines the engine loop entry point. Continuing the survey.",
				ProviderPhase: "commentary",
			})
		default:
			msgs = append(msgs, session.NewUserMessage(
				"Good. Now also check the governance evaluator and how permissions resolve."))
		}
		i++
	}
	return msgs
}

// benchRequest assembles a synthetic LLMRequest with an n-message conversation, a
// two-layer system prompt, and a fixed set of tool specs.
func benchRequest(n int) port.LLMRequest {
	return port.LLMRequest{
		Model: "gpt-5.1",
		System: prompt.Layered{
			StablePrefix:   "You are a careful coding agent. Follow the repository's layering rules.",
			VolatileSuffix: "Current working directory: /repo. Today is a benchmark day.",
		},
		Tools:    benchToolSpecs(),
		Messages: benchConversation(n),
	}
}

var benchSizes = []int{50, 200, 500}

// BenchmarkBuildParams measures OUR glue only: buildParams -> buildInput +
// buildTools, constructing the SDK param structs WITHOUT marshalling them. This
// isolates the slice/struct-building cost the adapter owns.
func BenchmarkBuildParams(b *testing.B) {
	for _, n := range benchSizes {
		req := benchRequest(n)
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var params paramsSink
			for b.Loop() {
				pr, err := buildParams(req)
				if err != nil {
					b.Fatalf("buildParams: %v", err)
				}
				params = pr
			}
			sinkParams = params
		})
	}
}

// BenchmarkBuildInput isolates buildInput alone (the per-turn conversation
// translation that grows with history) from tools/instructions assembly.
func BenchmarkBuildInput(b *testing.B) {
	for _, n := range benchSizes {
		msgs := benchConversation(n)
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var params paramsSink
			for b.Loop() {
				out, err := buildInput(msgs, port.ProviderCapabilities{Image: true, EmbeddedContext: true})
				if err != nil {
					b.Fatalf("buildInput: %v", err)
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
	for _, n := range benchSizes {
		req := benchRequest(n)
		b.Run(fmt.Sprintf("msgs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			var raw []byte
			for b.Loop() {
				params, err := buildParams(req)
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
	data, err := os.ReadFile(filepath.Join("testdata", "reasoning_turn.sse"))
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
