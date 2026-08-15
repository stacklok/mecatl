# ADR 0101 — OpenAI reasoning multiplicity: adapter-packed items + bounded encrypted-content repair

- Status: Accepted
- Date: 2026-08-10
- Scope: `provider/openai` (stream translation, request replay, error classification) and the `internal/adapter/llmresilience` retry-classification boundary. No domain, port, proto, or snapshot change.

## Context

A live `gpt-5.6` session through the ToolHive gateway died mid-run and could not
be resumed:

```text
invalid_encrypted_content: The encrypted content for item rs_01fee… could not be
verified. Reason: Encrypted content item_id did not match the target item id.
```

The Responses API's reasoning-replay unit is a **list**. A turn that interleaves
reasoning with tool calls emits several `response.output_item.done` reasoning
items, each carrying an `encrypted_content` blob cryptographically bound to that
item's own `rs_…` id. The failing turn carried three.

The adapter emitted one `ChunkReasoningItem` per item, each with its own id. The
loop (`engine/agent/loop.go`) folded them into the single domain pair on
`session.Message`:

```go
reasoningBlob += chunk.Text                  // ALL blobs concatenated
if chunk.ReasoningItemID != "" {             // only the LAST id kept
    reasoningItemID = chunk.ReasoningItemID
}
```

Replay (`assistantItems`) then sent three blobs' worth of ciphertext under
`rs_ccc` alone, and the provider refused to verify it. The loop carried a comment
asserting "the provider emits at most one per turn; concatenation is harmless if
it ever splits" — an untested assumption that turned out to be false.

Two properties made this much worse than a single failed turn. Replay is
**stateless full-history**, so the poisoned message was resent on every later
turn; and the rejection is a **permanent 400**, which `llmresilience` correctly
refuses to retry. One bad turn therefore bricked the whole session, and the
damage was already written to disk in sessions that pre-date any fix.

Three options were weighed:

1. **Widen the domain** to `Message.ReasoningItems []{ID, Blob}`. Correct, and it
   mirrors how `ToolCall.ItemID` already solves the identical
   several-items-per-turn problem. But it breaks the engine's public API and
   drags a new ADR-for-API-break, `task api:update`, a `sessnap` DTO migration, a
   proto field, and a mecatui struct behind it — nearly all migration ceremony
   rather than logic.
2. **Keep the last pair, drop the rest.** One-file change, but it produces a
   partial reasoning chain — a state nothing in the repo has ever produced —
   resting on an unverified assumption that an encrypted item verifies
   self-contained rather than chain-positionally. That is the same epistemic
   status as the comment that caused the bug.
3. **Pack the list in the adapter.** `provider/anthropic` already faced this
   exact shape (several thinking blocks, one opaque domain string) and solved it
   with a versioned JSON envelope inside `Message.Reasoning`.

Option 3 delivers option 1's correctness at option 2's cost, so the choice was
not close. The `Message.Reasoning` contract permits it: "provider-neutral" means
the **engine** never interprets the blob, not that the adapter may not give it a
shape. The structure stays entirely inside `provider/openai`.

## Decision

**Pack the turn's reasoning items into the opaque blob, and replay each under its
own id.** `provider/openai/reasoning.go` defines a versioned envelope
(`{"v":1,"items":[{"i":…,"e":…}]}`) mirroring `provider/anthropic/reasoning.go`.
`translate` buffers each `(item id, encrypted_content)` pair in `streamState` and
flushes one packed `ChunkReasoningItem` at `response.completed` — the first point
at which the full ordered list is known. `assistantItems` emits one input
reasoning item per entry, each under the id its blob is bound to. Pairing a blob
with the wrong id is now unrepresentable.

**Preserve the original interleaving too.** A reasoning turn that calls tools
emits `rs_a, fc_1, rs_b, fc_2, rs_c`, and the stateless-replay rule is to pass
those prior output items back *untouched*. Grouping all reasoning ahead of all
calls — what `assistantItems` did when a turn could only carry one reasoning item,
where it was trivially the true order — rewrites the model's own trajectory.
`session.Message` has no field ordering a reasoning blob against a tool call, so
each envelope entry records how many function calls preceded it (`After`) and
`assistantItems` weaves the two lists back together. Position is best-effort and
an entry is never dropped to preserve it: an `After` that overruns the calls a
message actually carries (a truncated snapshot) replays at the end rather than
vanishing. `After` is zero for every pre-existing blob, which reproduces the old
reasoning-then-calls order exactly.

`port.Chunk.ReasoningItemID` and `session.Message.ReasoningItemID` stay, unwidened
and unremoved, in a purely **legacy** role: a blob that does not parse as a
current-version envelope is a pre-packing bare `encrypted_content` string and is
replayed as exactly one item under `Message.ReasoningItemID`. Sessions already on
disk replay byte-for-byte as they did before.

**Repair a rejected replay exactly once, and bound it.** A blob the provider
previously returned can still be refused later — after a model or encryption-key
boundary, or because an older harness wrote a fused one. Permit one
protocol-private repair attempt, only when all three hold:

1. the request actually serialized a reasoning envelope (decided by the SAME
   `unpackReasoningItems` the wire projection uses, so the repair and the request
   builder can never disagree about whether one exists);
2. the first attempt emitted no provider-neutral chunk; and
3. the provider returned HTTP 400 with the typed `invalid_encrypted_content` code
   or the narrowly recognised verification/decryption message.

`withoutEncryptedReasoning` clones the request and removes **only** the reasoning
envelopes. Visible messages, tool calls and results, assistant phase markers, and
provider-assigned function-call item IDs all survive — stripping those would trade
this rejection for two other known failures (GPT-5.x treats a phase-less preamble
as a final answer and stops early; a function_call without its item id collides as
`Duplicate item found with id fc_N`). The caller's request is never mutated.

The repair lives in `provider/openai` because that is the only layer that knows
what the error means: `Session.Recover` is a provider-neutral state-machine
transition, and `llmresilience` is the provider-neutral retry wrapper whose
400-is-permanent classification is correct and must not be softened.

The repair is the only hidden retry. A failed repair returns a causal,
unwrap-visible `encryptedReasoningFallbackError` carrying an explicit
`Retryable() == false`, and `llmresilience` honours that decision **before**
classifying the wrapped HTTP or network error — otherwise a repair that failed
with a transient 5xx would let the outer wrapper replay the whole pair and spend
the repair again. Breaker health classification still inspects the underlying
cause, so a 5xx still describes provider health. The decision is deliberately NOT
promoted to `port.PermanentError`: a later user-initiated retry may still succeed.

An id-less reasoning item is dropped rather than replayed, preserving the existing
D1a degrade: the SDK serialises an unset id as `"id":""`, which strict gateways
reject outright, so losing that item's continuity beats a guaranteed 400.

## Consequences

**Easier.** A multi-step reasoning turn — the common shape when a model
interleaves reasoning with tool calls, and the one delegation reliably produces —
now replays correctly instead of poisoning the session. Sessions already poisoned
keep running rather than staying permanently `failed`, which no producer-side fix
alone could have achieved. Nothing outside `provider/openai` moved: no engine API
break, no `task api:update`, no snapshot migration, no proto field, no mecatui
change, no impact on the other three provider adapters.

**Harder / accepted costs.**

- The adapter now buffers payload across a turn, which its `streamState` comment
  previously disclaimed. The buffer is bounded by the turn's reasoning output and
  released with the stream.
- The reasoning replay blob no longer anchors TTFT at its natural position — it
  is emitted at the terminal event. In practice the display summary deltas
  (`ChunkReasoning`) already anchor TTFT earlier on any reasoning turn, and
  `provider/anthropic` has had this exact property since it started packing, so
  this is consistency rather than a new wart.
- Reasoning items are dropped on a turn that ends without `response.completed`.
  This costs nothing today — the engine discards the whole assistant message on
  those paths — but it is a coupling worth knowing about.
- A repaired request loses that turn's hidden reasoning continuity and its
  provider-side prompt-cache hit (the stripped prefix differs from the rejected
  one), so the next model turn reconstructs from the preserved visible and tool
  history. It is paid only on a request that would otherwise have failed outright.
- The adapter now owns a small error classifier and a two-attempt stream path.
  Its tests must pin the positive trigger, exact transcript preservation, the
  partial-envelope and unrelated-error rejections, the post-chunk no-replay rule,
  and the two-request ceiling under the outer resilience wrapper.
- The classifier reads a provider error. A typed 400 + `invalid_encrypted_content`
  is the primary signal, with the verification/decryption phrasing as the fallback
  for gateways that drop the code; a future rewording could silently stop
  repairing, which degrades to the pre-fix failure mode rather than anything worse.
- `llmresilience` now consults a duck-typed `Retryable() bool` before its own
  classifier. That is a second, adapter-driven authority over retry, deliberately
  private to the adapter/decorator interaction — it widens neither
  `port.LLMRequest` nor the provider port, but it is one more thing to reason
  about when changing retry behaviour.

**Committed to.** `Message.Reasoning` stays a bare string: two adapters now pack
a list into it, so the abstraction is load-bearing rather than incidental. It now
also carries *ordering* information (`After`) that the domain cannot express —
which is the price of keeping reasoning and tool calls in separate `Message`
fields. If a future provider needs richer interleaving than "how many calls
preceded this", that is the point to reconsider a single ordered output-item list
on the domain message rather than to grow the envelope further. Any
future adapter whose replay unit is a list packs its own envelope — emitting one
`ChunkReasoningItem` per unit and relying on the loop's concatenation is
structurally wrong, because the fold keeps only the last item id. That warning now
lives at the fold itself (`engine/agent/loop.go`). Widening the domain to a real
list remains available if a third provider needs per-item ids *and* packing proves
lossy; nothing here forecloses it.

## See also

- [Provider adapters](../architecture/providers.md) — the living description of
  the OpenAI request/stream mapping and the anthropic packing precedent.
- `user-docs/extension-points/llm-provider.md` — the contract an out-of-tree
  adapter author reads (outside the linked corpus; named, not linked).
- [ADR 0203](./0203-permanent-provider-error-signal.md) — the permanent-error
  signal this rejection travels under, and why it is not retried generically.
- [ADR 0016](./0016-multi-provider.md) — the provider-neutral `LLMRequest`
  discipline that keeps this fix inside the adapter.
