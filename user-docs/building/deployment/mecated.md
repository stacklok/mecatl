---
sidebar_position: 20
title: Run mecated standalone
description:
  Run the standalone Mecatl server with providers, persistence, security, and
  observability.
---

# Run mecated standalone

`mecated` runs Mecatl as a standalone service over gRPC and HTTP/SSE. It
provides the operator controls needed for authentication, TLS/mTLS, rate
limiting, observability, persistence, and graceful shutdown.

## Configure providers

Configure providers from local embedded `mecatui`. These are operator settings
for the embedded server or `mecated`, not for a connected client.

```sh
mecatui providers setup
mecatui providers
```

|Command|Purpose|
|-|-|
|`mecatui providers setup`|Run the guided provider setup.|
|`mecatui providers add NAME`|Define a custom HTTPS provider, API flavor, default model, and authentication.|
|`mecatui providers login NAME`|Enter or replace the provider's credential.|
|`mecatui providers logout NAME`|Remove its local OIDC enrollment.|
|`mecatui providers set-default NAME [MODEL]`|Select the deployment default. A supplied concrete model ID is saved without a live inventory check; availability is verified when the provider is used.|
|`mecatui providers remove NAME`|Remove a custom provider.|
|`mecatui providers` or `mecatui providers status`|Show configuration state without credentials.|

Custom definitions live under `providers` in operator `settings.yaml`. OIDC
providers share `credential_store.oidc`:

```yaml
providers:
  corp:
    base_url: https://gateway.example/v1
    default_model: corp-model
    api_flavor: openai-responses
    auth:
      method: oidc
      oidc:
        issuer: https://issuer.example
        client_id: mecatl
        scopes: [openid, offline_access]
        issuer_trust: { policy: public }
        gateway_trust: { policy: public }
credential_store:
  oidc:
    home: /home/operator/.local/state/mecatl/provider-oidc
    key:
      source: keyring
```

`auth.method` accepts `api_key`, `oidc`, or `none`. OIDC requires the
`openai-responses` flavor and `credential_store.oidc`. To supply the encryption
key through the environment, set `key.source: environment` and `key_env`. See
the [configuration reference](/reference/configuration.md#providers) and
[credential-store reference](/reference/configuration.md#credential_store).

Supply API keys through the environment or a provider-credentials YAML file.
Pass `--api-key-file PATH` to `mecatui` or `mecated`, or set
`credential_store.api_key.file` in operator settings:

```yaml
providers:
  corp:
    api_key: <API_KEY>
```

Do not put secrets in command-line arguments, settings YAML, prompts, or logs.

`mecated` never opens a browser. Log in through local embedded `mecatui` before
starting or restarting it. `mecatui login ADDRESS` instead enrolls the client
with a remote server and does not configure that server's providers.

ToolHive owns its LLM credentials. Use `thv llm` for setup and credential
management; Mecatl only uses its configured or discovered gateway.

If you are deploying to Kubernetes without persistent volumes, see
[mecak8s](/building/deployment/mecak8s.md) instead. That binary is purpose-built
for no-PVC pod deployments, with Redis-backed state when you configure
`--redis-url` and Kubernetes Lease coordination enabled by default.

---

## Quick start

Install with `brew install stacklok/tap/mecatl`. Then start the server:

```sh
mecated serve
```

A command word is required: `mecated serve` for the network daemon,
`mecated acp` for the ACP stdio mode. Bare `mecated` prints the command help and
exits with a usage error.

The minimal invocation starts a loopback-only server with in-memory sessions and
no authentication.

Authorize a global MCP OAuth profile before serving with
`mecated mcp login SERVER [--no-browser]`. The daemon restores and refreshes the
encrypted credential but never opens a browser. See
[MCP client](/building/what-you-get/mcp-client.md).

Default addresses:

|Listener|Default|
|-|-|
|gRPC|`127.0.0.1:8080`|
|HTTP/SSE|`127.0.0.1:8081`|
|Prometheus + admin|`127.0.0.1:9090`|

A slightly more configured invocation for unattended local operation:

```sh
export MECATL_AUTH_TOKEN="$(cat ~/.mecatl/token)"
mecated serve \
  --store-dir ~/.local/share/mecatl/sessions \
  --posture auto
```

`--log-level` accepts `debug`, `info` (default), `warn`, or `error`. Startup
logs the binary version with `msg="mecated starting"`.

`--store-dir` enables local JSONL persistence. The path and its ancestors must
be physical directories, not symlinks. On macOS, use `/private/...` instead of a
path through the `/var` symlink. `--auth-token` requires the token on every
request and can also read `MECATL_AUTH_TOKEN`. `--posture auto` allows
unattended runs while retaining child prompt-injection protections.

Before binding a non-loopback address, add TLS and caller authentication. See
[The trust model](#the-trust-model).

### Server-owned session placement

`--workspace` sets the server's default workspace. Clients can request the
default or the `no-fs` profile, but cannot submit a path. `ListWorktrees`
returns short-lived selectors for `ClearSession` and `ForkSession`; selectors
expire when the server restarts. Mecatl stores the exact placement privately and
reattaches it before each run. See
[Execution environments](/features/execution-environments.md) for the shared
placement and reattachment model. For the underlying design, see
[ADR 0291](https://github.com/stacklok/mecatl/blob/main/docs/adr/0291-server-owned-session-placement.md).

## Operator-defined providers

Custom provider IDs persist with sessions. Removing or renaming a provider makes
those sessions fail instead of selecting another provider. Configure built-in
endpoints under `provider_overrides`; matching `--*-base-url` flags take
precedence. See the [configuration reference](/reference/configuration.md).

If a self-hosted endpoint lacks required Responses API features, use the
`openai-chat-completions` flavor.

The built-in `openai` and `openrouter` providers send prompt-cache hints only
through their canonical base URLs. A base URL override disables these hints so a
compatible endpoint cannot reject an unsupported cache field.

---

## Flag reference

Flags are grouped by area. All have zero-value defaults that produce a working
loopback-only server. Flags not covered here are advanced operator tuning; run
`mecated serve --help` for common flags grouped by task, or
`mecated serve --help-all` for the exhaustive reference.

### Server

|Flag|Default|Notes|
|-|-|-|
|`--grpc-addr`|`127.0.0.1:8080`|gRPC listen address|
|`--http-addr`|`127.0.0.1:8081`|HTTP/SSE listen address. An empty value disables this and the admin listener; see [Hosting a spawned daemon](#hosting-a-spawned-daemon)|
|`--metrics-addr`|`127.0.0.1:9090`|Prometheus + admin listener; empty disables it|
|`--grpc-unix-socket`|`""` (off)|Serve gRPC on a UNIX-domain socket instead of a TCP port. Mutually exclusive with a configured `--grpc-addr`. See [Hosting a spawned daemon](#hosting-a-spawned-daemon)|
|`--ready-file`|`""` (off)|Absolute path to write a JSON readiness document to, atomically, once every listener is up|
|`--lifetime-pipe-fd`|`0` (off)|File descriptor of an inherited pipe read end or connected UNIX-domain stream socketpair endpoint; EOF on it stops the daemon gracefully (the parent-crash path)|
|`--lifetime-stdin`|`false`|Use piped stdin for parent liveness; EOF stops the daemon gracefully. Mutually exclusive with `--lifetime-pipe-fd`|
|`--auth-token`|`""` (off)|Bearer token required on every request; also `MECATL_AUTH_TOKEN`|
|`--tls-cert`|`""`|PEM server certificate; enables TLS on both listeners when paired with `--tls-key`|
|`--tls-key`|`""`|PEM server private key|
|`--client-ca`|`""`|PEM client-CA bundle; enables mTLS (requires `--tls-cert`/`--tls-key`)|
|`--cors-origins`|`""` (off)|Allow an exact browser origin to call the HTTP API; repeatable. See [Browsers and CORS](#browsers-and-cors)|
|`--deployment-id`|`""`|Optional opaque label for this deployment, echoed on `GetCompatibilityInfo`. Never inferred from the host|
|`--rate-limit`|`0` (off)|Sustained per-client request rate in req/s; with OIDC, also limits rejected bearer validation per direct transport peer IP|
|`--rate-burst`|`0` (derived)|Token-bucket burst; zero derives a sane default from `--rate-limit`, including the OIDC rejected-token bucket|
|`--oidc-issuer`|`""` (off)|OIDC issuer URL whose tokens identify callers; setting it turns caller identity on. See [Caller identity](#caller-identity-oidc)|
|`--oidc-jwks-uri`|`""` (derived)|Static JWKS endpoint, short-circuiting OIDC discovery (air-gapped or pinned-key deployments)|
|`--oidc-audience`|`""`|Audience (`aud`) this deployment accepts; **required** with `--oidc-issuer`|
|`--oidc-max-jwks-staleness`|`1h`|Maximum age of last-good signing keys during an IdP outage. `0` deliberately disables this bound; negative values are rejected.|

#### Caller identity (OIDC)

`--oidc-issuer` names the identity provider whose tokens identify your callers.
Every request must then present a bearer token, and `--oidc-audience` is
required. Mecatl records the verified `(issuer, subject)` pair as the owner of
sessions, schedules, teams, and memory records. This ownership does not isolate
a shared workspace; separate working files at the deployment layer.

Startup fails when the initial signing-key fetch fails. Afterward, cached keys
remain valid for `--oidc-max-jwks-staleness` (default `1h`). A failed refresh
after that bound returns 503; a rejected token returns 401. Setting the bound to
`0` accepts cached keys regardless of age.

With `--rate-limit`, rejected tokens use a separate bucket keyed by the direct
peer IP and return 429 when exhausted. Mecatl does not trust forwarded-IP
headers for this check. Authentication logs omit credentials, tokens, claims,
and caller identifiers. See
[Caller identity and OIDC](/features/caller-identity.md) for the full behavior.

#### Daemon config file (`daemon.yaml`)

Put listener addresses, TLS files, and rate limits in `daemon.yaml` when you do
not want to repeat flags. This is separate from the policy and provider settings
in `settings.yaml`. Mecatl loads it only when you pass `--config`:

```sh
mecated config daemon init                          # write the conventional skeleton
mecated config daemon init --print                  # print it to stdout, no file
mecated config daemon validate                      # validate the conventional path
mecated config daemon validate --file /etc/mecatl/daemon.yaml
mecated serve --config ~/.config/mecatl/daemon.yaml # start with it
```

The strict v1 schema rejects unknown keys. Explicit flags override file values,
including empty values and zero. Keep the bearer token in `MECATL_AUTH_TOKEN` or
`--auth-token`; `daemon.yaml` does not accept it. Validation prints neither
secrets nor file contents.

### Session state

|Flag|Default|Notes|
|-|-|-|
|`--store-dir`|`""` (in-memory)|Directory for the JSONL session store. Empty = in-memory, no persistence across restart|
|`--session-lease-dir`|`""`|Single-host flock lease backend; see [multi-replica](#multi-replica)|
|`--session-lease-k8s-namespace`|`""`|k8s `coordination.k8s.io` Lease backend; see [multi-replica](#multi-replica)|
|`--session-lease-ttl`|`30s`|Lease lifetime; a crashed holder's lease becomes claimable after this long|
|`--session-store-url`|`""`|gRPC driver endpoint replacing the local JSONL store (mutually exclusive with `--store-dir`)|

### Scheduled tasks

|Flag|Default|Notes|
|-|-|-|
|`--no-scheduler`|`false`|Disable firing due schedules. The management API remains available|
|`--scheduler-tick-interval`|`30s`|How often the tick loop polls for due schedules|
|`--scheduler-min-interval`|`1m`|Minimum schedule frequency; `0` disables the floor|
|`--scheduler-max-concurrent-fires`|`4`|Max schedules fired in parallel per tick|
|`--schedule-store-url`|`""`|gRPC driver for the schedule registry, independent of the session store. Not supported with OIDC ownership|
|`--learning-store-url`|`""`|Trusted single-tenant gRPC driver for distributed learning. Not supported with OIDC ownership|

See [Scheduled tasks](/building/what-you-get/scheduled-tasks.md) for the in-chat
`Schedule` tool and the gRPC/REST management APIs.

### LLM resilience

|Flag|Default|Notes|
|-|-|-|
|`--llm-recovery-budget`|`30m`|Maximum time recovering one precommit model step after its first retryable failure or breaker rejection. `0` disables additional waiting|
|`--llm-max-attempts`|`60`|Maximum model-stream attempts for one precommit step, including the initial call|
|`--llm-per-attempt-timeout`|`300s`|Bounds connection and the first raw chunk. It does not interrupt an active stream|
|`--llm-stream-idle-timeout`|`180s`|Maximum gap between raw chunks after activity starts. A precommit stall can recover; a visible stream failure is terminal|
|`--llm-breaker-threshold`|`5`|Consecutive transient establishment failures that open the circuit breaker; `0` disables it|
|`--llm-breaker-cooldown`|`30s`|How long the breaker remains open before one half-open probe|

The server applies this policy to each model step. It retries only before semantic output becomes visible, so it does not replay completed tool calls or visible assistant text. These command-line flags are the only recovery-policy configuration; `settings.yaml` has no equivalent key.

### Provider and model

|Flag|Default|Notes|
|-|-|-|
|`--model`|`""`|Model id sent to the provider; empty uses the provider-appropriate default|
|`--default-provider`|`""`|Deployment-wide default provider (`openai`, `openrouter`, `anthropic`, `opencode`); validated fail-fast|
|`--default-model`|`""`|Deployment-wide default model id for the default provider; validated fail-fast|
|`--subagent-model`|`""`|Global default model for child engines (Subagent, Parallel branches, team members) that do not pin their own|
|`--no-prompt-cache`|`false`|Disable provider-side prompt caching; see [ADR 0100](https://github.com/stacklok/mecatl/blob/main/docs/adr/0100-provider-prompt-caching.md)|
|`--anthropic-cache-ttl`|`""` (API default, `5m`)|TTL on every Anthropic ephemeral cache breakpoint: `5m` or `1h`|
|`--mock`|`false`|Offline canned provider with one text turn; smoke tests only|
|`--mock-script`|`""`|Path to a strict JSON mock script; implies the offline provider and replaces its canned turn with ordered text/tool-call turns|

Provider credentials are read from `OPENAI_API_KEY`, `OPENROUTER_API_KEY`,
`ANTHROPIC_API_KEY`, or `OPENCODE_API_KEY`, never flag values. You can instead
use an owner-only `auth.yaml` file. See
[Configure provider credentials](./settings.md#configure-provider-credentials).

An active `models.router.backend: jev` reads `TYPESAFE_API_KEY` from the process
environment. This router credential has no `auth.yaml` or command-line form. It
is inert unless an operator taxonomy selects the Jev backend.

The experimental `openai-codex` provider uses a manually supplied ChatGPT Codex
token and has no login or refresh flow. See the same credential guide for its
schema and lifecycle.

#### Context discovery recovery

For a provider that supports discovery, the first session prompt starts or joins
listing when the model has no known context window. A failed or empty listing returns
`context_window_unavailable` (HTTP 503 or gRPC `Unavailable`) without recording the
prompt. Native authenticated providers list on demand; starting the daemon does
not authenticate to their model-list endpoint. ToolHive and required Codex default
selection retain their bounded startup probes.

Restore the configured provider's reachability and credentials, then retry after
the ten-second per-provider cooldown. Each ordinary discovery attempt and admission
wait is bounded to ten seconds. Client cancellation ends only that client's wait;
shutdown cancels and joins discovery before closing its credential resources.
ListModels requests can also refresh providers after cooldown, but opening the
picker is not a prerequisite for retry.

If the provider cannot supply metadata, configure a verified window under the exact
provider/model key in operator-global `models.context_windows`, then restart
`mecated` to load the settings. The deployment-wide `--context-window-override`
takes precedence over that map. Use the provider's actual limit rather than a guessed
value to bypass rejection; see [Context windows](/features/context-windows.md) for
configuration and precedence. Discovery metadata is process-local and reacquired
after restart; previously successful metadata can remain usable until then even
after a listing failure. It does not establish current inference authorization.

This gate covers Service session entry, including failed-step retry and restored
approval resumption. Direct child, utility, and team engine entry can still use the
128000 defensive fallback for unknown models. For rejected text and attachment
recovery in `mecatui`, see [model context troubleshooting](/features/choose-models.md#model-context-metadata-is-unavailable).

#### Offline mock providers (no credentials)

`--mock` provides one canned text response for offline smoke tests.
`--mock-script PATH` instead loads ordered text and tool-call turns from a
strict JSON file. Both start without provider credentials and fail before
binding a listener when the script is invalid.

```json
{
  "turns": [
    {
      "tool_calls": [
        {
          "id": "write-1",
          "name": "Write",
          "args": { "path": "proof.txt", "content": "ok\n" }
        }
      ]
    },
    { "text": "continued after the tool" }
  ]
}
```

#### A ToolHive-managed LLM gateway (no API key needed)

If you have configured access to an LLM gateway through
[ToolHive](https://docs.stacklok.com/toolhive/), `--toolhive-llm` (on by
default) detects that configuration and registers two provider IDs: `toolhive`
uses OpenAI Responses, while `toolhive-anthropic` uses native Anthropic
Messages. ToolHive manages the gateway credentials. Use `/models` to select
either provider; the `mecatui` welcome screen reports when one is available but
not selected. Disable detection with `--toolhive-llm=false` on shared hosts.

`--toolhive-llm-mode` selects how both providers reach the gateway:

- `auto` uses direct mode when ToolHive has an HTTPS gateway URL, issuer, and
  client ID; otherwise it uses the local `thv llm proxy`.
- `direct` requires that complete configuration and refreshes the OIDC token in
  process.
- `proxy` requires a running local proxy and supports self-signed gateways.

Run `thv llm setup` before using direct mode. `mecated` reads ToolHive's
encrypted credentials, including THVSEC v1 files written by ToolHive v0.50.0.
It does not open a browser when credentials are missing. Direct mode does not
honor `tls_skip_verify`.

|Flag|Default|Purpose|
|-|-|-|
|`--toolhive-llm`|`true`|Detect ToolHive's local LLM configuration. Set it to `false` on a shared host where this process must not use another operator's configuration.|
|`--toolhive-llm-base-url`|`""`|Use an explicit loopback proxy URL. This always selects proxy mode.|
|`--toolhive-llm-mode`|`auto`|Use `direct` when the HTTPS gateway URL and OIDC settings are complete; otherwise use the loopback proxy. Set `proxy` or `direct` to require one path.|

For provider selection, protocol paths, independent catalog status, and
model-routing troubleshooting, see
[Choose models and providers](/features/choose-models.md#set-up-a-local-provider).

### Posture

|Flag|Notes|
|-|-|
|`--posture strict`|Default. Every mutating call asks for approval|
|`--posture trusted`|Honor a project's ALLOW rules (alias: `--trust-project`)|
|`--posture auto`|Allow unattended calls while keeping child injection defense on|
|`--posture yolo`|Also disable child injection defense. Isolated single-tenant only. Refused as root without `MECATL_SANDBOX=1`|

On a **headless** root (`--headless`), posture never raises `TrustProject`.
Explicit `--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust
admits BOTH repo steering and the read-only child shell. Without a trust source,
`--posture auto` keeps its approvals but gets neither because `.git` is not
vouched. See
[Permissions and posture](/features/permissions-and-posture.md#project-trust)
for the trust sources and headless behavior.

See [Permissions & guardrails](/building/what-you-get/permissions.md) for the
full rule engine. Posture is read from the operator-global `settings.yaml`
(`posture:` key) and out-ranked by the CLI flag when both are set.

### Guardrails

|Flag|Default|Notes|
|-|-|-|
|`--guardrails-model`|`""` (off)|Model id or alias for the content checker. Configuring a model **enables** guardrails|
|`--guardrails`|`""`|Kill-switch only: pass `--guardrails=off` to force off regardless of model config|

The rule list and cost knobs live in the operator-global `settings.yaml`
(`guardrails:` subtree). A project-tier `guardrails:` block is ignored with a
WARN because a checked-in file cannot weaken an operator security check.
Checker outage is fail-closed by default; set `onCheckerDown: warn` only when
continue-with-warning is the intended deployment policy. The owner-authorized
coverage and transient detail APIs are gRPC-only; no HTTP paths are implied.

### MCP

|Flag|Default|Notes|
|-|-|-|
|`--mcp-server name=URL`|(none)|Remote MCP server, repeatable. Per-server bearer token from `MCP_<NAME>_TOKEN`; a token-bearing URL must be `https` (or `http` to loopback)|
|`--mcp-server-insecure-http name`|(none)|Allow the named server's bearer over off-host HTTP. Use network controls and short-lived tokens. Unknown, HTTPS, and loopback names fail startup|
|`--mcp-resource-tools`|`true`|Register `ListMcpResources`/`ReadMcpResource` meta-tools when a server exposes resources|
|`--toolhive`|`true`|Discover MCP servers from running ToolHive workloads (fails soft when no container runtime is reachable)|
|`--toolhive-group`|`""` (default group)|ToolHive group to discover from|

`--mcp-server` uses streaming HTTP. ToolHive can proxy stdio backends. For OAuth
profiles, resources, debugging, and transport constraints, see
[MCP client](/building/what-you-get/mcp-client.md).

### Skills

|Flag|Default|Notes|
|-|-|-|
|`--skills-dir`|(none)|Trusted local Agent Skills directory; repeatable|
|`--skills-conventional`|`false`|Add conventional project/user skill locations|
|`--skill-source-url`|(none)|Remote `SkillSourceService`; replaces local discovery and snapshots metadata at startup|

Mecatl loads skill instructions and assets on demand. Assets do not become
workspace files or executable scripts. See
[Skills, commands, and soul](/features/skills-commands-and-soul.md).

### Observability

|Flag|Default|Notes|
|-|-|-|
|`--otlp-endpoint`|`""` (off)|OTLP trace collector, e.g. `localhost:4317`; empty disables tracing|
|`--otlp-protocol`|`grpc`|OTLP transport: `grpc` or `http`|
|`--flight-recorder`|`true`|Arm the execution-trace ring buffer; snapshots at `/debug/flightrecorder`|
|`--perf-mcp`|`false`|Mount a read-only perf MCP server at `/mcp`. Requires loopback `--metrics-addr`|
|`--mutex-profile-fraction`|`0` (off)|`runtime.SetMutexProfileFraction`; adds overhead when > 0|
|`--block-profile-rate`|`0` (off)|`runtime.SetBlockProfileRate` in ns; adds overhead when > 0|

The loopback admin listener carries `/metrics`, `/debug/pprof`, `/debug/vars`,
and `/debug/flightrecorder`. Diagnostic output can contain prompts, file paths,
and goroutine stacks. Keep this listener on loopback.

---

## The trust model

Mecatl exposes command and file execution. Both API listeners therefore bind to
loopback by default. Before binding to another interface:

- authenticate callers with `--auth-token`, OIDC, mTLS, or an operator-managed
  private edge;
- encrypt traffic with `--tls-cert` and `--tls-key`, unless the edge terminates
  TLS; and
- keep the unauthenticated admin listener on loopback.

Add `--client-ca` to require client certificates. OIDC records distinct caller
identities; a shared bearer token does not. Authentication does not isolate a
shared workspace. A non-loopback API with no authentication is allowed for
service-mesh deployments but produces a startup warning.

---

## Persistence

By default, sessions live in memory and disappear when the server restarts.

Enable JSONL persistence by pointing `--store-dir` at a directory:

```sh
mecated serve --store-dir /var/lib/mecatl/sessions
```

The store writes current snapshots, tool-call audit, events, inventory, and
lineage beneath `sid-v1`. Files at the addressed current paths must use the
current format; malformed or incompatible content fails validation without
being overwritten. Distinct root-level artifacts are not listed or loaded.
Files are plaintext and owner-only; do not edit or share them. See
[Session store](/building/extension-points/session-store.md) for the layout and
durability guarantees.

:::note[Kubernetes and persistent volumes]

If you run `mecated` in Kubernetes with `--store-dir`, you need a
PersistentVolume backed by ReadWriteOnce (or ReadWriteMany for multi-replica
with affinity routing). If a PVC is a hard constraint, use `mecak8s` instead.
When configured, its Redis-backed store has no PVC requirement.

:::

Configure retention in the operator `settings.yaml`. Main-session deletion is
off by default and requires `acknowledge_main_deletion: true` when enabled.
Follow [Operate local session storage](session-storage-operations.md) for the
schema, service definitions, backups, cleanup, and restore.

`--session-store-url` replaces the local store with a gRPC driver and cannot be
combined with `--store-dir`. Session and memory drivers must negotiate Mecatl's
current contract at startup; old or partially implemented peers are rejected.
Optional session operations such as listing, metadata paging, deletion, lineage,
atomic create, and activity projection remain capability-gated. Remote driver
operators own their backing namespace and upgrade policy: Mecatl does not scan,
adopt, migrate, or reject unrelated old driver artifacts. Malformed data returned
from the selected current namespace fails closed. Current remote drivers do not
support OIDC caller ownership; use local JSONL or `mecak8s` for multi-user
deployments.

`--learning-store-url` selects a trusted single-tenant driver for distributed
learning. Startup rejects partial driver support and deployments with OIDC
caller ownership.

### Import from Codex or Claude Code

`mecated import` creates a resumable Mecatl session from a local Codex or Claude
Code transcript. It can also copy project files and skills into a workspace.

Codex stores active session rollouts under `~/.codex/sessions/`; Claude Code
stores project transcripts under `~/.claude/projects/`. Select the JSONL session
you want and use the same `--store-dir` when starting the server:

```sh
mecated import \
  --from codex \
  --session ~/.codex/sessions/2026/08/05/rollout-....jsonl \
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

---

## Multi-replica

By default, route every request for a session to the same replica.

For failover or routing without session affinity, configure a lease backend.
Only one replica can hold a session lease; competing requests return gRPC
`FAILED_PRECONDITION` or HTTP 409.

Three lease backends are available:

|Backend|Flag|When to use|
|-|-|-|
|flock (single-host)|`--session-lease-dir <dir>`|Multiple `mecated` processes on one machine. flock auto-releases on crash|
|k8s Lease|`--session-lease-k8s-namespace <ns>`|Multi-replica in Kubernetes; uses `coordination.k8s.io` Leases|
|gRPC driver|`--session-lease-url <host:port>`|Custom or managed lease backend via the driver protocol|

The ServiceAccount for the k8s backend needs `get,create,update,delete` on
`leases.coordination.k8s.io` in the configured namespace. It does not need
`list` or `watch`.

:::warning[Remote shared stores still need an explicit lease]

A local `--store-dir` automatically uses a flock lease beneath the store root,
so multiple current mecated processes on one host participate without another
flag. Remote stores and multi-host filesystems still require an explicit
Kubernetes or gRPC lease backend (and local flock is not reliable over NFS/EFS).
Without one, use session affinity; destructive maintenance fails closed.

:::

The lease TTL defaults to 30 seconds. After a crash, another replica can claim
the session when that TTL expires.

---

## Operator subcommands

Two subcommands run a service:

```sh
mecated serve [flags]
mecated acp [flags]
```

The remaining commands perform offline operator tasks:

|Task|Command|
|-|-|
|Create operator settings|`mecated config init`|
|Validate operator settings|`mecated config validate [--file PATH]`|
|Create daemon settings|`mecated config daemon init`|
|Validate daemon settings|`mecated config daemon validate [--file PATH]`|
|Promote a legacy draft skill|`mecated skills promote [flags] NAME`|
|Print perf MCP configuration|`mecated perf-mcp print-config`|

Add `--print` to either `config init` command to write the skeleton to standard
output. Both validation commands are read-only and omit settings values. Use
`config validate --learning-patch PATH` to test one `learning:` mapping without
changing the base file.

`skills promote` is a deprecated compatibility path for model-authored files in
`--skills-draft-dir`; it does not activate skill-lifecycle repository records.
Manage schedules through the `Schedule` tool or the gRPC/REST API. See
[Scheduled tasks](/building/what-you-get/scheduled-tasks.md).

---

## Hosting a spawned daemon

When an SDK, editor, or wrapper starts `mecated`, use a private socket,
readiness file, and lifetime pipe:

```sh
mecated serve \
  --grpc-unix-socket /run/user/1000/myapp/mecated.sock \
  --http-addr "" \
  --ready-file /run/user/1000/myapp/ready.json \
  --lifetime-pipe-fd 3
```

`--grpc-unix-socket` serves gRPC without opening a TCP port. Dial the example as
`unix:///run/user/1000/myapp/mecated.sock`. Mecatl creates the socket
owner-only, rejects unsafe parent-directory permissions, removes stale sockets,
and refuses to replace a live listener. Configuring both the socket and
`--grpc-addr` is an error. An empty `--http-addr` disables both HTTP/SSE and the
admin listener, so it cannot be combined with `--perf-mcp`. Keep the full socket
path within 103 bytes on macOS or 107 bytes on Linux.

**`--ready-file`** is published atomically after every listener is bound. Its
presence means the parent can dial the daemon:

```json
{
  "schema": "mecated-ready/1",
  "pid": 5821,
  "transport": "unix",
  "grpc_address": "/run/user/1000/myapp/mecated.sock",
  "socket_path": "/run/user/1000/myapp/mecated.sock",
  "api_major": 1,
  "features": ["mcp_servers_on_create", "server_info"],
  "deployment": "eu-west-1 staging"
}
```

Use `api_major` and `features` to check compatibility before the first RPC. The
owner-only file contains no credentials and remains after shutdown, so check
`pid` before trusting an existing record.

`--lifetime-pipe-fd` accepts a pipe read end or a connected UNIX stream socket.
Keep the other endpoint open. EOF triggers graceful shutdown and persistence.
The socket, readiness-file, and lifetime-pipe flags are off by default.

`--lifetime-stdin` uses piped stdin for the same parent-liveness signal. A
parent using `Deno.Command` sets `stdin: "piped"` and holds the writer open.
Closing the writer or exiting closes the channel and triggers graceful shutdown.
The daemon discards any bytes received. Regular files and terminals are
rejected, and the flag cannot be combined with `--lifetime-pipe-fd`. The
[Deno SDK integration](/building/typescript-sdk/local-daemon.md#start-a-daemon-from-deno)
manages this channel for you.

### Add MCP servers per session

Clients can add per-session MCP servers only when `mecated` uses a UNIX socket
and HTTP is disabled:

|Listener topology|`mcp_servers`|
|-|-|
|`--grpc-unix-socket` **and** `--http-addr ""`|accepted|
|Anything else, including loopback TCP|refused with `UNIMPLEMENTED` / HTTP 501 and `client_mcp_unsupported`|

Clients can check for `mcp_servers_on_create` through `GetCompatibilityInfo` or
`GET /v1/compatibility`. Per-session entries support streaming HTTP without
redirects. Server names must use 1-64 characters from `[A-Za-z0-9._-]`, cannot
contain `__`, and must be unique. Put credentials in `headers`; Mecatl redacts
them and rejects URLs containing user information.

If any requested server is unreachable, session creation fails atomically with
`client_mcp_unreachable` and no session is created.

---

## Graceful shutdown

On `SIGINT` or `SIGTERM`, `mecated` drains listeners for 10 seconds, flushes
telemetry for up to five seconds, and persists configured session state.
Interrupted runs recover from their snapshots after restart.

EOF on an inherited `--lifetime-pipe-fd` or piped `--lifetime-stdin` follows the
same path. See
[Hosting a spawned daemon](#hosting-a-spawned-daemon).

## Browsers and CORS

For local browser development, allow each origin explicitly:

```sh
mecated serve --http-addr 127.0.0.1:8081 \
  --cors-origins http://localhost:5173 \
  --cors-origins https://app.internal.example.com
```

Origins require an exact scheme, host, and port. Wildcards, paths, queries, and
fragments are rejected. Grant only origins you control because the HTTP API can
start agent runs.

:::warning[Local development only]

In production, place a same-origin backend in front of `mecated`. Keep the
bearer token on the server and enforce Origin and CSRF policy there.

:::

---

## Next steps

- [Choose how to run Mecatl](/building/getting-started/deployment-decision.md)
  for trade-offs between `mecated`, `mecak8s`, `mecatequi`, and engine
  embedding.
- [Permissions and guardrails](/building/what-you-get/permissions.md) for the
  rule engine, posture ladder, and guardrail checker.
- [Cloud-native Kubernetes with mecak8s](/building/deployment/mecak8s.md) for a
  no-PVC deployment with Redis and Kubernetes Leases.
