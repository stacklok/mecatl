## 11. Configuration

### Scaffolding a settings file (`config init`)

The operator-tier config lives at `<XDG_CONFIG_HOME>/mecatl/settings.yaml`
(default `~/.config/mecatl/settings.yaml`). To scaffold a fully-commented
skeleton with every subtree and its exact enable semantics:

```console
$ mecated config init            # writes ~/.config/mecatl/settings.yaml (refuses if it exists)
$ mecated config init --print    # print the skeleton to stdout, write nothing
$ mecated config init --force    # overwrite an existing file
```

Validate operator settings offline without starting the server or printing file
contents or values:

```console
$ mecated config validate
$ mecated config validate --file /etc/mecatl/settings.yaml
$ mecated config validate --file /etc/mecatl/settings.yaml --learning-patch .scratch/learning.yaml
```

Without `--file`, validation uses the same conventional path as `config init` and
fails if it is missing. `--learning-patch` must be a single YAML document with
exactly one top-level `learning:` mapping. The command replaces or inserts that
node only in memory, validates the resulting complete settings document, and
never writes either file. A missing base is accepted only when a patch is supplied
and reports `valid (new file)`, enabling first-creation preflight. Both reads are
limited to 256 KiB; symlinks, directories, irregular files, aliases, duplicate
keys, and multi-document YAML are rejected.

For the exhaustive, auto-generated key/type/default/tier table, see the
[configuration reference](../configuration-reference.md). The inline examples in
this guide are illustrative; the reference page is the complete source of truth
(generated from the schema, so it never drifts).

### MCP authentication profiles

All three headless roots read the same operator-tier `mcp.servers` profiles. A project
`.mecatl/settings.yaml` cannot define them. Each server selects exactly one auth mode:
`none`, `static_bearer`, or `oauth`; secret-bearing fields name `MECATL_*` environment
variables rather than containing values. Global OAuth profiles support the strict
exact-origin `network` policy. Broker OAuth requires an explicit empty `network: {}`;
`additional_origins`, `private_origins`, and non-zero `max_redirects` are rejected until
ToolHive can enforce the policy equivalently. See the [generated reference](../configuration-reference.md)
for the complete strict schema.

Local OAuth credentials require an absolute `credentials.local.root` and a canonical
base64-encoded 32-byte key in `credentials.local.key_env`. Generate the key outside YAML,
keep the root owner-only, and put only references in settings:

```console
$ umask 077
$ key_dir="$HOME/.local/state"
$ mkdir -p "$key_dir"
$ chmod 700 "$key_dir"
$ key_file="$key_dir/mecatl-mcp.key"
$ openssl rand -base64 32 >"$key_file"
$ chmod 600 "$key_file"
$ export MECATL_MCP_CREDENTIAL_KEY=$(cat "$key_file")
$ export MECATL_MCP_CLIENT_SECRET='value-from-your-secret-manager'
$ mecated mcp login github
$ mecated mcp login github --no-browser  # prints the authorization URL to this terminal
```

The full command is `mecated mcp login SERVER [--no-browser] [--permission-config
PATH ...]`. The repeatable `--permission-config` selects trusted operator settings files;
it does not supply OAuth values. Issuer, client, scope, secret, root, and network policy remain
settings. Start serving after login and verify the
`mcp__<server>__*` inventory. Startup restores the encrypted credential without another
browser interaction. Expired access tokens refresh lazily; a mutable local store persists
refresh-token rotation, so the next process restart remains warm. If identity metadata
(profile, principal, client, scopes, or resource) changes, run login again. To roll back,
replace the whole profile with `static_bearer` or `none` and restart.

Normal serve/ACP, mecatequi, and mecak8s never open a browser. Environment-backed OAuth
records are read-only and intended for Kubernetes: provision the opaque record externally
and restart pods after rotation. They cannot be populated by `mecated mcp login`. DCR and
ACP cannot provide OAuth profiles or install/drive authorization; after operator authorization,
ACP sessions may invoke the shared global OAuth-backed tools under ordinary permissions. OAuth
for per-session MCP, inline agents, or discovered ToolHive servers is not
supported.

The legacy `--mcp-server name=URL` and `MCP_<NAME>_TOKEN` path remains supported. A
same-name legacy CLI entry replaces the whole settings profile case-insensitively.

### Automatic learning

`learning.mode` in the operator settings file is strict and defaults to `off`:

```yaml
learning:
  mode: off # off | review | auto
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

Sensitivity thresholds are conservative=6, balanced=4, and eager=3. The standard
weights are repeated correction/trusted host contradiction=5, failure recovery=4,
repeated stable tool sequence=3, and substantial success=2. Four model turns, five
successful tool calls, and 12,000 run tokens each add one only when a base signal
exists. Weighted admission accepts only a benign main-session `end_turn`. A genuine
current principal prompt that explicitly asks to remember a fact or learn a procedure
is hard admission on `end_turn`, `max_turns`, `max_tool_calls`, or `budget`: it bypasses
the score, cooldown, and deprecated interval downsampler, but still consumes count and
reserved-token budgets and coordinator capacity. Historical, tool, web, MCP, assistant,
and repository text cannot hard-trigger. An unverifiable post-compaction current span
fails closed.

The automatic limits are sliding durable reservations once a non-off standard
application has successfully selected its `AutomaticAdmissionLedger`. Zero for any maximum
disables automatic reflection under that bound; cooldown zero disables only cooldown.
The window must be 1m–24h. A reservation estimates the selected reflection model's
bounded canonical input plus a 4096-token output cap and remains consumed after failure,
timeout, or abstention. Queue-full does not consume it. The deterministic attempt ID binds
reservation to durable attempt create; after its backend-minted fence expires, a Build-owned
joined reconciler discovers it through either local or remote storage without replaying the original
admission. A crash before create is reclaimed, while an observable attempt retains its charge.
Local composition stores the ledger
beside the attempt store, caps durable reservation records at 512 globally and 128 per opaque
principal partition, and prunes resolved records after deduplication retention; unresolved
saturation fails closed instead of growing the bounded document indefinitely. Cooperating processes
share global/principal count and token
windows, cooldown, and 24-hour digest deduplication. A configured learning driver must
advertise and serve the ledger whenever automatic learning is enabled; startup fails rather
than falling back to per-process accounting. An embedding that does not wire the durable
ledger retains ADR-0114's process-local limitation and must not advertise global bounds.

Automatic admission reports `queued` only after an exact non-empty persisted `RunID` and
idempotent durable `AttemptRepository.Create`. That caller/session/run/digest record—not the
coordinator, receipt cache, or EventLog—is workflow authority across restart. It stores bounded
content-free provenance and source references, never transcript/tool/provider content. Recovery
reconstructs only the bounded canonical projection, verifies owner/run/order/digest, fences it at
every model boundary, and fails closed when exact evidence is unavailable. The attempt APIs expose
get/list and opaque-version retry/abandon only; no attempt-watch feed exists, and ADR-0250 session
EventLog watch is not a substitute.

`review` signal-gates eligible clean completions into the process-wide reflection
coordinator and durably stages valid proposals without changing memory. `auto` uses the same
stage-first path, then promotes operator facts only from explicit principal-authored remember evidence and project facts only from principal-authored evidence at the exact trusted configured workspace. Project candidates from admitted alternate roots remain staged/reviewable but cannot approve, undo, or read/write launch-root project memory until a safe exact-root lifecycle store exists; untrusted project material is not ingested. Tool/assistant/repository-only, conflicting, ambiguous,
sensitive, and unsupported material is not written. Procedures are first durably marked
`deferred_unsupported` for crash recovery and then, when the lifecycle pipeline is installed,
materialized and evaluated: review stages PASS/ABSTAIN and rejects FAIL. Auto PASS activates;
Auto `validated` may also activate structurally accepted/exact, non-legacy, evidence-backed ABSTAIN,
while Auto `evaluated` retains PASS-only behavior. Omitted activation resolves to validated only when
the standard app explicitly selects Auto; the engine pipeline zero remains evaluated. Evaluator
infrastructure failure records a generic durable ERROR verdict, rejects, and never publishes; the
raw error is neither persisted nor logged. Similar candidates, external collisions,
unpublishable partitions, and missing validated repository support stay staged. `off` performs no automatic procedure materialization; explicit drafts/imports
remain inactive. `off` installs no automatic observer/started coordinator worker or eager proposal repository and makes no automatic reflection provider
call. Authenticated explicit reflection remains bounded and synchronous, lazily initializes persistence, bypasses automatic admission/cooldown/budgets/recent-completed state, and uses the completed session's persisted provider/model; without genuine current-prompt promotion provenance its output remains stage-only. A project `.mecatl/settings.yaml` may only tighten the operator mode, sensitivity, and skill activation assurance (`validated` → `evaluated`); its `automatic` subtree is warning-ignored/operator-only. Explicit memory/user-model tools remain available in every mode.
The proposal store defaults to a `reflections/` directory beside the conventional or configured
user-model store; in off mode that directory/flock is not created until the first explicit reflection or proposal operation.

Consolidation is a separate maintenance authorization, not a learning mode. Positive
`--memory-consolidate-interval` and `--user-model-consolidate-interval` values independently start
automatic maintenance; both remain off by default and apply only byte-identical exact duplicates.
They do not raise `learning.mode`, and an effective project `learning.mode: off` cannot suppress an
explicit operator schedule.

Manual review is a separate, immediate maintenance action rather than a setting. In mecatui,
`/dream` chooses project memory or the user model, explicitly acknowledges that generation sends the
selected bounded values and descriptions to the configured model and spends tokens, and displays
exact-duplicate and synthesized-replacement operations. The operator applies or dismisses the whole
plan and receives applied/conflicted/skipped/failed counts. Regeneration is explicit and makes
another provider call. Manual dreaming requires the target store's reviewed atomic operations and a
planner, and is unavailable while ownership enforcement is enabled. Its bounded plans are
process-local and expire, so restart, expiry, or a wrong replica returns a non-retryable not-found state
that offers explicit fresh generation. A same-decision request still applying and an indeterminate
transport failure preserve the plan ID and exact decision for explicit same-decision receipt retrieval;
an opposite decision is never offered, and an applying opposite decision enables no fresh generation.
A terminal opposite decision is non-retryable and permits explicit fresh generation. Manual dreaming neither
changes schedule flags nor collects recall-usage telemetry.

The legacy `--user-model-review` flag maps to `auto` for one compatibility window and
conflicts with an explicit non-auto `learning.mode`. Changes are build-time settings and
require a server restart.

Provider secrets do not belong in `settings.yaml`. API keys and the experimental
manual `openai-codex` OAuth snapshot live in the separate, strict, read-only
`auth.yaml` selected by `--auth-file` (default
`$XDG_CONFIG_HOME/mecatl/auth.yaml`). The Codex snapshot is read once at startup,
has no refresh/write path, and requires a process restart after replacement. See
the [exact credential schema and trust boundary](mecated.md#openai-codex-subscription-manual-token-experimental).

### Operator-defined LLM providers

Only the operator-tier settings file may define custom providers or override an
eligible built-in endpoint. Project `.mecatl/settings.yaml` provider blocks are
ignored. A custom provider has a lower-case stable ID, an HTTPS `base_url` without
userinfo, query, or fragment, a required `default_model`, and one supported wire
flavor. `provider_overrides` is limited to `openai`, `openrouter`, `anthropic`, and
`opencode`; it cannot change the Codex or ToolHive endpoint policy.

```yaml
providers:
  team-gateway:
    base_url: https://gateway.example/v1
    default_model: team-chat
    api_flavor: openai-responses # openai-responses | openai-chat-completions | anthropic-messages
    auth:
      method: api_key # api_key | none
provider_overrides:
  openai:
    base_url: https://openai-proxy.example/v1
```

Keep the matching key out of `settings.yaml`; it belongs in the operator-local
`auth.yaml` as `providers.<id>.api_key`. A custom `api_key` provider is unavailable
without that exact record. A `none` provider needs no record. Custom keys never use
environment-variable fallback. `auth.yaml` accepts only built-in IDs plus the
validated custom IDs from the resolved operator configuration, so unknown or malformed
entries fail closed without echoing credentials.

### Workspace and placement

`--workspace` is trusted server-side configuration for the local deployment default;
it is never a per-session public field. `CreateSession` either omits `profile` to bind
that default or sends `profile:"no-fs"` to attenuate filesystem access. Alternate
worktrees are discovered from an owned source session and selected only with a fresh
opaque selector on ClearSession/ForkSession. Sessions persist the exact private
`EnvironmentRef`, not a duplicate workspace path; public inventory exposes bounded
placement metadata only.

### Model

`--model` (empty by default — the selected provider's own default is used:
`gpt-5` for OpenAI, `openai/gpt-5` for OpenRouter, `claude-sonnet-4-6` for
Anthropic; `openai-codex` uses its first entitled live model when it is the
sole/default provider) is the identifier sent to the provider and stamped into the
system-prompt env. Pass **strings** for forward-compatibility and for
compatible endpoints.

### Session store

| `--store-dir` | Store | Behaviour |
| --- | --- | --- |
| empty (default) | in-memory (`memstore`) | nothing persists across restarts |
| set to a dir | JSONL replay (`jsonlstore`) | snapshots + tool-call log on disk |

The JSONL store writes canonical session families in the owner-only `sid-v1/`
subdirectory under `--store-dir`. The `<versioned-token>` is an internal
reversible `sid-v1-…` encoding; it is **not** the raw logical session id. Logical
ids are opaque valid-UTF-8 strings (matching JSON/protobuf string requirements).
Read the `id` field from the latest snapshot line when identifying a session:

```
<dir>/sid-v1/<versioned-token>.session.jsonl   # one snapshot per Save (latest line wins)
<dir>/sid-v1/<versioned-token>.tools.jsonl     # one record per tool call
<dir>/sid-v1/<versioned-token>.events.jsonl    # the relayed event timeline
```

Older stores may contain lossy legacy families directly under `<dir>`. They are
readable only when the latest snapshot's embedded `id` exactly matches the
requested logical id; the next write migrates that verified family into
`sid-v1/` without rewriting or reordering its records. Mismatched legacy files
remain untouched.

> **Privacy:** the durable store holds the **raw conversation** — prompts, model
> output, and tool arguments/results — in **plaintext** on disk. The store
> directory is created mode `0700` (owner-only). `mecated` keeps the store **off**
> by default (empty `--store-dir` → in-memory); `mecatui` defaults it **on** at a
> per-workspace directory under `$XDG_STATE_HOME/mecatui/sessions` (see
> [the TUI guide](../tui.md)), so a session survives restart and can be inspected
> after the fact.

Persisted sessions are garbage-collected by a background sweep so the durable
store does not grow without bound. Retention requires **positive durable session
taxonomy**: records whose kind is missing, unknown, invalid, conflicts with
their relationship schema, or claims `main` under a reserved non-chat prefix are
protected rather than guessed from the absence of a reserved prefix. **Child** sessions (`subagent-*`/`parallel-*`/`team-*`
families, written by the delegation paths so `InspectSubagent`/`InspectMember`/
`resume:` work) are bounded by `--child-retention` /
`--child-retention-max-per-family` (defaults 168h / 500). The same policy is available as a strict, versioned operator-only block:

```yaml
retention:
  version: 1
  main: {max_age: 0, max_count: 0}
  child: {max_age: 168h, max_count: 500}
  scheduled: {max_age: 168h, max_count: 0}
  sweep_cadence: 1h
  acknowledge_main_deletion: false
```

Each zero limit disables that pass; a zero cadence disables repeat sweeps while
preserving the compatibility startup sweep. Negative durations/counts, unknown
subkeys, and unknown versions fail startup. Precedence is built-in defaults <
operator `settings.yaml` < explicitly supplied compatibility flags. A project
`.mecatl/settings.yaml` retention block is ignored with a warning.

Main retention is destructive and defaults off in both `mecated` and embedded
`mecatui`. Enabling either main limit requires `acknowledge_main_deletion: true`
or `--acknowledge-main-retention`; startup logs the effective planner summary,
including that unknown records remain protected. The authenticated storage-health
projection exposes the content-free effective `retention/v1` policy. Embedded
mecatui configures only its local server with the same explicit flags; connect mode
rejects them and displays remote policy only when the remote management capability
advertises it.

Remote OIDC deployments do not grant storage management to every authenticated
caller. Configure exact verified manager identities only in operator-tier
`settings.yaml` (never a project file):

```yaml
storage_management:
  version: 1
  principals:
    - issuer: https://idp.example/realms/operators
      subject: storage-admin
```

Both issuer and subject must match the verified request context exactly. An empty
or absent list advertises no remote management capability; grant type, display
name, request owner fields, and system-principal status are not shortcuts. Remote
and multi-writer deployments advertise destructive migration/cleanup only with a
working cross-process session lease (`--session-lease-url`,
`--session-lease-k8s-namespace`, or a single-host `--session-lease-dir` where
appropriate). A missing, disabled, held, or backend-unsupported lease fails the
mutation closed; unsupported leasing also clears the capability echo. The private
embedded mecatui server is the sole principal-less and lease-less exception because
composition explicitly proves its local single-process Unix-socket posture.

### Remote store drivers

A third option points the session store (and/or the memory store) at a
**remote driver process** speaking the `mecatl.driver.v1` gRPC protocol:

```sh
mecated serve --session-store-url 127.0.0.1:7443 --memory-store-url 127.0.0.1:7443
```

`--session-store-url` is mutually exclusive with `--store-dir` (and
`--memory-store-url` with `--memory-dir`) — a fatal startup error, never a
silent precedence. Equal URLs share one connection. The driver only ever sees
**opaque snapshots** (the `sessnap` encoding under a `"sessnap-json/1"` format
tag); it sits at the same trust tier as the on-disk store directory. A
conforming driver must accept snapshot payloads up to **64 MiB** (mount the
gRPC server with a matching receive limit; the harness client is already
configured for it).

`--learning-store-url` selects one driver target for the distributed learning
repository set. The target must implement the capability-negotiation service and
positively advertise `AttemptRepositoryService`, `ProposalRepositoryService`, and
`SkillRepositoryService` together. Startup fails if any member is absent; mecatl
never combines a partial remote set with local fallback repositories. The same
Build-owned connection cache and shutdown path used by the other driver seams owns
this connection. Principal and project repository partitions cross this transport
only as opaque SHA-256 values, never as authenticated identity claims or raw
workspace paths. This negotiation does not by itself make the raw driver a tenant
boundary. The shipped RPCs are permitted only as explicitly trusted single-tenant
infrastructure when `OwnershipEnforced=false`; ownership-enforced startup fails closed
until ADR-0213 workload-authenticated middleware, a private owner registry, and separated
maintenance RPCs land. The explicit flag is still dialed, probed, and composed in Off mode
for explicit reflection, learned-skill inspection, and recovery of already-admitted work;
it does not enable automatic observation or admission.

Transport posture: **only LOCAL targets may ride plaintext** — loopback hosts
and unix sockets (the single-user default). Any other driver target
**requires `--driver-tls`, token or not**: the client refuses cleartext
pre-dial, because a driver delivers session payloads, memories,
model-steering skill bodies, and executable skill assets — an on-path
attacker over a cleartext remote link would gain driver-equivalent
capability regardless of auth. `--driver-auth-token` adds per-RPC bearer
auth on top; `--driver-tls-ca` pins a custom CA;
`--driver-tls-cert`/`--driver-tls-key` add a client certificate for mTLS.
Setting any `--driver-tls-*` file **without** `--driver-tls` is a fatal
startup error (it would otherwise be silently ignored). There are **no
retries and no default deadline** on driver RPCs — a driver failure surfaces
as the same unit failure a disk error would.

### Cross-process session leasing (multi-replica single-writer)

By default mecatl assumes **session affinity**: route every session to exactly
one mecated process and never run two processes against the same session id
concurrently. The in-process run registry enforces single-writer WITHIN a
process, but two replicas over one shared store have no cross-process exclusion —
last-write-wins on the JSONL store. For a deployment that cannot guarantee
affinity (e.g. a load balancer that may reroute a session), wire a **session
lease** so the harness enforces single-writer itself:

```sh
# Single host, several mecated processes sharing one --store-dir:
mecated serve --store-dir /var/lib/mecatl/store --session-lease-dir /var/lib/mecatl/leases

# In-cluster multi-replica (coordination.k8s.io Lease per session):
mecated serve --session-store-url store-driver:7443 --session-lease-k8s-namespace mecatl

# Or a dedicated lease driver, independent of the store:
mecated serve --session-store-url store-driver:7443 --session-lease-url lease-driver:7443
```

When a lease is wired, the run-entry path acquires a per-session lease before
driving the engine. A second replica's run-start (or approve-resume) for a
session another replica holds is **refused with HTTP 409 Conflict** (gRPC
`FAILED_PRECONDITION`); the lease is held for the session's life, renewed in the
background (`--session-lease-renew-interval`, default `--session-lease-ttl`/3),
and released on session end / shutdown. A crashed holder's lease lapses after
`--session-lease-ttl` (or, for the flock backend, releases immediately on process
death), after which a survivor takes over. Losing the lease mid-run cancels the
run cleanly (recoverable). The three explicit backends are **mutually exclusive**.
With no lease flag, a local `--store-dir` automatically uses a flock lease under
`<store-dir>/.session-leases`; other stores use a store-provided lease or remain
unleased.

- **`--session-lease-dir` (flock):** SINGLE-HOST only. Several mecated processes
  on ONE machine sharing the dir contend via `flock(2)`, with free crash recovery
  (the OS releases a dead process's lock). NOT safe across hosts (flock semantics
  over NFS/EFS are unreliable) — use k8s or the driver for multi-host.
- **`--session-lease-k8s-namespace` (Kubernetes):** the in-cluster multi-replica
  path. Each session is a `coordination.k8s.io/v1` Lease object named
  `mecatl-lease-<hash>` (the raw id is in the `mecatl.stacklok.com/session-id`
  annotation). Uses in-cluster config, or the default kubeconfig out-of-cluster.
  The pod's ServiceAccount needs this **namespace-scoped RBAC**:

  ```yaml
  apiVersion: rbac.authorization.k8s.io/v1
  kind: Role
  metadata:
    name: mecatl-session-lease
    namespace: mecatl
  rules:
    - apiGroups: ["coordination.k8s.io"]
      resources: ["leases"]
      verbs: ["get", "create", "update", "delete"]
  ---
  apiVersion: rbac.authorization.k8s.io/v1
  kind: RoleBinding
  metadata:
    name: mecatl-session-lease
    namespace: mecatl
  subjects:
    - kind: ServiceAccount
      name: mecatl            # the mecated pod's ServiceAccount
      namespace: mecatl
  roleRef:
    kind: Role
    name: mecatl-session-lease
    apiGroup: rbac.authorization.k8s.io
  ```

  A missing RBAC verb surfaces as a hard error (a Forbidden, never a silent
  no-lease run).
- **`--session-lease-url` (driver):** a remote `mecatl.driver.v1.SessionLeaseService`
  (multi-host, store-independent), sharing the same `--driver-tls`/auth posture and
  connection cache as the store drivers above.

Without an explicit backend, mecatl can also discover a lease from a session
store that happens to implement the lease seam (type-assertion, like the
retention seam). See `docs/adr/0027-cloud-native.md` for the full design.

#### Session affinity and owner handoff

Official clients send `X-Mecatl-Session-ID` on each session-bound gRPC or HTTP
operation. The value is exact: it is neither trimmed nor normalized, and the HTTP
comparison uses the decoded path ID. A missing header remains compatible for older
clients. Duplicate values, an illegal field value, or any byte mismatch return the
non-disclosing `invalid session affinity metadata` error before work begins; the server
never echoes either value. The field is only a routing/correlation hint. It does not
authenticate, authorize, establish caller or lease ownership, fence storage, select
provider state, or grant any other authority.

Provider requests do not trust the ingress copy. The engine places the loaded session
ID in the authoritative run context, and each provider attempt/fallback reads it there.
An absent or illegal run binding is omitted without failing inference. The TypeScript
raw `withSessionAffinity` helper rejects an illegal explicit ID synchronously and leaves
caller headers untouched; high-level use of an unrepresentable server-issued ID remains
compatible by omitting affinity when no explicit bind was requested. This preserves
legacy clients and custom providers without a protobuf change.

With leasing configured, every session-family mutation must own the session lease. On
renewal loss, local mutation authority is invalidated before cancellation, so later
saves, deletes, event/tool records, metadata updates, and sidecar changes cannot start.
This is not backend fencing: a storage call admitted before invalidation may finish.
For a local awaiting run, lease loss retracts only the local ask and leaves its durable
`PendingAsk` unchanged and unresolved for a successor after TTL expiry.

`CloseSession` (and HTTP `DELETE /v1/sessions/{id}`) fails precondition while a local
run is active or awaiting; it does not release ownership or tear down that live
session. A persisted awaiting session with no live local run may close its local
resources while retaining the durable resume point. `GracefulDrain` first rejects all
new prompt/resume admission, then preserves awaiting snapshots, cancels and joins
executing runs, persists settled state, and only then releases their leases. If the
bound expires before a run joins, the stale process stops renewal and local mutation
but does not release the lease; takeover waits for process death and TTL.

Handoff is intentionally modeled as client retry, not owner forwarding. A killed
owner's stream drops; pre-TTL requests cannot acquire, then one post-TTL successor
acquires, reloads Redis, repairs a crash-orphaned `running` snapshot, and continues.
The offline fake-clock tests establish application sequencing only. They are not proof
of Gateway/mesh routing, EndpointSlice convergence, production timing, or exactly-once
external effects.

Before enabling affinity, the separate infrastructure rollout is blocked until live
acceptance proves authenticated admission, request and header-size bounds, and client,
IP, and principal rate limits apply before or independently of affinity. Legal,
attacker-chosen IDs must not create an unbounded targeted-replica sink. Helm and offline
checks prove only that this chart stays neutral; they do not satisfy that external gate.

### Remote content-source drivers (skills + soul)

The same protocol carries two **content sources**:

```sh
mecated serve --skill-source-url 127.0.0.1:7443 --soul-source-url 127.0.0.1:7443
```

`--skill-source-url` replaces local skills discovery entirely (mutually
exclusive with `--skills-dir`/`--skills-conventional`). The driver's skill
set is **snapshotted once at startup** (fatal if the driver cannot answer —
an explicitly configured source that is down is a misconfiguration, never a
silent no-skills run). Skills cross the wire as **logical bundles** — name,
description, body, and payloads addressed by slash-relative logical names
(`references/api.md`, `scripts/run.sh`) — no paths. Calling `Skill` with a name
returns the instructions and bounded logical inventory. If those instructions need
a textual payload, the model calls `Skill` again with `{name, asset}` and only that
asset is fetched. The harness validates the logical name, enforces the tool-output
size bound, and rejects invalid UTF-8 or NUL-containing assets. It does **not**
materialize bundles, honor executable bits by creating files, expose a base
directory, or grant `Read`/`Bash` access. A workflow needing a real file must create
or obtain it explicitly in the workspace under normal permissions. **Trust:** a
driver-served `SKILL.md` steers the model
like AGENTS.md/CLAUDE.md — point this only at a driver you trust (the same
tier as `--skills-dir`).

`--soul-source-url` serves the persona from the driver instead of the local
user soul file, occupying the **user slot** of the selection precedence (it
shadows a project soul exactly like a present user soul; `--no-soul` and the
`soul:apply` permission gate still apply). The driver is **probed at
startup** (fatal if unreachable); a fault at run time degrades fail-soft to
no fragment with a logged warning. The body is **re-validated locally**
(byte cap, injection scan, data-fence integrity — a driver is never trusted
to sanitize). The **drift baseline is skipped** for driver souls — the
baseline is sidecar-file machinery for a local file you edit, while a driver
sits behind the operator's own auth — so `--soul-strict` and
`--approve-soul` are no-ops for this provenance (one INFO line records the
skip).

Both share the `--driver-auth-token`/`--driver-tls*` posture, and equal URLs
share one connection with the store drivers.

### Remote content-source drivers (agent definitions + slash commands)

Two more sources ride the same protocol:

```sh
mecated serve --agent-source-url 127.0.0.1:7443 --command-source-url 127.0.0.1:7443
```

`--agent-source-url` serves the **agent definitions** (the Subagent
specialists / team-member roles) from the driver. Like skills, the set is
**snapshotted once at startup** (fatal if the driver cannot answer — per-def
child engines are built once at build time, so there is no re-fetch). It is
mutually exclusive with explicit `--agents-dir`; the default-on
`--agents-conventional` discovery is simply **superseded** (an INFO line
narrates it — failing every default deployment over an ON-by-default,
usually-inert flag would be wrong; this asymmetry vs the opt-in skills
conventional discovery is deliberate). Defs cross the wire whole — tools,
limits, model/provider hints, skills, hooks, scoped MCP servers — with **no
path**: diagnostics identify a driver def as `driver: <target>`. (The
`memory:` field — per-agent persistent memory — is **not** carried
over the driver wire in v1; a driver-served def stays cold-start.) Inline MCP
server **headers are secret-shaped** (e.g. `Authorization`): the harness
never logs or projects them; they ride this wire only because driver dials
refuse all non-local cleartext. The driver's claimed origin tier is ignored —
every driver-served def is stamped `driver`. **Trust:** this is STRONGER than
model steering — a def's `hooks:` map executes as **ungated shell on the
harness host** (`hookexec`, every scoped lifecycle phase, no permission ask),
strictly more capability than the skill driver, whose payloads still ride the
permission-gated Bash path. **A compromised agent-source driver executes
arbitrary shell on the harness host via def hooks; treat it as
harness-equivalent infrastructure.** The build narrates every driver def that
carries hooks (`agent def carries lifecycle hooks (harness-side shell)` —
names only, never hook values) so the capability is visible at startup.

`--command-source-url` serves **slash-command templates**. Unlike every
other source driver it **composes instead of replacing**: the expansion
order is file-backed commands → driver commands → MCP prompts
(first-match-wins), so a local `<name>.md` shadows a same-named driver
command, and the palette merges all three. It is also **live**, not a
snapshot — the driver is consulted on every expansion and palette listing,
matching the file expander's reads-current-files behaviour, so the command
set may change while the server runs. The driver returns the RAW template
(frontmatter allowed); the harness strips frontmatter and substitutes
`$ARGUMENTS`/`$1`/`$2`… exactly as for a file command, so templates are
portable between the two backends byte-for-byte. The driver is probed once
at startup (fatal if unreachable); a fault at run time **fails soft** — the
raw input passes through unchanged and the palette omits the source (a
transient blip never aborts a run and never latches a command "missing").

All four content-source drivers share the `--driver-auth-token`/
`--driver-tls*` posture, and equal URLs share one connection.

### Permission modes

Set per session via `CreateSession` `mode` (HTTP `mode` string / proto
`PermissionMode`):

| Mode | Proto enum | Posture |
| --- | --- | --- |
| `default` | `PERMISSION_MODE_DEFAULT` | standard deny → ask → allow |
| `plan` | `PERMISSION_MODE_PLAN` | read-only toolset; mutations hard-denied |
| `acceptedits` | `PERMISSION_MODE_ACCEPT_EDITS` | auto-accept edits |

`PERMISSION_MODE_UNSPECIFIED` (and any unknown string) defaults to `default`.

### Default limits

A **zero** `Limits` value disables every stop condition, so the composition root
injects non-zero defaults for any session created without explicit limits, so a
default session is always bounded:

| Limit | Default | Disables when 0 |
| --- | --- | --- |
| `max_turns` | `2000` | yes |
| `max_tool_calls` | `8000` | yes |
| `max_consecutive_failures` | `5` | yes |

Supplying **any** non-zero limit field is taken as explicit and used as-is.
