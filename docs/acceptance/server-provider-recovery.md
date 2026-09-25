# Server-owned provider recovery — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — extends the existing in-process provider-resilience policy and host composition without changing an engine, wire, persistence, authority, or deployment boundary.
**Decision record:** None — the directing human chose server/runtime ownership and explicitly requested no ADR; this plan records bounded operating policy rather than a new durable architecture boundary.
**Phase:** live-run provider recovery
**Status:** in-progress, 2026-09-25. Implementing human-approved contract `13a055fef4b4fbd57b239f488c9659a866bbddb6` under the recorded stacked-delivery override; [Plan / Interface PR #1912](https://github.com/stacklok/mecatl/pull/1912) remains subject to human merge.
**Delivery:** Split, with an explicit human-authorized stacked implementation before plan merge. The Plan / Interface and Implementation PRs remain separate and require human merge.
**Expected tasks:** deferred to orchestration
**Source baseline:** `20f1220e90c124a355ff103c63defdf279e68279`

The smallest solution extends the shared `internal/adapter/llmresilience` decorator and
`internal/app` composition so a live server run waits through provider cooldowns and
`Retry-After` before semantic output. It applies equally to parent and child model calls and
removes mecatui's second, terminal-run automatic retry. It does not add a terminal-run
coordinator, durable continuation, or an engine/wire/persistence contract.

## Human decisions

- [x] Runtime ownership and decision-record route — Decision: the directing human approved shared server/runtime-owned recovery, not a TUI timer, and explicitly requested no ADR. Frozen ADR 0239 remains unchanged.
- [x] Command defaults and exposure — Decision: 30m recovery budget and 60 actual wrapper calls (initial included) per model step for `mecated`, `mecak8s`, embedded `mecatui`, and `mecatequi`; expose `--llm-recovery-budget` and `--llm-max-attempts` on all four (mecatui flags configure only its embedded server), reject negative budget and explicitly nonpositive attempts, add no settings-YAML key, keep zero-valued `app.Config` inert, and give `mecatequi` the existing host parity defaults 300s/180s/5/30s for per-attempt/idle/threshold/cooldown. The approved extra-cost limitation is that this is not a task-wide spend budget: distinct model steps may each recover, and engine token ceilings are checked at turn boundaries rather than between wrapper retries. Existing shorter auxiliary-check deadlines (30s) and guardrail-down policy remain effective without bypass; scheduled fires retain their dominant 30m wall timeout and `StopTimeout` record.
- [x] Initial recovery scope — Decision: live-process, active stream operations, including returned iterators, only. Disconnect/cancellation and process shutdown still stop recovery; closing mecatui or restarting a host does not continue or resume a run. Broader detached/restart recovery requires a separately reviewed durable coordinator.
- [x] First-increment observability — Decision: existing active-run UI spinner plus injected INFO operational logs and existing durable `network.attempt` observations only. `network.attempt` remains log/timeline evidence, not TUI-visible; a remote client cannot distinguish provider cooldown from other active waits, and `--quiet` provides no operational-log feedback. Dedicated durable recovery reasons are absent. These limitations are accepted; a live countdown/new event is excluded.
- [x] Stacked delivery override — Decision: the directing human said "lovely, do an implementation in a stacked pr in a work tree", authorizing implementation against this reviewed plan before its PR merges. This overrides the merged-plan entry checkpoint for this work only; verification, contract-drift checks, and human merge authority remain unchanged.

## Interface contract

- **gRPC / protobuf:** None — recovery remains inside the active provider `Stream`; no RPC, message, retry-start frame, or field changes.
- **Exported Go APIs / interfaces:** Add `RecoveryBudget time.Duration` to internal `llmresilience.Config` and `LLMRecoveryBudget time.Duration` to internal `app.Config`. Add `(*BreakerError).RetryDisposition() session.RetryDisposition`, returning `session.RetryDispositionRetryable`; the wrapper supplies precommit progress, including breaker-only exhaustion with zero actual calls. Add no engine API or provider-neutral port method. Provider-private metadata wrappers in `provider/openai`, `provider/openaichat`, and `provider/anthropic` implement the structural method `RetryNotBefore() (time.Time, bool)` while preserving `errors.As`, status, cause, and explicit retry-veto behavior.
- **Tool schemas:** None — model tools and their call/result contracts are unchanged.
- **CLI / config:** all four commands expose `--llm-recovery-budget` (30m) and `--llm-max-attempts` (60); CLI values override command defaults. Mecatui applies them only to embedded mode; connect mode uses the server's values. No settings-YAML key is added. Negative budget and an explicitly nonpositive attempts value fail validation. Existing per-attempt, idle, breaker-threshold, breaker-cooldown, and 10s maximum-backoff defaults remain unchanged except for the proposed mecatequi parity values.
- **Events / persistence:** No new or changed event/persisted fields or vocabulary. Existing `network.attempt` observations describe actual calls and must pass the existing validator; admission-only waits and budget-only terminal decisions are diagnostic-only (see Scenario 7). Terminal failure still uses the existing result event. Existing shared provider-breaker state outlives calls; no request-owned resource may outlive the active stream operation, including its returned iterator.
- **Security / authority:** Retry eligibility never expands model/tool authority. Raw headers, response bodies/values, raw or credential-bearing URLs, and credential-bearing errors never enter diagnostics, events, terminal errors, or model context. Existing sanitized target-URL display remains supported; retry metadata exports only normalized timing, never raw header values. Caller cancellation, transport disconnect, and process shutdown remain dominant.
- **Compatibility / migration:** Existing embedders with zero `app.Config.LLMRecoveryBudget` retain ordinary bounded attempts/backoff without extended cooldown waiting. Production inference constructors and every remint set SDK-owned automatic transport retries to zero; standalone provider-library defaults and unrelated catalog/auth SDK calls remain unchanged. Current-version mecatui removes terminal auto-retry but preserves explicit `/retry`; older/arbitrary clients may invoke the existing explicit retry API as a fresh user/client action, so server-wide prohibition is not claimed.

## In scope — 7 scenarios, in implementation order

### Scenario 1 — One safe recovery policy covers parent and child calls

The existing [resilience boundary](../architecture/observability.md#reliability--provider-resilience)
uses the decorator's semantic buffering and composed provider constructors/remints
([`llmresilience.go`](../../internal/adapter/llmresilience/llmresilience.go),
[`registry.go`](../../internal/app/registry.go)).

**Acceptance:**
- AC1.1: Through real composition, a root delegates to a direct-write child that completes a write and records its result; the child's next model step fails precommit, waits on the breaker, and recovers in the same child under the same parent tool call. The earlier write is not repeated, no replacement child or duplicate user prompt is created, and the parent continues. Root model steps use the same recovery policy.
  - verify: `TestServerProviderRecovery_Scenario1_RootAndDirectWriteChildRecoverBeforeTerminal`
- AC1.2: After semantic visibility, failure is terminal and no replay occurs; a clean completion flushes tentative pure-tool/whitespace output under the existing semantic boundary.
  - verify: `TestServerProviderRecovery_Scenario1_VisibleFailureNeverReplays`
- AC1.3: Failed precommit text, reasoning, and tentative tool assembly never escape or dispatch. Usage received from every discarded attempt is accounted exactly once on eventual success, exhaustion, or cancellation; no retry erases or doubles it. Engine token ceilings remain enforced at turn boundaries, not between wrapper retries.
  - verify: `TestServerProviderRecovery_Scenario1_TentativeAssemblyAndDiscardedUsageExactlyOnce`

### Scenario 2 — Provider timing and physical request accounting are safe

The [provider-resilience decorator](../architecture/observability.md#reliability--provider-resilience)
consumes HTTP errors with private sanitized metadata and preserved SDK causes
([`provider/openai/stream.go`](../../provider/openai/stream.go)); outer wrapper attempts
remain distinct from provider-internal semantic repair.

**Acceptance:**
- AC2.1: OpenAI Responses, OpenAI Chat, and Anthropic parse exactly one standard `Retry-After` value as nonnegative integer delta-seconds or HTTP-date at receipt. Malformed, multi-valued, or negative values are absent; a past date adds no wait. A syntactically valid delay beyond the representable scheduling horizon normalizes to `time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)` with validity retained. That sentinel is always unschedulable and causes an immediate retryable terminal without a new call, never an eligible date even for an enormous budget. Raw values remain inside the provider and are not retained in exported metadata or projected elsewhere.
  - verify: `TestServerProviderRecovery_Scenario2_RetryAfterParsingAndSecrecy`
- AC2.2: The next call starts no earlier than `max(backoff, provider not-before, breaker eligibility)` plus nonnegative jitter. Compare absolute not-before against `now.Add(remaining budget)` before converting to a duration; overflow or saturation must never shorten a valid delay into an early retry. A delay beyond the remaining budget ends without a call, and no raw header/body appears in errors, events, logs, or model input; existing sanitized target-URL display is preserved.
  - verify: `TestServerProviderRecovery_Scenario2_EffectiveDelayNeverRetriesEarly`
- AC2.3: Every production inference constructor/remint disables SDK transport retries, so test-server request counts match outer calls; provider-internal bounded semantic repair remains enabled and may issue an additional physical HTTP request, and explicit `Retryable() == false` remains a veto.
  - verify: `TestServerProviderRecovery_Scenario2_OuterAttemptsAndProviderRepairAccounting`

### Scenario 3 — Breaker waiters admit one real half-open probe

The current [`allow` path](../../internal/adapter/llmresilience/llmresilience.go) fails fast and
can admit concurrent trials after cooldown. Recovery coordinates the existing shared
[provider breaker](../architecture/observability.md#reliability--provider-resilience), not a
server run coordinator, without changing its per-attempt health accounting.

**Acceptance:**
- AC3.1: Breaker-denied callers wait cancellably without consuming an actual-call attempt; after cooldown exactly one eligible caller becomes the half-open probe. State changes notify all surviving waiters to recheck eligibility; exclusivity is guaranteed, not fairness or FIFO ordering.
  - verify: `TestServerProviderRecovery_Scenario3_ExclusiveHalfOpenProbeAndNotifiedWaiters`
- AC3.2: Half-open ownership is retained through visible output until clean completion, error, iterator abandonment, or cancellation, then released and waiters notified. Visibility alone is not provider success. Generation checks prevent stale in-flight success/failure from closing, reopening, or retiming newer breaker state; local recovery-budget expiry and canceled admissions are breaker-neutral.
  - verify: `TestServerProviderRecovery_Scenario3_ProbeReleaseAndStaleGenerationIsolation`

### Scenario 4 — A non-sliding budget bounds only precommit recovery

Raw chunks reset the [idle watchdog](../architecture/observability.md#reliability--provider-resilience)
even while tentative ([`llmresilience.go`](../../internal/adapter/llmresilience/llmresilience.go));
they must not reset recovery time. Shorter caller deadlines remain authoritative.

**Acceptance:**
- AC4.1: The budget starts at the first retryable precommit failure or initial breaker rejection and never slides across backoff, breaker/provider waits, tentative chunks, or reasoning-only raw activity. `MaxAttempts` caps actual wrapper calls (initial included); breaker-denied waits consume no attempt and no terminal path auto-resets either limit. The 30m/60 defaults bound one model step, not task-wide time or spend; distinct steps may start new recovery budgets, and the finite cap may end before 30m.
  - verify: `TestServerProviderRecovery_Scenario4_NonSlidingBudgetAndActualCallCap`
- AC4.2: Budget or attempt exhaustion retains the last sanitized cause as retryable+precommit, not permanent. Initial breaker rejection followed by budget exhaustion with zero actual calls retains the typed retryable `BreakerError` and wrapper-supplied precommit progress. Permanent, unknown, classifier-vetoed, and explicit provider-vetoed errors remain terminal immediately. Local recovery cancellation is distinct from caller cancellation and breaker-neutral; true caller cancellation/deadline wins even when simultaneous with local expiry.
  - verify: `TestServerProviderRecovery_Scenario4_CancellationAndTerminalClassification`
- AC4.3: Recovery timing ends at semantic visibility or clean completion, stopping and joining its timer before committing output. Expiry-versus-visibility/`ChunkDone` races have one outcome: expiry that wins remains a precommit failure (even if the provider swallows cancellation), while a winning commit leaves no recovery timer able to cancel the visible continuation. Attempt/idle contexts retain their existing bounds without a blanket recovery deadline, and visible output is never replayed.
  - verify: `TestServerProviderRecovery_Scenario4_RecoveryDeadlineCannotCancelVisibleStream`
- AC4.4: A zero recovery budget disables extended cooldown/provider-hint waiting but preserves ordinary max-attempt backoff. If that ordinary backoff already satisfies a valid not-before, retry is allowed; if additional waiting is required, stop retryable+precommit rather than calling early. A later explicit manual retry is a new action with a new budget.
  - verify: `TestServerProviderRecovery_Scenario4_ZeroBudgetHonorsNotBeforeWithoutEarlyCall`
- AC4.5: Composed auxiliary checks retain their shorter 30s caller deadlines and existing guardrail-down policy without bypass. Scheduled fires retain their 30m wall timeout (or configured tighter bound), measured from fire start, and record `StopTimeout` when it wins over per-step recovery. Focused integration tests use shortened injected bounds, not real-time 30s/30m waits.
  - verify: `TestServerProviderRecovery_Scenario4_ShorterAuxiliaryAndScheduleDeadlines`

### Scenario 5 — Four hosts agree and mecatui stops duplicating recovery

The existing [host resilience policy](../architecture/observability.md#reliability--provider-resilience)
uses three-attempt defaults in [mecated](../../cmd/mecated/main.go),
[mecak8s](../../cmd/mecak8s/flags.go), and [embedded mecatui](../../cmd/mecatui/main.go);
[the TUI terminal path](../../cmd/mecatui/ui/update.go) then starts one automatic retry.

**Acceptance:**
- AC5.1: All four hosts validate and compose identical recovery/max-attempt semantics and documented precedence; mecatequi also receives 300s/180s/5/30s timeout/breaker parity, while hand-built zero-valued app config remains inert.
  - verify: `TestServerProviderRecovery_Scenario5_FourHostConfigParity`
- AC5.2: Current mecatui never starts an automatic run after retryable+precommit terminal; `/retry` remains available and prompt-free. During active or successful recovery a scheduled fire stays in its same live session without rearming. Existing explicit scheduler `one_shot_retry` policy after eventual `StopError` is unchanged, not suppressed by this plan.
  - verify: `TestServerProviderRecovery_Scenario5_TUINoTerminalAutoRetryAndScheduleNoRearm`

### Scenario 6 — Recovery ends with the active transport and process

Both gRPC and SSE disconnects cancel the current run
([`api-surface.md`](../architecture/api-surface.md)). The shared provider breaker already
outlives individual calls, and `Stream` returns an iterator: request-owned resources belong
to the entire active stream operation, not just the invocation that returns that iterator.

**Acceptance:**
- AC6.1: gRPC/SSE disconnect, parent cancellation, and host shutdown cancel waits/probes and leave no hidden sleeper or continuing provider call. Recovery timers are stopped and joined at commit or failure; request-owned waiters and probe ownership are released by completion, error, iterator abandonment, or cancellation, with no resource retained beyond the active stream operation.
  - verify: `TestServerProviderRecovery_Scenario6_DisconnectAndShutdownCancelRecovery`
- AC6.2: Closing mecatui, detaching a client, or restarting a process does not claim continuation/resumption; persisted session history remains governed by existing behavior. Implementation reviews the maintained ADR 0027 lifetime inventories for any new retained probe resources and updates owner, cleanup, and restart-state entries as required, without rewriting frozen decisions.
  - verify: `TestServerProviderRecovery_Scenario6_NoDetachedOrRestartContinuation`; inspection — review ADR 0027 maintained inventories against actual resource ownership and cleanup.

### Scenario 7 — Operators can diagnose recovery without exposing data

The wrapper uses [injected diagnostics](../architecture/observability.md#reliability--provider-resilience)
and sanitized `network.attempt` evidence ([`llmresilience.go`](../../internal/adapter/llmresilience/llmresilience.go)).
The [existing validator](../../engine/session/event.go) requires `Attempt >= 1`, decision
`retry` with empty suppression, or `terminal` with one of `visible_output`,
`attempts_exhausted`, `permanent`, `unknown`, `classifier_veto`, `provider_internal_veto`,
`breaker_open`; it has no dedicated recovery-budget reason.

**Acceptance:**
- AC7.1: Injected INFO logs record sanitized decision, actual-call count/max, wait duration, remaining budget, and source (`provider`, `breaker`, or `backoff`) when a wait is at least 1s and on terminal/recovered outcomes, with no per-second noise. Actual-call `network.attempt` evidence retains its existing schema and validates. Admission-only waits and budget-only terminals are diagnostic-only: no fabricated attempt zero or attempt-exhausted observation, and no new serialized `recovery_budget_exhausted` reason. For an actual call canceled solely by recovery expiry, keep usage accounting and the existing terminal result but emit only diagnostic decision evidence, not a misleading durable attempt reason. Prior genuine failure observations remain intact. The missing dedicated durable recovery reason is an explicitly accepted limitation.
  - verify: `TestServerProviderRecovery_Scenario7_SanitizedOperationalLogsAndStableAttemptVocabulary`
- AC7.2: Composition tests prove each host's actual sink destination and failure/quiet policy: daemon/k8s operational sink, mecatequi stderr, and embedded mecatui's file or `--quiet` discard. Log-delivery failure cannot change retry policy or outcome; synchronous diagnostics may block on their writer, and no asynchronous sink is added. Diagnostics and observer callbacks execute outside the breaker mutex; a blocked observer neither holds the admission mutex nor prevents another waiter's cancellation.
  - verify: `TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy`; `TestServerProviderRecovery_Scenario7_BlockedObserverDoesNotHoldAdmissionMutex`
- AC7.3: Implementation updates the owning contributor resilience section to replace stale first-raw-chunk wording with the verified semantic boundary, updates `docs/tui.md` to describe server-owned recovery/manual retry, and updates the public choose-models troubleshooting owner plus the applicable daemon, mecak8s, mecatequi, and embedded-mecatui flag references/links.
  - verify: inspection — compare the owning documentation against the composed runtime tests; `task docs` and `task site:build` validate references and rendering without pinning arbitrary prose in Go tests.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Detached recovery after client disconnect, close-TUI continuation, or process-restart resume | Separately reviewed durable coordinator | Explicitly not promised by this live-process plan; the broader earlier aspiration remains unmet. |
| Durable child continuation or new engine, wire, retry event, countdown, or persistence contracts | Future approved plan if needed | Keep this increment inside the existing provider wrapper and observations. |
| New settings-YAML recovery keys | Future operator-config review | Command flags only for the proposed default. |
| Changes to standalone provider-library retry defaults or catalog/auth SDK retries | Provider-specific work | Disable SDK retries only in production inference composition/remints. |
| Changes to frozen ADR 0239 text | None | Record replacement current operating behavior in living docs; do not rewrite the historical automatic-once clause. |

## Definition of done

1. Focused wrapper/provider/composition/TUI tests pass; run `task test` once after the integrated change set. Then `task lint`, `task test:race`, `task docs`, `task site:build`, `task api:check`, and the offline demo pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. The offline demo still shows a tool call, permission ask/approval, and result.
4. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The 60-call cap is a hard ceiling, not a promise of 30 minutes of recovery; provider repair can make physical HTTP request count exceed outer attempts.
- A long valid provider delay intentionally fails closed instead of being treated as absent, which can terminally pause a run until a new explicit action.
- The recommended live-process scope solves an overnight failure only while the client/transport remains connected; it deliberately does not satisfy the broader close-client/restart aspiration.
