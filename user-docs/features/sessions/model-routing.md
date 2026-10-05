---
sidebar_position: 115
title: Model routing
description: Assign models to internal calls and delegated tasks, and diagnose routing.
---

# Model routing

Use model routing when different jobs need different models. Aliases give model
IDs readable names, slots bind internal calls, and task categories select a
model for eligible delegated work. Start with a working provider and session
model in [Choose models and providers](./choose-models.md).

These settings belong to the operator-global configuration. The session provider
remains fixed; every selected model must be usable through the provider that
will run it.

## Configure aliases, slots, and task routing

For a deployment with several kinds of work, use the operator-global
`settings.yaml` to give models stable aliases and assign them to internal jobs
or delegation categories:

```yaml
models:
  default: gpt-5.6-terra
  aliases:
    planner: gpt-5.6-sol
    heavy: gpt-5.6-terra
    coder: gpt-5.6-luna
    quick: gemini-3.5-flash
    image: gpt-5.6-terra
  slots:
    compaction: heavy
    ask-reviewer: quick
    guardrail: coder
    plan: planner
    router: coder
    title: quick
  router:
    backend: jev
    jev:
      minimum-confidence: 0.5
      maximum-input-bytes: 16384
    default-category: medium
    categories:
      - name: large
        description: Deep reasoning, architecture, and subtle concurrency bugs.
        model: heavy
      - name: medium
        description:
          Multi-file implementation, integration, and substantial tests.
        model: coder
      - name: small
        description: Focused edits, known fixes, and quick lookups.
        model: quick
      - name: image
        description: Work requiring visual input.
        model: image
```

The `guardrail` slot also accepts a strict provider-aware object when the checker
must use a different configured provider:

```yaml
models:
  slots:
    guardrail:
      provider: review-provider
      model: coder
```

Both object fields are required. Unknown providers, missing fields, unknown keys,
and an unresolvable model fail startup; Mecatl never infers a provider from an
opaque model ID. The scalar form binds its selector to the deployment default
provider. Project-tier objects are ignored with a warning.

## Use a planning model in plan mode

Set `models.default` to the model that implements your changes and bind `plan`
to a model for plan-mode turns:

```yaml
models:
  aliases:
    implementation: gpt-5.6-terra
    planner: gpt-5.6-sol
  default: implementation
  slots:
    plan: planner
```

When the session enters plan mode, its next turn uses the `plan` model. When
you approve the plan and Mecatl continues in default or accept-edits mode, it
uses the session default again. Mecatl rebuilds the session engine at the mode
change, so the provider stays fixed. Configure both model IDs for the same
provider.

If `plan` is unset, Mecatl uses the `reasoning` slot when it is configured;
otherwise plan mode uses the session default model. See
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md#plan-mode) for
the plan-review workflow.

This example selects the Jev classifier backend. Set `TYPESAFE_API_KEY` in the
server process environment. When Jev is active, Mecatl sends each eligible
delegated task description and the configured category names and descriptions
to Typesafe. Keep the default `backend: llm` if delegated task text must remain
inside your configured LLM path.

Jev uses model `jev-1.13.0` by default. `minimum-confidence: 0` accepts every
valid choice; a higher value from `0` through `1` makes a lower-confidence
choice fall back to the inherited model. The optional `base-url` must use HTTPS,
except for loopback HTTP development endpoints. Jev routing accepts up to 255
categories. `maximum-input-bytes` defaults to 16384 and accepts an integer from
1 through 65536. Mecatl measures the complete rendered request text against this
limit and never permits more than 64 KiB. It does not truncate an over-limit task,
instructions, or taxonomy. Router observability uses the same backend-neutral
outcomes for both classifiers: `classifier-error`, `bad-verdict`, `unknown-category`,
`low-confidence`, `input-over-limit`, `capacity-timeout`, `cancelled`, and `timeout`.
In particular, a deadline is reported as `timeout`; `cancelled` means caller cancellation.
All remain fail-soft and count toward the existing three-miss per-run breaker.
`default-category: medium` is an advisory instruction for either backend when no
category clearly fits. It does not force a fallback category. A named model on a
delegation or agent definition is deterministic and takes precedence over category
routing; categories are advisory classification for otherwise unpinned work.

## Diagnose a delegated model decision

Start with the model line on the live delegation card. A successful decision keeps
the compact `routed: <category> → <model>` form. A fallback names the model that
actually ran and can add the rejected candidate and its confidence comparison:

```text
model: gpt-6-astra · fallback: low-confidence
candidate: medium → gpt-5.6-terra · confidence 0.42 < threshold 0.50
```

The candidate is evidence about the classifier result. It is not the model that
ran. The `model:` value remains the actual model after a fallback.

Press **F6** and focus the child, Parallel branch, or team member for the complete
decision. The detail identifies the configured backend and classifier, candidate,
actual model, final reason, threshold, miss count, and breaker state. A zero Jev
threshold appears as disabled. LLM routing has no native confidence score, so its
detail shows confidence as unavailable instead of `0.00`. Pin, fork, resume, and
breaker skips show the actual model and why the classifier was not called.

The live view explains the current run. To inspect retained evidence after the run,
start a target-bound debugger with `mecatui debug <SESSION_ID>` or
`mecatui connect <ADDRESS> debug <SESSION_ID>`, then ask it to use
`InspectSession` with the `delegation` view. The debugger reads the stored
Subagent, Parallel, and Team start events. If an older or incompletely persisted
event has no routing decision, the evidence remains absent. Do not infer that a
classifier ran from the displayed model or classifier configuration.

Effective configuration tells you which backend, classifier, taxonomy, threshold,
and category mappings the server can use. Runtime evidence tells you what happened
for one delegation. Jev confidence is a backend-native score, not measured accuracy.
Calibrate a nonzero threshold against a representative labeled workload from your
own tasks. Mecatl does not provide a router evaluation command or a universal
recommended threshold.

These mechanisms are independent:

- **Aliases** map readable names to concrete provider-specific model IDs.
- **Slots** select models for internal calls. `compaction`, `ask-reviewer`,
  `guardrail`, `plan`, and `router` do not replace the session model. The `plan`
  slot can use a stronger model while a plan is being written; compaction and
  checker slots can use cheaper models.
- **`title` is an explicit opt-in slot** for automatic session-title generation.
  It has no fallback at all: if the binding is absent, or if it is present but
  cannot be resolved for the session's fixed provider, generation is disabled
  and the server makes no title-provider call. This differs from other invalid
  slot or route targets, which may warn and fall back to the session model. With
  a compatible `title` binding, the server generates a title asynchronously from
  up to three early genuine prompts; it never delays or changes the chat. Its
  token usage is stored separately as `session_title`, not charged to the chat's
  displayed usage or run budget.
- **Auxiliary model calls** record provider-reported tokens in separate canonical
  session-usage buckets. A returned result without a purpose is recorded as
  `unknown` when its purpose cannot be determined. Model attribution is
  independent: it is `unknown` only when the provider or model is unavailable.
  These buckets do not change the chat's displayed usage or run budget, except
  that the router bucket retains its internal spend limit.
- **Router categories** select a model for a plain delegated Subagent, an
  unpinned named specialist (including `mode: "read-write"`), a Parallel branch,
  or an undefined team member from the task description. A taxonomy enables the
  router; with no taxonomy, delegation keeps its inherited/default model.

With the example above, routing resolves as:

```text
large  → heavy  → gpt-5.6-terra
medium → coder  → gpt-5.6-luna
small  → quick  → gemini-3.5-flash
image  → image  → gpt-5.6-terra
```

Resolution is fail-soft for slots and routes other than `title`: an invalid
alias, slot, or route target warns and falls back to the session model. The
`title` slot is the exception described above; an absent or unresolvable title
binding disables generation rather than falling back or making a provider call.
Explicit per-call models, model-pinned named agents, fork or resume choices, and
other higher-precedence selectors are not overridden by the router. A named
definition with no `model:` is routable; `model: inherit` is an explicit pin.
Writable named routing keeps the specialist's direct-write scope, while explicit
`read-write`+`agent`+`model` remains invalid. Model slots and router taxonomies
are operator decisions; project model settings are ignored unless the operator
explicitly allows the relevant model set on a trusted project via
`models.allowlist`.

An allowlisted model is not scoped to a particular use: a trusted project can
bind any allowlisted model to any slot, including the `guardrail` and
`ask-reviewer` safety checkers, not just the session default. Do not allowlist a
model you would be unwilling to see used as a safety checker.

This configuration belongs in the operator-global settings file, not a
checked-in project file. See the
[configuration reference](/reference/configuration.md#models) for the complete
field schema and defaults.

## Route OpenRouter models through preferred downstreams

OpenRouter can serve one model through several downstream inference providers.
By default, it balances among them by price. An operator can instead set a
preferred order for each model in the operator-tier `settings.yaml`:

```yaml
openrouter:
  models:
    'anthropic/claude-sonnet-4-6':
      order: ['anthropic', 'google-vertex']
      allow_fallbacks: false
    'openai/gpt-5':
      order: ['deepinfra/turbo']
```

`order` accepts lowercase-kebab downstream slugs and disables OpenRouter's
default price balancing. An absent `allow_fallbacks` keeps OpenRouter's default
(`true`), so it may try other downstreams after exhausting the list. Setting it
to `false` pins the request to the listed downstreams and can fail the turn when
none are available.

This configuration is operator-tier only because it controls spend, compliance,
and capabilities. Mecatl ignores a project-tier `openrouter` block with a
warning. Invalid slugs and empty orders are also dropped with a warning.

For each OpenRouter turn, Mecatl reports the selected downstream as a
`provider.route` event when OpenRouter supplies that metadata. The value may be
absent on a cache hit. It is OpenRouter's display name, such as `Google`, not
the configuration slug such as `google-vertex`, so treat it as human-readable
status rather than a round-trippable identifier.

See the [configuration reference](/reference/configuration.md#openrouter) for
the full field schema.

## Next steps

- [Choose models and providers](./choose-models.md) for provider setup and session selection.
- [Subagents, teams, and parallel work](/features/agent-behavior/subagents-and-teams.md) for delegation workflows.
- [Model configuration reference](/reference/configuration.md#models) for exact fields.
