---
sidebar_position: 110
title: Choose models and providers
description:
  Select the provider, model, and reasoning effort for a Mecatl session.
---

# Choose models and providers

A Mecatl session runs with a provider and a base model selected by the server
and, optionally, by the client. The server returns the effective selection and
input capabilities when the session is created.

Choose the path that matches how you use Mecatl:

- use **mecatui** for interactive selection;
- use the **CLI** to configure a server or one-shot run; or
- use the **API** when your client creates sessions directly.

For the rest of the terminal workflow, see [Use mecatui](/mecatui/index.md).

## Mecatui journey

When the connected server advertises model selection, type `/models` in
`mecatui`. Filter the server's inventory and use the keyboard or a primary click
on a visible model row to move the selection. The mouse wheel scrolls the
viewport while the selection remains pinned. Press **Enter** to switch to the
selected model. The picker warns that the choice creates a peer session and
carries over the visible conversation. Replaying a long history may be costly. The existing session's
provider and base model do not change.

A switch across providers keeps the visible conversation but drops provider-
private replay state, such as reasoning state that the new provider cannot
understand. The new session's provider, model, and capabilities are reported by
the server.

When broker OAuth is enabled, this peer is also a new broker session. Protected
MCP enrollment is session-scoped, so switching models may require enrolling the
protected backends again; authorization is not silently copied from the old
session.

Type `/effort` to choose a reasoning-effort tier. `mecatui` applies a changed
tier by creating a peer session with the same provider, model, and conversation.
The picker is available only when the connected server advertises the relevant
capability.

`mecatui` model choices are server-backed. In embedded mode, the local server's
configuration and credentials determine the inventory. In `connect` mode, the
remote server determines it; local embedded-server flags and credentials do not
apply.

See [Use mecatui](/mecatui/index.md) for the command-line startup, connection,
and keybinding details.

## CLI journey

### Set up a local provider

Run `mecatui providers setup [PROVIDER]` to configure the embedded server. Without
a name, choose from the listed providers. It does not start a server or open the
TUI. `mecatui connect ADDRESS` always uses the remote server's configuration.

For API-key providers, setup can reuse an effective credential without copying an
environment value to disk, or accept a replacement through hidden terminal input.
It explains where to obtain provider-specific keys; API use may incur charges.
Keys are never command arguments, and empty, invalid, or control-character values
are rejected before saving. Saving requires confirmation.

Environment credentials or an operator-selected owner-only API-key file supply
API keys. The file is plaintext, so its permissions are not encryption: same-UID
processes, including permitted agent Shell commands, can read it. Do not put a
secret in flags, prompts, settings, or logs. Use an environment variable or the
operator-managed credential file when interactive entry is unsuitable. See
[Run mecated standalone](/building/deployment/mecated.md#configure-providers) for
credential-file and daemon configuration details.

After setup, you may set the embedded default with
`mecatui providers set-default PROVIDER [MODEL]`; declining leaves the current
default unchanged. `mecatui providers` and `mecatui providers status [PROVIDER]`
report local configuration without revealing credentials. Presence does not prove
model access, billing, or account health.

`openai-codex` is distinct from the public `openai` API-key provider. Setup can
reuse a locally usable manual Codex subscription token for default selection but
never requests, writes, refreshes, imports, or removes that token. See the
[manual subscription-token deployment guidance](/building/deployment/mecated.md#provider-and-model).

Use `providers add PROVIDER` to define a custom provider, `login PROVIDER` to
manage its locally owned credential, and `logout` or `remove` to remove it.
Custom OIDC login can use `--no-browser` on a headless host. ToolHive credentials
and lifecycle are external: use `thv llm` tooling, not these provider commands.

### Endpoint overrides

The built-in provider endpoint flags (`--openai-base-url`,
`--openrouter-base-url`, `--anthropic-base-url`, and `--opencode-base-url`) are
non-secret configuration. They override the matching operator
`provider_overrides` setting; settings override the built-in endpoint. Custom
provider URLs remain defined only by their provider definition. OpenAI and
Anthropic keep their SDK endpoint when neither source supplies an override.

### Configure a server default

Use deployment flags when every session on a server should start from the same
provider and model:

```sh
mecated serve \
  --default-provider openai \
  --default-model gpt-5.6-terra
```

`gpt-5.6-terra` is an example model ID. Model IDs are provider-specific, so the
same example is valid only when that server's OpenAI provider can use it. The
server's built-in OpenAI default remains available when no explicit model is
configured.

For a zero-selector session, server-side resolution is separate for provider and
model:

- provider: `--default-provider`, otherwise the automatic available-provider
  preference;
- model: `--model`, then `--default-model`, then the selected provider's
  built-in default.

`--model` is a higher-priority deployment override. `--default-model` is the
validated default for the configured default provider. An invalid deployment
default fails startup rather than silently selecting a different provider or
model.

`mecatui` accepts these flags for its embedded server. They do not reconfigure a
server used through `mecatui connect`. `mecak8s` exposes the corresponding
server configuration. See the
[operator provider and model reference](/building/deployment/mecated.md#provider-and-model)
for credential sources and deployment options.

### Operator-defined gateways

An operator can declare a named HTTPS gateway in the user-global `settings.yaml`
under `providers:` and make it the deployment default with
`models.default_provider`. API-key gateways use the matching provider ID in the
operator-local `auth.yaml`; credentials are never read from a project file or
supplied by `mecatui connect`. The server snapshots these settings and
credentials once while it starts, so changing either file requires a restart.
When a custom provider's live model listing is unreachable, unauthorized, or
empty, `/models` keeps its configured default model selectable and displays only
a safe provider status; endpoints, credentials, and raw listing errors or
response bodies are never published to clients. Built-in `--*-base-url` flags
still take precedence over eligible built-in endpoint overrides. See the
[provider configuration reference](/reference/configuration.md#providers) for
the accepted flavors and fields.

### Select OpenRouter models

One OpenRouter API key registers two protocol-specific providers, the same
split the ToolHive gateway uses:

|Provider ID|Inference|
|-|-|
|`openrouter`|`POST /v1/responses`|
|`openrouter-anthropic`|`POST /v1/messages`|

Both providers cache. Mecatl asks for a prompt cache on every request, using
the Responses protocol's own `prompt_cache_breakpoint`, so a model that caches
only when asked is covered on any endpoint. That includes the ToolHive gateway
and any OpenAI-compatible endpoint you configure yourself.

Select Anthropic models under `openrouter-anthropic` when you want more than
the floor. That endpoint speaks the Anthropic Messages protocol, which carries
four cache breakpoints instead of one and lets you set a cache lifetime with
`--anthropic-cache-ttl` (`5m` or `1h`). The Responses protocol expresses
neither.

This matters for cost. Anthropic caches a prompt only when the caller asks, and
cache reads bill at a tenth of uncached input, so a long session on an unasked
path pays the full price every turn. Mecatl therefore prefers
`openrouter-anthropic` when the default model is an Anthropic model. An explicit
`--default-provider` or an operator `models.default_provider` still wins.

`openrouter-anthropic` lists Anthropic models only, because OpenRouter's
Anthropic endpoint does not serve other vendors' models.

In `/models`, a row marked `no-cache` is a Claude model Mecatl will not ask to
cache, which is where an unasked cache costs the most. You will see it if you
run with `--no-prompt-cache`, or if you route a Claude model through a provider
speaking the Chat Completions protocol: `opencode`, or one you defined with
`api_flavor: openai-chat-completions`. That protocol has no way to ask for a
cache, so every turn re-pays full input. Route Claude models through a Responses
or Messages provider instead.

To stop asking for a prompt cache, run with `--no-prompt-cache`. It turns off
every Responses-side ask and the three conversation breakpoints on the Anthropic
Messages providers.

It does not turn off caching completely. On a Messages provider, the breakpoint
covering the system prompt is emitted whatever you set, so the provider is still
asked to retain that prefix for the cache lifetime. A deployment relying on a
zero-retention arrangement therefore needs `--no-prompt-cache` *and* a model
route that avoids the Messages providers: `anthropic`, `openrouter-anthropic`,
`toolhive-anthropic`, and any provider you defined with
`api_flavor: anthropic-messages`.

### Configure aliases, slots, and task routing

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

A scalar alias resolves its model on the consumer's current provider. An object alias requires both `provider` and `model` and carries that atomic pair wherever the alias is used, including defaults, auxiliary slots, agent definitions, router categories, and delegation selectors. Unknown or unavailable object providers fail startup; model IDs remain opaque and are not checked through a live provider probe.

For ephemeral overrides, keep the model and provider in separate repeatable flags so model IDs need no delimiter:

```sh
mecated serve \
  --model-alias heavy=openai/gpt-5.6-terra \
  --model-alias-provider heavy=openrouter
```

A provider flag requires a matching CLI model alias with the same name. The CLI model replaces the complete lower-tier YAML target; it never inherits a YAML provider.

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
opaque model ID. The scalar guardrail form uses the deployment default provider. The entire project-tier `models:` block is ignored with one warning, regardless of project trust. Model policy belongs only in operator settings or operator flags.

### Use a planning model in plan mode

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

When the session enters plan mode, its next turn uses the `plan` target when that target is on the session's persisted provider. When you approve the plan and Mecatl continues in default or accept-edits mode, it uses the session default again. A session remains provider-bound because its history may contain provider-private replay data. If a provider-aware `plan` alias names another provider, Mecatl warns, keeps plan permission mode, and runs on the session's ordinary provider/model instead of replaying the conversation elsewhere.

If `plan` is unset, Mecatl uses the `reasoning` slot when it is configured;
otherwise plan mode uses the session default model. See
[Permissions and posture](/features/permissions-and-posture.md#plan-mode) for
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

### Diagnose a delegated model decision

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

Resolution is fail-soft for automatic slots and routes other than `title`: an invalid runtime target warns and falls back to the consumer's ordinary provider/model. The `title` slot disables generation instead. Explicit per-call selectors, pinned agents, fork, and resume are not overridden by automatic routing. A named definition with no `model:` is routable; `model: inherit` is an explicit pin. Routed named specialists keep their scoped prompt, tools, limits, and writable posture across provider/model targets.

This configuration belongs in operator-global settings, not a checked-in project file. Mecatl ignores every project-tier `models:` block with one warning, even for a trusted project. The legacy operator `models.allowlist` key remains accepted temporarily but has no effect and warns. See the [configuration reference](/reference/configuration.md#models) for the complete schema.

### Select a delegated target explicitly

Leave `provider` and `model` unset on Subagent, Parallel, and Team calls to preserve operator defaults and automatic routing. When a task requires a specific capability, call `DiscoverModels` first and pass the exact returned provider/model pair. For example:

```json
{"prompt":"Review this design","provider":"anthropic","model":"claude-opus-4-1"}
```

`DiscoverModels` also includes enabled router categories as virtual rows whose `provider_id` is `model-router`. Their descriptions participate in literal search, but their configured targets are not disclosed. Select one explicitly with its exact category name:

```json
{"prompt":"Review this design","provider":"model-router","model":"large"}
```

An explicit category bypasses classification and fails instead of silently inheriting when it cannot resolve. Mecatui renders its provenance as `selected: model-router/large → anthropic/claude-opus-4-1`; automatic classifier choices retain `routed:`.

Parallel accepts one selector for all branches and keeps its judge on the parent model. Each Team member accepts its own selector, and Mecatl validates the complete roster before adding members. A named specialist accepts a model-only override but rejects a provider-bearing call-level selector so its scoped definition is not replaced. `fork` and `resume` reject selectors; resume uses the actual provider/model persisted for the original child.

Router categories remain delegation policy. They do not appear in `ListModels`, mecatui's `/models`, or root-session creation.

### Route OpenRouter models through preferred downstreams

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

### Run one shot with mecatequi

`mecatequi` creates a new session for one prompt. It accepts model/provider
defaults and reasoning-effort settings, but has no interactive model picker. Use
it when the caller already knows the deployment and model configuration.

### Configure reasoning effort

The accepted reasoning-effort values are:

```text
auto, low, medium, high, xhigh, max
```

`reasoning-effort` may be set as a server default or supplied per session. The
important distinction is:

- omitted effort uses the server's configured default, or the provider default
  when no server default exists;
- explicit `auto` requests the provider's default effort; and
- a valid non-empty per-session value overrides the server default.

An invalid server value is ignored with a warning and becomes unset. An invalid
per-session value is ignored with a warning and falls back to the server
default. The server may normalize, clamp, or drop a value according to the
selected provider and known model capabilities.

The effective result is returned in `resolved_model.reasoning_effort`, so
clients can display what the server actually applied. Provider-specific effort
mapping belongs in the
[configuration reference](/reference/configuration.md#reasoning-effort), not in
the selection workflow.

## API journey

API clients can either omit provider/model fields and use the server defaults,
or name both fields when creating a session. The fields are available through
gRPC `CreateSession` and HTTP `POST /v1/sessions`.

### HTTP example

The HTTP/SSE API accepts JSON. For example:

```sh
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace": "/absolute/repo",
    "provider_id": "openai",
    "model_id": "gpt-5.6-terra"
  }'
```

The request requires a usable workspace and an available provider. The model ID
is passed to the selected provider; it does not need to appear in the server's
curated inventory to be accepted. A provider that is unknown or unavailable is
rejected.

### Selector rules

|`provider_id`|`model_id`|Result|
|-|-|-|
|omitted|omitted|Use the server-resolved provider and model.|
|set|omitted|Use that provider's own default model. The server's `--default-model` does not carry across to a different explicitly selected provider.|
|set|set|Use that provider and pass the model ID through to it. An uncatalogued model may be accepted and fail later at the provider.|
|omitted|set|Reject the request: a bare model ID is ambiguous.|
|unknown or unavailable|any|Reject the request; do not silently fall back to another provider.|

A bare `model_id` returns HTTP 400 or gRPC `InvalidArgument`. The same applies
to an unknown or unavailable provider. The API returns the new session ID and
resolved model information after successful creation.

See [Drive via gRPC / HTTP](/building/deployment/grpc-http.md) for the shared
session lifecycle and [the HTTP/SSE API reference](/reference/http-sse-api.md)
for endpoint details.

## Model inventory and capabilities

`ListModels` and mecatui's `/models` inventory expose public metadata,
including:

- provider ID and opaque model ID;
- display name when available;
- image-input support;
- reasoning support; and
- context limit when known.

They do not expose API keys or provider-private credentials. The inventory is
server-specific and depends on the providers and credentials configured at startup.
Each `ListModels` request, including client startup and SDK requests, can refresh
available providers after a ten-second per-provider cooldown. Concurrent requests
share a fetch; one provider's cooldown does not prevent another from refreshing.
The request waits up to ten seconds and returns models and safe provider statuses
from one snapshot. There is no periodic refresh or metadata cache on disk.

The server retains each provider's last non-empty model metadata for its lifetime,
even if a later listing fails, is unauthorized, or returns no models. The latest
status still reports that outcome where provider status is exposed. A later
non-empty listing replaces the retained list. Retained metadata can become stale;
it does not guarantee current model access or context limits.

Models whose catalog includes it can call the read-only `DiscoverModels` tool to inspect the bounded resolved inventory used for explicit selection. Start without `provider_id` when the provider is unknown. The first unfiltered result includes every selectable provider ID and model count, plus enabled delegation router categories under the virtual `model-router` provider. A router row uses the exact category name as `model_id` and its bounded operator description; it never reveals the category's alias, target, endpoint, credential, or provider topology.

Search spans provider ID, model ID, display name, and available descriptions. A query is a set of case-lowered literal terms, and every term must match. Exact filters cover direct and router rows. Each direct result contains the exact `provider_id` and `model_id` handle plus safe metadata; equal model IDs under different providers remain separate. Output defaults to 20 entries and is capped at 50 entries and 32 KiB. When `next_cursor` is present, call again with only that value. Inventory or description changes invalidate the cursor, so restart without it.

The tool does not probe or refresh providers, accept endpoints or credentials, select or route a model, or change the current session. It remains available in no-filesystem sessions. `model-router` rows are valid only on delegation surfaces, not root-session selection.

For a known model, the session's effective capabilities combine the model's
metadata with the selected adapter's transport capabilities. For an uncatalogued
model ID accepted through an explicit provider, the server can report only what
the adapter itself knows. Treat the capabilities returned for the created
session as authoritative.

## Limitations

- Provider credentials and model availability belong to the server host. A
  connected mecatui cannot use credentials configured only on the TUI host.
- Model IDs are provider- and deployment-specific opaque strings.
- Listing a model does not guarantee that a later provider request will succeed.
- In mecatui, changing the provider or base model creates a peer session. The
  client adopts the peer's complete authoritative transcript before making it
  interactive, then closes the source best-effort; if creation or transcript
  hydration fails, the open source chat remains available. API clients must
  implement equivalent history carryover themselves when they create a new
  session.
- Provider/model selection flags configure an embedded or server deployment;
  they do not override a remote server reached with `connect`.

## Next steps

- [Use mecatui](/mecatui/index.md) for the interactive model and effort pickers.
- [Start and resume sessions](./start-and-resume-sessions.md) for session
  creation and continuation.
- [Context windows](./context-windows.md) for context limits and fallback.
- [Capability and deployment matrix](./capability-matrix.md) for deployment
  availability.

## Troubleshooting

### Model context metadata is unavailable

You can send the first prompt in a new or resumed session without opening
`/models` first. If the selected model's context window is unknown and its provider
supports discovery, the server starts or joins discovery for that provider before
executing the prompt. Native authenticated providers perform this listing on demand
rather than at startup.
Known configured, retained live, or catalog windows need no listing.

When discovery fails or returns an empty list and no positive window is known,
the server rejects execution with `context_window_unavailable`. Your existing
conversation remains available, and the rejected prompt has not been recorded as
a server turn. Restore provider discovery, or ask the server operator to configure
the model's verified [exact context window](./context-windows.md). Retry after the
ten-second cooldown; another failed attempt requires another explicit request.
Cancelling your wait leaves the server's bounded discovery attempt running.

In `mecatui`, **Retry** sends the identical prepared text and attachments without
rereading files or the clipboard. **Back** restores the editable draft, including
staged pastes and images, and asks before replacing a newer draft. Cancelling that
confirmation keeps both drafts. **Discard submission** releases the rejected
payload. Recovery holds one submission in client memory under the existing size
limits; accepting the prompt, changing sessions, exiting, or an unrelated terminal
error releases it. There is no automatic replay or recovery after client restart.

API clients receive gRPC `Unavailable` with ErrorInfo domain `mecatl.stacklok.com`
and reason `context_window_unavailable`, or HTTP 503. Match the structured reason,
not error-message text, and retry explicitly after addressing discovery. For daemon
configuration, see [context discovery recovery](/building/deployment/mecated.md#context-discovery-recovery).
