# Agent model discovery search and continuation — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes one model-facing tool contract and its composition-owned projection, but preserves provider identity, selection, trust, persistence, transport, and module boundaries.
**Decision record:** None — provider facets, bounded literal search, and stateless continuation extend the accepted resolved-inventory behavior without a durable architectural decision.
**Phase:** agent-facing model discovery, phase 2
**Status:** proposed, 2026-09-24 — provider facets, query semantics, continuation, and prompt guidance were selected in operator conversation; ready for Plan / Interface review.
**Delivery:** Split. The model-facing schema, disclosure boundary, search semantics, and stale-continuation behavior benefit from contract review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** None assigned.
**Plan PR:** [#1857](https://github.com/stacklok/mecatl/pull/1857)
**Approved baseline:** absent until the Plan / Interface PR merges

`DiscoverModels` will let an agent find an exact selectable `(provider_id, model_id)` without
already knowing the provider or scanning only the first bounded page of a provider with hundreds
of models. The existing tool remains the single discovery affordance: an unfiltered first-page
response adds a compact facet of selectable providers, first-page calls may narrow the shared
resolved inventory with bounded literal terms, and an opaque cursor continues a stable inventory
snapshot. No `ListProviders` tool, provider probe, refresh, ranking, or selection side effect is
added.

The provider/model pair remains the only selection handle defined by
[ADR 0016](../adr/0016-multi-provider.md#5-providermodel-selection-primitive). Dynamic provider
names stay out of the stable system prompt; the prompt instead tells the model how to call the
tool when the provider is unknown. Live data remains in the same composition-owned inventory
used by `ListModels`, as described by the
[model-inventory architecture](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory)
and the [phase-1 implementation notes](../design/IMPLEMENTATION-NOTES.md#agent-facing-model-discovery-issue-1064-phase-1).

## Human decisions

- [x] Keep one discovery tool — Decision: extend `DiscoverModels`; do not add `ListProviders`. Provider IDs are a facet of finding an exact inference target, not a separate administration surface.
- [x] Expose only selectable providers — Decision: an unfiltered first-page call derives `providers` from valid model rows in the captured resolved inventory, sorted by `provider_id`, and carries only `provider_id` plus total `model_count`. Filtered calls and cursor continuations omit the facet so unrelated providers cannot consume their result budget. Configured providers absent from the selectable inventory, endpoints, credentials, registry internals, and status-only providers are not exposed or inferred.
- [x] Use bounded literal query terms — Decision: `query` is an optional UTF-8 string. Validation rejects Unicode control characters before normalization; an empty value or a value containing only permitted non-control Unicode whitespace is omitted. Other values normalize with Go `strings.Fields` and `strings.ToLower`, without Unicode normalization or full case folding, into at most eight terms; every term must occur as a literal substring in at least one similarly lowercased `provider_id`, `model_id`, or `display_name`. There is no regex, glob, quoting, field prefix, Boolean operator, negation, stemming, fuzzy match, or relevance ranking.
- [x] Use opaque snapshot-bound continuation — Decision: first-page calls accept exact filters, query, and limit; a non-empty `cursor` is exclusive of them and restores the normalized scope, effective limit, next offset, and SHA-256 digest of the complete canonical safe inventory. The cursor is canonical unpadded base64url of a strict versioned JSON envelope, at most 4096 encoded bytes, stateless, unsigned, and untrusted. A malformed cursor is a fixed bounded tool error; a valid cursor whose inventory digest changed is a fixed bounded tool error instructing the model to restart without a cursor. A supported-version cursor remains valid across process restart when the canonical safe inventory is unchanged. No historical snapshot or signing key is retained.
- [x] Keep dynamic inventory out of the system prompt — Decision: the stable prompt describes the provider-discovery workflow and exact-pair rule, but never embeds provider IDs, model counts, models, cursors, or status. The tool result is the sole model-visible source for current inventory data.

## Interface contract

- **gRPC / protobuf:** None — `ListModels`, `ModelInfo`, `ProviderStatus`, session creation, and every wire field remain unchanged; this is an in-loop tool-result contract only.
- **Exported Go APIs / interfaces:** None — no `engine/` export, provider port, server interface, or public Go API changes. Composition continues injecting the existing resolved inventory into the tool.
- **Tool schemas:** `DiscoverModels` keeps optional exact `provider_id`, optional exact `model_id`, and `limit` (default 20, maximum 50), and adds optional `query` and optional `cursor`. Empty `provider_id`, `model_id`, and `cursor`, plus an empty query or one containing only permitted non-control Unicode whitespace, mean omitted. Query validation rejects Unicode control characters before normalization. A non-empty cursor is mutually exclusive with non-empty exact filters/query and with an explicit limit. Query input is valid UTF-8 without controls, at most 512 bytes before normalization, normalized with Go `strings.Fields` and `strings.ToLower` without Unicode normalization or full case folding, at most eight whitespace-separated terms, at most 64 bytes per term, and at most 256 aggregate normalized term bytes. Exact filters and query compose with AND; omitting `provider_id` searches every selectable provider. The JSON result keeps `models`, `returned`, `available`, and `truncated`; it adds optional `providers:[{provider_id,model_count}]` on an unfiltered non-cursor call and optional `next_cursor`. `providers` describes the complete captured selectable inventory. Filtered calls and cursor continuations omit it. `available` counts filtered matches before pagination, `returned` counts complete rows emitted, `truncated` is true exactly when `next_cursor` is present, and the cursor resumes at the first unreturned filtered row. Results retain canonical `(provider_id, model_id)` ordering and the existing 32 KiB whole-result ceiling, including any provider facet and cursor. If the fixed envelope or next complete row cannot fit while making progress, the tool returns a bounded error rather than an empty looping page or partial handle.
- **CLI / config:** None — no flags, settings, environment variables, defaults, precedence, or deployment controls change.
- **Events / persistence:** None — query state, provider facets, inventory digests, and cursors are not session fields, events, logs, snapshots, or stored server state. Each call first copies the safe scalar fields (`provider_id`, `model_id`, `display_name`, `image`, `reasoning`, and `context_limit`) from one current inventory slice into an immutable local projection, canonically sorts it, and then digests, groups, filters, counts, and pages only that projection. Continuation validates against a newly captured projection and either returns one page or a tool error asking the model to restart.
- **Security / authority:** Discovery remains read-only and its existing permission-policy behavior is unchanged; this plan adds no default Allow/Ask/Deny rule or permission bypass. Search and provider facets inspect only the already model-visible safe fields `provider_id`, `model_id`, and `display_name`; output retains the established safe model metadata. The tool never exposes or searches credentials, endpoints, configuration, unavailable-provider existence, topology, raw listing failures, or provider-private metadata, and never probes, refreshes, routes, or selects. Cursor decoding accepts at most 4096 encoded bytes, requires canonical unpadded base64url of a strict versioned JSON envelope, binds SHA-256 over the complete canonical scalar projection, rejects unknown versions/fields and malformed or noncanonical encodings without echoing input, and grants no authority.
- **Compatibility / migration:** Existing `{}`, exact-filter, empty-filter, and limit calls remain valid and preserve their model rows, counts, ordering, default/max limits, permission behavior, and 32 KiB ceiling; only an unfiltered non-cursor response gains the additive provider facet, while filtered and continuation responses do not spend their budget on unrelated providers. The response otherwise gains only additive fields. The formerly rejected `query` and `cursor` names become supported; other unknown fields remain rejected. A supported-version cursor remains valid across process restart only when the canonical safe inventory is unchanged; visible inventory changes or unsupported future cursor versions return a tool error instructing the caller to restart rather than migrate the cursor. Living architecture, implementation notes, and user documentation update in the implementation PR.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — an agent discovers selectable provider IDs without guessing

The first bounded page alone can be dominated by a provider with hundreds of models. An unfiltered
call therefore returns a complete provider facet derived from the same captured safe inventory,
while preserving exact-pair identity from
[ADR 0016](../adr/0016-multi-provider.md#5-providermodel-selection-primitive).

**Acceptance:**
- AC1.1: every successful unfiltered non-cursor response contains one lexically ordered provider row for each distinct non-empty `provider_id` represented by a valid model row in the captured inventory, with `model_count` equal to that provider's total selectable rows; duplicate model IDs under different providers remain distinct model handles.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario1_SelectableProviderFacets`
- AC1.2: exact provider/model filtering, query calls, and cursor continuations omit the provider facet so unrelated provider data cannot consume their bounded result; an empty unfiltered inventory returns empty providers and models without inventing configured or unavailable providers.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario1_FacetsAreInitialAndSafe`
- AC1.3: no separate provider-list tool is registered in shared, selector, or no-FS catalogs; the existing catalog parity guards continue to cover the single `DiscoverModels` registration.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario1_SingleDiscoveryTool`

### Scenario 2 — bounded terms find candidates across one or all providers

Query is discovery over safe resolved metadata, not another selector or routing language. Omission of
`provider_id` means every selectable provider; an exact filter remains available after the provider
facet teaches the model a valid ID. The behavior extends the phase-1 narrowing boundary in the
[existing discovery plan](agent-model-discovery.md#scenario-2--narrowing-is-safe-and-cannot-create-a-second-selection-language)
while preserving the exact two-field selection primitive in
[ADR 0016](../adr/0016-multi-provider.md#5-providermodel-selection-primitive).

**Acceptance:**
- AC2.1: query validation rejects Unicode controls before normalization; permitted non-control Unicode whitespace is then split with Go `strings.Fields` and terms are normalized with `strings.ToLower`, with no Unicode normalization or full case folding; composed and decomposed forms remain distinct. Repeated permitted whitespace and case differences covered by that exact operation do not change matching. Every normalized term must be a literal substring of at least one similarly lowercased searchable field, terms may match different fields, exact filters compose with query using AND, and returned identifiers remain byte-exact.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario2_LiteralTermSearch`
- AC2.2: punctuation has no syntax role, and strings resembling regex, glob, quotes, `field:value`, Boolean operators, negation, capability comparisons, endpoints, or routes are matched only as literal terms and never interpreted.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario2_NoQueryLanguage`
- AC2.3: empty query and queries containing only permitted non-control Unicode whitespace are omitted; invalid UTF-8, any Unicode control character (including tab/newline), excess raw or normalized bytes, too many terms, and oversized terms produce fixed bounded errors that do not echo caller input or inspect any non-public field.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario2_QueryValidationAndNonDisclosure`
- AC2.4: omitting `provider_id` searches all selectable providers, while a known exact provider narrows the same query; unknown exact providers return an honest empty model result without a provider facet, fallback, probe, or provider inference from a model ID.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario2_AllProviderAndExactProviderSearch`

### Scenario 3 — opaque continuation traverses one coherent inventory

Large provider inventories require an actionable continuation rather than `truncated:true` with no
way to reach later handles. Pagination follows the repository's bounded cursor posture without
adding stored state or changing the live inventory owner described in the
[architecture](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).

**Acceptance:**
- AC3.1: when more filtered rows remain after the complete rows that fit both the requested limit and whole-result byte ceiling, the response sets `truncated:true` and returns one bounded opaque `next_cursor`; calling with that cursor alone returns the next non-overlapping page in canonical order, and the final page omits the cursor and sets `truncated:false`.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario3_CursorTraversal`
- AC3.2: the cursor binds its version, normalized filters/query, effective limit, next offset, and SHA-256 digest of the complete canonically sorted safe scalar projection (`provider_id`, `model_id`, `display_name`, `image`, `reasoning`, and `context_limit`). Republishing a byte-identical canonical projection, including across process restart or from a differently ordered source slice, remains valid; any canonical safe-projection addition, removal, or metadata change returns a fixed bounded tool error instructing the model to restart without a cursor and emits no partial page.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario3_InventoryBoundCursor`
- AC3.3: a non-empty cursor accompanied by a non-empty exact filter/query or explicit limit, plus a cursor over 4096 encoded bytes or one with malformed, padded/noncanonical base64url, invalid/unknown JSON fields or version, or semantically invalid scope/offset data, returns a fixed bounded tool error without echoing the cursor or inventory data. Empty cursor is omitted for serializer compatibility; supported cursors use canonical unpadded base64url of strict JSON and restore their complete first-page scope.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario3_CursorValidation`
- AC3.4: cursor bytes and any initial unfiltered provider-facet bytes participate in the 32 KiB ceiling; continuation pages do not repeat the provider facet. The first unreturned row is never skipped, identifiers are never truncated, and a response that cannot make forward progress returns a bounded error rather than an empty page with a continuation.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario3_ByteBoundMakesProgress`

### Scenario 4 — the model learns the workflow while live truth stays in the tool

The model needs an explicit workflow but not a copied inventory in its stable prompt. The existing
model-visible discoverability pattern remains factory-tested, while the live result follows the
same refresh/fallback snapshot semantics as phase 1 and the
[model-inventory architecture](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).
The implementation updates the canonical `user-docs/features/choose-models.md` model-inventory guidance.

**Acceptance:**
- AC4.1: the real factory-built stable prompt tells the model to call `DiscoverModels` without `provider_id` when the provider is unknown, that omission searches all selectable providers, that an unfiltered result lists exact selectable provider IDs, and that only a returned exact `(provider_id, model_id)` pair may be passed to an existing surface that explicitly accepts both or returned to the caller for selection; it states that `DiscoverModels` itself cannot switch the session and contains no concrete provider/model ID, count, status, or cursor from the live inventory.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario4_SystemPromptContainsWorkflowNotInventory`
- AC4.2: the tool specification explains query normalization, all-provider omission, exact filters, limit, cursor continuation, stale-cursor restart, provider facets, output bounds, and the no-probe/no-selection boundary using concise model-actionable descriptions.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario4_ToolSpecificationContract`
- AC4.3: before, during, and after inventory publication, each call reads one inventory slice, copies its safe scalar fields into an immutable local projection, and performs canonical sorting, digesting, provider grouping, filtering, counting, and paging only on that copy; no discovery call initiates refresh, performs provider I/O, creates a retained cache, or changes session selection.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario4_SharedLiveInventory`
- AC4.4: `DiscoverModels.ReadOnly()` remains true and the implementation adds no built-in or configured permission rule, bypass, or special verdict; the existing permission evaluator continues to resolve the call unchanged.
  - verify: `TestInvariant_agent_model_discovery_v2_Scenario4_PermissionPostureUnchanged`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Separate `ListProviders` or provider administration tool | demonstrated provider-health or administration workflow | Selectable provider IDs are a discovery facet; configuration and administration remain operator surfaces. |
| Literal live provider/model inventory in the system prompt | not planned | Dynamic data would become stale, duplicate the source of truth, spend tokens every turn, and destabilize the prompt-cache prefix. |
| Status-only, unavailable, configured-without-credentials, or endpoint/provider-registry discovery | separate authority and disclosure plan | This plan exposes only providers that own selectable rows in the existing safe inventory. |
| Capability filters, pricing, quality/relevance ranking, recommendations, aliases, automatic selection, or fallback | evidence-driven follow-up | Query finds candidates; returned safe metadata supports comparison without the tool choosing a target. |
| Regex, glob, Boolean, fielded, fuzzy, semantic, or upstream/provider-native search | not planned | The bounded literal-term contract is deterministic and cannot become a route or selection language. |
| Historical snapshot retention, cursor signing/encryption, cursors that survive a changed inventory or unsupported future cursor version, or cursor persistence as server state | scale or authority-driven follow-up | A supported stateless cursor may survive process restart only while its canonical inventory digest remains unchanged; otherwise continuation restarts. |
| Changes to `ListModels`, provider selection, child routing, or session model switching | separate focused work | Discovery remains informational and preserves existing exact-selector behavior. |

## Definition of done

1. Focused `internal/app` tests pass during implementation; `task lint`, `task test:race`, `task api:check`, `task docs`, and `task site:build` pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` still prints a complete offline session.
4. The implementation PR links the Plan / Interface PR and approved commit and reports tool-schema and disclosure conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The cursor's private strict-JSON field names and the collision-unambiguous canonical serialization fed to SHA-256 may use the smallest deterministic stdlib representation that satisfies AC3.2 and AC3.3; neither byte representation is a public contract.
- Each call must copy the safe scalar projection before grouping, hashing, filtering, and paging; it may not rely on mutable `*ModelInfo` pointer immutability and must not retain the copy after the call.
- Frequent visible inventory refresh can repeatedly stale a long traversal. Restart is intentional because this tool discovers current targets rather than exporting a historical catalog.
- The complete provider facet is still bounded by the 32 KiB whole-result ceiling on an unfiltered initial call. Deployments whose provider summary alone cannot fit receive the explicit no-progress error; filtered calls and cursor continuation remain usable because they omit the facet, while a separately paged provider inventory is outside this plan.
