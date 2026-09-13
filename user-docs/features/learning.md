---
sidebar_position: 230
title: Learning
description: Configure evidence-backed learning, reflection, and learned-skill admission.
---

# Learning

Mecatl's learning feature turns eligible completed runs into bounded, reviewable
reflection proposals. When standard learning is enabled, each admitted attempt
is stored durably so cooperating processes can recover and continue it. Learning
does not record every conversation or let the model rewrite policy, safety
rules, tools, or the soul. Explicit memory tools remain available when automatic
learning is off.

## Availability

Automatic evidence reflection is available in the standard application when a
reflection provider and proposal persistence can be built. It is **off by
default**. The engine can also be embedded with the learning pipeline configured
by the host.

The selected admission ledger determines the scope of automatic bounds. Standard
non-off application configuration selects a durable ledger, so its count and token
budgets, cooldown, and deduplication are global across cooperating processes. An
unwired embedding or unhealthy/absent durable ledger retains [ADR-0114's](https://github.com/stacklok/mecatl/blob/main/docs/adr/0114-configurable-learning-trigger-policy.md)
process-local limitation and must not claim global automatic bounds. Project memory
and the cross-project user model, and their lifecycle capabilities, must still be
configured separately.

## Learning modes

Configure learning in the operator-tier `settings.yaml`:

```yaml
learning:
  mode: review       # off | review | auto
  sensitivity: balanced # conservative | balanced | eager
  skills:
    activation: validated # validated | evaluated
  automatic:
    cooldown: 10m
    window: 1h
    max_reflections: 8
    max_tokens: 100000
    max_reflections_per_principal: 4
    max_tokens_per_principal: 50000
```

The modes provide increasing levels of automation:

- **`off`** disables automatic observation, admission, and coordinator work. With no
  `--learning-store-url`, it allocates no attempt repository or recovery worker and
  keeps explicit reflection on the lazy local path. An explicitly configured remote
  store is still dialed, capability-probed, and composed so explicit reflection,
  learned-skill inspection, and recovery of already-admitted attempts remain available;
  ordinary off-mode runs do not admit new automatic attempts. Explicit memory and
  reflection operations remain available.
- **`review`** admits eligible evidence and stages bounded proposals, but does
  not change active memory automatically.
- **`auto`** stages first and can promote only the narrow set of candidates that
  satisfy the explicit-principal evidence and trust rules.

A project settings file may tighten the operator choice by lowering autonomy or
changing `validated` to `evaluated`. It cannot enable automatic learning, raise
autonomy, or weaken assurance. The legacy
`--user-model-review` flag is a temporary compatibility alias for `auto`.

## What can be learned

After an eligible main-session completion, Mecatl evaluates evidence from the
verified current run. A direct request from the current caller to create, make,
build, learn, save, or turn a workflow into a skill is a hard admission signal
only when the run ends in an eligible clean state. Negated requests and
questions about learning are not admission signals. Mecatl stores admitted work
before it enters the `queued` state. It follows the automatic attempt lifecycle
instead of activating a direct `SkillDraft`. Other evidence must reach the
configured sensitivity threshold:

- `conservative`: 6 points;
- `balanced`: 4 points;
- `eager`: 3 points.

Signals include repeated correction or trusted-host contradiction, failure
recovery, repeated stable tool sequences, and substantial success. Turn count,
tool-call count, and token count can add weight only when a base signal already
exists.

The following cannot manufacture an admission by themselves: tool output,
WebFetch, WebSearch, MCP content, repository text, historical turns, event-only
content, or assistant-only claims. Current user intent is distinguished from
those sources so a page or tool result cannot ask the harness to remember itself.

Proposals are bounded, evidence-backed, and partitioned by owner and project.
Sensitive, ambiguous, conflicting, unsupported, colliding, or unpublishable
material is rejected or remains staged. A procedure follows a separate learned-
skill evaluation path; an unevaluated `SkillDraft` is never made active by a
startup sweep.

## Selected evidence and review safety

A retained transcript or event history can exceed the reflection request limit.
After automatic admission, or immediately for explicit reflection, Mecatl
deterministically selects one bounded view. Connected
tool turns are atomic: the assistant call and all corresponding tool results are
included together or omitted together. The selected entries are returned in source
order, and one selection produces at most one model request. Mecatl does not
chunk or merge multiple reflection passes.

Each staged proposal retains a content-free manifest of the selected entries and their
original coordinates and digests. Proposal lists remain metadata-only. Detail and
approval re-read the source and verify that exact manifest instead of rerunning
selection or substituting nearby content. If retained history was compacted, deleted,
or changed, the proposal becomes non-approvable. Mecatl does not expose a raw
transcript or manifest dump to recover it. Evidence previews remain bounded and
redacted.

## Budgets and safety

Automatic reflection reserves count and tokens through the selected admission
ledger. In the standard non-off application configuration, the durable ledger makes
those limits, cooldown, and deduplication global across cooperating processes. If a
durable ledger is absent or unhealthy, the capability is limited to ADR-0114's
process-local reservations: windows reset on restart and multiple replicas can each
spend their own budget. A reflection that fails, times out, or abstains consumes its
reservation; a queue-full admission does not. The time window must be between one
minute and 24 hours.

The attempt stores bounded, content-free provenance and references its exact source
session, durable run ID, and canonical digest; it does not copy the transcript. A worker
reconstructs only the bounded, secret-safe canonical evidence projection, verifies owner,
run, ordering, and digest, and fences that projection as untrusted at every restarted or
remote model boundary. Missing, gapped, unauthorized, mismatched, or unrecoverably
compacted evidence fails closed before proposal or skill mutation.

The reflection request is bounded and uses the selected reflection model. Raw
provider errors are not persisted or logged. Candidates retain enough provenance
and evidence digests for the later review path to verify ownership and detect
changed source content.

## Activation assurance

`learning.skills.activation` controls automatic learned-procedure activation:

- **`evaluated`** requires a passing evaluator verdict;
- **`validated`** (the standard application default when selected for `auto`) may
  also activate structurally accepted, exact, non-legacy, evidence-backed
  abstentions when the candidate meets the validated rules.

A failing evaluation rejects the candidate. Missing evaluator infrastructure,
similar skills, name collisions, unsupported repositories, and altered or
unavailable evidence remain inactive. Set `evaluated` when every automatic
activation must have a positive evaluator result.

Automatic learning does not activate operator/manual skills already supplied
through `--skills-dir`, and it does not make quarantined `SkillDraft` content
live. The operator-controlled promotion and lifecycle gates remain authoritative.

## Explicit reflection

Authenticated explicit reflection is separate from automatic admission. It runs
synchronously, lazily initializes persistence, uses the completed session's
persisted provider/model, and bypasses automatic cooldown and admission budgets.
Without genuine current-prompt promotion provenance, its output remains staged
rather than changing active memory. If bounded selection finds no safe evidence,
the call succeeds with a closed, content-free abstention reason instead; cancellation,
source mismatch, capacity, timeout, provider, and persistence failures remain typed
errors rather than being reported as abstentions.

When durable learning is enabled, authenticated clients can inspect attempts over
`GetLearningAttempt` / `ListLearningAttempts` or HTTP
`GET /v1/learning/attempts[/{id}]`. Lists use an optional closed state filter, an
opaque cursor, and a page limit of at most 200. Responses contain lifecycle state,
timestamps, safe failure/checkpoint codes, opaque versions, and authorized
proposal/skill links only. They never include prompts, transcripts, tool/provider
output, filesystem paths, identity values, credentials, diagnostics, metrics, or
event/watch payloads. Another caller sees the same not-found response as a missing
attempt. There is no attempt-watch endpoint, cursor, envelope, or process-local substitute;
ADR-0250 session EventLog watch is a different feed. Any future attempt notification is
advisory and clients must re-read the attempt repository under caller authority. A failed attempt can be retried with
`RetryLearningAttempt`, and an unclaimed nonterminal attempt can be abandoned with
`AbandonLearningAttempt`; HTTP uses `POST /v1/learning/attempts/{id}/retry` and
`POST /v1/learning/attempts/{id}/abandon`. Both controls require the attempt's
opaque `expected_version`, mutate only the caller's attempt record, and leave state
unchanged on stale versions, terminal conflicts, or live worker claims. Abandon is
non-compensating and does not roll back linked proposals, skills, or other downstream effects.

Use the memory tools to inspect and manage the resulting facts. Values remain
bounded and secret-shaped credentials or role/directive overrides are rejected.
The live operator profile is injected as data into model requests; it does not
enter conversation history or the cache-stable prefix, and it cannot change
permissions or tools.

## Limitations

- Automatic learning is off unless an operator explicitly enables `review` or
  `auto`; configuring dream/consolidation intervals does not enable it.
- Standard non-off composition claims global automatic count/token budgets,
  cooldown, and deduplication only after its durable ledger is selected. A Build-owned
  joined worker uses local or remote backend-authoritative discovery to reconcile an expired
  reservation after restart: an existing deterministic attempt retains the charge and absence
  reclaims it, without replaying admission or creating a duplicate. The local ledger caps durable
  reservation records at 512 globally and 128 per opaque principal partition. An unwired
  embedding retains the process-local ADR-0114 limitation and must report it honestly.
- `--learning-store-url` is currently for explicitly trusted single-tenant
  infrastructure only. Its attempt repository must implement bounded worker discovery:
  each replica continuously finds queued attempts (including those admitted after startup)
  and running attempts whose claims expired. Claims renew during evidence, model, and
  publication work; renewal loss cancels that worker, and shutdown joins it. A local
  coordinator capacity rejection therefore does not discard an already durable attempt.
  Ownership-enforced or multi-tenant startup fails closed until
  ADR-0213 workload-authenticated claims, a private owner registry, and separated
  maintenance RPCs are implemented; a driver's self-advertised `enforced` value does
  not satisfy that boundary.
- Project promotion requires the exact trusted configured workspace and a
  lifecycle-capable project memory store. Candidates from other roots can remain
  staged but cannot approve, undo, or write launch-root project memory.
- Remote stores must advertise the lifecycle operations required by the action;
  Mecatl does not silently replace a missing lifecycle operation with an
  unconditional legacy write.
- Automatic learning is not semantic or embedding search. No vector database or
  external embedding service is required or supported by this path.
- Manual `/dream` consolidation is a separate maintenance workflow; see
  [Dreaming and memory consolidation](./dreaming.md).

## Next steps

- [Dreaming and memory consolidation](./dreaming.md)
- [Memory and knowledge](/building/what-you-get/memory.md)
- [Skills, commands, and soul](./skills-commands-and-soul.md)
- [Capability and deployment matrix](./capability-matrix.md)
