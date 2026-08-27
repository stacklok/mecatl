# Agent Fabric Protocol

**Version 0.1 — Draft**

Fabric is a minimal protocol for remote access to files, folders, and
callable actions over HTTP and JSON. It borrows its resource model
from Plan 9's 9P (everything is a file, addressed by path, manipulated
through a small fixed set of operations) and its invocation/discovery
shape from MCP (services expose callable actions with schemas that an
agent can discover and act on) — but is transport-native to HTTP,
requires no client SDK, and is designed to be usable directly through
an ordinary FUSE mount, a shell, or a bare HTTP client with no
protocol-specific tooling at all.

If you can mount it, `cat` it, and `curl` it, you can use Fabric.

---

## 1. Motivation

MCP requires a purpose-built client in every agent harness — a
JSON-RPC session, a tool-call loop, schema parsing — before an agent
can use anything. Fabric's position is that most of what an agent needs
from a remote service is already expressible as filesystem operations,
and that the operations which aren't (invoking behavior, subscribing
to change) can be modeled *as* file operations rather than as a
separate protocol layered on top.

### Goals

1. A remote resource — data, config, a tool, a subscription — is
   always addressed as a path, and manipulated with the same small set
   of operations regardless of what it is.
2. Works over a bare FUSE mount with zero agent-side protocol
   awareness, and equally over direct HTTP/JSON for stateless or
   non-Linux clients.
3. Per-call authorization stronger than "does this session hold a
   valid token" — a compromised or manipulated caller should not be
   able to do more than the exact action it was authorized for.
4. Most operations are optional per-server; a server declares what it
   supports, and clients check before assuming.
5. Support knowledge-work use cases beyond development — a bundle of
   markdown, a support ticket queue, a customer knowledge base, a
   design spec — as first-class citizens, not just source trees. Git
   is one possible backing store among several (see §15 for the OKF
   mapping), never assumed.
6. Support **project-scoped** work: a session's root need not be a
   server's whole namespace. A client can be handed a mount rooted at
   a specific project/workspace, with everything outside it simply
   absent from that session — not hidden by convention, but genuinely
   unaddressable within it.
7. Support fully **stateless agents** as first-class clients, not just
   as a degraded case of a "normal" stateful one. An agent with no
   local disk, no OS, no persistent process — a request/response cloud
   function — must be able to speak Fabric directly over HTTP/JSON with
   nothing lost relative to a FUSE-mounted client. No part of the core
   protocol may assume a client can retain state between calls that
   the server hasn't itself made retrievable (see §10, §6.1).
8. Support a **reactive filesystem** as a core property, not a bolted-on
   feature: change is something a client can subscribe to and act on
   (§10), not something it must poll for by re-reading. Read, call, and
   watch are peers, not a base layer plus an afterthought.
9. Be usable as a **universal infrastructure layer for agent
   capabilities** — a single, uniform interface through which an agent
   reaches tools, memories, project context, and other agents'
   capabilities, regardless of what's actually running underneath
   (a container, a microVM, a stateless function, another agent). The
   protocol should be a plausible substrate for "containerization" of
   agent capability in the way a filesystem is a substrate for
   processes on a normal OS — not a specific implementation of memory
   or tooling, but the addressing and access layer any such
   implementation can sit behind.

### Non-goals

- A universal, generic CRDT / conflict-merge system. Fabric defines the
  *hook* (a `capabilities.crdt` declaration) but not merge algorithms —
  those are backend-specific.
- A federated multi-server execution engine. Single-server pipelines
  are core; cross-server delegated execution is an explicitly separate,
  later extension (§12).
- Prescribing an identity provider. Fabric assumes an OAuth 2.1-shaped
  bootstrap layer exists but does not define one from scratch.

---

## 2. Terminology

- **Path** — a `/`-delimited address of a resource. Paths need not
  correspond to stored bytes ("virtual files" — see §3.3).
- **Session** — the scope established by a mount or a connection; it
  fixes exactly one **zone** (§9) for its lifetime.
- **Flags** — a small, protocol-defined vocabulary describing what
  operations a path supports (`readable`, `writable`, `callable`,
  `exclusive`, `append_only`, `watchable`, ...).
- **Capabilities** — an open, implementer-defined namespace of
  additional metadata on a path (e.g. `crdt`, `cache_control`),
  ignorable by clients that don't recognize a given key.
- **Handle** — a stateful reference returned by `open`, governing
  concurrency semantics for subsequent operations on a path.
- **Execution token** — a single-use, audience-bound, fully-parameterized
  credential authorizing exactly one call.
- **Zone token** — a session-scoped credential establishing the trust
  boundary within which execution tokens are minted and honored.

---

## 3. Resource Model

### 3.1 Everything is a path

A path may be a plain file, a directory, a callable action, or a
virtual/synthesized endpoint. The protocol does not distinguish these
at the wire level — only via flags.

### 3.2 Flags (protocol-defined, closed vocabulary)

| Flag | Meaning |
|---|---|
| `readable` | `read` is supported |
| `writable` | `write` is supported |
| `callable` | `call` is supported (see §6) |
| `append_only` | writes ignore offset, always land at end |
| `exclusive` | at most one open handle at a time |
| `watchable` | subscriptions are supported (see §10) |

### 3.3 Virtual files

A path answering `stat`/`read`/`call` correctly need not correspond to
stored bytes. Implementers MUST NOT assume `read()` is idempotent,
cheap, or side-effect-free purely because a path looks like a plain
file — that is a storage-model assumption Fabric does not make.

### 3.4 Capabilities (open, implementer-defined)

```json
{
  "flags": ["readable", "writable"],
  "capabilities": {
    "crdt": { "type": "text_sequence" },
    "cache_control": { "max_age": 300 }
  }
}
```

Clients MUST be able to fall back to core flag behavior when they
don't recognize a capability key. A server MUST NOT require capability
support for basic correctness.

---

## 4. Core Operations

| Op | HTTP mapping | Notes |
|---|---|---|
| `stat` | `GET /stat/{path}` | Returns flags, capabilities, description, ETag/version |
| `readdir` | `GET /files/{path}/` | Returns entries **with metadata inlined** (no N+1 stat calls — see §4.1) |
| `read` | `GET /files/{path}` | `Range` header for partial reads |
| `write` | `PUT /files/{path}` | `If-Match` (ETag) for CAS; ignored offset if `append_only` |
| `create` | `POST /files/{path}` | |
| `remove` | `DELETE /files/{path}` | |
| `call` | `POST /files/{path}` on a `callable` path | See §6 |
| `walk` | implicit in path resolution | Servers MAY expose batch-walk for round-trip efficiency |

Only `stat` and `readdir` are mandatory for every server. All others
are declared per-path via flags (§3.2) and MAY be absent. See §11 for
unsupported-vs-unauthorized semantics.

### 4.1 `readdir` is metadata-inlined

Every entry returned by `readdir` carries its own flags/description/
schema inline (the NFS `READDIRPLUS` pattern) — a client building a
prompt or a capability list from a directory listing never needs a
follow-up `stat` per entry.

### 4.2 Directory listings are range-addressable

Large directories and subscription/call-progress logs share one
addressing convention:

```
GET /files/{path}/?range=entries=0-99   # head
GET /files/{path}/?range=entries=-50    # tail (requires known total)
```

---

## 5. Handle Kinds

Declared per-path via flags; a client checks before assuming a mode is
available.

- **Exclusive** (`exclusive` flag) — one open handle at a time. Must
  carry a lease/timeout (analogous to WebDAV `LOCK`'s `Timeout`
  header) so a crashed holder doesn't wedge the lock forever.
- **CAS** (implicit wherever `stat` returns an ETag) — `write` requires
  `If-Match`; mismatch → `409`. This is standard HTTP conditional
  request semantics, not a bespoke mechanism.
- **Append-only** (`append_only` flag) — offset in `write` is ignored;
  data always lands at the end. Naturally lock-free for concurrent
  writers.
- **CRDT** (`capabilities.crdt`, fully implementer-defined) — Fabric
  defines the discovery hook only. Directory-tree merges (add-wins
  OR-set) and structured-content merges are tractable; arbitrary binary
  merges are not, and implementers should declare `crdt_type: "opaque"`
  honestly rather than pretend a merge exists.

---

## 6. Calls

A `callable` path responds to `call` (`POST` to its own path) and
`stat` describes its schema:

```json
// GET /stat/tools/resize_image
{
  "flags": ["callable"],
  "input_schema": { ... },
  "output_schema": { ... }
}
```

### 6.1 Synchronous vs. long-running

A call MAY return `202` with a handle to an append-only progress log
(§4.2 addressing applies) instead of blocking — this reuses the same
range-addressable log primitive as subscriptions (§10), not a separate
streaming mechanism.

### 6.2 Idempotency

Calls with side effects SHOULD accept an `Idempotency-Key` so a
timeout-then-retry doesn't double-execute.

### 6.3 Pre-built-request calls (execution tokens)

A callable file MAY, instead of executing server-side, return its own
contents with a single-use, request-bound credential injected — the
descriptor is a fully-parameterized, ready-to-fire HTTP request. See
§9 for the security model this requires.

---

## 7. Shell / Pipelines

A server MAY expose `/prot/bin/shell` (or any callable path) accepting
a recursive, tagged node grammar:

```json
{
  "version": 1,
  "type": "pipeline",
  "stages": [
    { "type": "exec", "argv": ["grep", "-r", "TODO", "/src/"] },
    { "type": "exec", "argv": ["filter", "--field", "mtime", "--gt", "2026-07-01"] }
  ]
}
```

### 7.1 Rules

- **Every node is tagged with `type`.** There is no privileged
  top-level shape — `pipeline` is a node type like any other, and a
  bare `{"type":"exec", ...}` is a valid request body on its own.
- **Commands take `argv: string[]`, never a bespoke JSON schema per
  command.** New flags on a command never require a schema migration,
  mirroring how CLI tools evolve.
- **Binary data never travels inside `argv` or the JSON envelope.**
  Stage-to-stage data is a separate byte stream (stdin/stdout,
  multipart body, or chunked transfer) with an out-of-band
  `content_type` hint. The envelope carries structure; it never carries
  payload bytes.
- **Unknown envelope fields are ignored. Unknown `type` values are a
  hard error (fail closed).** A stage is a unit of behavior, not
  descriptive metadata — silently skipping an unrecognized stage
  (e.g. a future authorization-check node type) must never be treated
  as a no-op.
- **Depth/recursion limits are mandatory** given the grammar is
  genuinely recursive (`parallel`, `conditional`, nested `pipeline`
  nodes).

### 7.2 Standard commands

`/prot/bin/` (§8) holds protocol-defined commands with a fixed schema
per command name (e.g. `grep`) so any server declaring support for one
is interoperable with any client's expectations of it. A command
graduates to "standard" only when clients benefit from assuming a
fixed schema without per-server discovery; domain-specific behavior
stays an ordinary capability instead.

---

## 8. Reserved Namespace

Reserved in Fabric's own virtual root (not the host OS's — a FUSE mount
presents this namespace, it does not merge with `/` on the host):

```
/prot/
  version           -- readable; protocol version string (only universally mandated entry)
  bin/               -- every entry here is callable, full stop
    grep
    find
    shell
  capabilities/      -- curated discovery tree (§8.1); binds/aliases into the real tree
/home/{user}/         -- per-user namespace convention
/home/me              -- alias resolved from session identity, not a security boundary itself
README.md             -- if present, at any directory level: informational discovery entry point
```

Servers exposing a real backing filesystem at the root SHOULD bind it
under a non-colliding subtree rather than treating this as a true
collision risk — these names are reserved in Fabric's namespace, not the
host's.

### 8.1 Capabilities are curated, not comprehensive

`/prot/capabilities/` is an ordinary directory answering ordinary
`readdir` — progressive disclosure (expanding into a subdirectory) is
just a second `readdir` call at a deeper path, not a new mechanism.
Entries are binds/aliases into wherever the real path lives; the
protocol does not mandate curation policy — **the server decides what
is included**, including exposing purely virtual (non-file-backed)
entries.

### 8.2 README.md as untrusted context

`README.md`, at any level, is the informational discovery entry point
an agent reads on arrival at a directory — plain prose, no schema. It
is attacker-controllable content (same trust class as an MCP tool
description) and MUST be treated as informational-only: sufficient to
explain how to use a service, never sufficient on its own to justify
a sensitive action without a real, separately-authorized call.

---

## 9. Auth Model

### 9.1 Bootstrap: OAuth 2.1

A **zone token** is an OAuth 2.1 access token with an audience/resource
claim (RFC 8707) identifying the zone. This is the session-establishing
credential — issued once per session (one mount or one connection = one
session = one zone, no implicit widening).

Every zone token carries a **user id** — a globally unique identifier
for the connecting identity, taken directly from the token's OAuth
`sub` claim rather than asserted separately by the client as a
free-standing field. The server has this identity for the lifetime of
the session and MAY use it to gate any individual operation, not only
namespace membership (see §9.2.2).

### 9.2 Scoped namespaces (session-constructed mounts)

A session's view of the tree is not necessarily the server's whole
namespace rooted at `/`. Establishing a session is a single request
carrying the user id (§9.1), an optional project id, and optional
mount flags:

```json
POST /session
{
  "zone": "...",
  "project_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",  // optional
  "include": ["home"]                                     // optional, see §9.2.3
}
```

**If `project_id` is present**, the server resolves it against the
connecting user's permissions before the session comes up at all:

- Project exists and the user has access → the project is bound at
  `/` (or as specified) and the session establishes normally.
- Project exists but the user lacks access → `403`.
- Project id is not recognized by this server at all → `404`.

  Note this deliberately differs from the in-session
  out-of-namespace rule below: at connection time the caller already
  supplied a UUID they claim to know about, so confirming existence
  leaks nothing beyond what they already asserted. Once a session is
  live, however, an established namespace never distinguishes
  forbidden from absent for paths the caller didn't already name (see
  below) — the two situations are not symmetric, and the spec treats
  them differently on purpose.

**If `project_id` is absent**, no project is auto-bound; the session's
namespace is whatever `include` (§9.2.3) and system defaults provide,
with no project content at all unless bound in explicitly by path
(§9.2, general form) rather than by token.

**Session establishment is atomic.** If any requested bind — whether
via `project_id` or an explicit `namespace` list — cannot be resolved,
the entire session fails to establish rather than coming up with a
gap. A client never has to detect a partially-successful mount by
probing it; either the whole requested namespace is live, or none of
it is.

More generally, a caller (or whatever brokers the session on its
behalf) MAY instead supply a full explicit **namespace**: an ordered
list of binds, each mapping a `source` path in the server's real tree
to a `mount_at` path in the session's view — the same construction
Plan 9 uses to assemble a per-process namespace out of `bind`/`mount`
calls, applied once at session start rather than being a single fixed
root:

```json
{
  "namespace": [
    { "mount_at": "/prot", "source": "/prot" },
    { "mount_at": "/", "source": "/projects/acme-website" },
    { "mount_at": "/assets", "source": "/shared/design-assets" }
  ]
}
```

`project_id` and an explicit `namespace` list are two ways of
specifying the same underlying thing (a set of binds) — `project_id`
is the portable, path-independent shorthand (§9.2.1); `namespace` is
the general form for anything not expressible as a single project
token (multiple projects, shared folders, non-project system paths).

The session's namespace is the union of whatever binds resulted. This
is the mechanism behind **project-scoped work** (Goal 6): a client
working on one project gets a session whose entire visible tree is
that project (plus whatever shared or system paths were explicitly
bound in) — not the server's full multi-tenant tree with permission
checks filtering it down.

**Out-of-namespace paths MUST return `404`, never `403`.** A session
must not be able to distinguish "this path exists but you're
forbidden" from "this path does not exist" for anything outside its
bound set — otherwise a scoped session can enumerate the shape of a
server's real tree (other projects, other tenants) purely by observing
which out-of-scope probes come back `403` versus genuinely `404`. Scope
that leaks structure through its own denial behavior isn't scope.

**The namespace binding is carried by the zone token**, not
renegotiated separately, and re-validated at every operation — the
same fire-time-not-just-mint-time discipline as §9.4. A compromised
call attempting to walk outside the session's bound set is rejected by
the server on every attempt, not only prevented by the client's own
mount configuration.

`/prot/version` (and enough of `/prot/` to complete bootstrap
discovery) is conventionally always reachable regardless of project
scope or `include` flags — a session that can't reach it can't
discover anything else about the server, including what it's allowed
to ask for. Broader `/prot/` content (the full `/prot/bin/` command
set, `/prot/capabilities/`) is gated by the `include: ["system"]` flag
(§9.2.3), not bound unconditionally.

A `source` need not be local to the server handling the session — see
§12 for binding in paths that live on a different backend entirely.

#### 9.2.1 Project tokens (identity-addressed binds)

A `namespace` bind's `source` MAY be a **project token** — a UUID —
instead of a literal path, resolved locally by whichever server hosts
the session:

```json
{ "mount_at": "/", "project": "3fa85f64-5717-4562-b3fc-2c963f66afa6" }
```

A folder declares its own project identity via a `project_id`
capability (discoverable through ordinary `stat`, no separate registry
call):

```json
{ "capabilities": { "project_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6" } }
```

This decouples **project identity** from **path**: a project created
on one system and given a UUID can be referenced identically on any
other system that happens to host a folder tagged with that same UUID
— an orchestrator only needs to carry the token, never a
server-specific directory convention.

**No central registry is required.** A UUIDv4 is safely unique without
coordination, consistent with the no-registry posture already used
elsewhere in this spec (standard commands, capability descriptions,
OKF's unregistered `type` field) — a server mints a project token
locally when a project is created and needs no external authority to
do so.

**Overlap is expected, not an error.** Multiple project tokens MAY
resolve to overlapping or identical content (a "frontend" project and
a containing "monorepo" project may legitimately share a subtree), and
the same token MAY resolve to different actual folders on different
servers (a mirrored or independently-hosted copy of "the same"
project). Fabric asserts nothing about the consistency of content
behind a shared token across servers — that a token resolves
consistently is an operator claim, not a protocol guarantee, the same
way OKF makes no attempt to verify cross-link or cross-bundle
consistency.

**Resolving an unrecognized project token is `404`**, the same
convention as an out-of-namespace path (§9.2) — a server has no
obligation to know about every project token in existence, only the
ones it happens to host.

#### 9.2.2 Identity-scoped visibility (per-resource gating)

Namespace binds (§9.2) gate what a *session* can reach at all. They do
not by themselves gate what a *specific user* within an already-bound
project can see — two sessions bound into the same project, connected
by different users, may legitimately need to see different things (one
user's private working files within a shared project, say).

Since every session already carries a user id (§9.1) for its entire
lifetime, individual paths MAY declare ownership/ACL metadata as an
ordinary capability, checked against that user id on every `stat` /
`readdir` / `read` / `write` / `call`:

```json
{
  "capabilities": {
    "owner": "user_8f2a...",
    "acl": { "read": ["user_8f2a...", "group:acme-eng"], "write": ["user_8f2a..."] }
  }
}
```

This reuses the same enforcement principle as out-of-namespace paths,
just at finer granularity: **a path a user cannot see behaves exactly
as if it does not exist (`404`), not as a `403` refusal** — a
directory listing simply omits entries the requesting user isn't
permitted to see, rather than listing them and then failing to open
them. One rule, two granularities: namespace binding decides what a
*session* can reach; ACL capabilities decide what a *user within that
session* can reach. Neither layer needs to know about the other's
internals — a path merely has to be reachable by both to actually be
visible.

Fabric does not mandate a specific ACL model (owner/group/other,
capability lists, RBAC roles) — as with CRDT merge algorithms (§5),
this is deliberately left to the implementer, discoverable via
`capabilities` the same way any other backend-specific extension is.

#### 9.2.3 Opt-in mounts beyond project scope

Two things are never bound into a session by default, regardless of
`project_id` or explicit `namespace` binds, and must be requested via
`include`:

```json
{ "include": ["home", "system"] }
```

- **`"home"`** — binds the connecting user's own home directory
  (§8, `/home/{user}`) at a server-chosen or caller-specified mount
  point. Since the session already carries the user id (§9.1),
  `/home/me` for this session unambiguously resolves to exactly that
  user's own folder — no separate resolution step needed.
- **`"system"`** — binds broader server-level, non-project content:
  the full `/prot/bin/` command set, `/prot/capabilities/`, and any
  other non-project-scoped paths the server exposes. Without this
  flag, a project-scoped session gets only the minimal `/prot/version`
  bootstrap reachability described in §9.2, not the server's full
  system-level surface.

Both default to absent rather than present, consistent with the
overall posture of this section: a session sees only what it
explicitly asked for, never a broader default that has to be
whittled down by permission checks after the fact.

### 9.3 Execution tokens

Distinct from the zone token: a single-use, request-bound,
audience-bound credential minted for exactly one pre-built call (§6.3).
Not a general bearer credential — possessing it authorizes exactly the
request it was signed over (method + URL + body hash), nothing else.
Requires both single-use enforcement *and* a short TTL — single-use
alone does not protect an unfired, leaked token from sitting live
indefinitely.

### 9.4 Zone vs. execution token propagation

These two credentials are deliberately asymmetric:

- **Execution tokens are designed to travel** — handing one to a tool
  or a subagent is the whole point of mint/fire separation (§6.3).
- **Zone tokens are designed not to travel.** A lower-trust delegate
  operating in a different zone must never receive the zone token
  itself — only a narrower, re-minted execution token scoped to the
  one action being delegated. Attenuation is an explicit, deliberate
  re-scoping step, never an accidental byproduct of what happens to be
  reachable.

Zone enforcement MUST happen at fire time, at the server, not only at
mint time — mint-time-only checking cannot stop a leaked descriptor
from being misused after the fact.

**Delivery mechanism is not the trust boundary.** A FUSE mount, a
direct HTTP client, and a fully virtual cloud agent with no
filesystem concept at all are just different transports for presenting
a zone credential. The server MUST be able to reject a call solely from
what accompanies the request, and MUST NOT rely on any assumption about
transport-level isolation (a mount, a container boundary) as a
substitute for checking the credential.

### 9.5 Revocation

No deny-list or push-based kill signal is required. Zone tokens are
short-lived (seconds, not hours) and require active refresh against
the issuing broker; revocation is simply "the broker stops honoring
refresh for this session." The broker needs no persistent state beyond
currently-active sessions — it may be in-memory.

### 9.6 Cross-server calls (delegation)

A server acting as a client of another server on a caller's behalf
MUST NOT forward the caller's zone token unmodified (confused-deputy
risk). It MUST mint its own attenuated, single-purpose execution token
for that one remote call, using its own service identity plus the
calling session's authority as justification. See §12.

---

## 10. Subscriptions

A `watchable` path exposes change notification. The log is always
authoritative; push is a best-effort delivery optimization on top of
it — a push failure is never data loss, only a missed convenience.

```json
POST /watch/{path}
{ "callback_url": "https://agent.example/hooks/changed" }  // optional
→ 201 { "subscription_id": "...", "log_path": "/watch/{path}/log" }
```

- **Push, if registered:** best-effort `POST` to `callback_url` on
  change, bounded retries. Every attempt (delivered or not) is recorded
  in the log with a delivery status, including `abandoned` once retries
  are exhausted — a client relying on push must be able to see that
  push has silently died and fall back to polling.
- **Pull, always available:** the log is an ordinary range-addressable
  append-only file (§4.2) — a client without webhook infrastructure
  just polls it directly, no separate feature.
- **`last_event_at` / `last_delivery_status`** SHOULD be surfaced via
  `stat` on the subscription so liveness is checkable without reading
  the whole log.

### 10.1 Trigger mode

Declared per subscription:

- **`level`** — retain only the latest trigger. Sufficient for "has
  this changed since I last checked" (config, job status). No growing
  log, no retention policy needed.
- **`edge`** — every discrete event must be retained and replayable
  (activity feeds, audit trails). Requires a bounded retention/
  compaction policy and a client-visible way to detect "I've fallen
  behind far enough that there's a gap I can't replay."

Most subscriptions should default to `level`; only opt into `edge`
where losing intermediate events is actually unacceptable.

---

## 11. Errors

Minimal, HTTP-status-first. A JSON error body adds a machine-readable
`code` only where HTTP's status vocabulary is ambiguous.

| Status | Meaning |
|---|---|
| `403` | Attempted but unauthorized — might work with different credentials/zone |
| `404` | Not found |
| `405` | Operation not supported on this path/server at all — do not retry, do not escalate credentials |
| `409` | CAS conflict (`If-Match` mismatch) |
| `410` | Token expired or already used (`code: token_expired` / `token_already_used`) |
| `423` | Exclusive handle contention |
| `429` | Rate limited |

`403` vs `405` is a deliberate, load-bearing distinction: a client that
checked flags before attempting an operation needs to know whether the
absence was real (`405`) or whether different credentials might help
(`403`) — collapsing them defeats the point of flag-based discovery.

---

## 12. Cross-Server Composition (extension, not core)

Client-orchestrated composition — an agent with multiple mounts piping
between them via its own shell — requires nothing beyond what's
already specified; each server remains unaware of the others.

True single-call federated pipelines (a `type: "remote"` stage
executing on a different server within one pipeline request) are an
explicit, separate extension, not part of the v0.1 core, because they
require:

- Cross-server path addressing (a URI-shaped reference, not a bare path)
- A defined re-minting/delegation exchange between servers (§9.6)
- Partial-failure semantics across a distributed pipeline
- Streaming a byte-stream through an intermediate server as a proxy

Implementers should treat single-server pipelines as the solid,
shippable core, and layer federated execution in later once the
delegation model above is proven.

---

## 13. Conformance

A server is conformant with Fabric v0.1 if:

1. `stat` and `readdir` are implemented for every path the server
   exposes.
2. `stat` never advertises a flag the path cannot actually honor.
3. `/prot/version` exists and returns the protocol version the server
   implements.
4. Unknown pipeline stage `type` values are rejected (fail closed),
   never silently skipped.
5. Zone credentials are checked at call/fire time, not only at mint
   time.
6. Session establishment is atomic — a session with any unresolvable
   requested bind (§9.2) fails to establish entirely, never comes up
   with a silent gap.
7. Both namespace-level and identity-level visibility failures
   (§9.2, §9.2.2) return `404`, not `403`, for anything the requesting
   session/user didn't already name outright (e.g. a project id
   supplied directly at connection time is the sole documented
   exception — see §9.2).

All other behavior — which flags a given path supports, which standard
commands exist under `/prot/bin/`, whether writes, calls, or
subscriptions are available at all — is server-declared and clients
MUST check before assuming.

---

## 14. Versioning

Versioned `<major>.<minor>`. Minor versions add optional fields,
flags, or standard commands. Major versions may change reserved paths
or required semantics. `/prot/version` reports the version the server
implements; clients SHOULD attempt best-effort interaction with a
version they don't fully recognize rather than refusing outright,
consistent with the flags-based discovery model throughout.

---

## 15. Appendix: Serving OKF knowledge bundles

The [Open Knowledge Format](https://github.com/GoogleCloudPlatform/knowledge-catalog)
(OKF) is a markdown-and-frontmatter convention for portable knowledge
bundles, explicitly designed to be transport-agnostic. It requires no
adaptation to be served over Fabric — a bundle is a directory of
markdown files, and Fabric is a remote filesystem. This appendix
documents the mapping because it exercises Goal 5 (knowledge work
beyond git-backed development) end to end without adding protocol
surface.

### 15.1 Direct mappings

| OKF concept | Fabric equivalent |
|---|---|
| Bundle directory tree | Ordinary paths; `readdir` traversal (§4) |
| `index.md` progressive disclosure | Same role as `README.md` (§8.2); recognize both |
| Frontmatter `type` field | Surfaced as `capabilities.okf_type` on `stat` (§3.4) |
| Cross-links between concepts | Ordinary path resolution |
| `log.md` update history | Natural subscription target (§10) |
| Declared `okf_version` | SHOULD be surfaced in `readdir`/`stat` metadata so a client can identify a bundle without first reading and parsing `index.md` |

### 15.2 Shared design posture

OKF's consumption rules — do not reject a bundle for missing optional
frontmatter, unknown `type` values, unknown additional keys, broken
cross-links, or a missing `index.md` — are the same permissive-
consumption principle Fabric applies to unknown envelope fields (§7.1)
and unrecognized capabilities (§3.4). Neither spec fails closed on
unrecognized *metadata*; both fail closed only on unrecognized
*behavior* (§7.1's stage `type` rule).

`index.md` and `README.md` are treated identically with respect to
trust: both are attacker-controllable prose an agent reads and acts on,
and both are informational-only (§8.2). Serving an OKF bundle does not
change that boundary.

### 15.3 What Fabric adds to a bundle

A bundle served over a plain file share supports only reading. The same
bundle served over Fabric additionally gains, with no change to the
bundle itself:

- **Search** — `/prot/bin/grep` and pipeline stages (§7) over concept
  bodies and frontmatter fields.
- **Change notification** — subscriptions (§10) on `log.md` or on the
  bundle root; `level` trigger mode is usually sufficient.
- **Callable concepts** — a concept documenting an API endpoint MAY
  also be `callable` (§6), since callability is an independent flag
  (§3.2) rather than a property of content. This is a capability OKF
  alone does not have.
- **Curated discovery** — `/prot/capabilities/` (§8.1) MAY be defined
  as an OKF-conformant index rather than a bespoke manifest format.

### 15.4 Reference adapter: a git host as a Fabric server

A read-only adapter over a git forge's contents API is close to
mechanical, and demonstrates that a Fabric server need not be a
purpose-built implementation:

| Fabric op | Forge API equivalent |
|---|---|
| `readdir` | Directory listing endpoint |
| `read` | File contents endpoint (or raw file host) |
| `stat` | Same contents endpoint; the blob SHA serves directly as the ETag/version (§5, CAS) |
| `write` (optional) | Contents update endpoint, which already requires the current SHA — CAS semantics inherited from the backend rather than implemented |

Practical notes for such an adapter:

- **Cache aggressively**, keyed on the current commit SHA — a naive
  read-through adapter will exhaust API rate limits quickly.
- **Decide ref semantics explicitly.** Git is commit-addressed, not
  "current state"; an adapter must state whether it pins a ref or
  floats to the latest.
- **Ship read-only first.** Writes map poorly onto a synchronous
  `write` call: committing directly to a default branch skips review,
  and opening a pull request is not "written" at the moment `write`
  returns.
- **Webhooks satisfy §10's push side** more or less directly — relay
  the forge's own push events into the log-and-optionally-push
  convention.
- **The adapter holds its own forge credential** and never exposes it
  to the calling session — the delegation posture of §9.6, applied to
  a non-Fabric backend.
