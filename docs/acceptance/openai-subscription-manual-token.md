# OpenAI subscription manual token — acceptance plan

**Phase:** capability — ChatGPT subscription inference through the native Codex backend
**Status:** in-progress, 2026-08-05. Manual access token and native provider only; login and refresh are deferred.
**Planned decision record:** ADR 0101 — proposed before the compatibility probe,
accepted only if the honest-client gate passes; extends ADRs 0016, 0017, 0048,
and 0064 without superseding them.
**Accumulator branch:** `openai-oauth-login`

This plan is governed by the provider-neutrality, secret-scrubbing, offline-test,
and documentation-lifecycle rules in [AGENTS.md](../../AGENTS.md), the current
provider architecture in the [architecture guide](../architecture.md) and
[provider chapter](../architecture/providers.md), and the dense registry/model
mechanics in [IMPLEMENTATION-NOTES](../design/IMPLEMENTATION-NOTES.md).

## Capability

An operator can place a current ChatGPT Codex access token in mecatl's existing
read-only `auth.yaml`, select the distinct `openai-codex` provider, list the
models entitled to that ChatGPT account, and complete a streamed tool-using turn
through mecatl's existing agent loop.

API-key OpenAI remains a separate provider and billing identity. Mecatl keeps
its own transcript replay, tools, permissions, hooks, subagents, compaction, and
session storage. This slice adds no browser/device login, refresh token, auth
file writer, keyring, WebSocket transport, remote compaction, or quota UI.

The backend is an undocumented compatibility dependency:
`https://chatgpt.com/backend-api/codex`. The provider is experimental and must
not impersonate the Codex CLI. The implementation proceeds only if a manual
compatibility gate accepts an honest mecatl originator.

External contract evidence is limited to OpenAI's
[agent-loop explanation](https://openai.com/index/unrolling-the-codex-agent-loop/),
[separate-billing guidance](https://help.openai.com/en/articles/8156019-is-api-usage-included-in-chatgpt-subscriptions-even-if-i-have-a-paid-chatgpt-account),
and the official Codex source for the
[provider endpoint](https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs),
[models endpoint](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/endpoint/models.rs),
and [model ordering/default rule](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/openai_models.rs).
These sources document OpenAI's own client, not a stable third-party API promise.

## Decisions fixed by this plan

1. **One successful Responses implementation.**
   `internal/adapter/openai.Provider` remains the only encoder and successful
   SSE translator. `openai-codex` is a registry identity configured over that
   adapter. `internal/adapter/openaicodex` is an adjunct for credentials,
   request policy, error normalization, and the different models wire shape; it
   must not implement `port.LLMProvider` or parse successful Responses SSE.
2. **Manual, immutable credential snapshot.** The process reads
   `access_token`, optional `account_id`, and optional `expires_at` from
   `auth.yaml`. It never writes the file and never stores a refresh token. A
   restart is required after replacement.
3. **Request-time expiry check.** Startup validation is not sufficient for a
   daemon. Every inference and models request checks the immutable snapshot's
   expiry immediately before network I/O.
4. **Separate availability and billing identity.** An API key never enables
   `openai-codex`; a ChatGPT token never enables `openai` or `openrouter`.
5. **Stable default precedence.** Merely adding a subscription token must not
   redirect an existing deployment. Explicit `--default-provider` wins; then
   the existing provider preference is preserved; `openai-codex` is considered
   only after every pre-existing keyed provider and before intent-only
   gateways. It becomes the default automatically only when it is the sole
   usable provider.
6. **Explicit selectors are pinned; empty selectors still float.** A session
   created with `provider_id: openai-codex` persists that opaque selector and
   rehydrates through the same provider. A zero-selector session retains the
   existing contract: it stores an empty provider and follows the deployment's
   current default after restart. This slice does not change snapshot semantics.
7. **Subscription inventory is live-only.** `embeddedModels("openai-codex")`
   stays empty. The OpenAI API catalog may enrich metadata for a model ID already
   returned by the Codex entitlement feed, but it must never become fallback
   subscription inventory.
8. **Default model comes from entitlements.** When `openai-codex` is the
   resolved default and the operator did not set a model, startup performs one
   bounded models request and selects the first picker-visible model, preserving
   server order. This follows the official Codex model code's rule that the
   highest-priority available picker model is the default. A failed bootstrap
   is actionable and does not invent an API default. An explicit model bypasses
   only default discovery, not credential validation.
9. **`mecak8s` remains explicitly unsupported.** The shared credential resolver
   recognizes the file entry, but the `mecak8s` root deliberately refuses or
   ignores it with an actionable message. It must not gain subscription support
   accidentally through shared composition.
10. **Plaintext-file risk is stated honestly.** Mecatl will not inject, log,
    diagnose, persist, or project the token. However, main-session Bash runs as
    the same OS user and can read a conventional plaintext `auth.yaml` through
    `HOME`/`XDG_CONFIG_HOME`. Mode `0600` does not prevent same-UID access. Strong
    isolation requires the deferred keyring/broker or an OS sandbox.

## Verified repository seams

- `internal/adapter/authfile/authfile.go` (`ProviderEntry`, `Load`) is a
  warning-only, value-free, strictly decoded credential leaf. A generic OAuth
  field also needs provider-aware semantic validation or it would be accepted
  under unrelated providers.
- `internal/cliconfig/cliconfig.go` (`ProviderFlags.Apply`) is the common
  environment/file precedence path for all four real command roots.
  `cmd/mecatui/config.go` currently performs an environment-only availability
  check before `Apply`, so even existing file-only API keys can be rejected.
- `internal/adapter/openai/openai.go` (`WithBaseURL`, `WithHTTPClient`,
  `WithRequestOption`) already exposes the construction seams required for a
  different endpoint, exact middleware, and SDK retry control.
- `internal/app/registry.go` (`newOpenAICompatEntry`) captures extra
  `openai.Option` values in its single construction closure. Initial creation,
  default healing, capability/effort changes, and per-session remints therefore
  retain the same Codex policy without another resilience implementation.
- OpenAI's Go SDK reads ambient `OPENAI_*` defaults and has its own retry layer.
  The Codex request middleware must delete irrelevant API organization/project/
  custom headers, overwrite authorization/account/originator headers, pin the
  base URL, and set SDK retries to zero so mecatl's outer resilience policy is
  the only retry owner.
- `internal/adapter/llmresilience/llmresilience.go` already retries only
  classified pre-commit failures and never replays after a committing chunk.
  Its default classifier already treats 401/403 as permanent and 429/5xx as
  retryable; the manual-token slice needs error normalization, not a second
  resilience stack.
- `internal/app/modellister.go` (`modelLister`, `resolveProviderModels`,
  `publishSnapshot`) and `internal/app/livemeta.go` already provide the live
  fetch, last-known-good, picker, capability, and context-window pipeline.
- `internal/app/modellister.go` (`embeddedModels`) is both inventory and fallback.
  A wholesale provider alias would therefore fabricate subscription
  entitlements. Metadata lookup must be separated from inventory lookup.
- `internal/app/registry.go` (`healDefaultModel`) currently heals only
  `intentDriven` providers. Codex needs a distinct live-default/bootstrap path;
  it must not masquerade as a ToolHive intent provider.
- `internal/adapter/server/service.go` (`setSessionLabels`) stores the requested
  selector, not the resolved default. Explicit selectors are stable; empty
  selectors intentionally float.
- The Responses replay-ID logic in `internal/adapter/server/service.go` currently
  recognizes only `openai` and `openrouter`. `openai-codex` must join the same
  classification for cross-provider carryover.

## Mandatory implementation and review protocol

When the operator asks to implement this plan, the root agent acts as the
orchestrator and drives every step below to completion in order. Routine choices
that are already fixed by this plan do not require another confirmation. A
genuine authority or external-state blocker—most notably the live compatibility
gate requiring a current operator-supplied token—must still be surfaced rather
than guessed around.

Every implementation step uses this workflow:

1. **Establish the step base.** The orchestrator records the current commit and
   worktree status, preserves unrelated user changes, and defines the exact
   files, deliverable, and acceptance criteria assigned for the step. The diff
   reviewed later is the diff from this recorded base.
2. **One coding agent owns the step.** Spawn one dedicated implementation agent
   with the complete step contract, relevant repository instructions, and the
   requirement to implement code, tests, and step-local documentation. The
   agent follows the repository's `tdd-worker` and `test-writer` disciplines:
   named acceptance proof first, observed red, the smallest implementation to
   green, then refactor; tests use the lowest correct layer, reference fakes or
   recorded fixtures, and no live network. The coding agent edits the shared
   worktree and runs focused tests, but does not commit, push, or begin a later
   step.
3. **Two independent agents review before commit.** After the coding agent
   finishes, spawn both reviewers in parallel against the same step diff. They
   do not edit or commit:

   - **Specification/architecture reviewer:** checks every acceptance criterion,
     provider neutrality, layering, documentation lifecycle, session semantics,
     and scope boundaries.
   - **Correctness/security/reuse reviewer:** checks behavior, error and retry
     semantics, secret handling, concurrency/cancellation, test strength, code
     duplication, and reuse of the existing Responses/model/composition seams.
4. **Blocking findings return to the coding agent.** The orchestrator combines
   and deduplicates both reports, then sends every blocking finding back to the
   same coding agent. After fixes and focused tests, both review agents re-review
   every materially changed area. Repeat until both reports have no blocking
   findings. Unresolved disagreements are decided against this plan and
   `AGENTS.md`, with the conservative security/provider-neutrality interpretation
   winning.
5. **The orchestrator verifies the step.** Run the step's named acceptance
   proofs, relevant existing regression suites, formatting/lint appropriate to
   the touched code, and `git diff --check`. Confirm the diff contains only the
   completed step plus preserved pre-existing changes. A generated-file change
   produced by a gate is part of the candidate and must be reviewed before the
   commit. Do not defer a known failure to a later step unless this plan
   explicitly marks that dependency.
6. **Create one scoped local commit.** Only after both reviews and verification
   pass, the orchestrator stages the step-owned files by explicit path (never
   `git add -A`) and creates a local commit whose message names the delivered
   capability and ends with the repository-required `Co-Authored-By` trailer.
   Do not push, open a PR, squash, amend an earlier user commit, or include
   unrelated work. Record the commit ID and acceptance evidence in the
   implementation log.
7. **Advance automatically.** Start the next step from that commit and repeat the
   same coder → two reviewers → fixes → verification → local commit cycle. Stop
   only when all steps are committed and the aggregate Definition of done is
   satisfied, or when a genuine blocker requires operator input.

Concurrency is deliberately bounded to the four available roles: root
orchestrator, one coding agent, and two review agents. Coding for different
steps never overlaps, because each step's reviewed local commit is the base for
the next. Review agents may run concurrently with each other only after the
coding agent has yielded a stable candidate diff.

The implementation log is maintained at the end of this document while work is
active. For each step it records: base commit, coding agent, reviewer agents,
blocking findings and dispositions, verification commands/results, and final
local commit. The plan moves from `draft` to `in-progress` when Step 1 begins and
to `landed` only after Step 9 and the aggregate gates pass.

This sequential review-before-commit workflow is an explicit operator-directed
variance from ADR 0072's default isolated-worker/accumulator/one-PR mechanics for
this run. It does not amend repository policy: the ADR 0072 disciplines that do
not conflict with the instruction still apply—scenario-first acceptance,
red-green TDD, exact `verify:` proofs, aggregate gates, `ac-trace --strict`, and
the final panel review. The run stops with reviewed local commits; pushing or
opening the usual single PR requires a separate operator request.

## In scope — nine scenarios and implementation steps, in order

### Scenario 1 / Step 1 — Prove the private compatibility contract and record the decision

This scenario follows [ADR 0002's documentation lifecycle](../adr/0002-documentation-lifecycle.md):
settle and record the durable decision before product behavior claims it.

Run a one-shot, operator-invoked compatibility probe with a current manual
access token and an honest mecatl originator. Capture only sanitized wire shapes:
the models envelope, one text turn, one function-tool round trip, replay IDs and
phase, usage, and representative auth/quota errors. No live call enters the
automated suite.

Write ADR 0101 from `docs/adr/template.md` as Proposed before the probe. It
records the experimental private
backend, manual-token-only scope, no-impersonation rule, default precedence,
live-only inventory, explicit-selector persistence semantics, plaintext-file
risk, and `mecak8s` exclusion. If and only if the compatibility gate passes,
change it to Accepted before any product-code step begins and index it under
Providers & APIs in `docs/adr/README.md`. It supersedes no existing ADR.

**Deliverable:** an accepted ADR 0101, its index entry, a structural
`TestADR_0101_CompatibilityContract` pin, and sanitized contract notes/fixtures
that are sufficient to build offline tests. The acceptance plan and index enter
the first scoped commit. No raw token, account identifier, response ID, request
ID, or user content is committed.

**Acceptance:**

- AC1.1: the probe reaches `/codex/models` and `/codex/responses` with an honest
  mecatl originator and without Codex CLI impersonation.
  - verify: demonstration — sanitized compatibility record attached to ADR 0101.
- AC1.2: the captured tool round trip establishes which existing Responses
  fields/events are required and whether any Codex-specific actionable event
  needs shared translator support.
  - verify: inspection — sanitized fixture inventory in the ADR.
- AC1.3: if the backend rejects an unknown client/originator, implementation
  stops at this gate rather than adding spoofed headers.
  - verify: inspection — explicit stop condition in ADR 0101.
- AC1.4: the decision record passes the documentation lifecycle gate.
  - verify: `TestADR_0101_CompatibilityContract` and `go test ./docs/lint`.

### Scenario 2 / Step 2 — Extend `auth.yaml` without weakening its warning and secrecy contract

This scenario is governed by [`AGENTS.md`'s secret-scrubbing and adapter-layer
rules](../../AGENTS.md); credentials remain an outer-adapter concern and never
cross into the engine.

Add nested OAuth DTOs to `internal/adapter/authfile/authfile.go`:

```yaml
providers:
  openai-codex:
    oauth:
      access_token: eyJ...
      account_id: account-...              # optional when present in JWT
      expires_at: 2026-08-04T18:30:00Z     # optional when present in JWT
```

Keep raw fields as strings at the file boundary. Add a copy-returning accessor
and provider-aware semantic validation: OAuth is valid only for
`openai-codex`; `api_key` remains valid for the existing API-key providers.
Mis-scoped or malformed entries are ignored with the existing value-free
warning posture, without invalidating unrelated valid entries.

**Deliverable:** the extended read-only schema, accessor, semantic validator,
and table-driven `internal/adapter/authfile` tests.

**Acceptance:**

- AC2.1: every existing API-key-only fixture produces the same keys and warning
  behavior as before.
  - verify: `TestLoadFillsAPIKey`, `TestAPIKeyCompatibility`.
- AC2.2: the exact `openai-codex.oauth` shape parses, while unknown fields,
  malformed nesting, empty access token, and OAuth under another provider
  produce value-free warnings and no Codex credential.
  - verify: `TestOpenAICodexOAuthSchema`.
- AC2.3: no YAML decoder error or warning contains a secret sentinel or unknown
  provider name supplied as file content.
  - verify: `TestLoadMalformedYAMLNeverLeaksFileContent`,
    `TestLoadUnknownProviderNameNeverLeaksSecretShapedName`,
    `TestOAuthWarningsAreValueFree`.
- AC2.4: no file write, refresh-token field, or import of `engine/...` is added.
  - verify: inspection — package dependency and schema review, plus `task lint`.

### Scenario 3 / Step 3 — Build the immutable Codex credential and exact request policy adjunct

This scenario extends, but does not duplicate, [ADR 0017's stateless Responses
adapter](../adr/0017-openai-responses-api.md).

Create `internal/adapter/openaicodex` as a stdlib/OpenAI-SDK adapter adjunct,
not an LLM implementation. Its credential parser decodes the JWT payload only
to obtain routing metadata; it does not claim signature verification. It
resolves `chatgpt_account_id`, `exp`, and the FedRAMP claim against explicit
file fields, fails on account mismatch, and uses the earlier explicit/JWT
expiry. An injected clock makes validation deterministic.

The same immutable credential supplies:

- `Validate(now)` for startup and immediate pre-request expiry checks;
- exact inference and model-list headers;
- a request middleware that removes ambient OpenAI API organization/project/
  custom headers, overwrites bearer/account/FedRAMP/originator/user-agent
  values, and converts 401/403 into a bounded, secret-free manual-token error;
- OpenAI SDK `option.WithMaxRetries(0)`, leaving retry ownership to
  `llmresilience`.

There is no token refresh or request retry inside this adjunct.

**Deliverable:** `Credential`, JWT metadata parsing, request policy/middleware,
status-coded errors, and offline tests under `internal/adapter/openaicodex`.

**Acceptance:**

- AC3.1: malformed JWT payloads, missing account IDs, mismatched account IDs,
  invalid expiries, and already-expired tokens fail without echoing any token
  fragment.
  - verify: `TestCredentialValidation`.
- AC3.2: when both expiry sources exist the earlier value wins; a credential
  valid at startup but expired later fails the request-time preflight before
  the transport is called.
  - verify: `TestCredentialUsesEarlierExpiry`, `TestRequestPolicyRejectsLateExpiry`.
- AC3.3: a hostile process environment containing `OPENAI_API_KEY`, organization,
  project, base-URL, and custom-header defaults cannot change the Codex endpoint
  or leak those values onto the captured request.
  - verify: `TestRequestPolicyOverridesAmbientOpenAIDefaults`.
- AC3.4: 401/403 is permanent and actionable; 429/5xx retains status semantics
  for the shared resilience classifier; the SDK performs zero hidden retries.
  - verify: `TestRequestPolicyStatusMapping`, `TestSDKRetriesDisabled`.
- AC3.5: the package does not implement `port.LLMProvider`, construct Responses
  input items, or parse successful Responses events.
  - verify: `TestADR_0101_OpenAICodexIsAdjunctOnly`.

### Scenario 4 / Step 4 — Resolve credentials once and wire command scope explicitly

This scenario preserves [ADR 0016's composition-owned provider availability and
selection boundary](../adr/0016-multi-provider.md).

Refactor `internal/cliconfig/cliconfig.go` so resolution and projection are
separate but share one immutable result:

- `Resolve` reads environment keys and `auth.yaml` once;
- `ApplyResolved` writes that result to `app.Config`;
- the existing `Apply` can remain as a compatibility wrapper;
- the result carries API keys and a distinct validated Codex credential;
- `Any` includes file-backed Codex presence without treating it as an API key.

Make embedded mecatui cache the resolved result before `config.validate` and
reuse it later in `embeddedConfig`, fixing the existing file-only API-key bug as
well as Codex. Wire the same credential into `mecated` and `mecatequi`.
`mecak8s` passes an explicit opt-out and returns an unsupported guidance error
instead of silently registering the provider.

**Deliverable:** one shared credential-resolution value used by the three
in-scope roots, a deliberate `mecak8s` exclusion, and updated flag/no-provider
help.

**Acceptance:**

- AC4.1: embedded mecatui accepts an existing API key or Codex token supplied
  only by the conventional or explicit auth file, reads the file once, and
  emits at most one warning.
  - verify: `TestConfigValidateAcceptsAuthFileCredential`, `TestAuthFileReadOnce`.
- AC4.2: environment-over-file precedence remains byte-identical for API keys;
  Codex has no environment fallback in this slice.
  - verify: existing `TestApplyEnvWinsOverAuthFile` plus
    `TestCodexCredentialHasNoEnvAlias`.
- AC4.3: API OpenAI and Codex credential presence remain independent in the
  resolved value and in `app.Config`.
  - verify: `TestResolvedCredentialsKeepBillingIdentitiesSeparate`.
- AC4.4: `mecated`, embedded `mecatui`, and `mecatequi` project the same
  credential snapshot; `mecak8s` explicitly rejects the unsupported provider.
  - verify: `TestOpenAICodexCommandRootsShareCredentialSnapshot`,
    `TestMecak8sRejectsOpenAICodexCredential`.

### Scenario 5 / Step 5 — Register `openai-codex` through the one Responses construction path

This scenario uses [ADR 0016's provider registry](../adr/0016-multi-provider.md)
and the existing construction/remint seam described by the
[provider architecture](../architecture/providers.md).

Add the wire-stable provider ID and fixed base URL in
`internal/app/registry.go`, and add the credential field to
`internal/app/build.go` (`Config`). Register only a validated credential.

Call `newOpenAICompatEntry` with the access token, fixed Codex base URL, and
captured Codex request options. Do not add a second provider constructor,
request builder, successful SSE translator, resilience wrapper, or
`port.LLMRequest` field. Include `openai-codex` in the existing OpenAI-family
reasoning-effort clamp in `internal/app/reasoning_effort.go`.

Make default preference explicit: `openai`, then the existing keyed-provider
order, then `openai-codex`, then intent-driven gateways. Do not add a credential
environment variable or route it through `providerKey`/`providerEnvVars`.

**Deliverable:** a selectable registry entry over the existing OpenAI Responses
adapter, with fixed provider precedence and remint-safe options.

**Acceptance:**

- AC5.1: token-only configuration registers only `openai-codex`; API-key-only
  configuration registers no Codex provider; both credentials register two
  independently selectable entries.
  - verify: `TestRegistryOpenAICodexAvailability`.
- AC5.2: adding Codex to OpenRouter-only, Anthropic-only, or OpenAI deployments
  leaves the previous default unchanged; Codex becomes default when it is the
  sole provider or the operator names it explicitly.
  - verify: `TestADR_0101_OpenAICodexDefaultPrecedence`.
- AC5.3: the initial provider, build-time default/capability remint, and
  per-session effort/capability remint all hit `/backend-api/codex/responses`
  with identical request policy and the same immutable credential snapshot.
  - verify: `TestOpenAICodexOptionsSurviveEveryRemint`.
- AC5.4: OpenAI and Codex produce byte-equivalent Responses JSON for the same
  neutral request and construction knobs; only URL and explicitly configured
  headers differ.
  - verify: `TestOpenAICodexRequestBodyParity`.
- AC5.5: existing OpenAI, OpenRouter, and ToolHive construction tests remain
  unchanged when Codex configuration is absent.
  - verify: `TestOpenAICodexAbsentPreservesExistingProviders`.

### Scenario 6 / Step 6 — Add entitlement-authoritative live models and a bounded default bootstrap

This scenario follows [ADR 0064's live-inventory and bounded-probe
precedents](../adr/0064-toolhive-llm-gateway-provider.md) without classifying the
credential-backed Codex provider as an intent-only gateway.

Create the Codex models decoder/lister in `internal/adapter/openaicodex`. It
GETs `/backend-api/codex/models?client_version=<version>` using the same
credential/request policy as inference, preserves server order, and maps only
picker-visible entries. The official Codex source treats ChatGPT mode separately
from API support and selects the first picker-visible/highest-priority available
model, so do not filter subscription models by an API-only support flag.

Add one shared build-version source used by every command root; do not borrow
mecatui's private `version` variable. Development builds send an honest `dev`
value.

Extract a narrow stdlib-only `internal/adapter/modelhttp` helper only for the
behavior materially shared by all raw listers: context-aware GET execution,
default timeout, bounded body read, and status-coded errors. Migrate
`openaicompat`, `openrouter`, and Codex to it in the same change. Protocol JSON,
headers, redirect policy, and control/bidi label sanitization remain owned by
their respective adapters unless they are genuinely identical.

Plug the Codex projection into the existing composition-local `modelLister`,
`liveModelSnapshot`, `liveOutcomeStore`, and `liveMetaStore` path. Keep
subscription inventory empty before live success. Add metadata-only lookup that
may consult provider `openai` for an already-entitled or explicitly selected
matching ID; do not use it from `embeddedModels`, `providerEnvVars`, or any
inventory gate.

When Codex is the resolved default and no model was configured, run one bounded
bootstrap list and choose its first mapped model. Unauthorized/empty results are
fatal and actionable; an unreachable result is fatal unless the operator set an
explicit model. Background refresh and last-known-good behavior then use the
existing shared pipeline.

**Deliverable:** Codex model listing, shared raw-model HTTP safety, live-only
subscription inventory, metadata-only enrichment, and deterministic default
bootstrap.

**Acceptance:**

- AC6.1: the lister sends the exact URL, query version, bearer/account/FedRAMP/
  originator headers, respects cancellation and timeouts, caps response size,
  and rejects redirects according to the chosen Codex policy.
  - verify: `TestCodexModelsRequest`, `TestCodexModelsBoundsAndCancellation`.
- AC6.2: only picker-visible entries returned for the account appear, in server
  order before final UI sorting; malformed or empty IDs are skipped safely.
  - verify: `TestCodexModelsEntitlementProjection`.
- AC6.3: `embeddedModels("openai-codex")` remains empty before and after a live
  failure. A 401/403/empty response never exposes the OpenAI API inventory.
  - verify: `TestADR_0101_OpenAICodexNeverFallsBackToAPIInventory`.
- AC6.4: a matching entitled slug may borrow missing OpenAI catalog metadata;
  an unknown entitled slug remains selectable with adapter capabilities and the
  existing conservative context-window floor.
  - verify: `TestOpenAICodexMetadataEnrichmentIsNotInventory`.
- AC6.5: sole-provider startup with no model selects the first entitled picker
  model through the bounded bootstrap; an explicit model skips default discovery;
  coexistence never changes another provider's default.
  - verify: `TestOpenAICodexDefaultBootstrap`.
- AC6.6: picker rows, capability echo, reasoning support, and the resolve-at-use
  context window consume the same live `modelEntry` facts.
  - verify: `TestOpenAICodexLiveMetadataConverges`.

### Scenario 7 / Step 7 — Close Responses replay, error, and carryover compatibility gaps

This scenario preserves [ADR 0017's single stateless Responses replay and SSE
translation path](../adr/0017-openai-responses-api.md).

Replay the sanitized Codex fixtures through the existing
`internal/adapter/openai` translator. Add a shared event case only if the
compatibility gate proves that a known event changes mecatl's neutral stream
semantics. Unknown metadata events retain the current forward-compatible ignore
behavior. Codex-specific non-2xx messages are normalized by the request policy;
successful SSE is never decoded twice.

Extend the Responses replay-ID classifier in
`internal/adapter/server/service.go` so `openai-codex` receives the same stable
synthetic ToolCall item IDs during cross-provider carryover as `openai` and
`openrouter`. Prefer one `usesResponsesReplayIDs(providerID)` helper rather than
another inline provider list.

**Deliverable:** fixture-proven shared translator additions, generic Responses
replay-ID classification, and no Codex stream parser.

**Acceptance:**

- AC7.1: recorded text, tool call/result, encrypted reasoning, provider phase,
  usage, done, and cancellation events yield the same neutral chunks through the
  existing translator.
  - verify: `TestOpenAICodexResponsesFixtures`.
- AC7.2: a fixture-proven known unsupported actionable event fails loudly before
  commit, while harmless unknown metadata remains ignored.
  - verify: `TestCodexActionableEventPolicy`,
    `TestTranslateUnknownMetadataEventIgnored`.
- AC7.3: 401/403 is not retried; 429/5xx may retry only before a committing
  chunk; no failure after commit replays the turn; SDK retries do not multiply
  the configured outer attempts.
  - verify: `TestOpenAICodexRetryCounts`, `TestPostCommitErrorNotRetried`.
- AC7.4: same-provider Codex carryover preserves opaque reasoning/phase/item IDs;
  API OpenAI ↔ Codex is cross-provider and strips private blobs; carryover into
  Codex synthesizes collision-safe Responses item IDs.
  - verify: `TestOpenAICodexCarryoverReplayIDs`.
- AC7.5: no file under `internal/adapter/openaicodex` builds Responses input
  items or translates successful SSE.
  - verify: `TestADR_0101_OpenAICodexIsAdjunctOnly`.

### Scenario 8 / Step 8 — Prove full composition, persistence, and operator surfaces

This scenario exercises the per-session provider semantics fixed by
[ADR 0016](../adr/0016-multi-provider.md) through the real composition boundary.

Exercise `openai-codex` through the real provider registry and
`sessionEngineFactory`, using injected HTTP transports or provider constructors
so every automated test stays offline. Cover explicit session selection,
reasoning/capability remints, subagent inheritance, no-FS catalogs, stored
selector rehydration, and API OpenAI coexistence.

Update mecatui empty-state/help/goldens and mecatequi's noninteractive error
surface. Keep the persistence claim narrow: explicit Codex selectors are pinned;
zero-selector sessions continue to follow the server default after restart.

Add secret-sentinel coverage across auth warnings, startup diagnostics, request
errors, session/event snapshots, hooks, prompts, and command-runner environment.
Document rather than conceal the remaining same-UID Bash read risk.

**Deliverable:** offline composition scenarios for the three supported command
roots, persistence/restart proof, TUI goldens, and secret-leak regression tests.

**Acceptance:**

- AC8.1: an explicit `(openai-codex, model)` session completes a streamed
  tool-using turn while API OpenAI remains independently selectable.
  - verify: `TestOpenAICodexCompositionScenario`.
- AC8.2: effort/capability changes remint exactly once through the shared
  construction closure; subagents inherit the selected Codex provider; shared
  and per-session catalogs retain exact tool-name parity.
  - verify: `TestOpenAICodexRemintAndInheritance`,
    `TestPerSessionCatalogMatchesSharedCatalog`.
- AC8.3: save/restart/rehydrate of an explicit selector routes through
  `openai-codex`; a separate test pins the unchanged floating semantics of an
  empty selector.
  - verify: `TestOpenAICodexExplicitSelectorRehydrates`,
    `TestZeroSelectorStillFollowsDeploymentDefault`.
- AC8.4: embedded mecatui works with only the file credential, lists entitled
  models, and renders actionable expired/unauthorized guidance; mecatequi reports
  the same failure noninteractively.
  - verify: `TestOpenAICodexCommandRootSurfaces` and `task test:golden`.
- AC8.5: the token is absent from logs, errors, diagnostics, events, sessions,
  prompts, hooks, and child command environments. Documentation explicitly says
  same-UID Bash can still read the plaintext file.
  - verify: `TestADR_0101_OpenAICodexSecretSentinels`,
    `TestMainCommandRunnerScrubsSecrets`, `TestSandboxedCommandRunnerScrubsSecrets`.
- AC8.6: `engine/port.LLMRequest`, engine API snapshots, proto contracts, and
  session snapshot schemas require no change.
  - verify: `task api:check` and inspection.

### Scenario 9 / Step 9 — Land living documentation and run aggregate verification

This scenario closes [ADR 0002's living-truth and status-tracker
obligations](../adr/0002-documentation-lifecycle.md) and the capability workflow
defined by [ADR 0072](../adr/0072-acceptance-plan-spine.md).

After behavior ships, update the living truth in `docs/architecture.md` and
`docs/architecture/providers.md`, operator setup/troubleshooting in
`docs/usage.md`, `docs/usage/mecated.md`, `docs/usage/openai-compatible.md`,
`docs/usage/configuration.md`, `docs/usage/troubleshooting.md`, and `docs/tui.md`,
dense mechanics in
`docs/design/IMPLEMENTATION-NOTES.md`, and shipped/deferred status only in
`docs/design/PRODUCTION-READINESS.md`. Link this plan and ADR from the relevant
indexes. Update the existing matching `user-docs/what-you-get/` or
`user-docs/deployment/` pages—specifically `deployment/mecated.md`,
`deployment/mecatui.md`, `deployment/mecatequi.md`, `deployment/mecak8s.md`
(the exclusion), and `what-you-get/mecatui.md`—with short public setup notes and
links to the full operator reference. Do not create a new page unless no
existing page owns the surface.

Operator documentation must show the exact `auth.yaml` schema, explicit
provider selection, expiry/restart workflow, experimental backend warning,
separate billing identities, no-refresh limitation, default precedence,
`mecak8s` exclusion, and plaintext-file/Bash threat boundary.

**Deliverable:** complete living/operator documentation and a green aggregate
gate.

**Acceptance:**

- AC9.1: docs never describe ChatGPT subscription as public API credit and never
  imply the private backend is a supported third-party contract.
  - verify: inspection — documentation review against ADR 0101.
- AC9.2: setup and troubleshooting distinguish malformed, expired,
  unauthorized, missing-entitlement, empty-model, quota, and transient-service
  failures without exposing credentials.
  - verify: inspection — documentation review against the named error-surface
    tests from Scenarios 3, 6, and 8.
- AC9.3: every automated test is offline; the only live operation is the explicit
  Step 1 compatibility probe.
  - verify: inspection — test transport and fixture review.
- AC9.4: formatting, TUI goldens, lint, both module suites, standalone engine
  hygiene, internal docs, public-site docs, acceptance traceability, API
  compatibility, offline demo, and all binaries pass.
  - verify: `task test:golden`, `task ci`, `task docs`, `task site:build`,
    `task api:check`, `task ac-trace-strict`, `go run ./cmd/mecademo`, and
    `git diff --check`.

Before the Step 9 commit, set this plan to `landed` and run the aggregate gates
above. Then run the repository's final panel review over the entire feature diff
from the pre-Step-1 base, including its default-on duplication and library-reuse
checks. Panel ship-blockers return to the Step 9 coding agent; after repair, the
two Step 9 reviewers re-review all material changes and the aggregate panel runs
again. Only a zero-ship-blocker result permits the ninth local commit. If an
aggregate gate creates or rewrites a tracked file, that diff is reviewed too.

## Out of scope

| Item | Deferred path |
| --- | --- |
| Browser PKCE and device-code login | a later auth lifecycle ADR and command surface |
| Refresh tokens, rotation, locking, and automatic 401 recovery | a credential manager, not this immutable snapshot |
| Atomic auth-file writes, logout, and Codex auth import | the later auth command/write path |
| Keyring or privilege-separated broker | required to close the same-UID Bash file-read risk |
| WebSocket transport and Responses Lite | later transport compatibility work |
| Codex remote compaction | later compaction strategy decision |
| Quota dashboard/account management | later operator UI |
| `mecak8s` credential delivery | a separate external-secret design |
| Persisting resolved identity for zero-selector sessions | a separate session-semantics change |

## Definition of done

The capability is landed only when every acceptance criterion above is met,
the explicit-selector restart proof passes, subscription inventory is demonstrably
live-only, the existing Responses request/SSE/resilience paths remain
single-sourced, and the full offline verification suite is green.

It additionally requires nine reviewed local implementation commits (one per
step), with each commit produced only after its dedicated coding agent completed
the work, both independent review agents reported no blocking findings, and the
orchestrator recorded the step's acceptance evidence. The ninth commit also
requires `task ac-trace-strict`, the aggregate panel review with zero
ship-blockers, and the complete Step 9 gate list. No push or pull request is part
of this operator-directed run unless the operator requests it separately.

Conditional repository tripwires remain in force: an intentional exported
`engine/` API change requires `task api:update`, committed `engine/api/*.txt`,
and an `engine/CHANGELOG.md` entry; a proto change starts in `contracts/proto/`
and requires `task generate`; dependency changes require the repository's full
`task tidy` workflow. This plan expects none of those surface changes, so an
unexpected diff is a design-review finding, not something to accept silently.

## Implementation log

```text
Step 1 — Private compatibility contract and decision
Base: d9642d51fdbe177788a2da402aa4280880fb97bf
Coder: /root/step1_probe_coder (replacement for the interrupted first coder)
Reviewers: /root/step1_final_spec_review (specification/architecture) and
  /root/step1_security_review (correctness/security/reuse)
Findings: redirect refusal; fixed-vocabulary contract and strict mutation tests;
  sanitized replay fixtures; total body caps; status-driven error evidence; exact
  function-call validation; inventory last-known-good semantics; plan lifecycle;
  discoverability by the normal Go suite; response-body closure; and lint complexity.
  All blocking findings were repaired by the coder. Both final reviewers reported
  zero blockers, including for the generated llms.txt diff.
Verification: the honest live compatibility probe passed; `TMPDIR=/private/tmp task
  test`, fresh-cache `task lint`, `task api:check`, focused probe race tests,
  `go test ./docs/lint`, the acceptance-plan checker, strict matlatl link checking,
  formatting, and `git diff --check` passed. `task docs` could not authenticate to
  the private matlatl module; the locally installed pinned matlatl commands completed
  the equivalent generation and strict check successfully.
Commit: bb43e6634c99253abd99941db62f548096f1d5ab

Step 2 — Read-only OAuth auth-file schema
Base: bb43e6634c99253abd99941db62f548096f1d5ab
Coder: /root/step2_auth_coder
Reviewers: /root/step2_spec_review (specification/architecture) and
  /root/step2_security_review (correctness/security/reuse)
Findings: initial review found OAuth pointer state exposed through `File.Providers`,
  missing single-document enforcement, and YAML scalar coercion/alias/merge gaps.
  The coder moved stored OAuth state to unexported values, added an EOF check, and
  replaced coercive decoding with exact node-schema validation plus regressions.
  Security re-review then found custom-tagged mappings and `providers: null` crossed
  the typed top-level decode; the coder switched the whole root/providers/provider/
  OAuth chain to canonical node validation. Root lint then found one shadowed
  built-in test variable, which the coder renamed. Both final reviewers reported
  zero blockers after re-review.
Verification: coder observed the required OAuth tests fail before implementation;
  all six named acceptance proofs, the full authfile race suite, focused `go vet`,
  gofmt, the acceptance-plan checker, fresh-cache `task lint`,
  `TMPDIR=/private/tmp task test`, and `git diff --check` pass.
Commit: pending this scoped Step 2 commit; its hash will be recorded by Step 3.
```
