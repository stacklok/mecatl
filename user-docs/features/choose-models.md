---
sidebar_position: 110
title: Choose models and providers
description: Select the provider, model, and reasoning effort for a Mecatl session.
---

# Choose models and providers

A Mecatl session runs with a provider and a base model selected by the server
and, optionally, by the client. The server returns the effective selection and
input capabilities when the session is created.

The choice depends on how you use Mecatl, so this page separates three
journeys:

- the **mecatui journey** for interactive selection;
- the **CLI journey** for configuring a server or one-shot run; and
- the **API journey** for clients that create sessions directly.

For the rest of the terminal workflow, see [Use mecatui](./use-mecatui.md).

## Mecatui journey

When the connected server advertises model selection, type `/models` in
mecatui. Filter the server's inventory, select a model, and press `enter`. The picker
non-blockingly warns that the choice creates a new session, carries visible
conversation/context, and may make a long history costly to replay. Mecatui keeps the
visible conversation by creating a peer session seeded with its history; it does not
change the provider or base model of the existing session in place.

A switch across providers keeps the visible conversation but drops provider-
private replay state, such as reasoning state that the new provider cannot
understand. The new session's provider, model, and capabilities are reported by
the server.

When broker OAuth is enabled, this peer is also a new broker session. Protected
MCP enrollment is session-scoped, so switching models may require enrolling the
protected backends again; authorization is not silently copied from the old
session.

Type `/effort` to choose a reasoning-effort tier. Mecatui applies a changed
tier by creating a peer session with the same provider, model, and conversation.
The picker is available only when the connected server advertises the relevant
capability.

Mecatui's model choices are server-backed. In embedded mode, the local server's
configuration and credentials determine the inventory. In `connect` mode, the
remote server determines it; local embedded-server flags and credentials do not
apply.

### Set up a local embedded provider

On Linux, run the explicit, terminal-only setup command to add or replace a
provider key, choose the embedded server's default provider and model, or remove
a saved key:

```sh
mecatui llm setup
```

This is a line-oriented CLI flow, not a first-run wizard: starting `mecatui`
never launches it automatically. Credential and settings writes are unsupported
on other operating systems because the local writer cannot prove the required
filesystem properties there. On macOS and other unsupported systems, configure
the environment or files manually as described in the
[settings guide](/building/deployment/settings.md#configure-provider-credentials).

Setup can write API keys for the `openai`, `anthropic`, `openrouter`, and
`opencode` built-ins. It can also write a key for an existing custom `providers:`
entry whose authentication method is `api_key`; it does not create custom provider
definitions. Providers configured with no authentication, `openai-codex`, and
OAuth-shaped records are not API-key choices.

Before hidden key entry, the command identifies the provider console and explains
that you need an API/developer key rather than a consumer subscription. Confirming
a write stores the key as owner-only plaintext in the existing `auth.yaml`. Other
processes running as the same operating-system user can read that file, as can an
enabled agent Shell running under that user. Use a separate OS identity or another
credential delivery mechanism if that custody is too broad. API use may incur
provider charges.

Existing startup precedence does not change. A built-in provider's matching
environment variable wins over its file entry. OpenRouter checks
`OPENROUTER_API_KEY` first and retains its environment-only `OPENAI_API_KEY`
compatibility fallback; a custom provider key is file-only. Setup reports when an
environment credential is active and does not copy it into the file. If you choose
to replace a shadowed file key, the environment value continues to win.

Use a specific credential file when needed:

```sh
mecatui llm setup --auth-file "$HOME/.config/mecatl/work-auth.yaml"
mecatui llm status --auth-file "$HOME/.config/mecatl/work-auth.yaml"
```

An explicit missing auth file requires confirmation before setup creates only the
file in an existing protected parent directory. It does not recursively create a
custom parent. If setup offers to start `mecatui` after a successful change, that
start carries the same `--auth-file` path and re-reads normal settings; it does not
silently return to the conventional file.

Setup confirms credential and default changes separately because `auth.yaml` and
`settings.yaml` cannot be committed as one portable transaction. It prints one of
these outcomes for each attempted write:

- `no_op`: the requested value was already effective in that file;
- `durable`: the file replacement and directory sync completed;
- `not_applied`: the invocation did not commit the replacement; or
- `replacement_applied_durability_unknown`: replacement occurred, but crash
  durability could not be confirmed.

On either error outcome, setup stops without retrying or rolling back. A credential
may therefore be durable even when a later default-setting write fails. Run passive
status after resolving the filesystem condition rather than assuming both files
changed together.

Removing a saved key does not revoke it at the provider, and an environment
credential may remain active. If removal would leave the selected default without
a credential, setup first requires a separately confirmed replacement default and
commits it before attempting key removal.

Inspect local configuration without entering setup:

```sh
mecatui llm status
```

No-target status reports built-in and configured providers, credential provenance
and a shadowed-file presence flag, the selected default and model selector, native
endpoint names, and the ToolHive handoff. It never prints key values or
fingerprints. Every row says `verification: not checked`: status makes no network
request, refresh, browser launch, model-list check, or paid inference, so it cannot
prove that a key or model is accepted.

Native endpoint OIDC and ToolHive keep separate custody. Selecting a configured
native endpoint in setup asks before handing off to the existing in-process
`mecatui llm login ENDPOINT` operation; an active `--auth-file` override rejects
that handoff because native credentials do not use `auth.yaml`. ToolHive remains
owned by `thv llm` tooling. Endpoint-specific native and ToolHive status likewise
do not accept `--auth-file`.

The model list in setup is a bounded convenience, not a health check. Manual
entry is always available, including when suggestions exist, but the existing
embedded-startup validator still requires a catalogued model, that provider's
declared default, or a configured alias resolving to one of those. Setup refuses
to persist an unknown deployment default that ordinary embedded startup would
reject. `verification: not checked` means setup and status do not authenticate to
the provider; it does not mean arbitrary provider or model identifiers are accepted.
Active provider/model verification, an automatic first-run offer, OpenRouter OAuth
or key minting, and Gemini onboarding are not part of this flow.

See [Configure Mecatl](/building/deployment/settings.md) for the file locations and
manual configuration schema.

See [Use mecatui](./use-mecatui.md) for the command-line startup, connection,
and keybinding details.

## CLI journey

### Endpoint overrides

The built-in provider endpoint flags (`--openai-base-url`, `--openrouter-base-url`,
`--anthropic-base-url`, and `--opencode-base-url`) are non-secret command configuration.
They override the matching operator `provider_overrides` setting; settings override the
built-in endpoint. Custom provider URLs remain defined only by their provider definition.
OpenAI and Anthropic keep their SDK endpoint when neither source supplies an override.

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

For a zero-selector session, server-side resolution is separate for provider
and model:

- provider: `--default-provider`, otherwise the automatic available-provider
  preference;
- model: `--model`, then `--default-model`, then the selected provider's
  built-in default.

`--model` is a higher-priority deployment override. `--default-model` is the
validated default for the configured default provider. An invalid deployment
default fails startup rather than silently selecting a different provider or
model.

`mecatui` accepts these flags for its embedded server. They do not reconfigure a
server used through `mecatui connect`. `mecak8s` exposes the corresponding server
configuration. See the [operator provider and model reference](/building/deployment/mecated.md#provider-and-model)
for credential sources and deployment options.

### Operator-defined gateways

An operator can declare a named HTTPS gateway in the user-global `settings.yaml` under
`providers:` and make it the deployment default with `models.default_provider`. API-key
gateways use the matching provider ID in the operator-local `auth.yaml`; credentials are
never read from a project file or supplied by `mecatui connect`. The server snapshots these
settings and credentials once while it starts, so changing either file requires a restart.
When a custom provider's live model listing is unreachable, unauthorized, or empty,
`/models` keeps its configured default model selectable and displays only a safe
provider status; endpoints, credentials, and raw listing errors or response bodies
are never published to clients. Built-in `--*-base-url` flags still take precedence over eligible built-in endpoint overrides.
See the [provider configuration reference](/reference/configuration.md#providers) for the accepted flavors and fields.

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
        description: Multi-file implementation, integration, and substantial tests.
        model: coder
      - name: small
        description: Focused edits, known fixes, and quick lookups.
        model: quick
      - name: image
        description: Work requiring visual input.
        model: image
```

These mechanisms are independent:

- **Aliases** map readable names to concrete provider-specific model IDs.
- **Slots** select models for internal calls. `compaction`, `ask-reviewer`,
  `guardrail`, `plan`, and `router` do not replace the session model. The `plan`
  slot can use a stronger model while a plan is being written; compaction and
  checker slots can use cheaper models.
- **`title` is an explicit opt-in slot** for automatic session-title generation. It
  has no fallback at all: if the binding is absent, or if it is present but cannot
  be resolved for the session's fixed provider, generation is disabled and the
  server makes no title-provider call. This differs from other invalid slot or
  route targets, which may warn and fall back to the session model. With a
  compatible `title` binding, the server generates a title asynchronously from up
  to three early genuine prompts; it never delays or changes the chat. Its token
  usage is stored separately as `session_title`, not charged to the chat's
  displayed usage or run budget.
- **Router categories** select a model for a plain delegated Subagent, an unpinned
  named specialist (including `mode: "read-write"`), a Parallel branch, or an undefined
  team member from the task description. A taxonomy enables the router; with no taxonomy,
  delegation keeps its inherited/default model.

With the example above, routing resolves as:

```text
large  → heavy  → gpt-5.6-terra
medium → coder  → gpt-5.6-luna
small  → quick  → gemini-3.5-flash
image  → image  → gpt-5.6-terra
```

Resolution is fail-soft for slots and routes other than `title`: an invalid alias,
slot, or route target warns and falls back to the session model. The `title` slot is
  the exception described above; an absent or unresolvable title binding disables
  generation rather than falling back or making a provider call. Explicit per-call
  models, model-pinned named agents, fork or resume choices, and other higher-
  precedence selectors are not overridden by the router. A named definition with no
  `model:` is routable; `model: inherit` is an explicit pin. Writable named routing
 keeps the specialist's direct-write scope, while explicit `read-write`+`agent`+`model`
 remains invalid. Model slots and router taxonomies are operator decisions; project
 model settings are ignored unless the operator explicitly allows the relevant model
 set on a trusted project via `models.allowlist`.

An allowlisted model is not scoped to a particular use: a trusted project can bind any
allowlisted model to any slot, including the `guardrail` and `ask-reviewer` safety
checkers, not just the session default. Do not allowlist a model you would be
unwilling to see used as a safety checker.

This configuration belongs in the operator-global settings file, not a checked-in
project file. See the [configuration reference](/reference/configuration.md#models)
for the complete field schema and defaults.

### Route OpenRouter models through preferred downstreams

OpenRouter can serve one model through several downstream inference providers. By
default, it balances among them by price. An operator can instead set a preferred
order for each model in the operator-tier `settings.yaml`:

```yaml
openrouter:
  models:
    "anthropic/claude-sonnet-4-6":
      order: ["anthropic", "google-vertex"]
      allow_fallbacks: false
    "openai/gpt-5":
      order: ["deepinfra/turbo"]
```

`order` accepts lowercase-kebab downstream slugs and disables OpenRouter's default
price balancing. An absent `allow_fallbacks` keeps OpenRouter's default (`true`), so
it may try other downstreams after exhausting the list. Setting it to `false` pins
the request to the listed downstreams and can fail the turn when none are available.

This configuration is operator-tier only because it controls spend, compliance,
and capabilities. Mecatl ignores a project-tier `openrouter` block with a warning.
Invalid slugs and empty orders are also dropped with a warning.

For each OpenRouter turn, Mecatl reports the selected downstream as a
`provider.route` event when OpenRouter supplies that metadata. The value may be
absent on a cache hit. It is OpenRouter's display name, such as `Google`, not the
configuration slug such as `google-vertex`, so treat it as human-readable status
rather than a round-trippable identifier.

See the [configuration reference](/reference/configuration.md#openrouter) for the
full field schema.

### Run one shot with mecatequi

`mecatequi` creates a new session for one prompt. It accepts model/provider
defaults and reasoning-effort settings, but has no interactive model picker.
Use it when the caller already knows the deployment and model configuration.

### Configure reasoning effort

The accepted reasoning-effort values are:

```text
auto, low, medium, high, xhigh, max
```

`reasoning-effort` may be set as a server default or supplied per session.
The important distinction is:

- omitted effort uses the server's configured default, or the provider default
  when no server default exists;
- explicit `auto` requests the provider's default effort; and
- a valid non-empty per-session value overrides the server default.

An invalid server value is ignored with a warning and becomes unset. An
invalid per-session value is ignored with a warning and falls back to the
server default. The server may normalize, clamp, or drop a value according to
the selected provider and known model capabilities.

The effective result is returned in `resolved_model.reasoning_effort`, so clients
can display what the server actually applied. Provider-specific effort mapping
belongs in the [configuration reference](/reference/configuration.md#reasoning-effort),
not in the selection workflow.

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

| `provider_id` | `model_id` | Result |
| --- | --- | --- |
| omitted | omitted | Use the server-resolved provider and model. |
| set | omitted | Use that provider's own default model. The server's `--default-model` does not carry across to a different explicitly selected provider. |
| set | set | Use that provider and pass the model ID through to it. An uncatalogued model may be accepted and fail later at the provider. |
| omitted | set | Reject the request: a bare model ID is ambiguous. |
| unknown or unavailable | any | Reject the request; do not silently fall back to another provider. |

A bare `model_id` returns HTTP 400 or gRPC `InvalidArgument`. The same applies
to an unknown or unavailable provider. The API returns the new session ID and
resolved model information after successful creation.

See [Drive via gRPC / HTTP](/building/deployment/grpc-http.md) for the shared session
lifecycle and [the HTTP/SSE API reference](/reference/http-sse-api.md)
for endpoint details.

## Model inventory and capabilities

`ListModels` and mecatui's `/models` inventory expose public metadata, including:

- provider ID and opaque model ID;
- display name when available;
- image-input support;
- reasoning support; and
- context limit when known.

They do not expose API keys or provider-private credentials. The inventory is
server-specific and can differ according to the providers and credentials
configured at startup. A provider's live model catalog may refresh while the
server is running.

Models whose catalog includes it can call the read-only `DiscoverModels` tool to
inspect this same resolved inventory. Results contain the exact `provider_id` plus
`model_id` selection handle and the same safe metadata as `ListModels`; equal model
IDs under different providers remain separate. Exact provider/model filters are
supported. Output defaults to 20 entries and is capped at 50 entries and 32 KiB.
The tool does not probe providers, accept endpoints or credentials, or change the
current session, and remains available in no-filesystem sessions.

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

- [Use mecatui](./use-mecatui.md) for the interactive model and effort pickers.
- [Start and resume sessions](./start-and-resume-sessions.md) for session
  creation and continuation.
- [Context windows](./context-windows.md) for context limits and fallback.
- [Capability and deployment matrix](./capability-matrix.md) for deployment
  availability.
