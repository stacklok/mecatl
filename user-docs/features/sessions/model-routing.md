---
sidebar_position: 115
title: Model routing
description:
  Assign models to internal calls and delegated tasks, and diagnose routing.
---

# Model routing

Use model routing when different jobs need different models. Aliases give model
IDs readable names, slots bind internal calls, and task categories select a
model for eligible delegated work. Start with a working provider and session
model in [Choose models and providers](./choose-models.md).

These settings belong to the operator-global configuration. Session history
remains bound to its persisted provider; a provider-aware alias can select a
complete provider/model pair for a new session or for an eligible delegated or
auxiliary call.

## Configure aliases, slots, and task routing

For a deployment with several kinds of work, use the operator-global
`settings.yaml` to give models stable aliases and assign them to internal jobs
or delegation categories:

```yaml
models:
  default: gpt-5.6-terra
  aliases:
    planner: gpt-5.6-sol
    heavy:
      provider: openrouter
      model: openai/gpt-5.6-terra
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

A scalar alias uses `models.default_provider` (or `--default-provider`) when
configured; otherwise it uses the current provider. An object alias carries its
provider/model pair, even when used as `models.default` for a new session. A
bare literal Subagent model uses the parent's provider. Model IDs are opaque
and are not live-probed at startup.

For a temporary CLI alias with a provider, use matching flags rather than
splitting the model ID on `/`:

```sh
mecated serve --model-alias heavy=openai/gpt-5.6-terra \
  --model-alias-provider heavy=openrouter
```

The CLI model replaces the whole lower-tier alias target; its provider does not
silently inherit from a YAML alias.

The router example uses Jev. Set `TYPESAFE_API_KEY` in the server process:
eligible delegated task descriptions and category names and descriptions go to
Typesafe. Keep `backend: llm` if task text must stay in your configured LLM
path. A classifier choice below `minimum-confidence` falls back to the child's
ordinary model. `default-category` advises the classifier but does not force a
choice. See the [configuration reference](/reference/configuration.md#models)
for field defaults and ranges.

The `guardrail` slot also accepts a strict provider-aware object when the
checker must use a different configured provider:

```yaml
models:
  slots:
    guardrail:
      provider: review-provider
      model: coder
```

Both object fields are required. Unknown providers, missing fields, unknown
keys, and an unresolvable model fail startup; Mecatl never infers a provider
from an opaque model ID. The scalar form binds its selector to the deployment
default provider. Project-tier objects are ignored with a warning.

### How bindings resolve

Aliases map readable names to model IDs or provider/model pairs. Slots select
models for internal calls, while router categories choose models for eligible
delegated tasks. These bindings leave the existing session's provider and base
model unchanged. Use `compaction`, `ask-reviewer`, `guardrail`, and `router` for
internal calls; `plan` selects the model for plan-mode turns.

Those internal-call slots fall back to the `cheap` tier when they have no binding of
their own. Binding only `cheap` therefore also binds `guardrail`, which turns on
[guardrails](../security-and-execution/permissions-and-posture.md#guardrails) and
their checker cost.

Router categories apply to plain Subagents, unpinned named specialists
(including `mode: "read-write"`), Parallel branches, and team members without a
named definition. A configured category list enables routing; without one,
delegation keeps its inherited or default model.

With the example above, routing resolves as:

```text
large  → heavy  → gpt-5.6-terra
medium → coder  → gpt-5.6-luna
small  → quick  → gemini-3.5-flash
image  → image  → gpt-5.6-terra
```

For automatic routes and slots other than `title`, an unavailable target warns
and falls back to the call's ordinary provider/model. An absent or unresolvable
`title` slot disables generation without making a provider call. An explicit
per-call selector instead fails if its target is unavailable. The router does
not override a pinned named agent, `fork`, or `resume`. A named definition
without `model:` is routable; `model: inherit` is an explicit pin. Routing
preserves a specialist's scoped tools and writable posture.

Put model policy in operator settings, not a project file. Mecatl ignores every
project-tier `models:` block, including for trusted projects. The legacy
operator `models.allowlist` key remains parseable but has no effect and warns.
See the [configuration reference](/reference/configuration.md#models) for the
field schema and defaults.

### Session titles and auxiliary usage

The `title` slot explicitly enables automatic session-title generation. With a
compatible binding, the server generates a title asynchronously from up to three
early genuine prompts. It never delays or changes the chat. An absent or
unresolvable binding disables generation and makes no title-provider call.

Title token usage is stored separately as `session_title`. Other auxiliary calls
also record provider-reported tokens in separate session-usage buckets. An
unknown purpose uses the `unknown` bucket; model attribution is `unknown` only
when the provider or model is unavailable. These buckets leave the chat's
displayed usage and run budget unchanged. The router keeps its own internal
spend limit.

### Configure the task classifier

Jev confidence is a classifier score, not measured accuracy. Calibrate a
nonzero threshold against representative tasks rather than assuming a
universal setting. Automatic classification failures fall back to the child's
ordinary model.

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

Plan mode uses the `plan` target on the session's persisted provider. After
approval, subsequent turns use the session default again. A session stays
provider-bound because its history can contain provider-private replay data.
If a provider-aware `plan` alias names another provider, Mecatl warns and uses
the session's ordinary provider/model while keeping plan permissions.

If `plan` is unset, Mecatl uses the `reasoning` slot when it is configured;
otherwise plan mode uses the session default model. See
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md#plan-mode)
for the plan-review workflow.

## Check which delegated model ran

The live delegation card shows the provider and model that actually ran and
whether the child was routed, selected, or used as a fallback. Press **F6** and
focus a child for the routing reason and any rejected candidate. A candidate
is not proof that it ran. For retained evidence, use
`mecatui debug <SESSION_ID>` and ask `InspectSession` for the `delegation`
view; missing evidence in an older event does not prove classification occurred.

### Select a delegated target explicitly

Leave `provider` and `model` unset on Subagent, Parallel, and Team calls to
preserve operator defaults and automatic routing. When a task needs a specific
capability, call `DiscoverModels` and pass an exact provider/model pair:

```json
{"prompt":"Review this design","provider":"anthropic","model":"claude-opus-4-1"}
```

`DiscoverModels` also lists enabled router categories under the virtual
`model-router` provider. Choose an exact category to bypass classification:

```json
{"prompt":"Review this design","provider":"model-router","model":"large"}
```

An unavailable explicit category fails instead of inheriting a model.
Parallel uses one selector for all branches; each Team member can use its own.
The Parallel judge stays on the parent model. A read-only named specialist
accepts a model-only override but not a provider-bearing selector. A writable
named specialist uses its definition's resolved model; a writable generic
Subagent can select a model. `fork` and `resume` reject selectors; resume keeps
the original child's actual provider/model.

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

- [Choose models and providers](./choose-models.md) for provider setup and
  session selection.
- [Subagents, teams, and parallel work](/features/agent-behavior/subagents-and-teams.md)
  for delegation workflows.
- [Model configuration reference](/reference/configuration.md#models) for exact
  fields.
