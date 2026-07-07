# ADR 0064 — Interleaved-reasoning discrimination (the marker, not the flag)

- Status: Accepted
- Date: 2026-07-07
- Scope: the openai stream translator (`internal/adapter/openai/stream.go`), the catalog (`internal/adapter/providercatalog`), and the composition seam (`internal/app`).

## Context

Most reasoning models emit their chain-of-thought on a DEDICATED event channel:
OpenAI's Responses API surfaces reasoning as `response.reasoning_summary_text.delta`
/ `response.reasoning_text.delta` / `response.output_item.done` (reasoning) events,
which the openai stream translator maps to `ChunkReasoning` / `ChunkReasoningItem`
(display-only + replay blob). The visible answer rides a SEPARATE
`response.output_text.delta` channel, mapped to `ChunkText`. The two never collide,
and the harness's single-visible-text-part guard (a turn carries exactly one
visible text identity — `item_id`/`output_index`/`content_index`) only ever sees
the one answer part.

`z-ai/glm-5.2` (via OpenRouter) does not follow this shape. It emits reasoning
INLINE as a SIBLING field on the very same `response.output_text.delta` event —
a `reasoning_content` string sitting beside the `delta` string. The openai-go
SDK's typed struct for the event has no such field, so it arrives only in the
event's raw JSON. Two failures follow from the old, marker-less translator:

1. **Reasoning leaks into the user-visible text.** Each reasoning delta's `delta`
   string (which carries the reasoning prose, identical to its
   `reasoning_content`) is translated to `ChunkText` and concatenated into the
   assistant message — the user sees the model's chain-of-thought as the answer.

2. **The single-visible-text-part guard aborts the turn BEFORE the tool call.**
   GLM emits its reasoning deltas with a differing `content_index` from the
   final answer (reasoning on index 0 then 1, the answer back on index 0), so
   the guard sees what looks like multiple visible text parts and returns a
   terminal multi-part error. The stream dies before the trailing
   `function_call` `output_item.done` is translated, so a reasoning-then-tool
   turn terminates tool-less — the model called a tool the harness never saw.

A flag heuristic was the obvious first reach: when the model is known to
interleave (a catalog boolean), route EVERY `response.output_text.delta` to
`ChunkReasoning`. This is REJECTED. GLM emits its real final answer on the SAME
`output_text.delta` channel with NO marker — the answer delta is, on the wire,
indistinguishable from a reasoning delta save for the ABSENCE of the
`reasoning_content` sibling. A blanket reclassification would swallow the real
answer into display-only `ChunkReasoning`, leaving the turn textless — the same
no-progress run death the guard-abort caused, just moved. The wire MUST carry
the discriminator; a model-level flag cannot.

## Decision

**A marker-based discriminator keyed on `interleaved.field`, threaded
catalog → composition → adapter.** The catalog mirrors models.dev's
`interleaved` object: `providercatalog.Model.InterleavedReasoningField()` returns
the name of the sibling field the model emits reasoning INLINE on (e.g.
`"reasoning_content"`), empty for the standard dedicated-reasoning path. The
composition helper `interleavedReasoningField(reg, providerID, modelID)` reads
it CATALOG-ONLY (the live-metadata store does not surface this field today;
models.dev's `interleaved` is a static catalog property, not a live-listed one,
so a live entry is NOT authoritative over the catalog here — unlike
modalities/reasoning-effort-support). The per-session engine factory re-mints
the openai adapter with `openai.WithInterleavedReasoningField(field)` when a
session resolves a model that carries it — the SAME factory discipline as
reasoning effort (ADR 0055) and the per-session capability intersection (T7):
the model's reasoning wire shape is catalog-derived, not an operator/per-request
decision, so it is an adapter-CONSTRUCTION Option, NOT a `port.LLMRequest` field
(the frozen-neutral-request invariant holds; no `port` interface is widened).

**The translator inspects each delta's raw JSON for the named field.** In the
`response.output_text.delta` case, when `streamState.interleavedField` is
non-empty, `interleavedReasoningContent` decodes the event's `RawJSON()` into a
`map[string]json.RawMessage` (using `encoding/json`, NOT gjson, so the adapter's
depguard allowlist stays clean), picks the named field, and unmarshals it as a
string. A present AND non-empty sibling field marks the delta as reasoning:
emit a display-only `ChunkReasoning` and RETURN EARLY — do NOT invoke
`translateTextDelta`, so the single-visible-text-part guard only ever sees one
visible-text identity (reasoning never pins it). A delta carrying NO sibling
field (the model's real final answer) falls through to the visible-text path
(`ChunkText`) unchanged. The marker is REQUIRED for reclassification: the field
name is the opt-in, so a non-GLM model that happens to emit a
`reasoning_content` sibling (none do today) is unaffected, and the default path
(field empty) is byte-identical to the pre-#240 behaviour — the guard is NOT
weakened.

**Fail-safe on unrecognised shapes.** When the named field is absent, empty, or
not a JSON string (e.g. `reasoning_details` is an array), the helper returns `""`
and the caller treats that as "not a reasoning delta" — it falls through to the
visible-text path. An unrecognised shape never swallows the real answer.

## Consequences

- **GLM-5.2's reasoning stays display-only and the real answer stays visible.**
  The reasoning deltas reclassify to `ChunkReasoning`; the final-answer delta
  (no sibling) is the one `ChunkText`. The single-visible-text-part guard only
  ever sees the one answer identity, so a reasoning-then-tool turn now
  translates the trailing `function_call` `output_item.done` to `ChunkToolCall`
  — the model's tool call reaches the harness.

- **The reasoning prefix is now retry-safe (`isCommitting` implication).**
  `ChunkReasoning` is a NON-COMMITTING chunk in `llmresilience`'s establishment
  seam, so a leading interleaved-reasoning prefix is buffered and replayed on a
  successful re-establishment — the resilience wrapper can retry a turn whose
  first commit failed AFTER the reasoning streamed, without losing or
  duplicating it. This is the same property the dedicated-reasoning path already
  enjoyed; the marker extends it to interleaved reasoning for free.

- **No operator flag, no `port` widening.** The discrimination is entirely
  catalog-derived and adapter-internal. There is no `--interleaved-reasoning`
  flag (the model's wire shape is not an operator decision), no
  `port.LLMRequest` field, and no proto/`task generate` change. Composition is
  the ONLY layer that touches the catalog field; the loop is storage- and
  shape-agnostic.

- **Cost: a per-delta raw-JSON probe on interleaved-reasoning models only.**
  When `interleavedField == ""` (the overwhelming majority of models, including
  all dedicated-reasoning ones) the probe is skipped entirely — the default path
  is byte-identical and allocation-free. On a configured interleaved model, each
  `output_text.delta` decodes its top-level keys into a
  `map[string]json.RawMessage` and unmarshals the one named field — a small,
  bounded cost on the hot path, traded for correct discrimination. The probe
  materialises only top-level keys; values stay as `json.RawMessage` until the
  one named field is picked, keeping the allocation footprint minimal.

- **The catalog field is the single source of truth.** A future model that
  interleaves reasoning on a differently-named sibling field needs only a catalog
  entry (`interleaved.field: "<name>"`) — no adapter edit, no composition edit.
  The helper's map-keyed decode is deliberately field-name-agnostic.

## See also

- [ADR 0054](./0054-reasoning-rebalance-default-prompt.md) — the default-tone
  rebalance that named reasoning as a protected channel for interleaved-reasoning
  models; this ADR is the wire-side complement (keeping that reasoning
  display-only instead of leaking it into the answer).
- [ADR 0055](./0055-reasoning-effort.md) — the per-session re-mint factory
  discipline this feature reuses (the `remint` closure now carries the
  interleaved field as a third parameter).
- [ADR 0017](./0017-openai-responses-api.md) — the Responses-API event mapping
  this discriminator extends.
- `internal/adapter/openai/stream.go` — `interleavedReasoningContent` + the
  `response.output_text.delta` case.
- `internal/adapter/providercatalog/catalog.go` — `InterleavedReasoningField()`.
- `internal/app/capability.go` — `interleavedReasoningField` (catalog→composition
  helper).
- `docs/design/IMPLEMENTATION-NOTES.md` — the interleaved-reasoning translation
  note (providers/adapter section).
