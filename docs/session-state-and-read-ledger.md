# Session-owned extensible state and the read-before-write ledger

*Status: strawman / working draft. This is a concrete architecture proposal, not a
frozen design record under [ADR 0002](adr/0002-documentation-lifecycle.md). It is
the same documentation tier as [Scoped resource grants](scoped-resource-grants.md).
If accepted, the relevant decisions should be captured in one or more ADRs.*

## Requirements and goals

This proposal starts from one ownership rule: **the read-before-write ledger is
session state, not filesystem state and not an execution-environment
capability**.

The resulting design should:

1. Make the session the logical owner of the versions observed by that session.
2. Persist those observations with the rest of the session's state, so they
   survive runs, process replacement, and replicas.
3. Restore a session with its ledger intact, preferably without eagerly loading
   every ledger entry.
4. Introduce one extensible session-state facility. A future feature should be
   able to add session state without implementing its own memory, JSONL, Redis,
   gRPC, and other storage clients.
5. Keep filesystem-content storage independent from session-state storage. Two
   sessions may share one filesystem while retaining independent observations.
6. Keep the filesystem responsible for authoritative versions and atomic
   conditional mutation. The session ledger must not replace backend
   compare-and-swap.
7. Make agent-facing Read, Edit, and Write maintain the ledger transparently.
   Individual tool bodies should not coordinate the protocol manually.
8. Preserve opaque `tool.FileVersion` values and the existing I/O-free lexical
   path identity rules.
9. Preserve fail-closed behavior: absent, unavailable, or corrupt evidence must
   never authorize an existing-file mutation.
10. Give every subagent session its own ledger, even when parent and child share
    the same underlying filesystem content.

The proposal is deliberately grounded in today's `Session`, `SessionStore`,
`Workspace`, `Environment`, and `FileVersion` types and in the separation begun
by [PR #946](https://github.com/stacklok/mecatl/pull/946).

## Non-goals

This proposal does not specify the migration sequence or implementation tasks.
In particular, it does not:

- design the initial Redis-backed filesystem from
  [issue #889](https://github.com/stacklok/mecatl/issues/889);
- change any existing read-before-write, stale-read, create-only, or final-CAS
  behavior; this proposal changes ownership and wiring only;
- add POSIX-like filesystem operations such as rename, remove, mkdir, or chmod;
  that is an orthogonal filesystem-interface effort;
- change Shell behavior or require Shell writes to participate in the ledger;
- define the scoped-resource-grant wire protocol;
- make the session ledger a distributed lock or replace filesystem CAS; or
- require every session-state backend to use the same physical representation.

Implementation of this proposal is sequenced **after** the initial Redis-backed
filesystem. The remote filesystem should land without waiting for this software-
architecture cleanup. Its authoritative version and conditional-mutation
contract then gives the cleanup a concrete backend against which to validate the
new ownership boundary.

## Background: the current shape

The engine dispatches tools through the generic `tool.Tool` interface in
[`engine/tool/tool.go`](../engine/tool/tool.go). A tool receives a
`tool.Environment`, which currently identifies an execution namespace and
carries a `Workspace` plus an optional namespace-bound `CommandRunner`; see
[`engine/tool/environment.go`](../engine/tool/environment.go).

`Workspace` currently combines several responsibilities:

- rooted and confined content access;
- search operations such as Glob and Grep;
- authoritative opaque file versions;
- create-only and conditional-replace mutation; and
- an in-memory record of versions previously read by the agent.

The built-in file tools coordinate the last responsibility explicitly. Read
calls `ReadVersion` and records the returned version. Edit and existing-file
Write look up the recorded version, read the current version, compare them, and
then call `ReplaceFile`. This protocol is spread across
[`engine/adapter/fstools/read.go`](../engine/adapter/fstools/read.go),
[`engine/adapter/fstools/edit.go`](../engine/adapter/fstools/edit.go), and
[`engine/adapter/fstools/write.go`](../engine/adapter/fstools/write.go).

The `Session` aggregate already owns conversation history, lifecycle state,
pending approval, cumulative usage, execution-environment identity, ownership,
and other durable labels; see
[`engine/session/session.go`](../engine/session/session.go). `port.SessionStore`
loads and saves the complete aggregate through one backend-neutral interface,
and `sessnap.Snapshot` is the shared JSON-friendly representation used by the
in-tree stores; see [`engine/port/store.go`](../engine/port/store.go) and
[`engine/adapter/sessnap/sessnap.go`](../engine/adapter/sessnap/sessnap.go).

PR #946 makes an important first separation: `Workspace` becomes content-only
and a new `tool.ReadLedger` is carried separately by `Environment`. It also
proves in-memory and Redis ledger implementations. That solves independent
backend selection, but it leaves two architectural problems:

1. The API says the ledger belongs to the Environment even though its identity
   and lifetime are those of a session.
2. The ledger is another storage abstraction with its own backend
   implementations. Repeating that pattern for each future piece of session
   state would multiply clients, conformance suites, lifecycle wiring, and
   cleanup logic.

### Relationship to PR #946

This proposal intentionally goes beyond—and partly reverses—the ownership shape
introduced by PR #946. If that PR lands first, its extraction is a useful interim
step: it removes observation state from content Workspaces and proves persistent,
session-keyed evidence. The eventual design proposed here would then:

- move the ledger from `tool.Environment` into generic session state;
- replace ledger-specific memory/Redis adapters with each SessionStore's generic
  state implementation; and
- keep the transparent Workspace wrapper while binding it to the loaded Session.

That follow-up must be recorded as a superseding ADR rather than silently
changing the decision made by PR #946. This document does not ask the initial
Redis filesystem to implement generic session state; the separation is validated
and adopted afterward, as requested by the sequencing below.

## Proposed conceptual model

The concrete scope of this proposal is moving read-ledger behavior and state
into the engine. The ledger exists for the agent loop: it remembers what one
session was shown so the engine can decide whether a later mutation is safe.
That behavior belongs with engine-owned session state, not in a backing
filesystem implementation.

This change also illustrates a possible longer-term direction without attempting
to complete that redesign here. `Workspace` becomes a thin filesystem client,
while `Environment` bundles clients—currently a Workspace and an optional
CommandRunner—that operate over the same underlying file state. Neither is
responsible for maintaining engine behavior or session state.

Keeping those responsibilities inside the engine makes the engine a complete,
portable agent loop. It can be embedded in a single binary or run as a
Kubernetes-hosted agent loop while filesystem, command, and other tool backends
remain pluggable. Supporting an alternative backend then primarily means
implementing another client contract, without also reproducing or risking the
agent invariants enforced by the engine.

Separate three concerns explicitly:

```text
Session
├── core aggregate state
│   ├── conversation and lifecycle
│   ├── usage, approvals, labels, ownership
│   └── extensible session state
│       └── file/read-ledger/v1
│
Execution Environment (clients sharing underlying file state)
├── Workspace (filesystem client)
└── optional CommandRunner

Workspace backend
├── authoritative file content
├── authoritative opaque versions
└── atomic CreateFile / ReplaceFile CAS
```

The ledger answers, “Which version did this session observe?” The Workspace
answers, “Which version exists now, and may this conditional mutation commit?”
Those questions have different owners and may have different storage backends.

## Extensible session state

### A generic session-scoped state facility

A restored `Session` should carry generic, session-scoped state. Its initial
access pattern is namespaced key-value storage, allowing the read ledger and
future state consumers to share one persistence mechanism without sharing
schemas.

Making this facility extensible avoids adding a new persistence interface and
set of backend adapters for every kind of session-scoped state. It also avoids
expanding the core session snapshot with fields owned by individual engine
features. Its purpose is to give engine features one session-owned way to create
and modify durable state, so each feature does not have to solve backend storage,
restoration, and cleanup independently. Features remain responsible for the
meaning of their own state. Its lifetime follows the Session naturally:
restoring the Session restores its state, and deleting the Session reaps all of
it without individual features or underlying resource clients managing cleanup.

Exact-key access is the right initial contract for the read ledger because each
file observation can be read or updated independently, minimizing read
amplification and avoiding whole-ledger serialization. It need not be the only
access contract the session-state facility ever supports. A future feature with
demonstrably different requirements may add another contract while retaining the
same session ownership and storage lifecycle.

This proposal deliberately does not settle the exact Go interface, generic type
shape, namespace-registration API, or serialization format. Those belong in the
implementation ADR. The architectural contract is:

- The state view is already bound to one `SessionID`; callers never supply a
  second session identity.
- The initial key-value contract addresses values by stable, versioned namespace
  and key and provides exact-key Get, Put, and Delete operations. Whether those
  operations expose typed values directly is an API-design detail.
- Additional access contracts may be added when another feature demonstrates
  that key-value operations are insufficient; they remain part of the same
  session-owned state facility and lifecycle.
- Components own their value schema, validation, and corruption handling;
  storage adapters remain schema-agnostic.
- Get distinguishes absence from storage or decode failure.
- Operations are context-aware and concurrency-safe because read-only tools may
  execute concurrently.
- Enumeration is not required for the ledger hot path. Exact-key lookup permits
  lazy restoration where a backend supports it.
- Session deletion removes every namespace. Session-ID reuse starts empty.

This is intentionally smaller than a general database. Add iteration, bulk
operations, or transactions only when a real second state consumer requires
them.

### Session ownership

The `Session` owns and exposes its bound state view; the persistence adapter owns
the backing storage. The view is attached when a session is created or restored,
is permanently scoped to that session ID, and cannot be rebound. A child session
receives new empty state, and copying conversation history does not copy session
state. Deleting the session removes both its core state and extensible state.

### Storage ownership

`SessionStore` remains the owner of session persistence. Each store backend
implements generic extensible state once, rather than implementing each feature:

```text
                    SessionStore
               load/save/delete session
                         │
                         ├── core snapshot
                         └── generic namespaced state
                              ├── file/read-ledger/v1
                              └── future component state

memstore       → in-memory namespaced maps
jsonlstore     → namespaced state records
redisstore     → generic session-state hash/keyspace
gRPC driver    → generic session-state operations
```

The key-value model is a logical contract, not a required physical layout. Each
backend may implement it using its native persistence model.

The ledger component supplies only its namespace and value contract. It has no
Redis client, JSONL format, gRPC client, or backend-specific cleanup code.

The exact Go interface remains an implementation-ADR decision. Architecturally,
the session persistence implementation creates or restores a `Session` bound to
the correct state view, and session deletion cleans up both core and extensible
state. A separate deployment-wide ledger resolver must not be required at every
file-tool call.

### Lazy restoration

Loading a session should not require loading every observed path. Instead, the
store reconstructs the core aggregate and attaches a lazy, session-bound state
view:

```text
SessionStore.Load(session-A)
    ├── load core snapshot
    └── attach State(session-A)
             └── no ledger entries loaded yet

first lookup of file/read-ledger/v1 + "src/a.go"
    └── exact backend lookup
        └── cache the decoded result for this live Session, if useful
```

Redis can implement this as an exact hash-field lookup. An in-memory backend can
use a nested map. A file-oriented store may choose to load eagerly until it has
a reason to add an index; lazy loading is a preferred capability, not a reason
to impose Redis's physical model on every backend.

Writes should be durable before an agent-facing operation claims that evidence
was retained. The storage contract must also define how core snapshot updates
and extension writes relate. Full cross-record transactions are not required of
every backend, but the following must hold:

- failure to retain a Read observation is reported and does not create evidence;
- a failed or corrupt lookup is distinct from absence and refuses mutation;
- restored extensible state is used only when the persistence layer can establish
  that it corresponds to the restored Session state; when that cannot be
  established, ledger evidence is treated as absent and the agent must Read
  again;
- successful session deletion removes extension state;
- stale evidence remains harmless because the Workspace's final CAS is
  authoritative; and
- a crash may conservatively lose evidence and force another Read, but must
  never fabricate evidence or authorize an overwrite.

## Transparent agent-facing filesystem behavior

The ledger should disappear from `tool.Environment` and from the individual
Read/Edit/Write implementations. A session-scoped decorator over the existing
`tool.Workspace` presents the same interface while enforcing the protocol.

```text
raw content Workspace
        +
Session.State().namespace("file/read-ledger/v1")
        │
        ▼
session Workspace decorator
        │
        ▼
tool-execution Environment
        │
        ├── Read tool
        ├── Edit tool
        └── Write tool
```

The decorator is constructed for one session at run entry. Harness-internal
configuration, prompt, skill, and rule discovery continues to use the raw
`WorkspaceReader`; those reads are not agent observations.

### Read

The agent-facing Read tool calls the decorator's ordinary `Read` operation:

```text
Read(path)
  1. raw Workspace.ReadVersion(path) → content, version
  2. Session.State.Put(ledger namespace, normalized path, encoded version)
  3. return content
```

The tool neither sees nor records `FileVersion`. If state persistence fails, the
operation reports that the content was read but its evidence was not retained.

### Mutation behavior

Edit and Write retain their existing semantics: prior agent-facing Read is
required, stale evidence fails, existing-file replacement ends with the raw
Workspace's authoritative CAS, and new-file Write remains create-only. The
decorator enforces that protocol without allowing Edit's internal content read
to update the observation. This proposal changes where the evidence lives and
where the protocol is enforced, not its behavior.

## Subagents and shared filesystems

Today each parent or child session owns an independent ledger, even when agents
share an authoritative content Workspace. A child mutation therefore leaves the
parent's earlier observation stale. This proposal preserves that behavior: every
child receives its own session state and session-bound Workspace decorator;
ledger state is not inherited or merged.

## Relationship to scoped resource grants

This proposal does not block
[Scoped resource grants](scoped-resource-grants.md). It removes an internal
coupling that would otherwise make that direction harder.

The grant proposal makes filesystem access an explicit scoped capability. A
filesystem service returns content tokens and accepts conditional mutations
carrying an expected token. That service should not know about mecatl sessions
or own their observation history. Under this proposal:

```text
Session-owned decorator                  Granted filesystem service
-----------------------                  --------------------------
remembers observed token       ────────► accepts expected token
selects the session ledger               owns content and atomic CAS
applies model-facing policy              enforces grant verbs and scope
```

A grant-resolved remote Workspace can sit beneath the same session decorator as
osfs, ACP, memfs, or the initial Redis filesystem. Filesystem storage, transport,
grants, mount composition, and future higher-order verbs can evolve without
adding ledger implementations. Conversely, session-state persistence can evolve
without changing filesystem clients.

The grant document also discusses direct tool-to-filesystem data paths. Such a
tool would need an explicit contract for returning observations or performing
conditional mutation; it cannot silently update harness session state if the
harness is absent from the data path. That integration question belongs to the
grant design. The session-state facility proposed here provides a place to
retain observations once reported, without prescribing the grant wire shape.

## Open questions

### What exactly is protected by the ledger?

Today the structured Edit and existing-file Write tools enforce read-before-write.
Shell operates through `CommandRunner` and may modify the same filesystem without
consulting the ledger. A subsequent structured mutation can detect that change
because the authoritative file version changed, but the Shell mutation itself is
not protected by the prior-read rule.

It is not yet clear whether this is the desired long-term boundary:

- **Structured-tool protection only:** the ledger protects operations where the
  harness can provide precise semantics; Shell remains an explicitly broader
  capability governed by permissions and sandboxing.
- **All mutation protection:** every filesystem mutation, including shell
  writes, would need mediation through the same version-aware service or a much
  stronger execution substrate.

Changing this behavior is out of scope. This proposal preserves the current
boundary and records the question rather than silently claiming Shell participates.

## Sequencing

1. Implement and validate the initial principal-scoped Redis filesystem from
   issue #889, including authoritative opaque versions and cross-replica CAS.
2. Use the resulting backend contract to validate this proposal's separation:
   content remains principal-scoped while observations remain session-scoped.
3. Decide the generic session-state contract and its restoration/deletion
   semantics through an ADR.
4. Move ledger data into generic session state, place transparent enforcement in
   the session Workspace decorator, and remove ledger ownership from
   `tool.Workspace`, `tool.Environment`, and filesystem adapters.
5. Reconcile scoped-resource-grant integration separately, without making it a
   prerequisite for the internal cleanup.

The target end state is simple: **sessions remember; filesystems version and
conditionally mutate; the decorator connects the two for agent-facing file
operations.**
