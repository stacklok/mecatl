# ADR 0292 — TypeScript SDK local daemon and callback tools

- Status: Accepted
- Date: 2026-09-03
- Scope: `sdk/typescript/` — the `./node` subpath's `spawn()`, `query()`, `tool()`, the loopback MCP tool host, and the client-level diagnostics sink. Client-side only: no `contracts/proto/` change and no production change under `internal/adapter/server/` or `cmd/mecated/`.
- Supersedes: none. Extends [ADR 0279](./0279-typescript-sdk-architecture.md) (M1) and [ADR 0288](./0288-typescript-sdk-durable-attachment.md) (M2) into the M3 surface those two deferred.

## Context

[#821](https://github.com/stacklok/mecatl/issues/821) M1 and M2 are on `main`.
M3 is "Node/Bun process and callback capabilities": `spawn()`, `query()`,
`tool()`, and the local MCP host that makes a TypeScript function callable by
the model.

**The server half already shipped**, and auditing it is what fixes M3's shape
rather than leaving it to be invented per worker:

- [`cmd/mecated/daemonhosting.go`](../../cmd/mecated/daemonhosting.go) owns
  `--grpc-unix-socket` (mutually exclusive with a configured TCP gRPC address),
  `--ready-file` (absolute path, published atomically at mode `0600` after the
  listeners bind), and `--lifetime-pipe-fd` (fd ≥ 3; EOF on the read end means
  the parent died and the daemon stops through the ordinary shutdown path). An
  empty `--http-addr` disables the HTTP, metrics and admin listeners together,
  and `--perf-mcp` is refused in that case. `readyDoc` is a deliberate
  allowlist — schema, pid, transport, gRPC address, socket path, optional HTTP
  address, API major, features, deployment label — sourced from the same
  projection `GetCompatibilityInfo` serves rather than from raw config, so the
  next field someone adds cannot be a token. The acceptance record is
  [`sdk-server-enablers.md`](../acceptance/sdk-server-enablers.md) Scenario 8.
- `CreateSessionRequest.mcp_servers` carries `McpServerSpec{name, url, type,
  command, headers}`, classified by the one shared validator
  (`mcp.PartitionClientServers` → `Service.ClientMCPFromWire`). `stdio` and
  `sse` are hard-rejected on every listener; `http` is accepted for `https`, or
  `http` **only to an explicit loopback host**; `headers` is SECRET-SHAPED —
  never logged, never projected into an event, never echoed in an error. The
  acceptance record is `sdk-server-enablers.md` Scenario 9.

Three audited server facts decide most of what follows, and none of them is
guessable from the proto alone:

1. **`mcp_servers` is listener-scoped, and the permitting topology is exactly
   one shape.** `clientMCPOnCreateForListeners` returns true only for
   `grpcUnixSocket != "" && httpAddr == ""`. That is deliberately stricter than
   the workspace-authority rule, which accepts a loopback TCP bind: an MCP
   endpoint plus its auth headers lends the daemon's *outbound network*
   authority, and loopback TCP is reachable by every local process and user on
   the host. So a spawned daemon that also opens HTTP silently loses `tool()`.
2. **The mount is all-or-nothing but the tool registration is not.**
   `WithClientMCP` arms `clientMCPStrict`, so `verifyClientMCPMounted` fails the
   create with `client_mcp_unreachable` when a requested *server* does not
   connect. Tool-name collisions behave differently:
   [`mcp.Register`](../../internal/adapter/mcp/mcp.go) is skip-and-continue,
   first-wins, and reports the skipped names to the server's own WARN. A
   client server whose name matches an operator-configured server-global one
   therefore mounts, passes the strict check, and has its tools silently
   shadowed — with no wire signal to the client, because
   `CreateSessionResponse` reports no mounted-server or tool inventory and
   there is no `ListTools` RPC.
3. **The Go MCP client tolerates a JSON-only server, but its handshake has one
   more step than the transport suggests.**
   `mcp.StreamableClientTransport` sends `Content-Type: application/json` and
   `Accept: application/json, text/event-stream`, accepts an
   `application/json` POST response, and treats `405` on the standalone `GET`
   stream as a clean no-op. The pinned build is past SEP-2575, so
   `Client.Connect` first probes **`server/discover`** and only falls back to
   `initialize` on a non-modern error — and it rejects an `initialize` result
   whose `protocolVersion` is outside its `supportedProtocolVersions`. It also honours `structuredContent` and validates
   it against the tool's advertised `outputSchema`
   ([`internal/adapter/mcp/tool.go`](../../internal/adapter/mcp/tool.go)).

The remaining forces are client-side. `Client.close()` and
`Symbol.asyncDispose` already exist from M1/M2 and today tear down the status
monitor, abort attachments, and dispose only a client-*owned* transport;
`CreateSessionOptions.mcpServers` already exists as a raw passthrough. There is
no diagnostics sink anywhere in the SDK yet, and `spawn()`'s failure reporting,
`query()`'s ask-deny path, and the tool host's handler failures all need one.
M1's e2e harness ([`sdk/typescript/e2e/harness.ts`](../../sdk/typescript/e2e/harness.ts))
already spawns `mecated` over TCP and UDS and polls the ready file, so `spawn()`
is a productisation of something with a working reference, not a new idea.

## Decision

**1. `spawn()` resolves an existing binary and never fetches one.** Resolution
is ordered and total: an explicit `binaryPath` option, else the `MECATED_BIN`
environment variable, else a `PATH` lookup for `mecated` performed by the SDK
against `process.env.PATH` without a shell — there is no shell interpolation
anywhere on the spawn path. Each candidate must be an existing executable file;
a candidate that is not is a typed `spawn_failed` naming which source supplied
it, raised before any process is created. Auto-download is an explicit #821
non-goal and stays one: an SDK that fetches and executes a binary is a
supply-chain surface, and mecatl already ships signed release artifacts for the
operator to place. On `process.platform === "win32"` every local entry point —
`spawn()`, `query()`, `tool()` — fails with a typed `unsupported_platform`
before resolution; #821 scopes v0.1 Windows to remote `connect()`.

**2. The spawned daemon's argv is SDK-owned, and the tool-capable topology is
not a caller's to erode.** `spawn()` always passes `serve
--grpc-unix-socket <sock> --http-addr "" --ready-file <ready>
--lifetime-pipe-fd <fd>`. That is precisely the one topology
`clientMCPOnCreateForListeners` permits, which is why it is the default rather
than a preference. Caller-supplied extra arguments are appended through an
explicit `args` escape hatch, and the SDK **refuses** any extra argument naming
a flag it already set (`--grpc-unix-socket`, `--grpc-addr`, `--http-addr`,
`--ready-file`, `--lifetime-pipe-fd`) with a typed error rather than letting
last-flag-wins quietly decide. The SDK-owned argv carries no posture, trust, or permission flag — no
`--posture`, `--yolo`, `--trust-project` — so a library embedded in someone
else's program never loosens the daemon's default permission posture on the
operator's behalf. Enabling the HTTP listener is a first-class
`http: true` option, not a smuggled argument — and it is honestly costly:
the daemon then advertises no `mcp_servers_on_create`, so `tool()` is
unavailable on that client.

**The SDK never infers feature support from its own argv.** It reads the
`features` array out of the ready file, which the daemon sources from the same
projection `GetCompatibilityInfo` serves. One source of truth, and it is the
daemon's, so a future change to the listener rule cannot leave the client
believing something the server will refuse.

**3. Readiness is the ready file, read strictly, with three terminal
outcomes.** `spawn()` polls the ready-file path (the M1 harness's shape: short
fixed interval, deadline-bounded) rather than racing a connect loop or parsing
stdout. A document whose `schema` is not `mecated-ready/1` is a typed failure,
not a best-effort read — `readyDoc`'s own contract is that a parent "can fail
cleanly on a future version instead of misreading an added field". The three
outcomes are: a valid document (ready), the child exiting first
(`spawn_failed`), or the deadline (`readiness_timeout`, default 30 s,
configurable). Both failure outcomes carry a **bounded, redacted** tail of the
child's stderr: last 64 KiB captured, last 4 KiB reported. The reported tail
starts at the first **complete line boundary** — a leading partial line is
dropped — because a byte budget alone would hand back a fragment beginning
mid-value, which a line-anchored pattern cannot match and which is therefore
exactly the leak the redaction exists to stop. Redaction runs over the tail
actually reported, after truncation, replacing a matching line wholesale rather
than masking part of it. The child's stderr is piped for this reason alone;
stdout and stdin are `ignore`. **Every terminal failure after the child was
launched — including one after a valid ready file — stops the child (SIGTERM,
grace, SIGKILL) and then removes the runtime directory, in that order:** a
surviving daemon holds the operator's real credentials on a live socket, and
removing the directory first would unlink the socket out from under a process
still serving it.

**4. The runtime directory is private, and the socket path is bounded before
the daemon sees it.** `spawn()` creates one `0700` directory per client under
the OS temp directory — via `fs.mkdtemp`, atomically and with an unguessable
suffix, never at a predictable path and never with `recursive: true`, so a
pre-existing directory or symlink planted there is a typed failure rather than
something adopted — and puts `ready.json` and `mecated.sock` in it. That
matters more than the mode does: `ensureSocketDir` explicitly delegates the
socket's defence to its parent directory and exempts a sticky `/tmp`, so a
predictable name in a world-writable directory would inherit exactly the
weakness the `0700` appears to close. The
directory is what actually defends the socket — `listenUnixSocket` refuses a
group/world-writable non-sticky parent for the same reason — and it is removed
on disposal. Before spawning, the SDK checks
`Buffer.byteLength(socketPath) < 104` (the Darwin `sun_path` bound, the
smaller of the two platforms) and falls back to a shorter base directory when
the OS temp path is too long, failing typed if even that is over. `bind(2)`
would otherwise fail as an opaque `EINVAL`; `validateUnixSocketPath` already
turns that into a sentence, and paying the check client-side means the error
arrives without a process launch. A socket path left by a previous process is
the daemon's problem, not the SDK's: `reclaimStaleSocket` removes a
provably-dead one and refuses a live one, and the SDK does not second-guess it
by unlinking anything itself.

**5. The lifetime pipe is required by default, and the descriptor it passes
must be a FIFO — which is not what `stdio: "pipe"` gives you.**
`checkLifetimePipeFD` demands `S_IFIFO` and names `socket` as an explicit
failure, while libuv backs a `child_process` `stdio` `"pipe"` entry with a
**socketpair**: an experiment on Node 24 shows the child's fd 3 reporting
`SOCKET`, so the obvious wiring makes `mecated` refuse to start. This binds
every runtime, not just Bun.

The SDK therefore passes a descriptor that really is a FIFO — created as one,
opened **read-only** so the child is not itself a writer holding it open, and
handed to the child as an explicit fd in the `stdio` array — with the parent
retaining the write end and never writing to it. The child sees EOF whenever
the parent dies, crash included. `openLifetimePipe`'s startup refusal remains
the backstop rather than a client-side capability probe: a runtime that fails to
inherit the descriptor produces a loud startup failure, never a daemon that
outlives its parent. `lifetimePipe: false` is the documented escape hatch; it
costs only *parent-crash* shutdown, since `close()` stops the daemon regardless.

**Which of the three mechanisms ships is an open decision, deliberately left to
the server side rather than settled here** — Node has no `mkfifo` binding, so
the FIFO route needs the `mkfifo(1)` binary; the alternative is widening
`checkLifetimePipeFD` to accept `S_IFSOCK`, which is a production `cmd/mecated/`
change this plan's scope forbids; the fallback is dropping the flag from the
default argv. The acceptance plan proves whichever is chosen in its **first**
PR, because nine later PRs assume the answer.

**6. Disposal stops only what this client started.** `Client.close()` and
`Symbol.asyncDispose` (already the M1/M2 entry points) extend to: cancel runs
this client owns, detach its attachments and activity streams, stop the status
monitor, abort in-flight tool handlers and close the tool host, close the
transport, then — **only for a daemon this client spawned** — `SIGTERM`, wait a
bounded grace, `SIGKILL`, and remove the runtime directory. A `connect()`ed
daemon is never signalled. Disposal is idempotent and never throws for a
teardown fault: each failure is reported to the diagnostics sink and the
remaining steps still run, because a half-torn-down client that also threw is
the worst of both. The ordering is fixed — tool host before transport, transport
before daemon signal — so a handler mid-call is aborted rather than left writing
into a closed socket.

**7. Inheritance is total by default and overridden explicitly.** A spawned
daemon inherits the parent's environment, and therefore the operator's ordinary
mecatl configuration and provider credentials: the point of `spawn()` is a local
daemon that behaves like the one the user runs by hand. **Durable state is the
exception**, and calling it inherited would be false — `--store-dir` defaults to
empty ("in-memory store") and the SDK-owned argv does not pass it, so a spawned
daemon's sessions live and die with the process.
`env` (merge) and `workspace` are explicit options. The SDK never reads,
copies, logs or reports an operator credential — and the one credential it
*mints*, the tool host's bearer, travels only in `McpServerSpec.headers`, never
by argv, environment, or file (Decision 10): the environment is passed through to
`spawn` and never enumerated into a diagnostic, the ready file is already a
non-secret allowlist, and the stderr tail is redacted (Decision 3). The daemon's
own `envscrub` denylist (`AGENTS.md`) keeps those credentials away from
agent-facing shells; that is the server's guarantee and the SDK does not
duplicate or weaken it.

**8. `query()` owns its resources and refuses plan mode in this milestone.**
`query()` spawns, creates a session, runs one prompt, and cleans up; it deletes
the transient session unless `retainSession` is set, and closes only what it
created — given an existing `Client` it closes no client and stops no daemon.
`retainSession` means "do not delete this session", not "persist it": on the
SDK-owned argv the store is in-memory (Decision 7), so a retained session
outlives the call and not the daemon. A durable variant would need a `storeDir`
option pointing outside the runtime directory disposal removes; that is an
additive public-API change and is not made here.

A permission ask with no `onPermissionAsk` responder is **denied**, an SDK
diagnostic is emitted, and the model continues; it is never silently allowed
(#821, "Permissions"). That denial is `query()`'s policy and **not** the `Run`'s:
a `Run` obtained outside `query()` still leaves an unanswered ask pending for
`resolveAsk()`, which is M1's shipped manual path and must not change.

Plan mode is refused with a typed `unsupported_feature` naming
`session.resolvePlan()`, before spawning. #821 asks for a `query()` that
"requires `onPlanApproval` before starting", but `ApprovePlan` is a
server-streaming RPC that can emit the resumed run *and* a continuation run
with a different id — which is exactly why #821 scopes `session.resolvePlan()`
/ `PlanResolution` to M4, and why [ADR 0288](./0288-typescript-sdk-durable-attachment.md)
listed it out of scope. A required callback that cannot fire is a worse public
API than a refusal, and an options field M4 must then reinterpret is not
undoable after `v0.1.0`. The gate lands in M4 alongside the surface it gates;
adding `onPlanApproval` later is purely additive.

**9. `tool()` binds an immutable set at session creation, and collisions fail
loudly at both layers that can see them.** `tool(name, schema, handler)` is
exported from `./node` only and is accepted only on a client with a spawned,
tool-capable daemon; every other client raises a typed `unsupported_feature`
(Decision 12). The set is bound when the session is created and never mutated —
dynamic add/remove is an explicit #821 non-goal, and the server mounts a
per-session engine once.

Names: one configurable server name, defaulting to `sdk`, so a tool `lookup`
reaches the model as `mcp__sdk__lookup`. The SDK enforces
`validateClientServerName`'s grammar locally (`[A-Za-z0-9._-]`, no `__`, bounded
length) rather than discovering it as a server rejection. Collisions are caught
in the two places they are catchable: **locally**, a duplicate tool name or a
name that would produce a malformed namespaced name fails at registration; and
**pre-flight**, before creating a session, the SDK calls `ListMcpSources` and
refuses when the chosen server name matches a resolved server-global server
name. The pre-flight exists specifically because the server's own behaviour
there is skip-and-continue with a WARN the client never sees (Context, fact 2),
so without it "fail loudly on namespace collision" would be a promise the
client could not keep. It costs one unary RPC per tool-bearing create.
Annotations: a tool is **mutating** unless `readOnly: true` is set, which maps
to MCP's `readOnlyHint`; mecatl's `remoteTool` already derives its `ReadOnly()`
from that hint, so the conservative default composes with the harness's
read-parallel/mutate-serial dispatch rather than fighting it. The hint is an
**assertion the SDK does not verify**, and the public type says so: a
mis-annotated tool joins the concurrent read batch, and plan mode's hard-deny
set is the fixed `{Edit, Write}` list that no `mcp__` name is in.

**10. The tool host is a hand-written, stateless streaming-HTTP MCP server on
`node:http`.** It implements the narrow set the harness actually calls. That set is **six**
methods, not five: the pinned client is past SEP-2575 and `dial` calls
`Connect(ctx, transport, nil)`, so the first JSON-RPC method it sends is
`server/discover` — answered with a method-not-found error, which is precisely
what makes it fall back to the legacy handshake — followed by `initialize`,
`notifications/initialized`, `tools/list`, `tools/call` and `ping`. Calls are
answered POST with `application/json`; the notification is answered `202` with
no body; GET and DELETE get `405` plus `Allow: POST`. The `protocolVersion` the
host echoes must be a member of the client's `supportedProtocolVersions`, since
`Connect` closes the session outright when it is not. The alternative was the official
`@modelcontextprotocol/sdk` TypeScript package, rejected on dependency
footprint: it pulls a server-side tree (express, cors, zod, ajv,
eventsource, pkce-challenge) into `./node` for a server whose entire surface is
five methods, which is the same trade [ADR 0279](./0279-typescript-sdk-architecture.md)
made when it hand-wrote the browser HTTP/SSE client rather than importing a
framework. The cost is honest: hand-written protocol compatibility is proven on
the wire, against a real `mecated` and the real Go MCP client, by this plan's
e2e — not against a fake.

One host per spawned client, bound to the literal `127.0.0.1` on port 0,
protected by a 256-bit bearer minted per client from a CSPRNG
(`randomBytes(32)` / `getRandomValues`, never `Math.random()`), compared in
constant time over fixed-length digests, and sent to the daemon as an
`Authorization` header inside `McpServerSpec.headers` — the field the server
already treats as secret-shaped. The token never appears in a log, an error, a
diagnostic, the ready file, the spawn argv, the child's environment, or any
file: its only outbound channel is that header over the UDS connection, which
keeps it out of `ps` and out of every shell the daemon later spawns (whose
`envscrub` denylist would not know its name). The host lives under a
`./node`-only module; the existing package guard test keeps it out of the `.`
entrypoint's module graph.

**Loopback TCP is forced, so the bearer is the whole of the authentication —
and it needs a second control.** There is no UDS MCP transport to prefer:
`ValidateClientURL` accepts `https`, or `http` to a loopback host, and nothing
else. This ADR's own Context quotes the server's reason for refusing
`mcp_servers` on a loopback TCP *API* listener — reachable by every local
process and user, and for an HTTP surface by a browser page — and the tool host
is exactly such a listener. A peer-address check cannot help: behind a
`127.0.0.1` bind every peer is already loopback. What actually closes the
browser half is that the host emits **no** `Access-Control-Allow-*` header ever,
answers every non-POST method including `OPTIONS` with `405`, and refuses before
authentication any request carrying an `Origin` header or a `Host` other than
its own bound `127.0.0.1:<port>` — the DNS-rebinding control the MCP
specification requires of a local server. `initialize` and `tools/list` are
authenticated like every other method; there is no unauthenticated
introspection, an unauthenticated request is refused before its body is read,
and bodies are size-capped. The URL handed to the daemon is built from the
literal bound address rather than the name `localhost`, which `isLoopbackHost`
accepts and the daemon would then resolve through its own OS resolver.

**11. Execution is bounded, cancellable, and its failure modes are split.**
Concurrency is capped at 8 handlers per client by default, tightenable
per tool, with excess calls **queued** rather than rejected — a bound the model
cannot feel as an error — up to a fixed queue depth, beyond which a call returns
a model-visible error instead of growing memory. Every handler receives an
`AbortSignal` aborted by client disposal, by session close, by the request being
cancelled, and by a per-call wall-clock deadline after which the host returns a
typed error result without waiting for a handler that ignored it. Arguments are
validated against the tool's schema before the handler runs, **non-mutating** —
no type coercion, no default injection — and materialised on a null-prototype
object, because they are model-authored input crossing into user JavaScript
while the model may be summarising attacker-influenceable content. Results
normalise three ways: a `string` becomes one text block; any other JSON value
becomes `structuredContent` plus the text mirror the MCP spec recommends and
mecatl's `remoteTool` already expects; an explicit `CallToolResult` passes
through verbatim, `isError` included. A result whose serialized size would
exceed the harness's `toolkit.MaxOutputBytes` is refused **client-side** with a
typed error naming the cap: the harness silently truncates an over-cap string
and fail-closes an over-cap structured result into a remediation message telling
the model to paginate or apply a jq filter — advice a user-authored callback
cannot act on. The two error paths are deliberately
different: a **thrown** exception yields a generic model-facing failure carrying
a correlation id and nothing else, with the full cause going only to the
diagnostics sink, while an explicit `isError` result is the intentional
model-visible path. Handler code is the SDK user's, and its stack traces and
messages are not something the model — or a prompt-injected page the model is
summarising — should get to read.

**12. Diagnostics are a structured client-level sink that never writes to
`console`.** One optional `diagnostics` sink is installed at client
construction and receives structured records — a stable `code`, a level, a
message, and typed fields. Nothing is written to `console` unless the caller
installs a sink that does. M3's emitters are: the spawn stderr tail on a
startup failure, the `query()` ask-deny, a tool-handler exception, and a
cleanup failure during disposal. SDK-local diagnostics never enter the server
event union (#821, "Diagnostics and local state"); there is no telemetry and no
credential persistence.

**13. Error vocabulary extends the existing registry rather than forking it.**
Server codes stay `MECATL_ERROR_CODES`, verbatim from the Go registry —
`client_mcp_unsupported` and `client_mcp_unreachable` are already there and are
what a refused or unmountable tool host surfaces as. The local `SDKErrorCode`
union gains exactly four members: `spawn_failed`, `readiness_timeout`,
`unsupported_platform`, and `tool_registration` (duplicate name, malformed
name, invalid schema, pre-flight collision — one code with a discriminating
reason field, not four codes, because they are one authoring mistake at one
call site). Everything else reuses `unsupported_feature` and `invalid_state`.
No second taxonomy.

**14. Non-goals, stated so they are not re-litigated per PR.** Windows local
spawn; binary auto-download; remote tool hosting or any reverse callback
channel over `connect()`; dynamic tool add/remove after session creation;
stdio MCP in any form; and a `spawn()` that manages more than one daemon per
client.

## Consequences

- **`tool()` is coupled to one daemon topology, visibly.** UDS with HTTP
  disabled is the only shape that carries it, so `spawn({ http: true })` is a
  documented trade rather than a surprise, and the refusal comes from the
  daemon's advertised feature set rather than a client guess. If the server
  ever relaxes `clientMCPOnCreateForListeners`, no SDK change is needed.
- **The namespace pre-flight buys a real guarantee at a real price.** One extra
  RPC per tool-bearing create, in exchange for closing a silent-shadowing
  failure the wire cannot report. It is still not total: an MCP source resolved
  *after* the pre-flight, or a tool-name collision inside a same-named source,
  remains invisible. Closing that properly needs a server-side mounted-tool
  inventory on `CreateSessionResponse`, which is out of scope here.
- **A hand-written MCP host is ours to maintain.** Five methods and one
  transport shape, but a protocol revision that changes them is SDK work, and
  the only thing standing between us and a silent incompatibility is the
  on-the-wire e2e. That is the same standing cost 0279 accepted for the browser
  transport, taken knowingly a second time.
- **`query()` has no plan mode in v0.1's M3 slice.** A user in plan mode gets a
  clear error naming what to wait for. This is a deliberate divergence from
  #821's wording and it is the one place this ADR does not implement the issue
  as written.
- **Handler exceptions are opaque to the model on purpose.** A user debugging a
  failing tool must install a diagnostics sink; the model only ever sees a
  generic failure and a correlation id. That is the right default for a channel
  fed by model-controlled input, and it will be the first thing that surprises
  someone.
- **The lifetime pipe is the milestone's one unresolved mechanism.** The
  constraint is the descriptor's **type**, not its inheritance, and it binds
  Node as much as Bun (Decision 5). The design fails loudly rather than
  silently — `mecated` refuses a non-FIFO descriptor by name — so the residual
  is a choice between three known options with a named escape hatch, taken in
  the first PR rather than discovered in the last.
- **Two new long-lived client-side resources.** The tool host (an HTTP server, a
  bearer, a concurrency semaphore, a queue) and the spawned child process with
  its runtime directory and lifetime pipe. Both live in the client process,
  outside [ADR 0027](./0027-cloud-native.md)'s scope, per
  [ADR 0279](./0279-typescript-sdk-architecture.md) Decision 5 — which named
  "the M3 tool host" as the milestone that owns its own lifecycle contract.
  Decision 6 is that contract. The Go-side rows for the lifetime pipe and ready
  file (ADR 0027 List 1, rows 65–66) already exist and are unchanged.

## See also

- [ADR 0279](./0279-typescript-sdk-architecture.md) — the SDK architecture this
  extends: Connect-ES, the `.`/`./node`/`./gen` split, the dependency-footprint
  discipline Decision 10 applies, and Decision 5's deferral of the tool host's
  lifecycle contract to this milestone.
- [ADR 0288](./0288-typescript-sdk-durable-attachment.md) — M2's attachment
  contract; disposal (Decision 6) extends its detach semantics.
- [ADR 0237](./0237-listener-scoped-workspace-authority.md) — the
  listener-scoped authority rule `mcp_servers` follows, and which
  `clientMCPOnCreateForListeners` deliberately reads more strictly.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) — the feature
  vocabulary and error registry Decisions 2 and 13 consume.
- `docs/acceptance/sdk-typescript-local.md` — the acceptance plan this ADR
  anchors.
- [Issue #821](https://github.com/stacklok/mecatl/issues/821) — the settled
  design contract, "Spawned daemon lifecycle", "Local callback tools",
  "Diagnostics and local state", and the v0.1 non-goals.
