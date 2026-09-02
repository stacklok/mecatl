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
- add POSIX-like filesystem operations such as rename, remove, mkdir, or chmod;
  that is an orthogonal filesystem-interface effort;
- change Bash behavior or require Bash writes to participate in the ledger;
- define the scoped-resource-grant wire protocol;
- make the session ledger a distributed lock or replace filesystem CAS; or
- require every session-state backend to use the same physical representation.

Implementation of this proposal is sequenced **after** the initial Redis-backed
filesystem. That sequencing lets the first remote content backend establish its
version and conditional-mutation contract before the harness reorganizes how
session observations are retained.

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

Separate three concerns explicitly:

```text
Session
├── core aggregate state
│   ├── conversation and lifecycle
│   ├── usage, approvals, labels, ownership
│   └── extensible session state
│       └── file/read-ledger/v1
│
Execution Environment
├── EnvironmentRef
├── content Workspace
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

A restored `Session` should carry a session-scoped state view. The view is
namespaced and byte-oriented so the session package and storage adapters do not
need to understand each feature's schema.

An illustrative API is:

```go
type State interface {
    Get(ctx context.Context, namespace, key string) (value []byte, found bool, err error)
    Put(ctx context.Context, namespace, key string, value []byte) error
    Delete(ctx context.Context, namespace, key string) error
}

func (s *Session) State() State
```

The exact names are open, but several properties are required:

- The state handle is already bound to one `SessionID`; callers never supply a
  second session identity.
- Namespaces are stable, versioned component identifiers, not Go package names.
  The ledger might use `file/read-ledger/v1`.
- Keys and values are opaque to the generic facility. The owning component
  defines encoding, validation, and corruption handling.
- `Get` distinguishes absence from backend or decode failure.
- Operations are context-aware and concurrency-safe because read-only tools may
  execute concurrently.
- Enumeration is not required for the ledger hot path. Exact-key lookup is the
  primary operation and permits lazy restoration.
- Session deletion removes every namespace. Session-ID reuse starts empty.

This is intentionally smaller than a general database. Add iteration, bulk
operations, or transactions only when a real second state consumer requires
them.

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
jsonlstore     → snapshot extension or generic state sidecar
redisstore     → generic session-state hash/keyspace
gRPC driver    → generic session-state operations
```

The file-oriented choice is intentionally unresolved. `jsonlstore` is
append-oriented, while the proposed hot path is exact-key Get/Put; its eventual
adapter may use replay plus compaction, a generic sidecar, or an eager snapshot
extension. The ADR must choose deliberately rather than pretending Redis's
physical layout fits every store.

The ledger component supplies only its namespace and value codec. It has no
Redis client, JSONL format, gRPC client, or backend-specific cleanup code.

The generic state facility may be exposed as an extension of `SessionStore`, or
as a companion interface implemented by the same store. The important contract
is that `Load` returns a `Session` bound to the correct state view and `Delete`
cleans up both core and extensible state. A separate deployment-wide ledger
resolver must not be required at every file-tool call.

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

### Existing-file mutation

Edit must **not** use the ledger-recording Read operation for its internal
content fetch; doing so would overwrite the earlier evidence and let Edit
authorize itself. It calls the decorator's non-recording `ReadVersion` primitive
to obtain current content and `callerCurrentVersion`, computes the replacement,
and then calls `ReplaceFile`. The decorator performs the session check and sends
the earlier ledgered version to the authoritative backend:

```text
Edit(path)
  1. decorated Workspace.ReadVersion(path) → content, callerCurrentVersion
     (does not update the ledger)
  2. compute newContent
  3. decorated Workspace.ReplaceFile(path, callerCurrentVersion, newContent)

ReplaceFile(path, callerCurrentVersion, newContent)
  1. Session.State.Get(ledger namespace, normalized path) → observedVersion
  2. require observedVersion == callerCurrentVersion
  3. raw Workspace.ReplaceFile(path, observedVersion, newContent) → newVersion
  4. Session.State.Put(..., encoded newVersion)
```

An Edit with no earlier agent-facing Read therefore fails even though Edit is
allowed to fetch the current contents to construct a replacement. This invariant
needs an explicit test at the decorator/tool boundary.

Step 2 of `ReplaceFile` catches a file changed since the session's model-visible
Read. Step 3 is the final CAS and catches a mutation that races after Edit's
current read. The backend receives the ledgered expected version; the decorator
does not weaken or emulate its atomicity.

Existing-file Write follows the same path. New-file Write remains create-only;
a successful `CreateFile` records the returned version in session state.

A post-mutation state-write failure cannot roll back an independently committed
filesystem mutation. It must therefore report both facts truthfully: content
changed, but the new observation was not retained. The next mutation will
normally encounter stale or absent evidence and require another Read.

The existing Workspace return shapes need a small shared error contract to keep
that honesty behind the decorator. A Read whose content fetch succeeded but
whose evidence write failed, and a Create/Replace whose content commit succeeded
but whose evidence write failed, must return typed errors that preserve the
successful half. The file-tool result mapper can recognize those generic types
and report both facts; it must not infer commit status from an arbitrary error
string. This is still transparent protocol enforcement: tools map one outcome,
rather than sequencing ledger operations themselves.

## Subagents and shared filesystems

Optimistic concurrency across agents does not require a shared ledger. It
requires independent observations plus a shared authoritative version source.

```text
Parent session ── parent ledger ─┐
                                ├── shared content Workspace
Child session  ── child ledger ──┘
```

If the child changes a file from version V1 to V2, the parent's ledger still
contains V1. Its next Edit or existing-file Write fails when V1 is compared with
V2, or at the final backend CAS if the mutation races later.

Every child session therefore receives its own state view and its own Workspace
decorator:

- a direct-write child wraps the shared raw content Workspace with child state;
- an isolated child wraps its fork Workspace with child state; and
- ledger entries are neither inherited from the parent nor merged back.

Decorator stacking must be prohibited. A child must wrap the raw content
Workspace, not the parent's already session-bound decorator, or one read could
update two sessions' ledgers.

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
Bash operates through `CommandRunner` and may modify the same filesystem without
consulting the ledger. A subsequent structured mutation can detect that change
because the authoritative file version changed, but the Bash mutation itself is
not protected by the prior-read rule.

It is not yet clear whether this is the desired long-term boundary:

- **Structured-tool protection only:** the ledger protects operations where the
  harness can provide precise semantics; Bash remains an explicitly broader
  capability governed by permissions and sandboxing.
- **All mutation protection:** every filesystem mutation, including shell
  writes, would need mediation through the same version-aware service or a much
  stronger execution substrate.

Changing this behavior is out of scope. This proposal preserves the current
boundary and records the question rather than silently claiming Bash participates.

### What identifies an observed file?

The current `LedgerKey(root, path)` lexical rule is sufficient for one stable,
rooted Workspace. Session persistence, environment reattachment, composite
mounts, and scoped grants make the identity question more explicit. A relative
path alone must not authorize mutation after the session is attached to a
different content resource that happens to contain the same path and an equal-
looking token.

The final design must bind evidence to a stable content-resource identity as well
as a normalized path. For today's Workspaces that may be derived from the
persisted environment/workspace identity. Under scoped grants it may instead be
a grant-visible mount/resource identifier returned with the content token. It
must not be a process-local Go pointer, and session reattachment must fail closed
if the identity cannot be reproduced. Choosing that identity belongs in the
filesystem/grant contract; the generic session-state store merely retains the
opaque key and value.

### Is extensible state part of the aggregate or attached to it?

The proposal requires session ownership and one storage lifecycle, but the exact
Go representation remains open. A `Session` may hold a lazy `State` handle, or
composition may attach a state view while reconstructing it. In either case:

- callers access it through the loaded session, not through `Environment`;
- the handle is permanently scoped to that session ID;
- adapters remain behind the session-store boundary; and
- cloning, adoption, deletion, event-sourced reconstruction, and child-session
  creation need explicit state semantics.

### Snapshot and event-log reconstruction

The current reference event fold reconstructs core session state from durable
events. Extensible state introduces a choice: emit state mutations into the
event log, treat the generic state store as an additional source of truth, or
support both with explicit precedence. This proposal does not choose among them,
but a final design must not claim event-log-only reconstruction is complete if
ledger state is absent.

### State consistency and revisions

A future second state consumer may require compare-and-set, bulk reads, or an
atomic relationship with core session Save. Those operations should be added
from demonstrated requirements, not anticipated now. The ledger requires exact
lookup, durable put, absence/error distinction, concurrency safety, and cleanup;
that is the initial generic contract.

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
