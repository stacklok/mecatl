---
sidebar_position: 3
title: Learning
description: Configure evidence-backed learning, reflection, and learned-skill admission.
---

# Learning

Mecatl's learning feature turns eligible completed runs into bounded, reviewable
reflection proposals. It is not a general conversation recorder and it does not
let the model rewrite policy, safety rules, tools, or the soul. Explicit memory
tools remain separate and available even when automatic learning is off.

## Availability

Automatic evidence reflection is available in the standard application when a
reflection provider and proposal persistence can be built. It is **off by
default**. The engine can also be embedded with the learning pipeline configured
by the host.

The feature is process-local for admission budgets and coordination. It can use
project memory and the cross-project user model, but those stores and their
lifecycle capabilities must be configured separately.

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

The modes are deliberately different:

- **`off`** disables the automatic observer, coordinator, proposal repository,
  and reflection provider call. Explicit memory and reflection operations remain
  available.
- **`review`** admits eligible evidence and stages bounded proposals, but does
  not change active memory automatically.
- **`auto`** stages first and can promote only the narrow set of candidates that
  satisfy the explicit-principal evidence and trust rules.

A project settings file may tighten the operator choice — for example, lower
autonomy or change `validated` to `evaluated` — but cannot enable automatic
learning, raise autonomy, or weaken assurance. The legacy
`--user-model-review` flag is a temporary compatibility alias for `auto`.

## What can be learned

After an eligible main-session completion, mecatl evaluates evidence from the
verified current run. A genuine user prompt that explicitly asks to remember a
fact or learn a procedure is a hard admission signal. Otherwise, the weighted
signals must reach the configured sensitivity threshold:

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

## Budgets and safety

Automatic reflection uses sliding, process-local reservations. The limits under
`learning.automatic` bound reflection count and tokens globally and per
principal. The time window must be between one minute and 24 hours. A reflection
that fails, times out, or abstains consumes its reservation; a queue-full
admission does not. The duplicate cache, cooldowns, and reservations reset when
the process restarts, and multiple replicas have separate budgets.

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
rather than changing active memory.

When durable learning is enabled, authenticated clients can inspect attempts over
`GetLearningAttempt` / `ListLearningAttempts` or HTTP
`GET /v1/learning/attempts[/{id}]`. Lists use an optional closed state filter, an
opaque cursor, and a page limit of at most 200. Responses contain lifecycle state,
timestamps, safe failure/checkpoint codes, opaque versions, and authorized
proposal/skill links only. They never include prompts, transcripts, tool/provider
output, filesystem paths, identity values, credentials, diagnostics, metrics, or
event/watch payloads. Another caller sees the same not-found response as a missing
attempt. Attempt watch is not available.

Use the memory tools to inspect and manage the resulting facts. Values remain
bounded and secret-shaped credentials or role/directive overrides are rejected.
The live operator profile is injected as data into model requests; it does not
enter conversation history or the cache-stable prefix, and it cannot change
permissions or tools.

## Limitations

- Automatic learning is off unless an operator explicitly enables `review` or
  `auto`; configuring dream/consolidation intervals does not enable it.
- Admission budgets and duplicate detection are process-local and reset on
  restart. In a multi-replica deployment, aggregate capacity can be the per-
  replica limit multiplied by the replica count.
- Project promotion requires the exact trusted configured workspace and a
  lifecycle-capable project memory store. Candidates from other roots can remain
  staged but cannot approve, undo, or write launch-root project memory.
- Remote stores must advertise the lifecycle operations required by the action;
  mecatl does not silently replace a missing lifecycle operation with an
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
