---
sidebar_position: 110
title: Choose models and providers
description:
  Select the provider, model, and reasoning effort for a Mecatl session.
---

# Choose models and providers

A Mecatl session uses a provider and base model selected by the server or
client. The server returns the effective selection and capabilities when it
creates the session.

Choose the path that matches how you use Mecatl:

- use **`mecatui`** for interactive selection;
- use the **CLI** to configure a server or one-shot run; or
- use the **API** when your client creates sessions directly.

## Availability

Provider, model, and reasoning-effort selection is available in `mecated`,
`mecak8s`, `mecatequi`, `mecatui`'s embedded server, and the session APIs. A
connected `mecatui` uses the remote server's providers, credentials, and model
inventory.

For the rest of the terminal workflow, see [Use mecatui](/mecatui/index.md).

## Mecatui journey

When the connected server advertises model selection, type `/models` in
`mecatui`. Filter the server's inventory, select a model, and press **Enter**.
The picker warns that the choice creates a peer session and carries over the
visible conversation. Replaying a long history may be costly. The existing
session's provider and base model do not change.

A switch across providers keeps the visible conversation but drops private
provider state that the new provider cannot understand.

When broker OAuth is enabled, this peer is also a new broker session. Protected
MCP enrollment is session-scoped, so switching models may require enrolling the
protected backends again; authorization is not silently copied from the old
session.

Type `/effort` to choose a reasoning-effort tier. `mecatui` applies a changed
tier by creating a peer session with the same provider, model, and conversation.
The picker is available only when the connected server advertises the relevant
capability.

In embedded mode, local server configuration and credentials determine the
inventory. In `connect` mode, the remote server determines it.

## CLI journey

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

Model IDs are provider-specific. The example works only if the server's OpenAI
provider can use `gpt-5.6-terra`.

For a zero-selector session, server-side resolution is separate for provider and
model:

- provider: `--default-provider`, otherwise the automatic available-provider
  preference;
- model: `--model`, then `--default-model`, then the selected provider's
  built-in default.

`--model` has higher priority than `--default-model`. An invalid deployment
default prevents startup.

`mecatui` accepts these flags for its embedded server. They do not reconfigure a
server used through `mecatui connect`. `mecak8s` exposes the corresponding
server configuration. See the
[operator provider and model reference](/building/deployment/mecated.md#provider-and-model)
for credential sources and deployment options.

### Operator-defined gateways

Define a named HTTPS gateway under `providers:` in the user-global
`settings.yaml`, then select it with `models.default_provider`. API-key gateways
use the matching provider ID in operator-local `auth.yaml`. Restart after
changing either file.

If live model listing fails or returns no models, `/models` keeps the configured
default selectable and shows a safe status without endpoints, credentials, or
raw errors. Built-in `--*-base-url` flags take precedence over settings. See the
[provider configuration reference](/reference/configuration.md#providers) for
the accepted flavors and fields.

### Select ToolHive gateway models

When ToolHive gateway discovery is enabled, one configured gateway identity
appears as two protocol-specific Mecatl providers:

|Provider ID|Model discovery|Inference|
|-|-|-|
|`toolhive`|`GET /v1/models`|`POST /v1/responses`|
|`toolhive-anthropic`|`GET /anthropic/v1/models`|`POST /anthropic/v1/messages`|

Select native Anthropic models under `toolhive-anthropic`. Mecatl keeps the
inventories separate so these models use Anthropic Messages.

`toolhive` remains the automatic default between the two gateway providers. A
configured key-driven provider still takes precedence unless the operator
explicitly sets `toolhive` or `toolhive-anthropic` as the server default. When
the gateway is available but not selected, `/models` shows both protocol
inventories so you can choose one without removing another provider's
credential.

Each provider has its own availability and last-known-good catalog. A failure
from one protocol endpoint does not erase the other inventory.

If `/models` reports an unreachable provider, follow its hint to start the local
proxy or check direct gateway connectivity and OIDC. An empty list means the
gateway administrator must grant model access. For routing or cost errors,
select a fully qualified model slug or ask the administrator to add a route.
Create a new session after correcting an unresolved default model.

For proxy/direct routing, OIDC setup, TLS constraints, and daemon flags, see
[Run mecated standalone](/building/deployment/mecated.md#the-toolhive-llm-gateway-no-api-key-needed).

### Configure aliases, slots, and task routing

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

Use these mechanisms independently:

- **Aliases** map readable names to concrete provider-specific model IDs.
- **Slots** select models for internal calls. `compaction`, `ask-reviewer`,
  `guardrail`, `plan`, and `router` do not replace the session model. The `plan`
  slot can use a stronger model while a plan is being written; compaction and
  checker slots can use cheaper models.
- **`title`** opts into automatic session titles. Without a compatible binding,
  generation is disabled and makes no model call. With one, the server generates
  a title asynchronously from up to three early prompts and records its usage
  separately from the chat.
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

Invalid aliases, slots, and routes warn and fall back to the session model. The
`title` slot instead disables generation. Explicit model choices and pinned
named agents take precedence over routing. A named definition without `model:`
is routable; `model: inherit` pins it. Project settings can select only models
that the operator exposes through `models.allowlist` in a trusted project.

An allowlisted model is not scoped to a particular use: a trusted project can
bind any allowlisted model to any slot, including the `guardrail` and
`ask-reviewer` safety checkers, not just the session default. Do not allowlist a
model you would be unwilling to see used as a safety checker.

This configuration belongs in the operator-global settings file, not a
checked-in project file. See the
[configuration reference](/reference/configuration.md#models) for the complete
field schema and defaults.

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

An invalid server value becomes unset. An invalid session value falls back to
the server default. Mecatl warns in either case and may normalize or drop an
effort that the selected provider or model does not support.

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

`ListModels` and `mecatui`'s `/models` inventory expose public metadata,
including:

- provider ID and opaque model ID;
- display name when available;
- image-input support;
- reasoning support; and
- context limit when known.

The server-specific inventory contains no API keys or private credentials. Live
provider catalogs can refresh while the server runs.

Models whose catalog includes it can call the read-only `DiscoverModels` tool to
inspect this same resolved inventory. Results contain the exact `provider_id`
plus `model_id` selection handle and the same safe metadata as `ListModels`;
equal model IDs under different providers remain separate. Exact provider/model
filters are supported. Output defaults to 20 entries and is capped at 50 entries
and 32 KiB. The tool does not probe providers, accept endpoints or credentials,
or change the current session, and remains available in no-filesystem sessions.

For a known model, the session's effective capabilities combine the model's
metadata with the selected adapter's transport capabilities. For an uncatalogued
model ID accepted through an explicit provider, the server can report only what
the adapter itself knows. Treat the capabilities returned for the created
session as authoritative.

## Limitations

- Provider credentials and model availability belong to the server host. A
  connected `mecatui` cannot use credentials configured only on the TUI host.
- Model IDs are provider- and deployment-specific opaque strings.
- Listing a model does not guarantee that a later provider request will succeed.
- In `mecatui`, changing the provider or base model creates a peer session. The
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
