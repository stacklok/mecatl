# Model continuation

**Review question:** What must survive to reconstruct the effective model request without incidental drift?
This draft covers logical input fidelity and private provider continuation, not new provider retry behavior or raw transport recording.

## Ordinary reload: a high-resolution request

Suppose a server prepares an LLM request, then a replacement server later needs to reconstruct what was sent.
The conversation alone may not be enough: a request-local hook may have changed instructions, a prompt template may since have changed, or a provider may require private response data to continue.
The replacement needs the effective outgoing request, not a plausible request rebuilt from today's defaults.
Reconstructing it does not mean sending it again.

The server records the outgoing instructions, messages, tool definitions, settings, and needed private provider data before sending the request.
A later server can reconstruct those same values without reapplying today's configuration.

The same distinction matters for prompt-cache reuse.
For example, rebuilding tool arguments as `{"b":{"z":1,"a":2}}` versus `{"b":{"a":2,"z":1}}` may preserve their decoded meaning but change the representation the provider sees.
Changing `"a  b"` to `"a b"` changes the string itself, not merely the formatting around JSON tokens.
A reload should not introduce either kind of incidental drift.
Stable reconstruction promises neither a cache hit nor raw HTTP byte equality.
TTL, eviction, routing, and legitimate prefix changes can still prevent reuse.

## Collision and ordering example

An LLM provider can return several output items for one response: for example, a message, reasoning data, and a function-call item.
The engine's canonical assistant message does not necessarily retain that provider-specific structure by itself.
Some items relate to tool calls; others, such as reasoning items, do not bind to a tool call at all.
Private **provider replay** retains what the LLM adapter needs from that completed response to construct a later request.
Despite its name, it is retained data, not permission to invoke the provider again.

The engine assigns each tool call a globally unique ID.
It records how that ID connects to the provider's pairing key and any relevant output-item IDs.
These provider identifiers remain separate from the engine's IDs.
The record also identifies the session and model request that produced the tool call, so a provider can reuse a key in a later response without confusing the two tool calls or their results.

For example, response 1 can use provider key `call_7` for a file read, and response 2 can use `call_7` for a different read.
A map indexed only by `call_7` would overwrite one association and could attach a result to the wrong call.
The retained response occurrence and engine tool-call ID keep those associations distinct.

## Failure: lost output cannot be captured indirectly

A tool result is awaiting release and a post-tool hook has processed its bytes.
Its payload-bearing input/output/trace remains memory-only along with the held result.
A crash makes the bytes unavailable even if history and checkpoint storage remain intact.
Record safe disposition and execution evidence; do not stash the content in transformation provenance, replay, diagnostics, or receipts.
Capture fidelity cannot override withholding or destruction policy, and retrying the tool cannot restore the result.

## Imported assumptions

The examples rely on recording permitted historical values, not recreating them by repeating external work.
[Private committed history](vocabulary.md) is authoritative; replay reconstructs accepted facts without calling a model, tool, or hook.
[Execution gates](execution-and-recovery.md) require complete input acceptance before model execution and a canonical result for every required tool call before continuation.
[Authorization and hooks](authorization-and-hooks.md) bind any input transformation to the exact permitted values rather than today's configuration.
[Disclosure and memory-only holds](cancellation-and-results.md) limit capture: unreleased output and evidence containing its bytes remain only in process memory, even when private history could store them.

## Settled rules and why

**Detailed constraints begin here.**
The following inventories spell out what “the same effective request” and compatible private continuation require.
They qualify the examples without making raw transport capture a goal.

### Complete logical input and acceptance boundaries

A **logical model input** is the complete effective request: effective system instructions, ordered messages/media, tool declarations, model selection and behavior-affecting parameters, and relevant private provider continuation.
Retain values or immutable prior-record references that reconstruct it without current prompt templates, hook configuration, or mutable external content.
Request-only transformations cannot escape capture merely because stored conversation did not change.
Canonical model outputs, tool calls/results, reported usage, and permitted transformation provenance have their respective acceptance boundaries.
High fidelity is the default, with no first-cutover history trimming and no relaxation of payload admission bounds.
This preserves logical values, not raw HTTP framing, token chunks, or credentials.

A request-local hook changes instructions without changing stored conversation.
The owner commits permitted transformation evidence and the complete effective logical request before invocation.
Later full replay, or a checkpoint followed by every suffix record, restores that exact effective input without running the hook or consulting today's templates.
Changing templates afterward does not rewrite what the historical request contained.
A client can see authorized canonical conversation without receiving this private continuation material.

### Representation, identities, and cache controls

For unchanged history/effective configuration and a compatible provider codec, reload preserves the cache-relevant historical representation.
Decoded JSON semantic equality is insufficient.
Preserve nested tool-argument key order, whitespace inside strings, historical message/tool-call/result ordering, canonical identities, provider pairing tokens, item IDs, and opaque replay payloads.
Restore retained bindings instead of minting replacements; replay-free fallback pairing tokens are deterministic from retained identity and occurrence context.
Separately check rebuilt prompts, tool declaration order, model settings, cache-key configuration, and breakpoint placement.
Deliberate configuration/provider/context changes are not equivalent-input reloads.

### Versioned provider continuation

**Provider replay** is private versioned data owned by the LLM boundary: ordered output items and their occurrence-specific bindings to canonical facts.
It is not another history authority or a command to reexecute anything.
One canonical assistant message need not equal one provider message or reasoning item.
Preserve item boundaries, ordering, immutable payload ownership, and producer/version provenance rather than last-wins fields.
Core does not interpret opaque signed/encrypted payloads; accepted values stay byte-exact under capture policy.

A provider completes one turn with separate message, reasoning, and function-call items, then reuses a pairing key in another completed turn.
Retained ordered items preserve the separate boundaries; occurrence-specific tool-call bindings connect function-call items to their own canonical tool-call identities.
Reconstructing by a global provider-key map would alias tool calls and is invalid.
An incompatible provider switch must report unsupported continuation rather than reinterpret the keys as engine IDs.
These semantics reserve richer replay capacity; they do not authorize incremental output-item acceptance, partial-response retry, or WebSocket continuation.

The model's output and its required replay data must be accepted together and describe the same completed response.
Incomplete or mismatched replay data cannot authorize dependent execution.
After a restart, compaction, fork, or provider switch, the LLM adapter must be able to interpret the retained replay data and make any required conversions.
If it cannot interpret data needed to continue, it must stop dependent execution and report the problem rather than silently drop the data.

## Cache-stable reconstruction check

This is the detailed validation boundary, not a requirement to obtain a cache hit in a live provider test.
Compare the real storage round trip and actual provider request construction using fresh adapters, not just a serializer or hand-built object.
Exercise full replay and checkpoint-plus-suffix, stored replay and replay-free fallback pairing.
Nested nonlexicographic keys and string whitespace must survive; deliberately reorder keys or change string content as negative controls.
Provider-specific comparisons can allow established formatting/escaping equivalences, but [tested route tolerances](session-commit-history-prior-art.md#provider-cache-stability-across-session-reload) do not authorize arbitrary normalization or paid live requests in offline tests.
Successful parsing does not establish cache equivalence.

## Open decisions and detail

How should the server represent a complete effective request and the transformations that produced it?
How should the adapter encode ordered provider items and connect each relevant item to the right occurrence of a canonical fact?
How should it detect compatibility and convert retained continuation when forking, compacting, or switching providers?

Interface details still to specify:

- Concrete logical-input/transformation APIs, ordered provider-item codecs, occurrence binding formats, and compatibility/fork/compaction/provider-switch interfaces remain open.
- They cannot weaken complete effective capture or add new execution/retry behavior.
- Exact raw historical transport requests remain outside scope, not an unresolved fidelity target.

The detailed owners are [complete logical inputs](session-commit-history.md#complete-logical-model-inputs-and-transformation-evidence), [cache stability](session-commit-history.md#cache-stable-reconstruction-across-reload), and [versioned provider replay](session-commit-history.md#versioned-provider-replay-and-richer-response-items).
[Acceptance scenarios](session-commit-history-contracts.md#acceptance-scenarios) separate cache-prefix configuration checks from history reconstruction.
