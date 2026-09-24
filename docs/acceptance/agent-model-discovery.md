# Agent model discovery — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this changes one model-facing tool contract and its composition-owned projection, but preserves provider identity, selection, trust, persistence, transport, and module boundaries.
**Decision record:** None — provider facets, bounded literal search, and stateless continuation replace the alpha tool contract without changing the durable provider identity or selection architecture.
**Phase:** alpha contract replacement
**Status:** landed, 2026-09-24 — stacked implementation candidate passed aggregate gates, strict AC tracing, and panel review; this transition becomes authoritative only after human merge of amendment PR #1864 followed by the implementation PR.
**Delivery:** Split. The replacement model-facing schema, disclosure boundary, search semantics, and stale-continuation behavior require amended contract review before implementation resumes.
**Expected tasks:** deferred to orchestration
**Issue:** [#1064](https://github.com/stacklok/mecatl/issues/1064)
**Plan PR:** [#1864](https://github.com/stacklok/mecatl/pull/1864)
**Prior approval:** Plan / Interface PR [#1857](https://github.com/stacklok/mecatl/pull/1857), merged as `dd77497c10071be17b1a3c302ac18eec3aadca8f`, is superseded by this amendment.
**Approved baseline:** absent until the amendment Plan / Interface PR merges

`DiscoverModels` will let an agent find an exact selectable `(provider_id, model_id)` without
already knowing the provider or scanning only the first bounded page of a provider with hundreds
of models. This amendment replaces the existing alpha contract in place: the one tool returns a
selectable-provider facet on an unfiltered first page, narrows the shared resolved inventory with
bounded literal terms, and uses an opaque cursor to continue a stable inventory snapshot. There
is no `DiscoverModelsV2`, `ListProviders`, compatibility shim, provider probe, refresh, ranking,
or selection side effect.

The provider/model pair remains the only selection handle defined by
[ADR 0016](../adr/0016-multi-provider.md#5-providermodel-selection-primitive). Dynamic provider
names stay out of the stable system prompt; the prompt instead tells the model how to call the
tool when the provider is unknown. Live data remains in the same composition-owned inventory
used by `ListModels`, as described by the
[model-inventory architecture](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).

## Human decisions

- [x] Replace the alpha contract in place — Decision: update the single `DiscoverModels` tool and canonical `agent-model-discovery.md` plan directly. Do not ship v1/v2 names, dual schemas, aliases, deprecation windows, response-shape shims, or compatibility guarantees for the pre-release contract.
- [x] Keep one discovery tool — Decision: extend `DiscoverModels`; do not add `ListProviders`. Provider IDs are a facet of finding an exact inference target, not a separate administration surface.
- [x] Expose only selectable providers — Decision: an unfiltered first-page call derives `providers` from valid model rows in the captured resolved inventory, sorted by `provider_id`, and carries only `provider_id` plus total `model_count`. Filtered calls and cursor continuations omit the facet so unrelated providers cannot consume their result budget. Configured providers absent from the selectable inventory, endpoints, credentials, registry internals, and status-only providers are not exposed or inferred.
- [x] Make the shared snapshot value-owned — Decision: `resolvedModelInventory` deep-copies every safe `ModelInfo` field on publication and returns fresh deep copies to readers. Mutating caller-owned input after `SetModels`, or a row returned by `CurrentModels`, cannot change the stored snapshot, `ListModels`, provider facets, search, or cursor digests. No mutable protobuf pointer is shared across the inventory boundary.
- [x] Use bounded literal query terms — Decision: `query` is an optional UTF-8 string. Validation rejects Unicode control characters before normalization; an empty value or a value containing only permitted non-control Unicode whitespace is omitted. Other values normalize with Go `strings.Fields` and `strings.ToLower`, without Unicode normalization or full case folding, into at most eight terms; every term must occur as a literal substring in at least one similarly lowercased `provider_id`, `model_id`, or `display_name`. There is no regex, glob, quoting, field prefix, Boolean operator, negation, stemming, fuzzy match, or relevance ranking.
- [x] Use opaque snapshot-bound continuation — Decision: first-page calls accept exact filters, query, and limit; a non-empty `cursor` is exclusive of them and restores the normalized scope, effective limit, next offset, and SHA-256 digest of the complete canonical safe inventory. The cursor is canonical unpadded base64url of a strict versioned JSON envelope, at most 4096 encoded bytes, stateless, unsigned, and untrusted. Restored terms are already-normalized lowercase single terms with zero to eight entries, no empty/whitespace/control content, the same 64-byte per-term and 256-byte aggregate bounds, and must round-trip the first-page normalizer unchanged; restored exact filters pass their first-page validator. Restored limit is in `[1,50]`, and restored offset must be positive and strictly less than the filtered match count for the unchanged snapshot. A malformed cursor is a fixed bounded tool error; a valid cursor whose inventory digest changed is a fixed bounded tool error instructing the model to restart without a cursor. A supported-version cursor remains valid across process restart when the canonical safe inventory is unchanged. No historical snapshot or signing key is retained.
- [x] Keep dynamic inventory out of the system prompt — Decision: the stable prompt describes the provider-discovery workflow and exact-pair rule, but never embeds provider IDs, model counts, models, cursors, or status. The tool result is the sole model-visible source for current inventory data.

## Interface contract

- **gRPC / protobuf:** None — `ListModels`, `ModelInfo`, `ProviderStatus`, session creation, and every wire field remain unchanged; this is an in-loop tool-result contract only.
- **Exported Go APIs / interfaces:** None — no `engine/` export, provider port, server interface, or public Go API changes. Composition continues injecting the existing resolved inventory into the tool.
- **Tool schemas:** `DiscoverModels` has one replacement input schema with optional exact `provider_id`, optional exact `model_id`, optional `query`, optional `cursor`, and `limit` (default 20, maximum 50). Empty `provider_id`, `model_id`, and `cursor`, plus an empty query or one containing only permitted non-control Unicode whitespace, mean omitted. Non-empty exact filters remain byte-exact: each must be valid UTF-8, at most 512 bytes, have no leading/trailing whitespace, and contain no Unicode control character; they are never trimmed, lowercased, or inferred. Cursor-restored exact filters pass the identical validation. Query validation rejects Unicode control characters before normalization. A non-empty cursor is mutually exclusive with non-empty exact filters/query and with an explicit limit. Cursor-restored exact filters, normalized terms, limit, and offset must satisfy the same first-page validators plus the canonical cursor constraints in AC3.3; cursor decoding is never an alternate weaker input path. Query input is valid UTF-8 without controls, at most 512 bytes before normalization, normalized with Go `strings.Fields` and `strings.ToLower` without Unicode normalization or full case folding, at most eight whitespace-separated terms, at most 64 bytes per term, and at most 256 aggregate normalized term bytes. Exact filters and query compose with AND; omitting `provider_id` searches every selectable provider. The JSON result schema is `models`, `providers`, `returned`, `available`, `truncated`, and `next_cursor`: `providers:[{provider_id,model_count}]` appears on an unfiltered non-cursor call and `next_cursor` appears only when more filtered rows remain. `providers` describes the complete captured selectable inventory. Filtered calls and cursor continuations omit it. `available` counts filtered matches before pagination, `returned` counts complete rows emitted, `truncated` is true exactly when `next_cursor` is present, and the cursor resumes at the first unreturned filtered row. Results retain canonical `(provider_id, model_id)` ordering and the existing 32 KiB whole-result ceiling, including any provider facet and cursor. If the fixed envelope or next complete row cannot fit while making progress, the tool returns a bounded error rather than an empty looping page or partial handle.
- **CLI / config:** None — no flags, settings, environment variables, defaults, precedence, or deployment controls change.
- **Events / persistence:** None — query state, provider facets, inventory digests, and cursors are not session fields, events, logs, snapshots, or stored server state. `resolvedModelInventory` owns an immutable value snapshot: publication deep-copies every safe model field, and each read returns fresh deep copies, so neither publisher nor reader mutation can race or change stored state. Each discovery call then copies those safe scalar fields (`provider_id`, `model_id`, `display_name`, `image`, `reasoning`, and `context_limit`) into its local projection, canonically sorts it, and digests, groups, filters, counts, and pages only that projection. Continuation validates against a newly captured projection and either returns one page or a tool error asking the model to restart.
- **Security / authority:** Discovery remains read-only and its existing permission-policy behavior is unchanged; this plan adds no default Allow/Ask/Deny rule or permission bypass. Search and provider facets inspect only the already model-visible safe fields `provider_id`, `model_id`, and `display_name`; output retains the established safe model metadata. The tool never exposes or searches credentials, endpoints, configuration, unavailable-provider existence, topology, raw listing failures, or provider-private metadata, and never probes, refreshes, routes, or selects. Cursor decoding accepts at most 4096 encoded bytes, requires canonical unpadded base64url of a strict versioned JSON envelope, binds SHA-256 over the complete canonical scalar projection, rejects unknown versions/fields and malformed or noncanonical encodings without echoing input, and grants no authority.
- **Compatibility / migration:** This is an intentional in-place replacement of an alpha tool contract. `DiscoverModels` keeps its name and exact `(provider_id, model_id)` identity, but the old three-argument schema and old response-shape behavior receive no compatibility shim, alias, deprecation period, migration reader, or parallel v2 surface. Models and clients must consume the newly advertised schema and result. Existing phase-1 tests and proof names are replaced or renamed to the canonical AC proofs in this amended plan; obsolete expectations such as rejecting `query` are deleted rather than retained beside contradictory tests. Cursor values remain valid across process restart only when their version is supported and the canonical safe inventory is unchanged; visible inventory changes or unsupported future cursor versions return a tool error instructing the caller to restart. The implementation updates current architecture and user documentation and removes superseded phase/v2 wording from the acceptance index.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — an agent discovers selectable provider IDs without guessing

The first bounded page alone can be dominated by a provider with hundreds of models. An unfiltered
call therefore returns a complete provider facet derived from the same captured safe inventory,
while preserving exact-pair identity from
[ADR 0016](../adr/0016-multi-provider.md#5-providermodel-selection-primitive).

**Acceptance:**
- AC1.1: every successful unfiltered non-cursor response contains one lexically ordered provider row for each distinct non-empty `provider_id` represented by a valid model row in the captured inventory, with `model_count` equal to that provider's total selectable rows; duplicate model IDs under different providers remain distinct model handles.
  - verify: `TestInvariant_agent_model_discovery_Scenario1_SelectableProviderFacets`
- AC1.2: exact provider/model filtering, query calls, and cursor continuations omit the provider facet so unrelated provider data cannot consume their bounded result; an empty unfiltered inventory returns empty providers and models without inventing configured or unavailable providers.
  - verify: `TestInvariant_agent_model_discovery_Scenario1_FacetsAreInitialAndSafe`
- AC1.3: no separate provider-list tool is registered in shared, selector, or no-FS catalogs; the existing catalog parity guards continue to cover the single `DiscoverModels` registration.
  - verify: `TestInvariant_agent_model_discovery_Scenario1_SingleDiscoveryTool`

### Scenario 2 — bounded terms find candidates across one or all providers

Query is discovery over safe resolved metadata, not another selector or routing language. Omission of
`provider_id` means every selectable provider; an exact filter remains available after the provider
facet teaches the model a valid ID. The replacement preserves the exact two-field selection
primitive in [ADR 0016](../adr/0016-multi-provider.md#5-providermodel-selection-primitive).

**Acceptance:**
- AC2.1: query validation rejects Unicode controls before normalization; permitted non-control Unicode whitespace is then split with Go `strings.Fields` and terms are normalized with `strings.ToLower`, with no Unicode normalization or full case folding; composed and decomposed forms remain distinct. Repeated permitted whitespace and case differences covered by that exact operation do not change matching. Every normalized term must be a literal substring of at least one similarly lowercased searchable field on the same candidate model row; all terms must match that row (AND semantics), though different terms may match different fields.
  - verify: `TestInvariant_agent_model_discovery_Scenario2_LiteralTermSearch`
- AC2.2: punctuation has no syntax role, and strings resembling regex, glob, quotes, `field:value`, Boolean operators, negation, capability comparisons, endpoints, or routes are matched only as literal terms and never interpreted.
  - verify: `TestInvariant_agent_model_discovery_Scenario2_NoQueryLanguage`
- AC2.3: empty query and queries containing only permitted non-control Unicode whitespace are omitted; invalid UTF-8, any Unicode control character (including tab/newline), excess raw or normalized bytes, too many terms, and oversized terms produce fixed bounded errors that do not echo caller input or inspect any non-public field.
  - verify: `TestInvariant_agent_model_discovery_Scenario2_QueryValidationAndNonDisclosure`
- AC2.4: omitting `provider_id` searches all selectable providers, while a known exact provider narrows the same query; unknown exact providers return an honest empty model result without a provider facet, fallback, probe, or provider inference from a model ID.
  - verify: `TestInvariant_agent_model_discovery_Scenario2_AllProviderAndExactProviderSearch`
- AC2.5: non-empty `provider_id` and `model_id` filters remain byte-exact and accept only valid UTF-8 values of at most 512 bytes with no leading/trailing whitespace or Unicode controls; cursor-restored filters pass the same validator, and neither path trims, lowercases, or otherwise normalizes an exact identifier.
  - verify: `TestInvariant_agent_model_discovery_Scenario2_ExactFilterValidation`

### Scenario 3 — opaque continuation traverses one coherent inventory

Large provider inventories require an actionable continuation rather than `truncated:true` with no
way to reach later handles. Pagination follows the repository's bounded cursor posture without
adding stored state or changing the live inventory owner described in the
[architecture](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).

**Acceptance:**
- AC3.1: when more filtered rows remain after the complete rows that fit both the requested limit and whole-result byte ceiling, the response sets `truncated:true` and returns one bounded opaque `next_cursor`; calling with that cursor alone returns the next non-overlapping page in canonical order, and the final page omits the cursor and sets `truncated:false`.
  - verify: `TestInvariant_agent_model_discovery_Scenario3_CursorTraversal`
- AC3.2: the cursor binds its version, normalized filters/query, effective limit, next offset, and SHA-256 digest of the complete canonically sorted safe scalar projection (`provider_id`, `model_id`, `display_name`, `image`, `reasoning`, and `context_limit`). Republishing a byte-identical canonical projection, including across process restart or from a differently ordered source slice, remains valid; any canonical safe-projection addition, removal, or metadata change returns a fixed bounded tool error instructing the model to restart without a cursor and emits no partial page.
  - verify: `TestInvariant_agent_model_discovery_Scenario3_InventoryBoundCursor`
- AC3.3: a non-empty cursor accompanied by a non-empty exact filter/query or explicit limit, plus a cursor over 4096 encoded bytes or one with malformed, padded/noncanonical base64url, invalid/unknown JSON fields or version, returns a fixed bounded tool error without echoing the cursor or inventory data. Restored exact filters pass AC2.5. Restored terms contain zero to eight non-empty valid-UTF-8 lowercase values with no whitespace or controls, at most 64 bytes each and 256 aggregate bytes, and each must round-trip `strings.Fields` plus `strings.ToLower` unchanged. Restored limit is in `[1,50]`; restored offset is positive and strictly less than the filtered match count for the unchanged snapshot. Empty cursor is omitted for serializer compatibility; supported cursors restore their complete first-page scope and no cursor field bypasses the corresponding first-page validator.
  - verify: `TestInvariant_agent_model_discovery_Scenario3_CursorValidation`
- AC3.4: cursor bytes and any initial unfiltered provider-facet bytes participate in the 32 KiB ceiling; continuation pages do not repeat the provider facet. The first unreturned row is never skipped, identifiers are never truncated, and a response that cannot make forward progress returns a bounded error rather than an empty page with a continuation.
  - verify: `TestInvariant_agent_model_discovery_Scenario3_ByteBoundMakesProgress`

### Scenario 4 — the model learns the workflow while live truth stays in the tool

The model needs an explicit workflow but not a copied inventory in its stable prompt. The existing
model-visible discoverability pattern remains factory-tested, while the live result follows the
same resolved-inventory refresh/fallback semantics documented in the
[model-inventory architecture](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).
The implementation updates the canonical `user-docs/features/choose-models.md` model-inventory guidance.

**Acceptance:**
- AC4.1: the real factory-built stable prompt tells the model to call `DiscoverModels` without `provider_id` when the provider is unknown, that omission searches all selectable providers, that an unfiltered result lists exact selectable provider IDs, and that only a returned exact `(provider_id, model_id)` pair may be passed to an existing surface that explicitly accepts both or returned to the caller for selection; it states that `DiscoverModels` itself cannot switch the session and contains no concrete provider/model ID, count, status, or cursor from the live inventory.
  - verify: `TestInvariant_agent_model_discovery_Scenario4_SystemPromptContainsWorkflowNotInventory`
- AC4.2: the tool specification explains query normalization, all-provider omission, exact filters, limit, cursor continuation, stale-cursor restart, provider facets, output bounds, and the no-probe/no-selection boundary using concise model-actionable descriptions.
  - verify: `TestInvariant_agent_model_discovery_Scenario4_ToolSpecificationContract`
- AC4.3: inventory publication deep-copies every safe model field, every inventory read returns fresh deep copies, and tests mutate both publisher-owned inputs and reader-returned rows (including concurrent mutation under the race detector) without changing the stored snapshot or racing discovery. Each call then builds one local scalar projection and performs canonical sorting, digesting, provider grouping, filtering, counting, and paging only on that copy; no discovery call initiates refresh, performs provider I/O, creates retained cache state, or changes session selection.
  - verify: `TestInvariant_agent_model_discovery_Scenario4_SharedLiveInventory`
- AC4.4: `DiscoverModels.ReadOnly()` remains true and the implementation adds no built-in or configured permission rule, bypass, or special verdict; the existing permission evaluator continues to resolve the call unchanged.
  - verify: `TestInvariant_agent_model_discovery_Scenario4_PermissionPostureUnchanged`
- AC4.5: the implementation replaces or renames the phase-1 `TestAgentModelDiscovery_*` proofs and deletes obsolete expectations that `query` is unknown; only the canonical proof names in this amended plan claim the final contract, and strict acceptance tracing resolves all of them without an `agent-model-discovery-v2` plan or test namespace.
  - verify: `task ac-trace-strict`

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
- The shared inventory must own deep-copied model values at publication and return fresh deep copies to every reader; discovery then makes its own scalar projection. No mutable `*ModelInfo` pointer crosses the inventory boundary or is retained by a call.
- Frequent visible inventory refresh can repeatedly stale a long traversal. Restart is intentional because this tool discovers current targets rather than exporting a historical catalog.
- The complete provider facet is still bounded by the 32 KiB whole-result ceiling on an unfiltered initial call. Deployments whose provider summary alone cannot fit receive the explicit no-progress error; filtered calls and cursor continuation remain usable because they omit the facet, while a separately paged provider inventory is outside this plan.
