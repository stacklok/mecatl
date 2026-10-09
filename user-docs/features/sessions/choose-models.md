---
slug: /features/choose-models
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

<span id="mecatui-journey" />

## Select a model in `mecatui`

When the connected server advertises model selection, type `/models` in
`mecatui`. Filter the server's inventory and use the keyboard or a primary click
on a visible model row to move the selection. The mouse wheel scrolls the
viewport while the selection remains pinned. Press **Enter** to switch to the
selected model. The picker warns that the choice creates a peer session and
carries over the visible conversation. Replaying a long history may be costly.
The existing session's provider and base model do not change.

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

<span id="cli-journey" />

## Configure provider and model defaults

### Set up a local provider

Run `mecatui providers setup [PROVIDER]` to configure the embedded server.
Without a name, choose from the listed providers. It does not start a server or
open the TUI. `mecatui connect ADDRESS` always uses the remote server's
configuration.

For API-key providers, setup can reuse an effective credential without copying
an environment value to disk, or accept a replacement through hidden terminal
input. It explains where to obtain provider-specific keys; API use may incur
charges. Keys are never command arguments, and empty, invalid, or
control-character values are rejected before saving. Saving requires
confirmation.

Environment credentials or an operator-selected owner-only API-key file supply
API keys. The file is plaintext, so its permissions are not encryption: same-UID
processes, including permitted agent Shell commands, can read it. Do not put a
secret in flags, prompts, settings, or logs. Use an environment variable or the
operator-managed credential file when interactive entry is unsuitable. See
[Run mecated standalone](/operating/mecated/configure-providers-and-storage.md#configure-providers)
for credential-file and daemon configuration details.

After credential setup, the wizard offers to set the provider as the embedded
deployment default. This includes ToolHive after its externally managed login.
Declining leaves the current default unchanged. You can change the selection
later with `mecatui providers set-default PROVIDER [MODEL]`. `mecatui providers`
and `mecatui providers status [PROVIDER]` report local configuration without
revealing credentials. Presence does not prove model access, billing, or account
health.

`openai-codex` is distinct from the public `openai` API-key provider. Setup can
reuse a locally usable manual Codex subscription token for default selection but
never requests, writes, refreshes, imports, or removes that token. See the
[manual subscription-token deployment guidance](/operating/mecated/configure-providers-and-storage.md#manual-codex-subscription-token).

Use `providers add PROVIDER` to define a custom provider, `login PROVIDER` to
manage its locally owned credential, and `logout` or `remove` to remove it.
Custom OIDC login can use `--no-browser` on a headless host. ToolHive
credentials and lifecycle are external: use `thv llm` tooling, not these
provider commands.

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
[operator provider and model reference](/operating/mecated/configure-providers-and-storage.md#manual-codex-subscription-token)
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

One OpenRouter API key registers two protocol-specific providers, the same split
the ToolHive gateway uses:

|Provider ID|Inference|
|-|-|
|`openrouter`|`POST /v1/responses`|
|`openrouter-anthropic`|`POST /v1/messages`|

Both providers cache. Mecatl asks for a prompt cache on every request, using the
Responses protocol's own `prompt_cache_breakpoint`, so a model that caches only
when asked is covered on any endpoint. That includes the ToolHive gateway and
any OpenAI-compatible endpoint you configure yourself.

Select Anthropic models under `openrouter-anthropic` when you want more than the
floor. That endpoint speaks the Anthropic Messages protocol, which carries four
cache breakpoints instead of one and a cache lifetime, which defaults to `1h`
and can be set with `--anthropic-cache-ttl` (`5m` or `1h`). The Responses
protocol expresses neither.

This matters for cost. Anthropic caches a prompt only when the caller asks, and
cache reads bill at a tenth of uncached input, so a long session on an unasked
path pays the full price every turn. Mecatl therefore prefers
`openrouter-anthropic` when the default model is an Anthropic model. An explicit
`--default-provider` or `models.default_provider` overrides this preference,
unless a provider-aware `models.default` alias selects its own provider/model
pair.

`openrouter-anthropic` lists Anthropic models only, because OpenRouter's
Anthropic endpoint does not serve other vendors' models.

In `/models`, a row marked `no-cache` is a Claude model Mecatl will not ask to
cache, which is where an unasked cache costs the most. You will see it if you
run with `--no-prompt-cache`, or if you route a Claude model through a provider
speaking the Chat Completions protocol: `opencode`, or one you defined with
`api_flavor: openai-chat-completions`. That protocol has no way to ask for a
cache, so every turn re-pays full input. Route Claude models through a Responses
or Messages provider instead.

See
[Prompt caching](/features/runtime/observability-and-resilience.md#prompt-caching)
for cache controls and their retention limits.

### Route models by task

Use [Model routing](./model-routing.md) to configure aliases, internal-call
slots, plan-mode models, delegated task categories, and OpenRouter downstream
choices. Provider-aware aliases can also choose a complete pair for a new
session; existing session history stays on its persisted provider.

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

OpenAI Responses providers request display summaries automatically, regardless
of your effort setting. A model can complete without producing a summary. If a
compatible endpoint rejects the summary request, Mecatl reports the provider
error rather than retrying with a different request. Anthropic Messages and
Chat Completions have their own request behavior.

<span id="api-journey" />

## Select a model through the API

API clients can either omit provider/model fields and use the server defaults,
or name both fields when creating a session. The fields are available through
gRPC `CreateSession` and HTTP `POST /v1/sessions`.

### HTTP example

The HTTP/SSE API accepts JSON. For example:

```sh
curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{
    "provider_id": "openai",
    "model_id": "gpt-5.6-terra"
  }'
```

The server assigns its configured placement, and the selected provider must be
available. The model ID is passed to that provider; it does not need to appear
in the server's curated inventory to be accepted. A provider that is unknown or
unavailable is rejected.

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

See [Drive via gRPC / HTTP](/building/grpc-http.md) for the shared session
lifecycle and [the HTTP/SSE API reference](/reference/http-sse-api.md) for
endpoint details.

## Model inventory and capabilities

`ListModels` and mecatui's `/models` inventory expose public metadata,
including:

- provider ID and opaque model ID;
- display name when available;
- image-input support;
- reasoning support; and
- context limit when known.

The reasoning flag is boolean: `false` can mean that an Anthropic-compatible
model listing did not report thinking support, not that the model explicitly
rejects it. Mecatl keeps a configured reasoning effort when support is unknown;
an explicit unsupported declaration suppresses it. A compatible endpoint can
still reject the request, in which case Mecatl reports the provider error.

These endpoints do not expose API keys or provider-private credentials. The inventory is
server-specific and depends on the providers and credentials configured at
startup. Each `ListModels` request, including client startup and SDK requests,
can refresh available providers after a ten-second per-provider cooldown.
Concurrent requests share a fetch; one provider's cooldown does not prevent
another from refreshing. The request waits up to ten seconds and returns models
and safe provider statuses from one snapshot. There is no periodic refresh or
metadata cache on disk.

The server retains each provider's last non-empty model metadata for its
lifetime, even if a later listing fails, is unauthorized, or returns no models.
The latest status still reports that outcome where provider status is exposed. A
later non-empty listing replaces the retained list. Retained metadata can become
stale; it does not guarantee current model access or context limits.

Models whose catalog includes it can call the read-only `DiscoverModels` tool to
inspect this same resolved inventory. Start without `provider_id` when the
provider is unknown. The first unfiltered result includes every selectable
provider ID and its model count, plus the first bounded model page and enabled
delegation categories under the virtual `model-router` provider. Each category
row uses its exact name as `model_id` and a bounded description, without
disclosing the configured target. You can then search across providers or add
an exact provider filter. A query is a set of
case-lowered literal terms; every term must occur in the provider ID, model ID,
or display name of a result. Punctuation has no special query syntax.

Each result contains the exact `provider_id` plus `model_id` selection handle
and the same safe metadata as `ListModels`; equal model IDs under different
providers remain separate. Output defaults to 20 entries and is capped at 50
entries and 32 KiB. When `next_cursor` is present, call the tool again with only
that value as `cursor`. A changed inventory invalidates the cursor, so restart
without it. The tool does not probe or refresh providers, accept endpoints or
credentials, select or route a model, or change the current session. It remains
available in no-filesystem sessions. Router-category rows are delegation-only:
they do not appear in `ListModels`, `/models`, or root-session creation.

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
- [Start and resume sessions](/features/sessions/start-and-resume-sessions.md)
  for session creation and continuation.
- [Context windows](/features/sessions/context-windows.md) for context limits
  and fallback.
- [Capability and deployment matrix](/features/get-oriented/capability-matrix.md)
  for deployment availability.

<span id="related-topics" />

## Related information

<span id="configure-aliases-slots-and-task-routing" />
<span id="use-a-planning-model-in-plan-mode" />
<span id="diagnose-a-delegated-model-decision" />
<span id="route-openrouter-models-through-preferred-downstreams" />

[Model routing](/features/sessions/model-routing.md)

## Troubleshooting

### A provider error ended a model step

The server retries transient provider failures before meaningful assistant text is
visible. If recovery ends in an error, use `/retry` from an idle `mecatui`
session to ask the server to retry the failed step without duplicating your
prompt. A failure after visible output is terminal and is not automatically
replayed, because the model might otherwise repeat visible text or tool calls.

Each model step defaults to a 30-minute recovery window and at most 60 wrapper
calls, including the initial request. Extra provider calls can be billed even
when Mecatl discards their precommit output. These limits apply separately to
each model step, so they are not a task-wide spending ceiling. Engine token
budgets are checked at turn boundaries, not between wrapper calls within one
step. Prompt-cache retention can end while a model step is recovering; a later
attempt can incur cache-write charges or full input charges. A matching prompt
does not guarantee a cache hit: reuse also depends on the provider's model and
routing, and cache lifetimes vary. A longer cache lifetime may carry a higher
write price. Check the current [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
and [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)
guides for retention and billing details before choosing a cache setting.

For an embedded terminal session, set `--llm-recovery-budget` and
`--llm-max-attempts` when starting `mecatui`. In `connect` mode, the remote
server owns these values. Daemon, Kubernetes, and CI configuration is described
in [Model-call resilience](/features/runtime/observability-and-resilience.md#model-call-resilience).

### A provider request failed

OpenAI Responses, Chat Completions, and Anthropic errors show a failure category
or status, such as `503 Service Unavailable`, instead of the provider's raw error
message. Raw messages can contain reflected credentials or request content. When
an HTTP error includes a sanitized target and request ID, use them to locate the
request in your provider's support tools. Context-window and content-filter
failures retain their specific categories. If a manual Codex token is rejected,
Mecatl shows its local remediation: replace the token in `auth.yaml` and
restart Mecatl.


### Model context metadata is unavailable

You can send the first prompt in a new or resumed session without opening
`/models` first. If the selected model's context window is unknown and its
provider supports discovery, the server starts or joins discovery for that
provider before executing the prompt. Native authenticated providers perform
this listing on demand rather than at startup. Known configured, retained live,
or catalog windows need no listing.

When discovery fails or returns an empty list and no positive window is known,
the server rejects execution with `context_window_unavailable`. Your existing
conversation remains available, and the rejected prompt has not been recorded as
a server turn. Restore provider discovery, or ask the server operator to
configure the model's verified [exact context window](./context-windows.md).
Retry after the ten-second cooldown; another failed attempt requires another
explicit request. Cancelling your wait leaves the server's bounded discovery
attempt running.

In `mecatui`, **Retry** sends the identical prepared text and attachments
without rereading files or the clipboard. **Back** restores the editable draft,
including staged pastes and images, and asks before replacing a newer draft.
Cancelling that confirmation keeps both drafts. **Discard submission** releases
the rejected payload. Recovery holds one submission in client memory under the
existing size limits; accepting the prompt, changing sessions, exiting, or an
unrelated terminal error releases it. There is no automatic replay or recovery
after client restart.

API clients receive gRPC `Unavailable` with ErrorInfo domain
`mecatl.stacklok.com` and reason `context_window_unavailable`, or HTTP 503.
Match the structured reason, not error-message text, and retry explicitly after
addressing discovery. For daemon configuration, see
[context discovery recovery](/operating/mecated/configure-providers-and-storage.md#recover-unavailable-model-context).
