// Package anthropic implements port.LLMProvider over the native Anthropic
// Messages API (POST /v1/messages) using github.com/anthropics/anthropic-sdk-go.
//
// Like the openai adapter, the harness owns its own conversation state: every
// request is STATELESS (no server-side conversation id) and resends the full
// messages array each turn. The two-layer system prompt (prompt.Layered) is
// rendered into the dedicated `system` param with a single ephemeral
// cache_control breakpoint at the StablePrefix/VolatileSuffix boundary, so the
// byte-stable prompt prefix is cached across turns.
//
// Extended thinking is ON and MODEL-AWARE: Claude Opus 4.8/4.7/4.6 and Sonnet
// 4.6 require thinking:{type:"adaptive"} (a manual {type:"enabled",budget_tokens}
// 400s on Opus 4.8/4.7); older families (Sonnet 4.5, Opus 4.5, Haiku 4.5 and
// earlier) take thinking:{type:"enabled",budget_tokens:N}. The adapter selects
// the config by model class (see thinkingConfigFor) so a wrong config never
// reaches the wire. The budget and the required max_tokens are adapter-
// CONSTRUCTION Options (WithThinkingBudget / WithMaxTokens), never LLMRequest
// fields — the DTO stays provider-neutral.
//
// The reasoning-replay token is Anthropic's thinking-block signature (opaque,
// encrypted full thinking). Because interleaved thinking can produce SEVERAL
// thinking blocks per assistant turn — plus redacted_thinking blocks that must
// also round-trip — the adapter packs the ordered LIST of {thinking,signature}
// and {redacted_thinking data} blocks into the existing opaque
// session.Message.Reasoning STRING as a versioned JSON envelope (reasoning.go),
// and unpacks it to reconstruct the thinking/redacted_thinking content blocks
// BEFORE the tool_use blocks on the next turn. Omitting or misordering them on a
// tool-bearing assistant turn 400s — this round-trip is the load-bearing
// correctness item. session.Message.Reasoning stays a bare string: the
// abstraction holds.
//
// The streaming SSE events are translated into provider-neutral port.Chunk
// values by the pure translate function, exercised directly from recorded
// fixtures in tests; no network is required to test the translation.
package anthropic
