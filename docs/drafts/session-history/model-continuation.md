# Model continuation

**Review question:** What must survive to reconstruct the effective model request without incidental drift?
This draft covers logical input fidelity and private provider continuation, not new provider retry behavior or raw transport recording.

## Imported assumptions

[Private committed history](vocabulary.md) is authoritative; replay reconstructs accepted facts without calling a model, tool, or hook.
[Execution gates](execution-and-recovery.md) require complete input acceptance before model execution and a canonical result for every required tool call before continuation.
[Authorization and hooks](authorization-and-hooks.md) bind any input transformation to the exact permitted values rather than today's configuration.
[Disclosure and memory-only holds](cancellation-and-results.md) limit capture: unreleased output and evidence containing its bytes remain only in process memory, even when private history could store them.

## Settled rules and why

A **logical model input** is the complete effective request: effective system instructions, ordered messages/media, tool declarations, model selection and behavior-affecting parameters, and relevant private provider continuation.
Retain values or immutable prior-record references that reconstruct it without current prompt templates, hook configuration, or mutable external content.
Request-only transformations cannot escape capture merely because stored conversation did not change.
Canonical outputs, calls/results, reported usage, and permitted transformation provenance have their respective acceptance boundaries.
High fidelity is the default, with no first-cutover history trimming and no relaxation of payload admission bounds.
This preserves logical values, not raw HTTP framing, token chunks, or credentials.

For unchanged history/effective configuration and a compatible provider codec, reload preserves the cache-relevant historical representation.
Decoded JSON semantic equality is insufficient.
Preserve nested tool-argument key order, whitespace inside strings, historical message/call/result ordering, canonical identities, provider pairing tokens, item IDs, and opaque replay payloads.
Restore retained bindings instead of minting replacements; replay-free fallback pairing tokens are deterministic from retained identity and occurrence context.
Separately check rebuilt prompts, tool declaration order, model settings, cache-key configuration, and breakpoint placement.
Deliberate configuration/provider/context changes are not equivalent-input reloads.
Stable reconstruction promises neither a cache hit nor raw HTTP byte equality.
TTL, eviction, routing, and legitimate prefix changes can still prevent reuse.

**Provider replay** is private versioned data owned by the LLM boundary: ordered output items and their occurrence-specific bindings to canonical facts.
It is not another history authority or a command to reexecute anything.
One canonical assistant message need not equal one provider message or reasoning item.
Preserve item boundaries, ordering, immutable payload ownership, and producer/version provenance rather than last-wins fields.
Core does not interpret opaque signed/encrypted payloads; accepted values stay byte-exact under capture policy.

Engine-owned globally unique call IDs, provider pairing keys, and provider item IDs are distinct namespaces.
Bindings include producing session and request/turn occurrence so repeated provider keys cannot alias another call/result.
Required replay and canonical outputs share a consistent acceptance boundary; partial or mismatched replay cannot authorize execution.
Restart, compaction, forks, and provider switches require compatible LLM-boundary interpretation and transformations.
Unsupported continuation-critical state fails explicitly, not by silently dropping metadata.

## Ordinary reload: a high-resolution request

A request-local hook changes instructions without changing stored conversation.
The owner commits permitted transformation evidence and the complete effective logical request before invocation.
Later full replay, or a checkpoint followed by every suffix record, restores that exact effective input without running the hook or consulting today's templates.
Changing templates afterward does not rewrite what the historical request contained.
A client can see authorized canonical conversation without receiving this private continuation material.

## Failure: lost output cannot be captured indirectly

A tool result is awaiting release and a post-tool hook has processed its bytes.
Its payload-bearing input/output/trace remains memory-only along with the held result.
A crash makes the bytes unavailable even if history and checkpoint storage remain intact.
Record safe disposition and execution evidence; do not stash the content in transformation provenance, replay, diagnostics, or receipts.
Capture fidelity cannot override withholding or destruction policy, and retrying the tool cannot restore the result.

## Collision and ordering example

A provider completes one turn with separate message, reasoning, and function-call items, then reuses a pairing key in another completed turn.
Retained ordered items preserve the separate boundaries; occurrence-specific bindings connect each to its own canonical call identity.
Reconstructing by a global provider-key map would alias calls and is invalid.
An incompatible provider switch must report unsupported continuation rather than reinterpret the keys as engine IDs.
These semantics reserve richer replay capacity; they do not authorize incremental output-item acceptance, partial-response retry, or WebSocket continuation.

## Cache-stable reconstruction check

Compare the real storage round trip and actual provider request construction using fresh adapters, not just a serializer or hand-built object.
Exercise full replay and checkpoint-plus-suffix, stored replay and replay-free fallback pairing.
Nested nonlexicographic keys and string whitespace must survive; deliberately reorder keys or change string content as negative controls.
Provider-specific comparisons can allow established formatting/escaping equivalences, but [tested route tolerances](session-commit-history-prior-art.md#provider-cache-stability-across-session-reload) do not authorize arbitrary normalization or paid live requests in offline tests.
Successful parsing does not establish cache equivalence.

## Open decisions and detail

Concrete logical-input/transformation APIs, ordered provider-item codecs, occurrence binding formats, and compatibility/fork/compaction/provider-switch interfaces remain open.
They cannot weaken complete effective capture or add new execution/retry behavior.
Exact raw historical transport requests remain outside scope, not an unresolved fidelity target.

The detailed owners are [complete logical inputs](session-commit-history.md#complete-logical-model-inputs-and-transformation-evidence), [cache stability](session-commit-history.md#cache-stable-reconstruction-across-reload), and [versioned provider replay](session-commit-history.md#versioned-provider-replay-and-richer-response-items).
[Acceptance scenarios](session-commit-history-contracts.md#acceptance-scenarios) separate cache-prefix configuration checks from history reconstruction.
