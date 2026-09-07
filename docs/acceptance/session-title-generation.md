# Session title generation and token usage — acceptance plan

**Phase:** mecatui session-title UX and opt-in title-model accounting  
**Status:** in-progress, 2026-08-30. Reconciled with ADRs 0308 and 0307.
**Issue:** [stacklok/mecatl#621](https://github.com/stacklok/mecatl/issues/621).  
**ADR:** [ADR 0308](../adr/0308-session-title-generation-and-auxiliary-usage.md) — server-owned asynchronous title lifecycle and opt-in title slot; [ADR 0307](../adr/0307-canonical-durable-token-accounting.md) — canonical durable token usage and run-scoped budget baseline.
**Accumulator branch:** `acc/session-title-generation` (off `main`).

The smallest set of work that lets a mecatui operator set an active session title directly and,
when an explicit title model slot is configured, receives a best-effort server-owned
model-generated title after the first real prompt establishes a topic. Generated titles are
single-line normalized and capped at 80 runes total, including a truncation ellipsis. Every title,
including existing first-prompt and operator titles, canonicalizes all whitespace to a single space
so it remains one line. Automatic generation is not an agent turn, cannot delay or alter chat, and
uses only the `session_title` canonical token-usage kind. That usage is attributed by
selected model and never affects the main run's budget or result usage.

## Design boundaries

- `/title <text>` is a mecatui-only client command over the existing `RenameSession` operation;
  it is not an engine tool, prompt command, or synthetic conversation message.
- There is no `GenerateSessionTitle` or generic client model-invocation RPC. The Service owns
automatic job admission and every client benefits from it.
- At creation, composition resolves whether `models.slots.title` can run on the session's fixed
  provider. The aggregate durably records `pending` or `disabled` intent; it never persists model
  configuration and every attempt revalidates current policy.
- The loop captures only the first three genuine, non-empty principal text prompts at original
  prompt ingress. Each source is capped at 2,000 runes and the provider input at 6,000 runes total;
  the generator is capped at 128 output tokens. It never infers title input by scanning user-role
  conversation history.
- A bounded server-owned coordinator admits, deduplicates, and drives jobs. It has two concurrent
  workers and a non-blocking 64-item process-wide queue for outstanding title requests across
  active sessions. A full queue drops that submission without changing lifecycle, allowing a later
  eligible prompt to submit. A job claims durable state under the session lock/lease, snapshots its
  input, releases exclusion while streaming, then reacquires, reloads, revalidates, and atomically
  commits. A timeout/provider failure gets one retry after a fixed five-second backoff.
- The host-private generator receives exactly one provider-scoped `port.LLMProvider`, resolved
  model, and bounded source prompts. It has no registry, credentials, session store, or mutation
  authority. It makes one zero-tool direct stream call with a 30-second timeout and returns strict
  `title` or `defer`.
- Every persisted title/title-generation change emits a `session.title` event, appended to the
  EventLog when enabled and published through gRPC `StreamSessionLive`. That live push is
  best-effort. Per-run HTTP SSE has no out-of-band title push; HTTP clients discover title changes
  from the authoritative session snapshot and durable event stream.
- The engine owns title provenance, sources/lifecycle, and the event value. ADR 0307 owns
  canonical token usage: title usage never changes the deprecated `Session.Usage` main mirror,
  run budgets, ordinary result usage, or conversation history.

These cuts follow [ADR 0308](../adr/0308-session-title-generation-and-auxiliary-usage.md),
[ADR 0307](../adr/0307-canonical-durable-token-accounting.md),
[ADR 0030](../adr/0030-model-selection-heuristics.md),
[ADR 0016](../adr/0016-multi-provider.md),
[ADR 0020](../adr/0020-diagnostics.md), and the aggregate, provider-neutrality, durable-event, and
resource-inventory invariants in [`AGENTS.md`](../../AGENTS.md).

## In scope — 5 scenarios, in implementation order

### Scenario 1 — An active mecatui session has a direct title command

An operator enters `/title <text>`. Mecatui optimistically renders the requested label, calls the
existing `RenameSession` through its client/session-management transport, and adopts the returned
session as authoritative. A rejected request restores or refetches the stored label. `/title`
without text adds a local, non-persisted scrollback notice containing only the title and whether it
was generated or explicitly set; restarting or reloading may lose that notice.

**Acceptance:**
- AC1.1: `/title <non-blank text>` uses existing `RenameSession`, updates the active display,
  persists an `operator`-provenance title, and permanently suppresses automatic generation.
  - verify: `TestSessionTitleGeneration_Scenario1_TitleCommandPersistsOperatorTitle`
- AC1.2: `/title` adds a local, non-persisted scrollback notice containing only the active title
  and generated/operator provenance without mutation; whitespace-only input fails clearly and
  preserves the authoritative title.
  - verify: `TestSessionTitleGeneration_Scenario1_TitleCommandReadAndRejectsBlank`
- AC1.3: A stale, unauthorized, unavailable, or rejected rename cannot leave an optimistic title
  presented as authoritative; the UI restores or refetches it.
  - verify: `TestSessionTitleGeneration_Scenario1_TitleCommandReconcilesFailure`
- AC1.4: The command is client-only: it creates no engine tool, prompt-command source, synthetic
  conversation message, title-generation RPC, or generic auxiliary dispatcher.
  - verify: `TestSessionTitleGeneration_Scenario1_TitleCommandIsClientOnly`

### Scenario 2 — Genuine prompts durably establish automatic-title input

At original prompt ingress, the loop records the first three genuine, non-empty principal text
prompts as bounded title-source candidates. At creation, composition has already persisted
`pending` when an explicit compatible title slot is available, or `disabled` otherwise. Synthetic
continuations, compaction summaries, empty text, and media-only prompts never become candidates.

**Acceptance:**
- AC2.1: Creation persists `pending` only when `models.slots.title` resolves on the session's fixed
  provider; otherwise it persists `disabled` without a provider call.
  - verify: `TestSessionTitleGeneration_Scenario2_CreationPersistsEligibility`
- AC2.2: The first three genuine non-empty principal text prompts are captured at prompt ingress,
  bounded, and distinct from harness continuations, compaction, and generic user-role history.
  - verify: `TestSessionTitleGeneration_Scenario2_GenuinePromptCandidatesRoundTrip`
- AC2.3: Source prompts, lifecycle, and provenance round-trip through all in-tree snapshot/store
  and event-sourced reconstruction paths.
  - verify: `TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip`
- AC2.4: An automatic attempt neither adds synthetic conversation turns nor delays, rewrites, or
  spends the completed main chat exchange.
  - verify: `TestSessionTitleGeneration_Scenario2_AutomaticWorkIsOutsideChatRun`

### Scenario 3 — Composition and a bounded generator own routing and output

`models.slots.title` is recognized by validation but has no default-tier fallback. Composition
resolves the selected title model against the persisted session selector and fixed provider. The
server passes the host-private generator only that provider-scoped provider, model, and source
prompts. The generator uses one bounded direct stream call and returns only strict `title` or
`defer`; malformed output ends automatic work with the deterministic first-prompt fallback.

**Acceptance:**
- AC3.1: An absent title slot never falls back to a default, cheap, or session model and causes no
  title provider call.
  - verify: `TestSessionTitleGeneration_Scenario3_AbsentTitleSlotDisablesGeneration`
- AC3.2: A configured title slot resolves through existing slot/alias policy on the session's fixed
  provider and never changes the main session model.
  - verify: `TestSessionTitleGeneration_Scenario3_TitleSlotRoutesOnlyGenerator`
- AC3.3: The generator has no routing, credential, store, or mutation authority and receives no
  client-supplied title input, model, provider, or system prompt.
  - verify: `TestADR_0301_TitleGenerationServerOwnsInputAndModel`
- AC3.4: Input is fenced as untrusted data and bounded before the provider call. Valid generated
  output is strict, UTF-8, whitespace-canonicalized to one line, and capped at 80 runes total
  including truncation. First-prompt and operator titles retain their existing length limit but use
  the same one-line whitespace canonicalization.
  - verify: `TestADR_0301_TitleGenerationInputOutputBoundary`
- AC3.5: `defer` permits an attempt after source prompt two or three; a valid title, malformed
  output, unavailable configuration, or third-prompt exhaustion ends the lifecycle with the
  fallback retained.
  - verify: `TestSessionTitleGeneration_Scenario3_DeferAndTerminalOutcomes`

### Scenario 4 — Coordinator lifecycle, races, and notifications are safe

After a successful exchange that added a source prompt, the Service submits the session to a
bounded coordinator. It has bounded admission/workers, durable session-and-attempt deduplication,
and cancellation/join on Service/Build shutdown. A job claims an eligible attempt under the
run-entry mutex and lease, releases them while streaming, then reloads and conditionally commits.
Operator rename wins every late-completion race. Every persisted title or lifecycle change produces
a sanitized `session.title` event published after save. Terminal automatic non-success shows one
muted client-only notice directing the operator to `/title <text>`; it contains no provider detail.

**Acceptance:**
- AC4.1: Concurrent completion/submission admits at most one physical provider call per durable
  attempt and does not hold a session lock or lease during streaming.
  - verify: `TestSessionTitleGeneration_Scenario4_TwoPhaseAttemptAdmission`
- AC4.2: An operator rename permanently wins over pending or late automatic completion; generated
  titles and deleted/ineligible sessions are never overwritten.
  - verify: `TestSessionTitleGeneration_Scenario4_OperatorTitleWinsRace`
- AC4.3: One known provider timeout/failure queues at most one durable retry with bounded backoff;
  cancellation, malformed output, unavailable config, and crash-interrupted claims record terminal
  outcomes and never issue duplicate billable work after restart.
  - verify: `TestSessionTitleGeneration_Scenario4_InterruptionAndRetryPolicy`
- AC4.4: The coordinator has bounded admission/workers, is cancelled and joined at shutdown, and
  has explicit ADR-0027 resource-inventory and restart-fidelity decisions. Restart reconciliation
  is accepted bounded best-effort: it reads exactly one capped initial metadata page, never follows
  its cursor, and may leave later-page eligible sessions for a normal eligible exchange and submission.
  - verify: `TestSessionTitleGeneration_Scenario4_CoordinatorShutdownAndInventory`,
    `TestSessionTitleGeneration_Scenario4_ReconciliationUsesOneCappedPageBestEffort`
- AC4.5: Every persisted title/lifecycle change appends and publishes `session.title` containing
  authoritative title, provenance, lifecycle, a durable title revision, and only bounded latest-attempt
  metadata—never prompt, provider-error text, or token-usage detail.
  - verify: `TestSessionTitleGeneration_Scenario4_TitleEventIsAuthoritativeAndSanitized`,
    `TestSessionTitleRevisionWireCompatibility`
- AC4.6: Active/open clients apply `session.title` immediately and reconcile via `GetSession` after
  live-stream reconnect or session reopen; no periodic inventory polling is introduced. Both the
  snapshot and live event carry the durable title revision, and the client adopts only a higher positive
  revision. Legacy revision `0` remains acceptable only until a positive revision is observed for the
  active session, so a snapshot followed by a delayed older live event cannot regress the title.
  - verify: `TestSessionTitleGeneration_Scenario4_ClientReconnectReconcilesTitle`,
    `TestTitleRevisionSnapshotRejectsDelayedLiveEvent`,
    `TestTitleRevisionAcceptsLegacyOnlyBeforePositive`
- AC4.7: A terminal automatic non-success produces one muted client-only notice without provider
  detail that retains the fallback and directs the operator to `/title <text>` for a manual title.
  - verify: `TestSessionTitleGeneration_Scenario4_QuietFailureOffersManualTitle`

### Scenario 5 — Canonical token usage is durable, attributed, and budget-safe

Every admitted physical title call aggregates `session_title` token usage by the selected opaque
provider/model key. The title lifecycle retains only an attempt identity, outcome, and time; it has
no per-attempt usage ledger. `token_usage[main]` is canonical for main work; deprecated
`Session.Usage` remains its lifetime compatibility mirror.

**Acceptance:**
- AC5.1: Each physical title-generation call aggregates its input/output tokens in the
  `session_title` usage kind under the selected model attribution.
  - verify: `TestSessionTitleGeneration_Scenario5_RecordsTokenUsage`
- AC5.2: Token usage is canonical durable accounting: each `TokenUsage.Total` is the sum of its
  opaque model entries, and legacy unattributed records use `unknown` rather than a guessed model.
  - verify: `TestSessionTitleGeneration_Scenario5_RecordsTokenUsage`
- AC5.3: Token usage and title lifecycle round-trip through every in-tree snapshot/store and
  event-sourced reconstruction path, and authorized projections expose the canonical aggregate.
  - verify: `TestSessionTitleGeneration_Scenario5_TokenUsageRoundTripAndProjection`
- AC5.4: Title-generation tokens do not alter the deprecated `Session.Usage` main mirror,
  `MaxRunTokens`, normal turn/result usage, or the agent conversation.
  - verify: `TestADR_0302_AuxiliaryUsageDoesNotSpendRunBudget`
- AC5.5: The internal immutable budget baseline leaves `Session.Usage` and
  `token_usage[main]` lifetime totals untouched: ordinary runs start at zero; team
  synthesis and a budget-stopped free-text Subagent's one-turn cleanup capture current
  cumulative main usage immediately before their bounded re-drive; cleanup spend is
  added to the same lifetime totals; and no externally callable usage-reset or
  baseline-selection API exists.
  - verify: `TestADR_0302_RunBudgetBaselineDoesNotResetLifetimeUsage`,
    `TestSubagentBudgetStopSalvages`
- AC5.6: Other auxiliary callers are not migrated by this plan.
  - verify: `TestSessionTitleGeneration_Scenario5_OnlyTitleIsPlumbed`

## Out of scope

| Item | Decision |
|---|---|
| Currency/price estimates, rate history, and billing reconciliation | Deferred; ADR 0303 records tokens only. |
| Migrating other model calls into canonical token usage | Deferred; this plan wires `session_title` only. |
| Client-triggered generation or arbitrary client model invocation | Rejected; automatic work is Service-owned. |
| Generic model-invocation RPC/dispatcher | Rejected; later operations require their own authorization, input, lifecycle, accounting, and notification design. |
| Regenerating an accepted generated title | Deferred to future explicit UX. |

## Cross-cutting deliverables

- Update `docs/architecture.md`, `docs/usage.md`, and relevant `user-docs/` coverage for `/title`,
  explicit title-slot enablement, automatic lifecycle, live reconciliation, and token usage.
- Add the `title` slot to validation/configuration/help while preserving provider-neutral
  `port.LLMRequest`.
- Add the coordinator to [ADR 0027](../adr/0027-cloud-native.md) Lists 1 and 2 with owner,
  bounded resources, shutdown, and restart decision.
- If the engine public API changes, run `task api:update` and update `engine/CHANGELOG.md` under
  `engine/COMPATIBILITY.md`.

## Definition of done

1. `task lint`, `task test`, `task docs`, and `go run ./cmd/mecademo` pass.
2. `task api:check` passes, or intentional exported engine changes have generated API contracts and
   changelog classification.
3. `task ac-trace-strict` passes once this plan is landed and every named `verify:` proof resolves.
4. Offline mock-provider tests prove both configured and absent title-slot paths, coordinator races,
   shutdown, durable recovery, live-event/reconnect reconciliation, and no main-chat disruption.
5. Panel review finds no ship blockers.

## Deferred decisions and known risks

- Token accounting is not monetary cost accounting.
- A provider execution interrupted after durable claim is unknowable and is not retried.
- Live delivery is best-effort; snapshot reconciliation, not push, is authoritative.
- Reasoning-effort policy for title calls is intentionally deferred; it remains provider-construction
  configuration and never widens `port.LLMRequest`.

## Exit criteria

When every Definition-of-done item holds on the accumulator, this plan is satisfied.
