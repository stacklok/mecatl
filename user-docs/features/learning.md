---
sidebar_position: 230
title: Learning
description:
  Configure evidence-backed learning, reflection, and learned-skill admission.
---

# Learning

Learning turns evidence from eligible completed runs into bounded reflection
proposals. It does not record every conversation or let the model rewrite
policy, safety rules, tools, or the soul. Memory tools remain available when
automatic learning is off.

## Availability

Automatic evidence reflection is available in `mecated`, `mecak8s`, `mecatequi`,
and `mecatui`'s embedded server when reflection and proposal storage are
configured. It is **off by default**. Engine embeddings can also configure the
learning pipeline.

Standard `review` and `auto` configurations use a durable ledger, so count and
token budgets, cooldowns, and deduplication apply across cooperating processes.
Embeddings without a healthy durable ledger enforce these bounds per process.
Configure project memory and the cross-project user model separately.

## Learning modes

Configure learning in the operator-tier `settings.yaml`:

```yaml
learning:
  mode: review # off | review | auto
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

- **`off`** disables automatic observation and new attempts. Explicit memory and
  reflection remain available. A configured remote store can still serve
  existing attempts and learned-skill inspection.
- **`review`** admits eligible evidence and stages bounded proposals, but does
  not change active memory automatically.
- **`auto`** stages first and can promote only the narrow set of candidates that
  satisfy the explicit-principal evidence and trust rules.

A project settings file can lower autonomy or require `evaluated` activation. It
cannot enable learning, raise autonomy, or weaken assurance.

The deprecated `--user-model-review` flag maps to `learning.mode: auto`.
`--user-model-review-interval` down-samples admitted reflections. Prefer the
`learning` settings for new deployments.

## What can be learned

After an eligible main-session completion, Mecatl evaluates evidence from that
run. A direct request to save a workflow as a skill is a strong signal only when
the run completes cleanly; negated requests and questions are not signals. Other
evidence must reach the configured sensitivity threshold:

- `conservative`: 6 points;
- `balanced`: 4 points;
- `eager`: 3 points.

Signals include repeated correction or trusted-host contradiction, failure
recovery, repeated stable tool sequences, and substantial success. Turn count,
tool-call count, and token count can add weight only when a base signal already
exists.

Tool output, web or MCP content, repository text, historical turns, events, and
assistant claims cannot trigger admission by themselves. This prevents untrusted
content from asking the harness to remember it.

Proposals are bounded, evidence-backed, and partitioned by owner and project.
Sensitive, ambiguous, conflicting, unsupported, colliding, or unpublishable
material is rejected or remains staged. A procedure follows a separate learned-
skill evaluation path; an unevaluated `SkillDraft` is never made active by a
startup sweep.

## Selected evidence and review safety

Mecatl selects one bounded view of the evidence for each reflection. It keeps an
assistant tool call and its results together, preserves source order, and makes
at most one model request.

Each staged proposal retains a content-free manifest of the selected entries and
their original coordinates and digests. Proposal lists remain metadata-only.
Detail and approval re-read the source and verify that exact manifest instead of
rerunning selection or substituting nearby content. If retained history was
compacted, deleted, or changed, the proposal becomes non-approvable. Mecatl does
not expose a raw transcript or manifest dump to recover it. Evidence previews
remain bounded and redacted.

## Budgets and safety

Automatic reflection reserves count and tokens through the admission ledger. A
failed, timed-out, or abstaining reflection consumes its reservation; a
queue-full admission does not. The budget window must be between one minute and
24 hours. Without a durable ledger, windows reset on restart and each replica
has its own budget.

An attempt references its source session and run without copying the transcript.
Before proposing or changing a skill, Mecatl verifies ownership, ordering, and
content digests. Missing, unauthorized, changed, or unrecoverably compacted
evidence fails closed.

The reflection request is bounded and uses the selected reflection model. Raw
provider errors are not persisted or logged. Candidates retain enough provenance
and evidence digests for the later review path to verify ownership and detect
changed source content.

## Activation assurance

`learning.skills.activation` controls automatic learned-procedure activation:

- **`evaluated`** requires a passing evaluator verdict;
- **`validated`** (the standard application default when selected for `auto`)
  may also activate structurally accepted, exact, non-legacy, evidence-backed
  abstentions when the candidate meets the validated rules.

A failing evaluation rejects the candidate. Missing evaluator infrastructure,
similar skills, name collisions, unsupported repositories, and altered or
unavailable evidence remain inactive. Set `evaluated` when every automatic
activation must have a positive evaluator result.

Automatic learning does not activate operator/manual skills already supplied
through `--skills-dir`, and it does not make quarantined `SkillDraft` content
live. The operator-controlled promotion and lifecycle gates remain
authoritative.

## Explicit reflection

Authenticated explicit reflection runs synchronously with the completed
session's provider and model. It bypasses automatic cooldowns and budgets but
keeps results staged unless the current prompt supplies valid promotion intent.
No safe evidence produces a content-free abstention; operational failures remain
errors.

With durable learning, inspect attempts through `GetLearningAttempt`,
`ListLearningAttempts`, or HTTP `GET /v1/learning/attempts[/{id}]`. Responses
contain lifecycle metadata and authorized proposal or skill links, but no
prompts, transcripts, tool output, paths, identities, credentials, or raw
diagnostics. Another caller receives the same not-found response as for a
missing attempt.

There is no attempt-watch endpoint. Re-read the attempt to follow its state.

Retry failed attempts with `RetryLearningAttempt`, or abandon an unclaimed,
nonterminal attempt with `AbandonLearningAttempt`. HTTP uses
`POST /v1/learning/attempts/{id}/retry` and
`POST /v1/learning/attempts/{id}/abandon`. Both controls require the attempt's
opaque `expected_version`, mutate only the caller's attempt record, and leave
state unchanged on stale versions, terminal conflicts, or live worker claims.
Abandon does not roll back linked proposals or skills.

Use the memory tools to inspect and manage the resulting facts. Values remain
bounded and secret-shaped credentials or role/directive overrides are rejected.
The live operator profile is injected as data into model requests; it does not
enter conversation history or the cache-stable prefix, and it cannot change
permissions or tools.

## Limitations

- Automatic learning is off unless an operator explicitly enables `review` or
  `auto`; configuring dream/consolidation intervals does not enable it.
- Global count and token budgets, cooldown, and deduplication require a durable
  admission ledger. The local ledger retains up to 512 reservation records and
  128 per opaque principal partition.
- `--learning-store-url` supports trusted single-tenant infrastructure only. The
  driver must provide the complete attempt, proposal, and skill repository
  services. Partial drivers and ownership-enforced or multi-tenant startup fail
  closed.
- Project promotion requires the exact trusted configured workspace and a
  lifecycle-capable project memory store. Candidates from other roots can remain
  staged but cannot approve, undo, or change the configured project's memory.
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
