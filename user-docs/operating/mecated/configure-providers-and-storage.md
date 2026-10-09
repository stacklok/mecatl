---
title: Configure mecated providers and storage
description: Select server-side model access and durable session storage.
sidebar_position: 2
---

# Configure mecated providers and storage

Configure the server under its service account, then select durable storage
before admitting sessions. For shared-team deployments, start with
[mecak8s](/operating/mecak8s.md).

## Configure providers

Use [provider setup](/features/sessions/choose-models.md) to select a provider
and enroll its credentials. Run setup under the OS account that will run
`mecated`; the daemon never opens a browser. Remote client login authenticates
the client to the service and leaves server provider setup with its operator.

### Provider credential file

Supply API keys through `OPENAI_API_KEY`, `OPENROUTER_API_KEY`,
`ANTHROPIC_API_KEY`, or `OPENCODE_API_KEY`, or use an operator-owned
`auth.yaml`:

```yaml
providers:
  anthropic:
    api_key: <ANTHROPIC_API_KEY>
  openai:
    api_key: <OPENAI_API_KEY>
  openrouter:
    api_key: <OPENROUTER_API_KEY>
  opencode:
    api_key: <OPENCODE_API_KEY>
  exa:
    api_key: <EXA_API_KEY>
```

A matching API-key environment variable takes precedence over the file entry.
The optional Exa entry authenticates `WebSearch` (paid tier) for `mecated` and
embedded `mecatui`; without it or `EXA_API_KEY`, Exa search stays anonymous.
It does not configure an LLM provider. The default path is `$XDG_CONFIG_HOME/mecatl/auth.yaml`, normally
`~/.config/mecatl/auth.yaml`. `--api-key-file <PATH>` selects another file;
`credential_store.api_key.file` in operator settings can also select the path.

The parser reports unknown providers, fields, duplicate keys, and invalid
credential formats without printing values. A missing conventional file is
accepted; a missing explicitly selected file produces a warning. On Unix, Mecatl
warns when group or other users can read the file. Use mode `0600` and restart
the server after replacing a credential.

The file is plaintext. Another process running under the same OS account can
read it even with mode `0600`, including an authorized agent Shell command. Use
a dedicated service account or stronger sandbox when that access must be
isolated. Keep the file outside project-controlled paths and source control.

### Manual Codex subscription token

The experimental `openai-codex` provider uses a file-only manual token snapshot.
Add this provider entry to the credential file when using it:

```yaml
providers:
  openai-codex:
    oauth:
      access_token: <CHATGPT_CODEX_ACCESS_TOKEN>
      account_id: <ACCOUNT_ID>
      expires_at: <RFC3339_EXPIRY>
```

Replace the expiry placeholder with an RFC3339 timestamp. `account_id` and
`expires_at` are optional when the token contains usable values for those
claims. When both the token and file supply an account ID, they must agree. When
both supply an expiry, the earlier expiry applies.

The daemon reads the snapshot once at startup. This provider has no login or
refresh flow; replace an expired token through your credential-management
process and restart the daemon. The provider setup wizard can reuse a valid
local token for default selection, but never requests, writes, refreshes,
imports, or removes it.

## Operator-defined providers

Custom provider IDs persist with sessions. Removing or renaming a provider makes
those sessions fail instead of selecting another provider. Configure built-in
endpoints under `provider_overrides`; matching `--*-base-url` flags take
precedence. See the [configuration reference](/reference/configuration.md).

If a self-hosted endpoint lacks required Responses API features, use the
`openai-chat-completions` flavor.

Responses providers request a protocol-native prompt-cache breakpoint on every
endpoint, including overridden base URLs, unless `--no-prompt-cache` is set.
Endpoint-specific cache fields depend on the provider and URL. See
[prompt caching](/features/runtime/observability-and-resilience.md#prompt-caching)
for cache controls and Messages-provider behavior.

<span id="context-discovery-recovery"></span>

## Recover unavailable model context

For a provider that supports discovery, the first session prompt starts or joins
listing when the model has no known context window. A failed or empty listing
returns `context_window_unavailable` (HTTP 503 or gRPC `Unavailable`) without
recording the prompt. Native authenticated providers list on demand; starting
the daemon does not authenticate to their model-list endpoint. ToolHive and
required Codex default selection retain their bounded startup probes.

Restore the configured provider's reachability and credentials, then retry after
the ten-second per-provider cooldown. Each ordinary discovery attempt and
admission wait is bounded to ten seconds. Client cancellation ends only that
client's wait; shutdown cancels and joins discovery before closing its
credential resources. ListModels requests can also refresh providers after
cooldown, but opening the picker is not a prerequisite for retry.

If the provider cannot supply metadata, configure a verified window under the
exact provider/model key in operator-global `models.context_windows`, then
restart `mecated` to load the settings. The deployment-wide
`--context-window-override` takes precedence over that map. Use the provider's
actual limit rather than a guessed value to bypass rejection; see
[Context windows](/features/sessions/context-windows.md) for configuration and
precedence. Discovery metadata is process-local and reacquired after restart;
previously successful metadata can remain usable until then even after a listing
failure. It does not establish current inference authorization.

This gate covers Service session entry, including failed-step retry and restored
approval resumption. Direct child, utility, and team engine entry can still use
the 128000 defensive fallback for unknown models. For rejected text and
attachment recovery in `mecatui`, see
[model context troubleshooting](/features/sessions/choose-models.md#model-context-metadata-is-unavailable).

<span id="a-toolhive-managed-llm-gateway-no-api-key-needed"></span>

## Use a ToolHive-managed LLM gateway

If you have configured access to an LLM gateway through
[ToolHive](https://docs.stacklok.com/toolhive/), `--toolhive-llm` (on by
default) detects that configuration and registers two provider IDs: `toolhive`
uses OpenAI Responses, while `toolhive-anthropic` uses native Anthropic
Messages. ToolHive manages the gateway credentials. A connected `mecatui` client
can use `/models` to select either provider. Disable detection with
`--toolhive-llm=false` on shared hosts.

`--toolhive-llm-mode` selects how both providers reach the gateway:

- `auto` uses direct mode when ToolHive has an HTTPS gateway URL, issuer, and
  client ID; otherwise it uses the local `thv llm proxy`.
- `direct` requires that complete configuration and refreshes the OIDC token in
  process.
- `proxy` requires a running local proxy and supports self-signed gateways.

Run `thv llm setup` before using direct mode. `mecated` reads ToolHive's
encrypted credentials, including THVSEC v1 files written by ToolHive v0.50.0. It
does not open a browser when credentials are missing. Direct mode does not honor
`tls_skip_verify`.

|Flag|Default|Purpose|
|-|-|-|
|`--toolhive-llm`|`true`|Detect ToolHive's local LLM configuration. Set it to `false` on a shared host where this process must not use another operator's configuration.|
|`--toolhive-llm-base-url`|`""`|Use an explicit loopback proxy URL. This always selects proxy mode.|
|`--toolhive-llm-mode`|`auto`|Use `direct` when the HTTPS gateway URL and OIDC settings are complete; otherwise use the loopback proxy. Set `proxy` or `direct` to require one path.|

For provider selection, protocol paths, independent catalog status, and
model-routing troubleshooting, see
[Choose models and providers](/features/sessions/choose-models.md#set-up-a-local-provider).

## Persistence

By default, sessions live in memory and disappear when the server restarts.

Enable JSONL persistence by pointing `--store-dir` at a directory:

```sh
mecated serve --store-dir /var/lib/mecatl/sessions
```

The store writes current snapshots, tool-call audit, events, inventory, and
lineage beneath `sid-v1`. Files at the addressed current paths must use the
current format; malformed or incompatible content fails validation without being
overwritten. Distinct root-level artifacts are not listed or loaded. Files are
plaintext and owner-only; do not edit or share them. See
[Session store](/building/go/extension-points/session-store.md) for the layout
and durability guarantees.

:::note[Kubernetes and persistent volumes]

If you run `mecated` in Kubernetes with `--store-dir`, you need a
PersistentVolume backed by ReadWriteOnce (or ReadWriteMany for multi-replica
with affinity routing). If a PVC is a hard constraint, use `mecak8s` instead.
When configured, its Redis-backed store has no PVC requirement.

:::

Configure retention in the operator `settings.yaml`. Main-session deletion is
off by default and requires `acknowledge_main_deletion: true` when enabled.
Follow [Operate local session storage](/operating/session-storage-operations.md)
for the schema, service definitions, backups, cleanup, and restore.

`--session-store-url` replaces the local store with a gRPC driver and cannot be
combined with `--store-dir`. Session and memory drivers must negotiate Mecatl's
current contract at startup; old or partially implemented peers are rejected.
Optional session operations such as listing, metadata paging, deletion, lineage,
atomic create, and activity projection remain capability-gated. Remote driver
operators own their backing namespace and upgrade policy: Mecatl does not scan,
adopt, migrate, or reject unrelated old driver artifacts. Malformed data
returned from the selected current namespace fails closed. Current remote
drivers do not support OIDC caller ownership; use local JSONL or `mecak8s` for
multi-user deployments.

`--learning-store-url` selects a trusted single-tenant driver for distributed
learning. Startup rejects partial driver support and deployments with OIDC
caller ownership.

## Import from Codex or Claude Code

`mecated import` creates a resumable Mecatl session from a local Codex or Claude
Code transcript. It can also copy project files and skills into a workspace.

Codex stores active session rollouts under `~/.codex/sessions/`; Claude Code
stores project transcripts under `~/.claude/projects/`. Select the JSONL session
you want and use the same `--store-dir` when starting the server:

```sh
mecated import \
  --from codex \
  --session <CODEX_SESSION_JSONL> \
  --store-dir ~/.local/share/mecatl/sessions \
  --workspace ~/work/imported-project \
  --copy-files \
  --skills

mecated serve \
  --store-dir ~/.local/share/mecatl/sessions \
  --workspace ~/work/imported-project \
  --skills-dir ~/work/imported-project/.mecatl/skills
```

For Claude Code, use `--from claude-code` with a transcript under
`~/.claude/projects/`. Imported history keeps user and assistant text but omits
provider-private reasoning, instructions, and tool activity. The new session is
idle and uses the provider and model selected when you resume it.

Only use `--skills` or `--skills-dir` with trusted sources because imported
skills steer the model. `--copy-files` requires `--workspace` and skips `.git`,
symlinks, and special files. Mecatl never overwrites existing sessions, files,
or skill names. Imported data remains local in the plaintext JSONL store.

Use `--source-workspace` when the transcript's recorded working directory has
moved. Use `--id` to choose a different session ID after a collision.

## Next steps

- [Operate the instance](/operating/mecated/operate-instance.md).
- [Secure caller access](/operating/mecated/secure-and-expose.md).
