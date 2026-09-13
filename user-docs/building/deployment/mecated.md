---
sidebar_position: 20
title: Run mecated standalone
description: Run the standalone Mecatl server with providers, persistence, security, and observability.
---

# Run mecated standalone

`mecated` runs Mecatl as a standalone service over gRPC and HTTP/SSE. It
provides the operator controls needed for authentication, TLS/mTLS, rate
limiting, observability, persistence, and graceful shutdown.

## Native LLM endpoints

Native **LLM endpoints** are deployment-wide operator configuration, not remote-client
settings. Create or update one in the user-global settings file with
`mecatui llm config set ENDPOINT --gateway-url URL --issuer ISSUER --client-id ID
--default-model MODEL [--credential-home ABSOLUTE_PATH]`. This command preserves unrelated
configuration, does not change `models.default_provider`, and does not start login. Public
CA trust is the default; `--issuer-ca-bundle` and `--gateway-ca-bundle` configure the
existing private-CA policies. Repeat `--scope` as needed (the defaults are `openid` and
`offline_access`) and pass `--resource-audience` only when required.

The resulting strict `llm.endpoints.ID` entry uses the Responses protocol and requires a
canonical HTTPS gateway URL, default model, OIDC issuer/client/scopes, an optional resource
audience, and independent issuer/gateway trust policies. `llm.credential_home` is shared by
all native endpoints. When `--credential-home` is omitted, `config set` creates the conventional
owner-only (`0700`) home at `$XDG_STATE_HOME/mecatl/provider-oidc` (or
`~/.local/state/mecatl/provider-oidc`) and persists its canonical absolute path. An explicit
custom home must already exist, be owned by the current user, and have mode `0700`; later
updates, including ones that omit the flag, must retain the configured home until credentials
are migrated. When `resource_audience` is omitted, Mecatl
omits the authorization request parameter and does not require an audience during local
access-token validation; a configured value remains strictly requested and matched. The
native credential is always encrypted under that configured home, using the OS keyring by
default or the explicit environment-key option below. `mecated` never
opens a browser: enroll with embedded `mecatui llm login ENDPOINT` (add `--no-browser`
to print the authorization URL to stderr and wait at the fixed ToolHive-compatible redirect
`http://localhost:8666/callback`), then start or restart mecated to use the same native Mecatl
record. This registration compatibility does not reuse or copy ToolHive credentials. A missing record leaves an optional endpoint
`not-enrolled`/unavailable, fails startup when it is the effective default, and never
falls back to another endpoint or ToolHive.

### Keyring-free encrypted credentials

The default remains the OS keyring. Environment-key custody is an explicit operator opt-in;
credentials remain encrypted, with **no plaintext refresh-token storage**. For a new
enrollment in an environment without an OS keyring:

1. In a secret manager, generate and retain one stable key: canonical **padded standard
   base64** encoding exactly **32 cryptographically random bytes**, with no whitespace or
   line breaks. Provision it as `MECATL_NATIVE_LLM_CREDENTIAL_KEY` in both the embedded
   `mecatui` login environment and the `mecated` service environment. Use the **same value**
   for login, server execution, and every restart; do not generate a new key at startup.
2. Add this block to the existing `llm` mapping in **operator settings**, alongside
   `credential_home` and `endpoints` (do not replace those entries):

   ```yaml
   credential_key:
     source: environment
     key_env: MECATL_NATIVE_LLM_CREDENTIAL_KEY
   ```

   Settings contain only the environment-variable **reference**, never its value. The
   reference must be a valid `MECATL_*` name; see the
   [`llm` configuration reference](/reference/configuration.md#llm).
3. With the key securely injected, enroll and inspect the exact configured endpoint ID
   (replace `ENDPOINT` below):

   ```sh
   mecatui llm login ENDPOINT
   mecatui llm status ENDPOINT
   ```

   Use `--no-browser` on login if needed, as described above. Confirm status is `usable`.
4. Start or restart the service with the same operator settings, credential home, and key
   value. For a foreground server in that provisioned environment:

   ```sh
   mecated serve
   ```

Use a secret manager or protected service-environment provisioning; never paste secrets into
CLI arguments, settings YAML, shell history, prompts, or logs. Model-facing Shell environments
scrub `MECATL_*` variables. Access/refresh tokens are persisted only in the owner-only
**encrypted** credential store, and refresh-token rotation is persisted there before a bearer
is returned.

Omitting `credential_key` retains the existing OS-keyring behavior. Explicit
`credential_key: {source: keyring}` is equivalent and forbids `key_env`, even when empty.
Unknown sources/fields and invalid combinations are rejected. There is **no automatic
fallback** from a missing or broken keyring. An unset or malformed environment key fails
before OAuth or credential mutation. A well-formed but wrong key cannot decrypt an existing
record; login refuses before OAuth rather than overwriting it. A missing encrypted namespace
reports `storage-unavailable` in environment mode; login initializes it, while status,
logout, and serving never create it or a key.

The source is shared across **all endpoints in that credential home**, not configured per
endpoint. `llm config set` preserves the existing shared selection. There is **no automatic
migration**: changing the source or variable name does not migrate, re-encrypt, or overwrite
records. Changing or losing the key value makes existing records unreadable; restore the
original key or **re-enroll** with a new key and fresh protected storage. Login cannot
overwrite an unreadable record. For an existing enrollment, follow the
[native endpoint recovery guidance](/mecatui/troubleshooting.md#native-credential-storage-failures)
before changing custody. Coordinate provisioning across all processes sharing the home,
and restart serving processes after an intentional change. Native and ToolHive credentials
remain isolated.

Every admitted caller shares a usable endpoint's deployment-scoped gateway identity, quota,
gateway-side audit/retention posture, and model availability. Use a dedicated deployment/service
gateway identity. Caller OIDC only authenticates/attributes ownership: mecatl drops the raw
inbound caller bearer and never forwards or retains caller credentials. For mutually untrusted or
per-user upstream authorization, use separate deployments pending an explicit forwarded-token or
RFC 8693-style token exchange contract. Lifecycle confirmations are stderr-only and never print
tokens.

If you are deploying to Kubernetes without persistent volumes, see
[mecak8s](/building/deployment/mecak8s.md) instead. That binary is purpose-built
for no-PVC pod deployments, with Redis-backed state when you configure
`--redis-url` and Kubernetes Lease coordination enabled by default.

---

## Quick start

Get the executable first: `brew install stacklok/tap/mecatl`, or a signed release archive —
see [Install Mecatl](/install.md). The canonical invocation is `mecated serve`:

```sh
mecated serve
```

A command word is required: `mecated serve` for the network daemon, `mecated
acp` for the ACP stdio mode. Bare `mecated` prints the command help and exits
with a usage error.

The minimal invocation starts the server on loopback with an in-memory session
store. No persistence, no auth — the single-user localhost trust model.

Global MCP OAuth profiles are operator settings. Serving never opens a browser; authorize
a mutable local profile explicitly with `mecated mcp login SERVER [--no-browser]
[--permission-config PATH ...]`. The repeatable permission-config option selects trusted
operator settings only, never OAuth values. Serving then warm-restores the encrypted record, persists lazy refresh-token
rotation, and remains warm after restart. Roll back with a whole `static_bearer`/`none`
profile change and restart. ACP cannot provide OAuth profiles or install/drive authorization;
after operator authorization it may invoke the shared global OAuth-backed tools under ordinary
permissions. See [MCP client](/building/what-you-get/mcp-client.md).

Default addresses:

| Listener | Default |
|---|---|
| gRPC | `127.0.0.1:8080` |
| HTTP/SSE | `127.0.0.1:8081` |
| Prometheus + admin | `127.0.0.1:9090` |

A slightly more configured invocation for unattended local operation:

```sh
mecated serve \
  --store-dir ~/.local/share/mecatl/sessions \
  --auth-token "$(cat ~/.mecatl/token)" \
  --posture auto
```

Set `--log-level` to one of the exact values `debug`, `info`, `warn`, or `error`
(default `info`). The same threshold is used for ambient `slog` output and
injected diagnostics. Invalid values, including `--log-level=`, fall back to
`info` and emit one warning. Each startup also emits the build identity as an
INFO log with `msg="mecated starting"` and a `version` field; use it to identify
the binary that produced the remaining server logs.

`--store-dir` enables local JSONL persistence with an authoritative v2 current
snapshot plus readable v1 history. The configured path and every ancestor must be
physical non-symlink directories; on macOS, use the physical `/private/...` spelling
instead of a `/var/...` path that traverses the `/var` symlink. Startup reports the
verified atomic-replace, file-sync, and directory-sync posture without logging the
store path. `--auth-token` requires the token on every
request (also readable from `MECATL_AUTH_TOKEN`). `--posture auto` sets allow-all
for unattended runs while keeping the child substitution floor (prompt-injection
defence) on.

Before binding a non-loopback address, add `--tls-cert` / `--tls-key` and
`--auth-token` — see [the trust model](#the-trust-model) below.

### Server-owned session placement

Every deployment owns session placement. `--workspace` configures the server's private
local default; the public session API has no workspace, cwd, placement ID, or
exact environment ref—even over loopback or an embedded UNIX socket. Omit `profile` to
bind that default or request `profile:"no-fs"` to attenuate filesystem access.

`ListWorktrees(session_id)` discovers alternatives from an owned source session and
returns safe labels plus a short-lived opaque selector. Only `ClearSession` and
`ForkSession` accept it. `/clear` creates a non-destructive empty-history successor;
Fork preserves valid history. Omitted selector inherits the source's exact placement.
Selectors expire on restart, so clients relist; a failed relist or switch leaves their
current session unchanged.

The exact private `EnvironmentRef{kind,id,revision}` is persisted in snapshots and trusted
driver storage and reattached at run entry. It is never exposed by public session/event
projections. Schedules resolve and persist exact placement before firing, delegation
derives it from the parent, and ACP cwd is only a local consistency assertion. See
[ADR 0291](https://github.com/stacklok/mecatl/blob/main/docs/adr/0291-server-owned-session-placement.md).

## Operator-defined providers

Operator-local `~/.config/mecatl/settings.yaml` can declare a named compatible
provider with an HTTPS base URL, required default model, and one explicit API flavor:
`openai-responses`, `openai-chat-completions`, or `anthropic-messages`. Keep an
`api_key` provider's credential in the matching `providers.<id>.api_key` record in
owner-readable `~/.config/mecatl/auth.yaml`; `auth.method: none` needs no credential.
The ID is persisted with sessions, so removing or renaming it makes those sessions fail
loudly instead of selecting another provider. Built-in endpoint settings belong under
`provider_overrides`; the matching `--*-base-url` flag wins. See the
[configuration reference](/reference/configuration.md)
for the strict schema.

Self-hosted `openai-responses` endpoints, including vLLM, llama.cpp, and LiteLLM
proxies, vary in their `/v1/responses` support. An endpoint can omit hosted tools,
automatic prompt caching, encrypted reasoning content or summaries, strict mode, or
`parallel_tool_calls`. If the endpoint does not support the Responses API features you
need, use the `openai-chat-completions` flavor, which works with more self-hosted
endpoints.

The built-in `openai` and `openrouter` providers send prompt-cache hints only through
their canonical base URLs. A base URL override disables these hints so a compatible
endpoint cannot reject an unsupported cache field.

---

## Architecture

`mecated` owns the network daemon surface. Everything inside `internal/app.Build`
is shared with the TUI.

```mermaid
flowchart TD
    CLI["mecated CLI flags"] --> PARSE["parseFlags → config struct"]
    PARSE --> BUILD["internal/app.Build\n(engine, tools, providers, MCP, skills)"]
    BUILD --> SVC["server.Service"]
    SVC --> GRPC["gRPC :8080\nHarnessServiceServer\n+ health"]
    SVC --> HTTP["HTTP/SSE :8081\n/v1/* + health probes"]
    BUILD --> ADMIN["admin mux :9090\n/metrics /debug/pprof\n/debug/flightrecorder"]
```

`internal/app.Build` is the composition root shared with `mecatui`. Flags that
control the network daemon surface — TLS/auth/rate-limit, listen addresses, the
Prometheus registry, the FlightRecorder — are owned by `mecated`. Flags that
control agent behaviour (posture, model, store, MCP, skills, guardrails) flow
through `app.Config` into `Build`.

---

## Flag reference

Flags are grouped by area. All have zero-value defaults that produce a working
loopback-only server. Flags not covered here are advanced operator tuning; run
`mecated serve --help` for common flags grouped by task, or `mecated serve --help-all` for the exhaustive reference.

### Server

| Flag | Default | Notes |
|---|---|---|
| `--grpc-addr` | `127.0.0.1:8080` | gRPC listen address |
| `--http-addr` | `127.0.0.1:8081` | HTTP/SSE listen address. **Empty disables the HTTP/SSE listener and the admin listener together** — see [Hosting a spawned daemon](#hosting-a-spawned-daemon) |
| `--metrics-addr` | `127.0.0.1:9090` | Prometheus + admin listener; empty disables it |
| `--grpc-unix-socket` | `""` (off) | Serve gRPC on a UNIX-domain socket instead of a TCP port. Mutually exclusive with a configured `--grpc-addr`. See [Hosting a spawned daemon](#hosting-a-spawned-daemon) |
| `--ready-file` | `""` (off) | Absolute path to write a JSON readiness document to, atomically, once every listener is up |
| `--lifetime-pipe-fd` | `0` (off) | File descriptor of an inherited pipe read end or connected UNIX-domain stream socketpair endpoint; EOF on it stops the daemon gracefully (the parent-crash path) |
| `--auth-token` | `""` (off) | Bearer token required on every request; also `MECATL_AUTH_TOKEN` |
| `--tls-cert` | `""` | PEM server certificate; enables TLS on both listeners when paired with `--tls-key` |
| `--tls-key` | `""` | PEM server private key |
| `--client-ca` | `""` | PEM client-CA bundle; enables mTLS (requires `--tls-cert`/`--tls-key`) |
| `--cors-origins` | `""` (off) | Allow a browser at this **exact** origin to call the HTTP API; repeatable. Local development only — see [Browsers and CORS](#browsers-and-cors) |
| `--deployment-id` | `""` | Optional opaque label for this deployment, echoed on `GetCompatibilityInfo`. Never inferred from the host |
| `--rate-limit` | `0` (off) | Sustained per-client request rate in req/s; with OIDC, also limits rejected bearer validation per direct transport peer IP |
| `--rate-burst` | `0` (derived) | Token-bucket burst; zero derives a sane default from `--rate-limit`, including the OIDC rejected-token bucket |
| `--oidc-issuer` | `""` (off) | OIDC issuer URL whose tokens identify callers; setting it turns caller identity on. See [Caller identity](#caller-identity-oidc) |
| `--oidc-jwks-uri` | `""` (derived) | Static JWKS endpoint, short-circuiting OIDC discovery (air-gapped or pinned-key deployments) |
| `--oidc-audience` | `""` | Audience (`aud`) this deployment accepts; **required** with `--oidc-issuer` |
| `--oidc-max-jwks-staleness` | `1h` | Maximum age of last-good signing keys during an IdP outage. `0` deliberately disables this bound; negative values are rejected. |

#### Caller identity (OIDC)

`--oidc-issuer` names the identity provider whose tokens identify your callers.
When it is set, every request must present a bearer that provider vouches for,
and the verified `(issuer, subject)` pair is recorded as the **owner** of each
session the caller creates. `--oidc-audience` is required alongside it — an
audience-less verifier would accept tokens minted for a different service.
`--oidc-jwks-uri` pins the signing-key endpoint instead of discovering it.

**This is application-level caller isolation, not filesystem isolation.** The
server authorizes access to sessions, schedules, teams, and memory records by
their verified owner, and conceals foreign resources as absent. A shared
workspace remains outside that boundary, so isolate callers' working files at
the deployment layer when they must not share filesystem access.

The production OIDC/JWT validator is a delegated, actively-maintained library —
Mecatl never hand-rolls token verification. A bad OIDC
configuration, including an unreachable initial key fetch, fails closed at startup
rather than falling back to unauthenticated traffic. After a successful fetch, the
last good JWKS can cover a brief IdP outage. `--oidc-max-jwks-staleness=1h` bounds
that fallback: once keys are older than the bound, a failed refresh returns **503
Service Unavailable**. `0` is an explicit acceptance of unbounded cached-key
availability and its signing-key revocation exposure.

When OIDC and `--rate-limit` are both enabled, rejected bearer traffic has a
separate pre-validation bucket keyed only by the **direct transport peer IP**.
The server never trusts `Forwarded` or `X-Forwarded-For` for this decision. Once
a peer exhausts the bucket, requests return HTTP **429** / gRPC
`RESOURCE_EXHAUSTED` before another validator call. Valid tokens do not consume
that bucket; they continue to the existing verified-principal limiter and are
charged there once. A normally admitted bad token remains **401** / gRPC
`UNAUTHENTICATED`, while an IdP outage remains **503** / gRPC `UNAVAILABLE`.
Enabled authentication also writes structured operator diagnostics. Rejected and
unavailable outcomes are logged per request; accepted authentication logs one INFO
record per closed category/transport pair (static bearer or validated identity, over
HTTP or gRPC), not per request. Fields are closed outcome/category and
transport/status values only; credentials, JWTs, validator errors, issuer, subject,
claims, and KID are never logged.

This is a bound on **signing-key** revocation during an outage, not per-token
revocation. An otherwise valid token remains acceptable until its normal expiry.
The JWKS cache is process-local and not persisted; a restarted process fetches
current keys again. The flags are identical on `mecak8s`. See [Caller identity
and OIDC](/features/caller-identity.md) and [ADR 0204](https://github.com/stacklok/mecatl/blob/main/docs/adr/0204-caller-identity-threading.md).

#### Daemon config file (`daemon.yaml`)

The listener topology above (gRPC/HTTP/metrics addresses, TLS cert/key/CA,
rate-limit/burst) can live in a small, strict, versioned YAML file instead of
repeated flags. The file is a **distinct** file from `settings.yaml` (which is
**policy**: permissions, posture, guardrails, models) and is loaded ONLY when you
start with `mecated serve --config PATH` — there is **no conventional
auto-load**. Scaffold and validate it offline:

```sh
mecated config daemon init                          # write the conventional skeleton
mecated config daemon init --print                  # print it to stdout, no file
mecated config daemon validate                      # validate the conventional path
mecated config daemon validate --file /etc/mecatl/daemon.yaml
mecated serve --config ~/.config/mecatl/daemon.yaml # start with it
```

The v1 fields are `version` (required, `v1`), `grpc_addr`, `http_addr`,
`metrics_addr`, `tls_cert`, `tls_key`, `client_ca`, `rate_limit`, `rate_burst`.
The schema is strict (unknown keys are rejected). Precedence is
**defaults < file < explicit CLI** — an explicit flag overrides the file,
including an explicit empty/zero.

**Security:** the API bearer **token is NOT accepted in `daemon.yaml`** — keep
using `MECATL_AUTH_TOKEN` / `--auth-token`. A **non-loopback** bind still
requires auth/TLS (it logs a prominent WARNING otherwise); `daemon.yaml`
changes topology, not the trust model. `config daemon validate` never prints
secrets or raw file content. See [ADR 0088](https://github.com/stacklok/mecatl/blob/main/docs/adr/0088-daemon-config-file.md) for the rationale.

### Session state

| Flag | Default | Notes |
|---|---|---|
| `--store-dir` | `""` (in-memory) | Directory for the JSONL session store. Empty = in-memory, no persistence across restart |
| `--session-lease-dir` | `""` | Single-host flock lease backend; see [multi-replica](#multi-replica) |
| `--session-lease-k8s-namespace` | `""` | k8s `coordination.k8s.io` Lease backend; see [multi-replica](#multi-replica) |
| `--session-lease-ttl` | `30s` | Lease lifetime; a crashed holder's lease becomes claimable after this long |
| `--session-store-url` | `""` | gRPC driver endpoint replacing the local JSONL store (mutually exclusive with `--store-dir`) |

### Scheduled tasks

| Flag | Default | Notes |
|---|---|---|
| `--no-scheduler` | `false` | Disable the in-process scheduler tick loop (ON by default when the store exposes a `ScheduleStore` — `--store-dir` or redisstore). The create/list/fire API still works. The removed `--scheduler` opt-in fails fast as an unknown flag |
| `--scheduler-tick-interval` | `30s` | How often the tick loop polls for due schedules |
| `--scheduler-min-interval` | `1m` | Frequency floor enforced at schedule-create time (fail-closed, by both the in-chat `Schedule` tool and the REST/gRPC create). Defaults to `1m`; `0` disables the floor |
| `--scheduler-max-concurrent-fires` | `4` | Max schedules fired in parallel per tick |
| `--schedule-store-url` | `""` | gRPC driver endpoint (`ScheduleStoreService` + `ScheduleOneShotReArmerService`) for the durable schedule registry, **independent of the session store** — when set, replaces the `ScheduleStore()` discovery from the configured store. Empty keeps the default (the configured store's own `ScheduleStore()`, or no scheduling). The driver runs atomic fire advancement server-side, but current remote drivers do not expose atomic create-only publication; this option is therefore rejected when OIDC caller ownership is enabled |
| `--learning-store-url` | `""` | One distributed-learning driver endpoint. Startup requires explicit capability advertisement of the complete Attempt/Proposal/Skill repository set and, when automatic learning is non-off, the automatic admission ledger; partial drivers fail instead of mixing remote and local persistence or accounting. The explicit flag still dials/probes/composes repositories in off mode for explicit reflection, learned-skill inspection, and recovery of already-admitted work; it does not enable automatic admission. Repository partitions are opaque on the wire. Current raw RPCs are permitted only as trusted single-tenant infrastructure with `OwnershipEnforced=false`; ownership-enforced/multi-tenant startup fails closed pending ADR-0213 workload-authenticated ownership |

See [Scheduled tasks](/building/what-you-get/scheduled-tasks.md) for the in-chat `Schedule` tool and the gRPC/REST management surface.

### LLM resilience

| Flag | Default | Notes |
|---|---|---|
| `--llm-per-attempt-timeout` | `300s` | Bounds **establishing** the stream only (connect + first chunk). Does not cut an actively-streaming turn |
| `--llm-stream-idle-timeout` | `180s` | Max idle gap between chunks after the first arrives. A stall exceeding this is terminal and not retried |
| `--llm-max-attempts` | `3` | Max stream-establish attempts (initial call + retries) |
| `--llm-breaker-threshold` | `5` | Consecutive LLM failures that open the circuit breaker; `0` disables |
| `--llm-breaker-cooldown` | `30s` | How long the breaker stays open before half-opening |

`--llm-per-attempt-timeout` uses a separate timer that fires only if no first chunk
arrived — it does NOT set a `context.WithTimeout` that would silently truncate a
slow reasoning turn. Once the first chunk arrives, the timer is stopped and only
`--llm-stream-idle-timeout` bounds subsequent inactivity.

### Provider and model

| Flag | Default | Notes |
|---|---|---|
| `--model` | `""` | Model id sent to the provider; empty uses the provider-appropriate default |
| `--default-provider` | `""` | Deployment-wide default provider (`openai`, `openrouter`, `anthropic`, `opencode`); validated fail-fast |
| `--default-model` | `""` | Deployment-wide default model id for the default provider; validated fail-fast |
| `--subagent-model` | `""` | Global default model for child engines (Subagent, Parallel branches, team members) that do not pin their own |
| `--no-prompt-cache` | `false` | Disable provider-side prompt caching (on by default — see [ADR 0100](https://github.com/stacklok/mecatl/blob/main/docs/adr/0100-provider-prompt-caching.md)) |
| `--anthropic-cache-ttl` | `""` (API default, `5m`) | TTL on every Anthropic ephemeral cache breakpoint: `5m` or `1h` |
| `--mock` | `false` | Offline canned provider — one text turn, no credentials; smoke tests only |
| `--mock-script` | `""` | Path to a strict JSON mock script; implies the offline provider and replaces its canned turn with ordered text/tool-call turns |

Provider credentials are read from environment variables — `OPENAI_API_KEY`,
`OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `OPENCODE_API_KEY` — never flag
values. `opencode` is [OpenCode Go](https://opencode.ai), a subscription LLM
gateway reached over the OpenAI Chat Completions protocol rather than OpenAI's
own Responses API — a separate adapter under the hood, but it configures the
same way as any other provider here. An optional `auth.yaml` credentials file
is also supported for operators who'd rather not export a key into the shell
environment. See [Configure provider credentials](./settings.md#configure-provider-credentials).

Experimental provider `openai-codex` can instead use a manually supplied
ChatGPT Codex subscription token from that file. It is a separate billing
identity from public API-key `openai`, uses an undocumented private backend,
and has no login or refresh flow. Configure `providers.openai-codex.oauth`,
keep the file owner-only, select `--default-provider openai-codex` (or an
explicit session selector), and restart after replacing the token. `0600` does
not stop same-UID Shell from reading a known plaintext file. See the
[exact schema, lifecycle, and failure guidance](https://github.com/stacklok/mecatl/blob/main/docs/usage/mecated.md#openai-codex-subscription-manual-token-experimental).

#### Offline mock providers (no credentials)

`--mock` starts the daemon on a canned offline provider that answers with a
single text turn — enough for a smoke test, never a tool call. `--mock-script
PATH` reads one strict JSON document at startup (failing before the listener
binds if it is missing or malformed) and replaces that canned turn with ordered
text and tool-call turns, so an offline run can exercise permission asks and, via
a turn's `delay_ms`, cancellation. Both imply the offline provider, so neither
needs a provider credential.

Each scripted turn sets exactly one of `text` or `tool_calls`. Tool-call `args`
is ordinary JSON. Turns are consumed in order across model calls:

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
    { "text": "continued after the tool" },
    { "delay_ms": 2000, "text": "a cancellable turn" }
  ]
}
```

#### The ToolHive LLM gateway (no API key needed)

If you have [ToolHive](https://docs.stacklok.com/toolhive/)'s local LLM proxy running, `--toolhive-llm`
(on by default) auto-detects it and registers it as provider id `toolhive` — no API key
required, since ToolHive holds the credential. `/models` (or the mecatui welcome splash)
tells you when it's available but not your default, so you can opt in with `/models` or
`--default-provider toolhive` without unsetting whatever key-based provider you already
have. On a host other operators also use, pass `--toolhive-llm=false` — a per-user
ToolHive config detected by one operator's process shouldn't surprise another.

There are two routing modes for how the `toolhive` provider reaches the gateway,
selected by `--toolhive-llm-mode` (default `auto`):

- **Proxy mode** (the original path): Mecatl talks to a local reverse proxy
  (`thv llm proxy`, loopback `127.0.0.1:<port>/v1`) that holds the credential and
  forwards to the real `gateway_url`. The proxy must be running.
- **Direct mode** (`auto` when configured, or `--toolhive-llm-mode direct`): Mecatl
  imports ToolHive as a library and talks DIRECTLY to the real `gateway_url` — no local
  proxy hop, no subprocess. The OIDC bearer token is minted and refreshed in-process
  by a per-request HTTP RoundTripper. Get the credential once with
  `mecatui llm login toolhive` (in-process interactive OIDC flow; add `--skip-browser` for
  headless/SSH/CI) or `thv llm setup`. Direct mode needs the OIDC trio
  (`gateway_url` + `issuer` + `client_id`) configured AND an HTTPS `gateway_url`
  (`http://localhost`/`http://127.0.0.1` are the dev carve-out); `auto` falls back to
  proxy when either is absent, `direct` Build-fails fast with the remediation.

`mecated` is headless, so a direct-mode cache-miss surfaces a terminal error
(naming `thv llm setup` / `mecatui llm login toolhive` / `--toolhive-llm-mode proxy`) rather than
launching a browser — run `mecatui llm login toolhive` (or `thv llm setup`) to obtain the
credential, or `--toolhive-llm-mode proxy` to fall back. If your gateway uses a
self-signed certificate, use `--toolhive-llm-mode proxy` — direct mode does not honor
`tls_skip_verify` (an upstream ToolHive gap), and proxy mode does.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--toolhive-llm` | `true` | Detect ToolHive's local LLM configuration. Set it to `false` on a shared host where this process must not use another operator's configuration. |
| `--toolhive-llm-base-url` | `""` | Use an explicit loopback proxy URL. This always selects proxy mode. |
| `--toolhive-llm-mode` | `auto` | Use `direct` when the HTTPS gateway URL and OIDC settings are complete; otherwise use the loopback proxy. Set `proxy` or `direct` to require one path. |

Run `mecatui llm login` for the interactive OIDC flow, or add `--skip-browser`
to print the authorization URL for an SSH session. This command writes the
refresh-token reference to ToolHive's configuration and does not start a Mecatl
session.

Two things worth knowing before you rely on it:

- **The proxy has to actually be reachable.** `/models` names the exact fix when it isn't:
  `thv llm proxy start` if the proxy isn't running, `thv llm setup` if your credential was
  rejected. An empty model list from a *valid* credential is an organizational problem
  (ask your platform admin), not a local one.
- **A model that lists fine can still fail at request time.** Some gateways expose "friendly"
  model aliases that have no cost route configured, so a request to one 5xxs with a
  cost-enforcement error even though `/models` reported the gateway healthy. If requests are
  failing but the gateway looks fine, pick a fully-qualified or provider-namespaced model
  slug instead (via `/models`, `--model`, or mecatui's `ctrl+g` global default) — or ask
  whoever runs the gateway to add a cost route for the alias. A session created before you
  fix this keeps failing on every turn even after the fix lands; open a fresh session rather
  than waiting for it to self-heal.

### Posture

| Flag | Notes |
|---|---|
| `--posture strict` | Default. Every mutating call asks for approval |
| `--posture trusted` | Honour a project's ALLOW rules (alias: `--trust-project`) |
| `--posture auto` | Allow-all server-wide + main substitution loosening; child injection defence **on**. Recommended for unattended use |
| `--posture yolo` | Also loosens child substitution (injection defence **off**). Isolated single-tenant only. Refused as root without `MECATL_SANDBOX=1` |

On a **headless** root (`--headless`), posture never raises `TrustProject`. Explicit
`--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust admits BOTH repo steering and
the read-only child shell. Without a trust source, `--posture auto` keeps its approvals but gets
neither because `.git` is not vouched. See
[Permissions and posture](/features/permissions-and-posture.md#project-trust)
for the trust sources and headless behavior.

See [Permissions & guardrails](/building/what-you-get/permissions.md) for the full rule
engine. Posture is read from the operator-global `settings.yaml` (`posture:` key)
and out-ranked by the CLI flag when both are set.

### Guardrails

| Flag | Default | Notes |
|---|---|---|
| `--guardrails-model` | `""` (off) | Model id or alias for the content checker. Configuring a model **enables** guardrails |
| `--guardrails` | `""` | Kill-switch only: pass `--guardrails=off` to force off regardless of model config |

The rule list and cost knobs live in the operator-global `settings.yaml`
(`guardrails:` subtree). A project-tier `guardrails:` block is ignored with a WARN —
a checked-in file weakening a security checker would be a downgrade.

### MCP

| Flag | Default | Notes |
|---|---|---|
| `--mcp-server name=URL` | (none) | Remote MCP server, repeatable. Per-server bearer token from `MCP_<NAME>_TOKEN`; a token-bearing URL must be `https` (or `http` to loopback) |
| `--mcp-server-insecure-http name` | (none) | Per-server opt-in, repeatable: let the named server's bearer ride plain `http` off-host (cleartext on the network path — rely on network-layer controls + short-lived tokens). Unregistered or already-`https`/loopback names fail startup |
| `--mcp-resource-tools` | `true` | Register `ListMcpResources`/`ReadMcpResource` meta-tools when a server exposes resources |
| `--toolhive` | `true` | Discover MCP servers from running ToolHive workloads (fails soft when no container runtime is reachable) |
| `--toolhive-group` | `""` (default group) | ToolHive group to discover from |

`--mcp-server` uses streaming-HTTP transport only. Mecatl never speaks stdio MCP
directly; ToolHive stdio backends are HTTP-proxied and fine. A `mecatui connect … debug
SESSION_ID --debug-mcp NAME` session can borrow only the named server's direct tools. The
selection and exact direct tool set persist across restart; any addition, removal, or rename
fails closed. Every selected call—including tools marked read-only—requires a fresh interactive
approval even under yolo. Denies remain absolute, headless calls deny, and allow-always is not
learned. The intended flow is diagnose
and draft first, then send a separate current publication request and approve exactly that call.

### Skills

| Flag | Default | Notes |
|---|---|---|
| `--skills-dir` | (none) | Trusted local Agent Skills directory; repeatable |
| `--skills-conventional` | `false` | Add conventional project/user skill locations |
| `--skill-source-url` | (none) | Remote `SkillSourceService`; replaces local discovery and snapshots metadata at startup |

The `Skill` tool uses path-free progressive disclosure. `{name}` loads instructions
and a logical asset inventory; `{name, asset}` fetches one bounded textual asset.
Local and remote skills behave the same. Assets are not materialized or exposed as
workspace files, and bundled scripts are not implicitly executable. If a skill needs
a real file, its instructions must create or obtain one explicitly in the workspace,
where ordinary Write/Shell permissions apply.

### Observability

| Flag | Default | Notes |
|---|---|---|
| `--otlp-endpoint` | `""` (off) | OTLP trace collector, e.g. `localhost:4317`; empty disables tracing |
| `--otlp-protocol` | `grpc` | OTLP transport: `grpc` or `http` |
| `--flight-recorder` | `true` | Arm the execution-trace ring buffer; snapshots at `/debug/flightrecorder` |
| `--perf-mcp` | `false` | Mount a read-only perf MCP server at `/mcp` on the admin listener. Requires loopback `--metrics-addr` (unauthenticated surface) |
| `--mutex-profile-fraction` | `0` (off) | `runtime.SetMutexProfileFraction`; adds overhead when > 0 |
| `--block-profile-rate` | `0` (off) | `runtime.SetBlockProfileRate` in ns; adds overhead when > 0 |

The admin listener (`--metrics-addr`) is separate from the harness API and loopback by
default. It carries `/metrics`, `/debug/pprof`, `/debug/vars`, and
`/debug/flightrecorder`. pprof and the FlightRecorder can embed prompt text, file
paths, and goroutine stacks — never expose this listener off-loopback.

---

## The trust model

Mecatl exposes command and file execution. The security model has three layers:

1. **Network binding.** Both listeners default to `127.0.0.1` — the loopback
   interface. No traffic crosses the machine.
2. **Authentication.** Off by default for the loopback case. Enable a bearer token
   (`--auth-token` / `MECATL_AUTH_TOKEN`), OIDC, or mTLS before binding a non-loopback
   address unless a deliberately controlled private network is the shared authority.
   [Caller identity](#caller-identity-oidc) is a separate, additive axis: a shared
   token is one credential with no subject behind it, while `--oidc-issuer` gives
   each caller a distinct identity. Identity records **who** acted; it does not
   yet decide **what** they may act on.
3. **Transport.** Plaintext by default. Add `--tls-cert` + `--tls-key` for TLS;
   add `--client-ca` to require and verify client certificates (mTLS). Ordinary TLS
   encrypts traffic and authenticates the server, but does not authenticate callers.

A non-loopback bind with no auth is **permitted** (a service mesh may legitimately
front Mecatl) but generates a prominent startup warning:

```
WARN  API bound to a NON-loopback address with NO caller authentication: it exposes
      UNAUTHENTICATED command/file execution to every network caller
```

This is not a hard failure — if you see it intentionally, your mesh owns the auth
layer. If you see it unexpectedly, add `--auth-token`.

mecated logs the effective security posture once at startup:

```
INFO  API security posture  bearer_auth=true  tls=true  mutual_tls=false  rate_limit_rps=0  store=jsonl
```

---

## Persistence

By default (`--store-dir ""`) sessions live in-memory. The server holds state for
all active sessions but loses everything on restart. This is the right default for
development and single-shot clients.

Enable JSONL persistence by pointing `--store-dir` at a directory:

```sh
mecated serve --store-dir /var/lib/mecatl/sessions
```

Each session has an authoritative `.session.json` v2 current snapshot and
`.tools.jsonl` audit and `.events.jsonl` durable-event sidecars under `sid-v1`.
EventLog success requires both file and directory sync. Delete, retention, and migration
cannot proceed without directory sync; this fail-closed durability rule can reduce
availability. Existing canonical snapshots may still Save with a reported weaker
capability, but the first Save of a root-level legacy family fails before mutation if its
migration cannot sync directories. ToolCall audit remains best-effort and may leave an
unsynced or partially synced record. Capability probes establish syscall support, not
media persistence. Startup emits exactly one durability-posture fact (or warning for weak
capabilities) with the capability fields and consequences, without including the path.
Older `.session.jsonl` snapshot histories remain readable and are promoted lazily
on the next write. The files are plaintext and owner-only; do not edit or share
them. Completed sessions are immediately readable by the event-sourced
rehydration path (`internal/adapter/eventsource`); in-flight sessions are
rehydrated from the snapshot on restart.

The stem is derived from the session id but is **not** reversible, so locate a
session by reading the id from the authoritative snapshot rather than from the
filename — see [Session store](/building/extension-points/session-store.md) for
the layout. A store directory written by an older version keeps its files directly
under `--store-dir`; they stay readable and move into `sid-v1/` on that session's
next write, so no migration step is needed.

:::note[Kubernetes and persistent volumes]

If you run `mecated` in Kubernetes with `--store-dir`, you need a PersistentVolume
backed by ReadWriteOnce (or ReadWriteMany for multi-replica with affinity routing).
If a PVC is a hard constraint, use `mecak8s` instead. When configured, its
Redis-backed store has no PVC requirement.

:::

The daemon owns automatic cleanup. Configure the strict operator-only
`retention.version: 1` block in `~/.config/mecatl/settings.yaml` with separate
`main`, `child`, and `scheduled` `max_age`/`max_count` limits plus
`sweep_cadence`; every `0` disables that limit. Negative values, unknown keys,
and unknown versions fail startup. Existing retention CLI flags remain compatible
and explicitly supplied flags win over YAML. Project settings cannot set retention.

Destructive main cleanup is off by default. Enabling its age or count limit also
requires `acknowledge_main_deletion: true` or `--acknowledge-main-retention`; the
server logs the effective planner summary first, and durable `unknown` sessions
remain protected. The authenticated storage-health response reports the secret-free
effective `retention/v1` policy. Embedded mecatui has local-only policy flags;
`mecatui connect` rejects them and cannot configure a remote server without an
advertised management capability. Follow [Operate local session storage](session-storage-operations.md)
for tested systemd/launchd service definitions and the backup, migration, and restore runbook.
The `--session-store-url` flag replaces the JSONL store with a remote gRPC driver
(`mecatl.driver.v1.SessionStoreService`). This is the path for a managed Redis backend
or a custom store behind the driver protocol, and is mutually exclusive with
`--store-dir`. The current driver protocol has no atomic create-only session RPC, so
an OIDC/ownership-enforced server rejects `--session-store-url`; use the local JSONL
backend (or the configured Redis store in `mecak8s`) for multi-user deployments until
the driver adds `port.SessionCreator` parity.

For distributed learning persistence, `--learning-store-url` selects one driver
for attempts, staged proposals, learned skills, and—when automatic learning is
non-off—the automatic admission ledger. The target must implement
`LearningRepositoryCapabilitiesService` and advertise all required repositories;
startup rejects an old or partial driver rather than silently keeping any local
repository or accounting authority. The automatic-ledger service includes bounded,
backend-authoritative discovery of expired held reservations; replacement Builds use it to retain
charges linked to an existing deterministic attempt or reclaim absent attempts without replaying
admission. Equal driver targets reuse one Build-owned connection and shutdown
path. Proposal and skill partition keys are opaque hashes on this wire, not raw
workspace paths or identity claims. The current raw repository RPCs are trusted,
single-tenant infrastructure only, and may be composed only with `OwnershipEnforced=false`.
An ownership-enforced or multi-tenant deployment
fails startup even if the driver self-advertises `enforced`; ADR-0213 workload-authenticated
claims, a private durable owner registry, and separately authenticated maintenance RPCs
must land before that posture is available. Selecting the flag remains an explicit
repository opt-in in off mode: startup still dials, probes, composes, and inspects the
remote set for explicit reflection, learned-skill publication, and recovery of work
admitted by another process, but ordinary off-mode runs do not automatically admit attempts.

### Import from Codex or Claude Code

`mecated import` is an offline migration command for local Codex and Claude Code
projects. It can seed a resumable Mecatl session, copy the project files into a
new workspace, and copy Agent Skills bundles into `<workspace>/.mecatl/skills`.
It does not start a server or require an API key.

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

For Claude Code, use `--from claude-code` and a transcript such as
`~/.claude/projects/<project>/<session-id>.jsonl`. `--skills` discovers the
source tool's conventional project and user skill directories. You can instead
or additionally repeat `--skills-dir <source>` to name exact skill directories.

TRUST BOUNDARY: imported `SKILL.md` files steer the model exactly like
`AGENTS.md`/`CLAUDE.md` — only import skills from a source you trust, the same
way you would point `mecated serve --skills-dir` at a trusted directory.

The safety and portability rules are intentional:

- The imported session keeps user and assistant text only. Provider-private
  reasoning, system/developer instructions, and provider-specific tool calls or
  results are omitted because another provider cannot safely replay them.
- The new session is idle, uses Mecatl's default permission mode, and binds to
  the provider/model selected when it is resumed.
- `--copy-files` is opt-in and requires an explicit `--workspace`. It skips
  `.git`, symlinks, and special files.
- Existing sessions, files, and skill names are never overwritten. Resolve a
  collision or choose a new `--id`/workspace and run the import again.
- Session transcripts and copied files may contain secrets. Import stays local,
  and the JSONL store remains plaintext with owner-only directory permissions.

Use `--source-workspace` when the transcript's recorded `cwd` moved, and use
`--workspace` without `--copy-files` to attach imported history to an existing
project without copying its files.

---

## Multi-replica

`mecated` expects **session affinity** by default: a load balancer should route all
requests for a given session id to the same replica. With affinity, single-writer
enforcement is free — the in-process run registry ensures a session cannot be driven
from two goroutines concurrently within the same process.

Without affinity, or when failover between replicas is required, wire a **session
lease backend**. The lease backend enforces cross-process single-writer: only one
replica may hold the lease for a session at a time; a competing request from a
second replica returns `FAILED_PRECONDITION` (gRPC) / HTTP 409.

Three lease backends are available:

| Backend | Flag | When to use |
|---|---|---|
| flock (single-host) | `--session-lease-dir <dir>` | Multiple `mecated` processes on one machine. flock auto-releases on crash |
| k8s Lease | `--session-lease-k8s-namespace <ns>` | Multi-replica in Kubernetes; uses `coordination.k8s.io` Leases |
| gRPC driver | `--session-lease-url <host:port>` | Custom or managed lease backend via the driver protocol |

The ServiceAccount for the k8s backend needs `get,create,update,delete` on
`leases.coordination.k8s.io` in the configured namespace — no `list` or `watch`.

:::warning[Remote shared stores still need an explicit lease]

A local `--store-dir` automatically uses a flock lease beneath the store root, so
multiple current mecated processes on one host participate without another flag.
Remote stores and multi-host filesystems still require an explicit Kubernetes or
gRPC lease backend (and local flock is not reliable over NFS/EFS). Without one,
use session affinity; destructive maintenance fails closed.

:::

The lease TTL defaults to 30s (`--session-lease-ttl`). A crashed holder's lease
becomes claimable after that interval. The renew interval defaults to
`TTL / 3`; tune it well below the TTL so a slow store does not lose the lease mid-run
and cancel the session.

---

## Operator subcommands

`mecated` ships several subcommands. Two start the daemon, the rest are
one-shot offline actions:

```sh
# Start the network daemon (gRPC + HTTP/SSE) — canonical
mecated serve [flags]

# Serve the Agent Client Protocol over stdio (JSON-RPC 2.0) for an editor
mecated acp [flags]

# Write the operator settings.yaml skeleton to ~/.config/mecatl/settings.yaml
# (POLICY: permissions, posture, guardrails, models)
mecated config init

# Print the settings.yaml skeleton to stdout without writing (paste-ready reference)
mecated config init --print

# Validate settings offline without printing values or writing either input.
# --learning-patch applies one learning: mapping in memory for preflight.
mecated config validate
mecated config validate --file /etc/mecatl/settings.yaml
mecated config validate --file /etc/mecatl/settings.yaml \
  --learning-patch .scratch/learning.yaml

# Write the daemon.yaml listener-topology skeleton to
# ~/.config/mecatl/daemon.yaml (loopback defaults + commented TLS/rate examples).
# It is NOT auto-loaded; start with `mecated serve --config <path>` to use it.
mecated config daemon init

# Print the daemon.yaml skeleton to stdout without writing
mecated config daemon init --print

# Strictly validate a daemon.yaml (default: the conventional path). Never
# starts the server; never prints secrets/raw file content.
mecated config daemon validate
mecated config daemon validate --file /etc/mecatl/daemon.yaml

# Promote a model-authored candidate skill out of quarantine
mecated skills promote \
  --skills-draft-dir /var/mecatl/skill-drafts \
  --skills-dir /var/mecatl/skills \
  <name>

# Print a paste-ready .mcp.json snippet for the loopback perf MCP server
mecated perf-mcp print-config
```

`mecated config validate` defaults to the same
`$XDG_CONFIG_HOME/mecatl/settings.yaml` path as `config init`. It reads and
validates only regular files, never prints settings values, and never writes. A
`--learning-patch` file must contain exactly one top-level `learning:` mapping;
the command replaces or inserts that mapping only in memory and validates the
complete result. With an explicit patch, a missing base is treated as an empty
new file and reports `valid (new file)` without creating it.

`mecated skills promote` is the deprecated compatibility path from a legacy
model-authored quarantine skill (`--skills-draft-dir`) into an operator-managed live catalog
(`--skills-dir`). It shows the full candidate content, asks for operator confirmation (or `--yes`
for CI), validates the promotion, and moves the file. It does **not** read or activate evaluated
skill-lifecycle repository records, so it cannot silently promote an unevaluated lifecycle Draft.
Existing operator/manual skills are unchanged. New lifecycle integrations explicitly import a
legacy `origin:model` draft as inactive. The standard evaluator abstains on that unevidenced
record; later review/evaluation/stage/activation requires an explicit host or operator path. The model
cannot perform the legacy promotion step — it does not have filesystem access outside the workspace.

Schedules are managed **in-chat** via the model-facing `Schedule` tool or over the
gRPC/REST `ScheduleService` API — there is no `mecated schedules` CLI (it was removed;
see [Scheduled tasks](/building/what-you-get/scheduled-tasks.md#host-composition-surfaces-in-chat-grpc-and-rest)).

---

## Hosting a spawned daemon

If something else launches `mecated` — an SDK, an editor extension, a wrapper CLI —
the parent needs three things a network daemon does not: a private endpoint, a way
to know when the server is reachable, and a way for the daemon to notice the parent
died. Four flags cover it.

```sh
mecated serve \
  --grpc-unix-socket /run/user/1000/myapp/mecated.sock \
  --http-addr "" \
  --ready-file /run/user/1000/myapp/ready.json \
  --lifetime-pipe-fd 3
```

**`--grpc-unix-socket`** serves gRPC on a socket and opens **no TCP port at all**.
Dial it as `unix:///run/user/1000/myapp/mecated.sock`. Reachability is filesystem
permission on one path, which is strictly narrower than a loopback port that any
local process may connect to. Some details worth knowing:

- The socket is created **owner-only**, inside an owner-only (`0700`) directory that
  mecated creates if it is missing. If the directory already exists mecated never
  chmods it — it will not touch your `/tmp` or your systemd `RuntimeDirectory` — but
  it does check it. A directory that is **writable by group or other and not sticky**
  is **refused at startup**: deleting a file needs write permission on the directory,
  not on the file, so any local user could unlink your socket and put their own
  listener at that path. A directory that is merely **readable** beyond you is
  accepted with a warning — others can see the socket but cannot connect to it or
  remove it.
- A **stale socket** left behind by a process that was killed is removed on start. A
  socket a **live** process is still accepting on refuses the start instead, because
  removing it would silently steal the running daemon's address.
- `--grpc-unix-socket` **suppresses** the `--grpc-addr` default. Setting both — on the
  command line or in a config file — is rejected at startup rather than resolved by a
  precedence rule you would have to look up.
- Socket paths are short by kernel rule: `sockaddr_un` stores at most 103 bytes on
  macOS and 107 on Linux. mecated checks this at startup and tells you the path, its
  length, and the limit, instead of letting `bind` fail with a bare `EINVAL`.

**An empty `--http-addr`** disables the HTTP/SSE listener *and* the `--metrics-addr`
admin listener. They go together deliberately: both are TCP listeners you did not
have to ask for, and "HTTP is off" would not be true if a second one on port 9090
survived it. `--perf-mcp` is refused in this mode, since the listener it mounts on no
longer exists.

**`--ready-file`** removes the startup race. The file is published **atomically**
(temp file plus rename, so a poller sees the whole document or nothing) and only
**after** composition finishes and every listener is bound — so the moment the path
exists, you can dial:

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

The descriptive half comes from the same projection `GetCompatibilityInfo` serves,
so `api_major` and `features` let a parent refuse an incompatible daemon before its
first RPC. `features` reports what this build implements **and** this deployment
permits, so a listener-scoped identifier like `mcp_servers_on_create` appears here
exactly when the daemon will honour it — which is why the example above, a
socket-only daemon, lists it. The field set is a short allowlist and carries **no credential, TLS
detail, or capability set** — the file is a local artefact with no authentication in
front of it, and it is written `0600`. Ask over the socket for anything more.

The file is **not removed on shutdown**: removing it on a graceful exit but not on
a `SIGKILL` would be a guarantee you could not rely on, so treat it as possibly
stale and check the `pid`. A restart over the same path overwrites it atomically.

**`--lifetime-pipe-fd`** is the parent-crash path. Pass either a pipe's read end
or one endpoint of a connected UNIX-domain stream socketpair to the child, and
hold the other endpoint without ever writing to it. Node and Bun create this
socketpair shape for `child_process` `stdio: "pipe"`. If the parent exits —
cleanly, by `SIGKILL`, or by crashing — the kernel closes its descriptors, the
daemon's endpoint sees EOF, and it shuts down through the same graceful path a
`SIGTERM` takes, persisting session state on the way out. The parent has nothing
to remember. Bytes are read and discarded: this is a liveness signal, never a
control channel. Regular files, terminals, listening or network sockets, closed
descriptors, and nonzero descriptors below 3 are rejected; `0` disables the
watcher.

All four flags are off by default, and a daemon that sets none of them behaves
exactly as before.

### What a local-only daemon additionally unlocks

Your choice of listener also decides one capability, without a flag of its own:
**client-provided MCP servers on session creation**.

A client may pass `mcp_servers` on `CreateSession` (and on `POST /v1/sessions`) to
mount streaming-HTTP MCP servers for that session's lifetime — its own tools, with
its own auth headers, isolated to that session. Whether the daemon accepts the
field depends on where it listens:

| Listener topology | `mcp_servers` |
|---|---|
| `--grpc-unix-socket` **and** `--http-addr ""` | accepted |
| Anything else — including plain loopback TCP | refused on **every** listener, with `UNIMPLEMENTED` / `501` and the code `client_mcp_unsupported` |

Only the fully socket-bound daemon qualifies. A loopback TCP port does **not**, and
neither does a socket-plus-HTTP daemon: serving HTTP at all means serving TCP.

That bar is higher than the one for workspaces, which does accept loopback, and the
difference is deliberate. A workspace path picks among roots you already own. An MCP
endpoint plus a credential points the daemon's **outbound network authority**
wherever the caller chooses and has it carry the caller's token there — a larger
grant, and one worth a narrower door. A loopback port is reachable by every process
and every user account on the machine, browser pages included; a UNIX socket is
guarded by filesystem permissions on a directory created for you alone.

The decision is made once at startup from your listener topology, so a daemon that
serves both a socket and a port refuses the field on both — the same `Service`
answers for each, and the wider listener decides.

The refusal is the **server's**, not a convention clients are asked to honour: a
client that never checks still gets a clean, typed error rather than a mounted
server. Clients that do check read `mcp_servers_on_create` from
`GetCompatibilityInfo` (`GET /v1/compatibility`) — a deployment advertises it
exactly when it will accept it.

**Either all of them mount, or none does.** If a server you asked for cannot be
reached, the create fails with `UNAVAILABLE` / `503` and the code
`client_mcp_unreachable`, naming the ones that did not answer — no session is
created. That code is distinct from `client_mcp_unsupported` because the fix is
different: the unsupported one means this daemon will never accept the field, while
the unreachable one means your own endpoint was down and a retry may work. A
half-mounted session is never reported as success, since from the API it would look
exactly like a working one while quietly missing tools.

Two client-side rules are worth knowing before you wire an SDK. **Server names**
must be 1-64 characters of `[A-Za-z0-9._-]` with no `__` and no duplicates in one
request — they become `mcp__<name>__<tool>`, so `__` would forge another server's
namespace and a duplicate would collide in the tool catalog. **Credentials go in
`headers`**, never in the URL: `https://user:pass@host/mcp` is rejected, because
the standard library turns userinfo into a `Basic` header that would bypass the
protections `headers` values get. Anything logged or echoed shows the URL as
`scheme://host/path` — including the connection error itself, which otherwise
carries the full request URL — so a token in a query string stays out of your
operator log.

Client endpoints also may not redirect, so a vetted URL cannot bounce the daemon on
to a host that was never vetted. Servers you configure yourself are unaffected.

One rule holds regardless of topology: transport is streaming-HTTP only. A `stdio`
entry — or an untyped one carrying a `command` — and an `sse` entry are rejected as
malformed requests everywhere, because Mecatl never spawns an MCP server process.
Header values are never written to logs, never carried in an event, and never
echoed in an error.

---

## Graceful shutdown

On `SIGINT` or `SIGTERM`, mecated shuts down all three listeners with a 10-second
drain. In-flight gRPC streams get `GracefulStop`; in-flight HTTP requests get
`http.Server.Shutdown`. The telemetry pipeline flushes with a 5-second timeout.

Background subagent children owned by active sessions are cancelled when their parent
run is cancelled (the harness cancels runs on shutdown). A session's state is
persisted (if `--store-dir` is set) before the process exits; interrupted runs are
recoverable from the snapshot.

EOF on an inherited `--lifetime-pipe-fd` takes this same path — see
[Hosting a spawned daemon](#hosting-a-spawned-daemon).

---

## What's next

- [Pick your deployment shape](/building/getting-started/deployment-decision.md) — trade-offs between mecated, mecak8s, mecatequi, and engine embedding.
- [Permissions & guardrails](/building/what-you-get/permissions.md) — the rule engine, posture ladder, and guardrail checker in detail.
- [mecak8s — cloud-native k8s](/building/deployment/mecak8s.md) — the no-PVC Kubernetes peer with configured Redis and `coordination.k8s.io` Leases.
- [The agent loop](/building/what-you-get/agent-loop.md) — what mecated is serving: the streaming loop, tool dispatch, and the permission handshake.

## Browsers and CORS

A browser will not call `mecated` cross-origin unless the server says the origin
is allowed. `--cors-origins` grants that, one **exact** origin at a time:

```sh
mecated --http-addr 127.0.0.1:8081 \
  --cors-origins http://localhost:5173 \
  --cors-origins https://app.internal.example.com
```

Matching is exact — scheme, host, and port must all agree. There is no wildcard,
no suffix match, and no subdomain match. `mecated`'s HTTP API can start agent
runs, so a loose match is not an information leak but arbitrary action taken with
a user's credentials; `--cors-origins '*'` and `--cors-origins null` are refused
at startup, as is any origin carrying a path, query, fragment, or wildcard.

With no `--cors-origins` no CORS middleware is installed at all and responses are
unchanged.

> **This is the local-development path.** In production, put a same-origin
> backend-for-frontend in front of `mecated`: it holds the bearer token
> server-side, enforces its own Origin/CSRF policy, and never ships a credential
> to the browser. A token that reaches JavaScript is a token an XSS can take.
