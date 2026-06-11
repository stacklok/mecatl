# Cloud-Native Harness Kit — subsystem inventory

*Status: exploratory / scoping. Not a committed direction.*

*Originally written against `main` at commit `a1baf7ee` ("fix(osfs,skills,app): make
out-of-workspace skill files readable"), then re-verified against later `main` (through
`987d88d`).*

> **Update since first writing — two structural shifts the rest of the doc is now read
> against.** (1) **The importable core moved `internal/` → `engine/`.** The domain packages,
> the port interfaces, the agent loop, and the in-tree *reference* adapters now live under
> `engine/` (`engine/session`, `engine/port`, `engine/tool`, `engine/agent`,
> `engine/adapter/*`) — a self-contained, importable library; `internal/` keeps only the heavy
> adapters (`internal/adapter/*`) and composition (`internal/app`). Every `internal/<core>`
> path below has been corrected to `engine/<core>`. (2) **The driver-seams arc shipped** (see
> the new `docs/design/DRIVERS.md`): a versioned gRPC **driver protocol**
> (`contracts/proto/mecatl/driver/v1`) now backs `port.SessionStore` (+ an optional
> `port.PrunableStore` retention seam), `tool.MemoryStore`, and the skill / soul / agent-def /
> command sources, each with a conformance suite the remote client must pass over bufconn. So
> several "Where to explore" notes below — "an external store adapter is a drop-in", "wrap a
> Tier-B port in RPC" — are **no longer hypothetical for those seams; they are implemented**.
> The notes are kept (the reasoning still holds) but annotated where reality has caught up. The
> one store/execution seam still *unbuilt* is the workspace/FS driver — DRIVERS.md carries it
> as an explicit proto **sketch**, which is exactly the "shared-handle problem" ranked #1 in
> *Where to focus*.*

## Why this doc exists

The question behind it: how far is mecatl from a **cloud-native harness** — one whose
conversation/context state lives in an external store rather than process memory, whose
tool execution can be a separate service, and whose process is near-stateless and cheaply
restartable? (See the internal "Cloud native harnesses and Atrium" note for the framing:
the clean architecture gives a hosting platform high-fidelity visibility and *cooperative*
suspend/resume, instead of black-box gVisor process snapshots.)

The short answer is that mecatl is unusually close, *by construction*, for two reasons:

1. **Session state is externalized at every turn boundary** through `port.SessionStore`,
   serialized via a stable snapshot (`sessnap`).
2. **The LLM provider is stateless full-replay** (`store:false`, byte-stable prompt
   prefix). There is no hidden provider-side conversation state to externalize — resume is
   just *load the snapshot and replay*.

What follows is the inventory of subsystems a "cloud-native harness kit" would break out
from the core loop, where each lives in this repo, the core methods on each interface, the
**purpose** of the system, the **supporting types** that matter, and **where to explore**
to adapt it toward the vision.

The subsystems split into two tiers:

- **Tier A** — stateful & execution-bearing. This is the genuine disaggregation work.
- **Tier B** — already-clean narrow ports. Distributing them is "wrap in an RPC adapter";
  no interface redesign.

---

## Tier A — stateful & execution subsystems

These are where the work is. Each is already a port/interface met by adapters only in
composition (`internal/app` + the `cmd/` mains), so the swap points exist; the question is
whether the *contract* admits a distributed/remote backend without leaking single-host
assumptions. (#1–#5 are existing seams; **#6, resource-lifetime management, is the one
subsystem with no seam yet** — it is the abstraction the hand-managed resource lifecycles in
the others are circling, surfaced by the #4 deep-dive and promoted here to be tracked.)

### 1. Session / conversation state — `port.SessionStore`

**Where:** `engine/port/store.go`; adapters `engine/adapter/memstore` (default) and
`internal/adapter/store/jsonlstore`; serialization in `engine/adapter/sessnap`. A remote
backend now also exists as a gRPC driver client (`internal/adapter/grpcdriver`,
`SessionStoreService`).

**Purpose.** This is *the* durable unit of the harness. A `Session` is the aggregate that
holds the entire conversation, the state-machine lifecycle, the permission mode, the run
limits/counters, the workspace root, and the pending permission ask. Persisting it at turn
boundaries is what makes the harness process disposable: kill the process between turns and
a reload reconstructs the run exactly. Everything else in the system is either derived from
the session or is infrastructure around it.

**Core methods:**
```go
Save(ctx, *session.Session) error
Load(ctx, id session.SessionID) (*session.Session, error)   // not-found wraps ErrSessionNotFound
```

**Supporting type — the snapshot (`sessnap.Snapshot`).** The session aggregate is not
serialized field-by-field; it round-trips through a deliberately stable, JSON-tagged DTO:
```
id, state, mode, limits, counters, workspace (root path),
created_at, messages[], pending (the awaiting permission ask), stop_reason
```
Each `message` carries `role, text, tool_calls[], tool_result, reasoning (opaque replay
blob), provider_phase (a second opaque replay marker — the OpenAI Responses
commentary/final_answer phase; same neutral-but-provider-private discipline as reasoning),
parts[] (non-text media)`. **Restore drives the state machine through its public
transitions** (`New → BeginTurn → PauseForApproval → Complete/Stop/Cancel/Fail`) rather
than poking fields, so every invariant holds on the rebuilt aggregate. This is the property
that makes the state genuinely *portable* and not just *dumpable* — any store adapter gets a
correct round-trip for free.

**Supporting type — `session.Usage`.** Accumulated per run; the spend proxy for the token
budget. `InputTokens, OutputTokens, CacheReadTokens (⊂ Input), CacheWriteTokens`;
`TotalTokens() = Input + Output` (cache excluded). Relevant to a cloud kit because per-run
cost accounting is exactly the kind of structured signal a hosting platform wants, and it
is already a first-class value object.

**Where to explore.** An external adapter (S3 / Postgres / Redis) is a drop-in — the
interface is right and `sessnap` already isolates the wire format. *This is now partly
realized:* the gRPC `SessionStoreService` (DRIVERS.md) carries the snapshot as an opaque,
format-tagged blob (`sessnap-json/1`), so a remote driver in any language stores/returns it
verbatim — exactly the "the interface is right, just wrap it" prediction, with a conformance
suite (`storeconformance`) pinning parity. A retention/GC companion also landed as a
*separate optional* port, `port.PrunableStore` (List/Delete + `StoredSession`), discovered by
type assertion so a Save/Load-only backend is simply never swept (issue #38; policy stays in
`internal/app/childgc.go`). The remaining open design question is **mid-turn suspend
granularity**: today resume re-enters at *turn* boundaries (`loadAndReopen` recovers
completed → idle (Reopen), cancelled → idle (Interrupt), and now **failed → idle** (Recover,
issue #51 — retry made *possible*, not guaranteed)), but a session parked *awaiting human
approval* needs a mid-turn resume entry — detailed in 1.4 below.

#### 1.1 The persisted state is the *replay* view, not the *user* view

The snapshot deliberately captures exactly what is needed to replay the conversation to the
`Model` — and nothing more. The conversation *as a user sees it* is strictly richer than the
conversation *as the LLM sees it*, and that richer record is today either ephemeral or
scattered. Concretely, against the things a user-facing transcript would want:

- **Reasoning from previous runs** is *partially* captured. `Message.Reasoning` is persisted
  and replays, but it is the provider *replay blob* — human-readable for Anthropic extended
  thinking, opaque/encrypted for OpenAI. The live `reasoning.delta` stream the user watched
  is not separately stored.
- **Tool-call details** are persisted but **shaped**: `ToolResult.Content` is already
  truncated/token-shaped for the model, so the full raw output is not in the conversation.
  (A richer per-call record — args, result, dispatch/exec timings — does exist in the
  separate `.tools.jsonl` audit log, but it is not stitched into the conversation and is not
  loaded on resume.)
- **Approvals and questions are not durably logged at all.** Only the single in-flight
  `PendingAsk` is in state, and only while awaiting; once resolved, the verdict lived only on
  the `Event` stream. There is no "this call was approved, allow-once, at T" record in the
  `Session`. (`allow-always` survives only as a learned rule in a side store keyed by
  session — not a chronological approval log, and not in the snapshot.)
- **Sub-agent execution state and history** are not in the parent's record. The parent
  conversation sees only the child's result text plus an `agentId` trailer. The child's full
  lifecycle lives in two *other* places: ephemeral client-only delegation events
  (`subagent.*` / `team.*` / `parallel.*`), and the child's *own* separately-persisted
  session (`WithSubagentStore` / `WithMemberStore`), addressable by id and resumable — but
  nothing weaves that timeline back into the parent. (Those separately-persisted child
  sessions now have a retention/GC sweep, via the `port.PrunableStore` seam above — issue #38;
  it bounds their accumulation but does not change that they are disjoint from the parent's
  record.)

So there are effectively **three disjoint records and no single durable user-rich one**: the
persisted `Conversation` (LLM replay view), the `Event` stream (the rich user view — live
reasoning, every ask + verdict, delegation lifecycle, progress, retracts — which the server
*emits and discards*; only a connected `Client` ever holds it), and the separately-persisted
child sessions. The `Event` stream already carries everything a user-rich transcript needs;
it simply is not stored server-side.

The clean architectural answer is to **event-source the stream the harness already emits**:
persist the `Event` log as the source of truth (CQRS / event sourcing — the write side is an
append-only log of events) and make the LLM-replay snapshot *one projection* of it and the
user-rich timeline *another* (the read side has many views, each shaped for its consumer).
This also subsumes the tool-audit log and the scattered child sessions into one ordered
record. It is a larger move than swapping the store backend — a new durable artifact, not
just a new `SessionStore` adapter — and worth scoping deliberately, because a hosting
platform that wants high-fidelity visibility *and* a rich resumable user transcript needs
that superset record, not the replay snapshot.

#### 1.2 Prior art: Claude Code's append-only transcript

Claude Code's on-disk transcript already
takes roughly the event-sourced shape: an append-only, tree-structured (parent-linked) JSONL
that is the durable source of truth, from which the live model context is *projected*. It
records far more than is sent to the model — reasoning blocks (with signatures), per-record
metadata (timestamp, cwd, git branch, model, token usage), sub-agent conversations stitched
inline as linked sidechains, permission-mode changes, periodic workspace file-state
snapshots, and a structured raw tool-result payload (separate stdout/stderr; pre-edit file +
patch) distinct from the shaped, model-facing result. mecatl made the opposite choice — it
persists the projection (the replay snapshot) and discards the rich stream — so the
append-only-source-of-truth design is a proven, practical target, not a speculative one. The
one place both are weak is an explicit per-call permission *verdict* log (allow-once /
allow-always / deny): Claude Code records permission-mode *changes* but leaves the per-call
decision implicit, and mecatl keeps only the single in-flight ask. And note Claude Code's
transcript is still a *local, per-project* file — it solves the richness question, not the
distributed-storage one.

#### 1.3 Compaction makes the two views diverge — the crux

As long as
nothing is compacted, the LLM-replay view and the user view differ only in *richness* — same
messages, more or less metadata. **Compaction changes that qualitatively.** Its entire job is
to scope *down* what the model sees: the snip → strip → collapse → summarize cascade replaces
spans of real history with summaries so the prompt fits the context window. After the first
compaction the two views diverge in *content*, not just detail — the summary exists in the
LLM view, and the original turns it replaced exist only in the user view. The model
deliberately forgets; the **user must still be able to scroll back to the full,
uncompacted transcript** to see what actually happened.

This is the strongest argument for the event-sourced split, because mecatl's current
compaction is **destructive to the only durable record**: `maybeCompact` rewrites the
session's `Conversation` in place via `ReplaceHistory`, and that compacted history is what
the next `Save` persists. The pre-compaction detail is gone server-side — recoverable only if
a connected `Client` happened to retain the `Event` stream. So compaction turns "persist the
projection" from merely *incomplete* into *actively lossy*: the durable store cannot answer
"what did the agent actually do in turn 12?" once turn 12 has been summarized away.

Event sourcing resolves this cleanly and is the natural design: compaction becomes a
transformation that produces a *new projection* (the compacted model context) **without
mutating the source-of-truth log**. The full transcript stays intact for the UI; the model
context is a separate, lossy, regenerable view. This is exactly what Claude Code's
append-only-file-plus-summary-record achieves — the summary is added, the original lines
remain on disk — and it is the behavior a hosting platform needs: the user (and the platform)
keep the complete history while the model runs on the compacted projection.

#### 1.4 Mid-turn suspend: durable state is correct, liveness is the gap

A turn is one model exchange, and state checkpoints to `SessionStore` at every turn boundary —
the *awaiting-approval* state is itself fully serializable (the server already persists it when
it raises the permission ask). So the gap is not durability, it is **liveness**: while waiting
for a human the run is parked on an in-memory Go channel, so the goroutine + engine + heap stay
resident for the whole deliberation, and resume is an in-process channel write
(`ErrNoActiveRun` if the process died). The fix is to **evict the parked run and rehydrate from
the awaiting snapshot on approval**; the stateless full-replay provider makes eviction free on
the provider side. The only genuinely new piece is a *mid-turn* resume entry — today resume
re-enters at turn boundaries — i.e. a "resume from awaiting" cursor: a small interface/loop
addition, not a new backend. This is what makes the process *genuinely* disposable rather than
"disposable except while a human is mid-approval" (prioritized in *Where to focus* #2).

### 2. Filesystem / workspace — `tool.FileSystem`, `tool.Workspace`, `tool.WorkspaceReader`

**Where:** `engine/tool/tool.go`; adapters `internal/adapter/osfs` and `engine/adapter/memfs`.
*The FS is now optional:* a **no-FS session profile** exists (`engine/adapter/nofs`), so a
session can run with no workspace at all (chat/tool-only) — the seam is the one *every
FS-touching tool* executes against, not every session.

**Purpose.** The session-scoped seam *every (filesystem-touching) tool executes against*. It does three jobs:
(a) scopes all paths to one session root and rejects escapes (`../`, symlinks) — the
containment boundary; (b) exposes the read/search/write operations the core tools need;
(c) carries the per-session **Edit read-ledger** that lets the Edit tool enforce its
read-before-edit / unchanged-since invariant. In the cloud-native vision this is the
*shared key thing*: the moment tool execution becomes a separate service, this is the
resource that service and the harness must both see.

**Core methods (`Workspace`):**
```go
Root() string                                   // absolute session root
Read(ctx, path) ([]byte, error)
Stat(ctx, path) (FileInfo, error)
Write(ctx, path, data []byte) error
Glob(ctx, pattern) ([]string, error)
Grep(ctx, pattern, pathGlob) ([]GrepMatch, error)
RecordRead(path, version string)                // Edit ledger: note the version read
WasReadUnchanged(ctx, path) (bool, error)       // read-before-edit invariant check
```
`WorkspaceReader` is the read-only subset (`Root + Read + Stat`) handed to consumers that
must never write (e.g. the permission-config resolver). `FileSystem` is the lower,
path-oriented seam (`Read/Write/Stat/Glob`) that `Workspace` composes.

**Supporting type — `FileInfo`.** A provider-neutral subset of `io/fs.FileInfo` carried as
plain fields (`Name, Size, Mode, ModTime, IsDir`) so adapters don't leak `os` types into the
domain. A remote FS adapter populates this from whatever its backend returns.

**Supporting type — `GrepMatch`.** `{Path (session-relative), Line (1-based), Text}`. Worth
noting because `Grep` is the one operation a naive remote FS would implement as "read every
file and scan" — a git-backed or indexed backend would want server-side search instead.

**Supporting type — the version token.** `RecordRead(path, version)` /
`WasReadUnchanged` treat `version` as an **opaque** comparable token (the osfs adapter uses
a content hash / mtime). The Edit invariant is therefore *already FS-agnostic* — a remote or
git-backed FS picks its own version scheme (an etag, a blob SHA) and the invariant still
holds. This is a good sign the abstraction was taken seriously.

**Where to explore.** A remote / virtual / git-backed-native `FileSystem` adapter. `memfs`
already proves a non-OS FS works through every tool end-to-end (the whole test suite runs on
it). *This is the one store/source seam the driver arc deliberately left unbuilt:* DRIVERS.md
carries a proto **sketch** for a `WorkspaceService` and spells out exactly why it is heavier
than every shipped seam — it breaks the 64 MiB unary rule (needs chunked/streamed reads,
paged Glob/Grep), Bash needs real exec on real files (so a remote FS serves the
`WorkspaceReader` read side only, or pairs with a local materialization step), and part of the
`Workspace` surface (the Edit read-ledger) is harness state, not storage. The four design
notes below capture where this interface needs to change to get there.

#### 2.1 Drop `Root()` — mounting belongs to the environment

`Root()`
conflates two unrelated things: *containment* (paths confined, escapes rejected — a security
property) and *mount location / identity* (where the tree physically lives — an environment
concern). It exists only because three consumers need "where am I": the permission-config
resolver, the surfaced fork-root paths, and the `CommandRunner` `workdir`. That is leakage.
The clean model is the Unix VFS: **a filesystem does not know its mount point — the mount
table does.** Drop `Root()`; make paths opaque *within* a filesystem (relative to its own
origin, which it cannot represent escaping). Containment then falls out for free ("no mount
covers that path → not found"). The binding *which filesystem is mounted where, and what the
execution environment sees* moves to the **environment / composition layer** that owns both
the filesystem(s) and the `CommandRunner` — exactly where it belongs once both exist as
separate services.

#### 2.2 Make the workspace a composed namespace, not a singleton

Today there is one
`Workspace` per session (one root) plus ephemeral forks. The richer model is a **mount table
/ composite filesystem** built at harness construction that routes path prefixes to backend
`FileSystem`s, presenting one unified namespace to the agent. Precedent: Unix VFS, overlayfs
union mounts (read-only lower + writable upper), Plan 9 / 9P per-process composable
namespaces, FUSE. This unlocks real cases — the project repo (git-backed) at `/repo`, a
read-only reference tree at `/refs`, a writable ephemeral `/scratch`, a synthetic FS exposing
MCP resources or memory-as-files. Note the existing `osfs.WithReadRoots` skill carve-out is
already an *ad-hoc, special-cased mount* ("these skill dirs, read-only"); generalizing it to
a real mount table makes the special case disappear and gives a natural home for the
per-mount read-only/writable flag. Containment stays compositional — each backend is
independently scoped.

#### 2.3 The capability surface is moving past POSIX (two thin spots)

The
core tools already exceed POSIX (line-numbered Read with line `offset`/`limit`, Glob, Grep,
structured Edit). Two concrete gaps:
- **Grep is minimal** — `pattern` + `path` only. A modern agent grep (ripgrep-backed) wants
  `output_mode` (content / files-with-matches / count), context lines (`-A`/`-B`/`-C`), case
  control, `type` filters, multiline, and a head limit. This is the single biggest richness
  delta and changes how an agent navigates a codebase.
- **No directory manipulation as first-class tools** — no mkdir / rename / move / delete.
  Claude Code does these via Bash. mecatl prizes the shell-less posture (`--no-bash`), and in
  that mode it has *no way to rename or delete a file at all* — so directory-manipulation-as-
  FS-tools matters precisely *because* of the shell-less goal. Higher priority than it looks.

  The frontier beyond that: multi-edit / unified-diff application (token-efficient large
  edits), multimodal/binary reads (images, PDFs, notebooks), and a **semantic / LSP-backed
  tier** (go-to-definition, find-references, rename-symbol — correct everywhere, not textual).
  The guiding principle: shape operations around **what an LLM can reliably address —
  content anchors, line ranges, names/symbols — not syscall units like byte offsets.** Byte /
  codepoint ranges are deliberately absent and should stay so for the text path; they are
  brittle for models (off-by-one, multibyte) for the same reason raw version tokens are (next
  note). Grep was step one past POSIX; LSP-aware editing is step two.

#### 2.4 The read-ledger is session state — explicit interface, implicit to the model

The Edit safety check (must read-before-edit, and the file must be
unchanged since) is **optimistic concurrency control**, and today the check is internalized:
the osfs `Workspace` keeps a per-session ledger of sha256 content fingerprints (the `version`
parameter in the interface is vestigial — osfs computes its own). That ledger is exactly the
kind of per-session mutable state the stateless / distributed goal wants to externalize — so
**it should be part of the `Session` state** (persisted in the snapshot, portable, on the
event log), not hidden in the FS adapter.

The clean way to get there: **make the version token explicit at the `FileSystem` interface**
(read returns a token; write is conditional on it — an etag / If-Match / CAS shape), which
makes the FS adapter *stateless* and distributed-friendly, **but have the harness store and
supply those tokens** out of session state so the *model* still experiences it as implicit
("read, then edit"; no token in its mental model). The model sees implicit; the interface
sees explicit; the harness mediates. This is the rare design where the usable choice and the
cloud-native choice align — the burdensome part (carrying an opaque token, which models
garble) is handled by the harness, while the statelessness the interface gains is what makes
a remote/shared FS tractable.

Pair that with **content-match as a second, token-free method**: requiring Edit's `old_string`
to still be present is itself an optimistic-concurrency check whose "token" is the content the
model already reasons about — it needs no stored state and fails safe if the file changed.
The likely best answer is to **offer the model multiple edit methods** and let the situation
pick: token-conditional edit (precise, harness-mediated), content-anchored edit (stateless,
robust), and — the same "multiple methods" idea extended — **distinct methods for text vs
binary** content (line/string-anchored for text; whole-blob or explicit byte-range for
binary, where byte offsets are appropriate and the model isn't expected to author them
free-hand). Richer fallbacks if hard-reject churn becomes annoying: three-way merge (merge the
intended edit against the changed base, like git) and snapshot isolation (the fork/worktree
model already provides this — conflicts surface at merge).

#### 2.5 The filesystem is an ecosystem substrate, not just an agent seam

mecatl's `Workspace` today is *both* the raw filesystem and the
LLM-shaped surface (line-numbered Read with caps, content-anchored Edit). Those are two
layers, and they should be separated, because the filesystem will be consumed by systems that
are **not LLM-based**:

- A generic **bash / CLI execution environment** mounted onto the filesystem needs a
  *superset of POSIX*, not the model-friendly subset — `open`/`pread`/`pwrite`/`seek`,
  `readdir`, `rename`/`unlink`/`mkdir`/`symlink`, `truncate`, `stat`/xattrs, streaming for
  large files. A real shell and the programs it runs assume full POSIX semantics.
- **Specialized tools** (PDF manipulation, image processing, compilers, indexers, an LSP
  server) want to be *handed the filesystem — or a scoped reference into it — and read/modify
  files directly*, byte-level, rather than round-tripping through the LLM Read/Edit/Write
  tools.

So the right shape is two layers: a **substrate filesystem** — rich, POSIX-superset,
byte-level, streaming, directory-and-metadata-complete, mountable, *shared across the whole
ecosystem* — and, built on top of it, **consumer-specific surfaces**, of which the LLM tool
layer (Read/Edit/Grep, model-addressable) is just *one narrowing adapter*. A POSIX/mount
adapter (FUSE / 9P) is another; a direct handle passed to a PDF tool is another. **Design the
substrate for the most demanding consumer (POSIX/bash is the floor), and let the LLM tools
narrow it** — which scopes the "no byte offsets, content anchors only" principle from 2.3 to
the *tool layer*, not the substrate. The substrate must expose byte
offsets; the LLM tools choose not to.

Two consequences worth pulling forward:

- **The mount table operates at the substrate level.** The bash environment and the
  specialized tools *mount the substrate*; the agent tools are just another consumer reading
  through it. This is the same VFS picture from the mount-table note, now with non-LLM
  tenants — and it is the concrete meaning of "the filesystem is the shared key thing" in a
  web of services: the `CommandRunner`'s environment definition is largely *which substrate
  filesystems are mounted where*.
- **Multi-writer concurrency becomes first-class, not an edge case.** Once a bash command or
  a PDF tool writes to the same substrate the LLM is editing, the read-before-write guard is
  no longer protecting against a rare external change — there are genuinely concurrent
  writers. Real concurrency then belongs *at the substrate* (locking / transactions /
  change-notification such as inotify-style watches), and the LLM's optimistic ledger (and
  the harness-managed token from 2.4) sits on top as a UX guard, ideally
  *sourced from and invalidated by* the substrate's own native versioning (mtime+inode, blob
  SHA, etag). This is exactly the "modified outside the context of the agent" case the
  read-ledger gestures at — promoted from edge case to a defining property of a shared
  substrate.

Substrate capabilities worth having beyond POSIX, for the same ecosystem reason: content
search (the grep backend), **watch / change-notification**, snapshots / copy-on-write
versioning (a git-backed substrate gives this natively), atomic multi-file commit
(transactions), and synthetic / virtual files (MCP resources or memory presented as files).

#### 2.6 Adapt existing stores as backends — the enterprise integration story

The highest-value substrate adapters are not new filesystems we build —
they are *existing* content stores wrapped to present as one: **Google Drive**, SharePoint /
OneDrive, S3 / object stores, Box, Dropbox, a document-management system, NFS. The reason is
not "more storage options"; it is that **backing the agent's filesystem onto a governed store
makes the agent inherit the corporate controls that store already enforces**, instead of
mecatl reinventing them (badly) or operating outside them (a compliance liability). The list
of controls is effectively endless and non-negotiable in a corporate setting: **litigation /
legal holds, retention schedules and records management, eDiscovery, DLP, data residency /
sovereignty, sensitivity classification and labeling, per-object access control and sharing,
audit / chain-of-custody, disaster recovery and replication.** A hosting platform that wants
agents to touch corporate information at all needs this; it is arguably the whole point of
the integration.

This refines — does not contradict — the "POSIX is the floor" note: POSIX is the floor *for a
consumer that needs it* (a bash environment), but most of these backends are **not POSIX**.
Google Drive is ID-canonical not path-canonical, has first-class versioning, per-object ACLs,
coarse upload/download/export operations (no `pread`/`pwrite`/`seek`), a non-tree parent
model, and eventual consistency. So the substrate interface must be **capability-negotiated**:
a backend *declares* what it supports (random write, rename, watch, versioning, holds), and a
consumer either gets native support, gets it via a translation/caching layer (the
rclone / Drive-File-Stream / FUSE-bridge pattern — cache + write-back to fake POSIX over a
cloud store), or is told this mount does not offer that capability and refuses to mount.
Prior art for "adapt existing stores as one filesystem" is mature: **rclone** (≈70 backends),
FUSE / 9P / WebDAV, **CMIS** (the document-management standard, with versioning/retention/holds
built in), and **S3 Object Lock / WORM** (the immutability primitive that *is* a litigation
hold).

Two hazards this creates, both of which intersect other subsystems:

- **Forks and scratch copies can *break* governance.** The fork/worktree model (subsystem 3)
  and any `/scratch` mount create *copies* of governed content. A litigation-held or
  residency-bound document copied into an unmanaged scratch area is now an uncontrolled copy —
  a real compliance violation, not a hypothetical. So the mount table must mark which mounts
  are **governed / system-of-record** versus **ephemeral**, and the substrate adapter must
  *propagate or enforce* constraints across copies: a hold makes the file read-only and
  undeletable (the agent gets an error, by design); a residency constraint forbids copying it
  into an out-of-region mount.
- **The agent's actions on governed content must feed the enterprise audit trail.** This is
  the same durable-record need as subsystem 1's event log, now with an external consumer:
  reads, edits, and deletes of governed files are audit / eDiscovery events the corporate
  system expects to see. Event-sourcing the harness and wiring those events to the backend's
  audit plane is the same work, done once.

#### 2.7 Handing a descoped filesystem to another service (bigger than the filesystem)

Once the filesystem is a substrate shared across an ecosystem,
the load-bearing mechanic is: *how does the harness let another service operate on a file — or
a subtree — directly, without transiting the bits through the harness, and never through the
LLM?* A PDF service should read the 50 MB PDF straight from the substrate; the harness should
not proxy the bytes and the model should certainly never see them. The right mental model is
**object-capability passing**: the *handle is the authority*, you **descope by attenuating the
handle before you pass it** (read-only, subtree-only, time-boxed, carrying the governance
constraints from 2.6), and you never hand out "a path + ambient authority." The
LLM only ever holds *references* (paths/ids); a tool invocation resolves a reference to a real
handle and hands *that* to the service; the bytes flow service↔substrate.

The inspirations Joe named are exactly the two ends of this:

- **Plan 9 / 9P — the protocol and namespace model.** Resources are *file servers*; a process
  is handed a *per-process namespace* (a curated mount table) rather than absolute paths, so it
  sees only what was mounted in — descoping by construction. 9P is network-transparent (pipe,
  TCP, TLS), and a `walk` derives a narrower fid from a broader one — attenuation as a protocol
  operation. A 9P-like substrate protocol gives the local *and* remote handoff story under one
  model: "pass the VFS to a service" = export a sub-namespace to it.
- **Unix-domain-socket fd passing (`SCM_RIGHTS`) — the local-host OS primitive.** Pass an open
  file descriptor across a process boundary; the receiver gets a real, kernel-access-checked,
  zero-copy handle to the same open file — the bits never enter the sender's userspace. Crucially,
  a **directory fd is itself a descoped filesystem root**: hand a dirfd and the receiver can only
  `openat` *beneath* it (with `openat2(RESOLVE_BENEATH)` on Linux for kernel-enforced
  containment — essentially what mecatl's `os.Root` already does). **Capsicum** (FreeBSD)
  is the principled version: `cap_rights_limit` attenuates an fd (read-only, no-seek) *before*
  it is passed. That is "a descoped version of the VFS" as a kernel object.

These compose into a two-regime answer (capability-negotiated, like everything else in this
subsystem):

- **Same host, trusted-ish service:** pass an attenuated directory/file fd over an `SCM_RIGHTS`
  socket. Zero-copy, kernel-enforced, the harness is not in the data path at all. Fast path.
- **Cross host, or non-POSIX backend:** the handle becomes a **signed, scoped, expiring lease /
  token** — the harness mints a grant naming a subtree + rights + governance constraints, and the
  peer service calls the substrate directly (gRPC stream, 9P-over-TLS, or a **presigned URL** —
  S3/GCS signed URLs and Drive resumable-upload sessions are *already* exactly "a descoped,
  time-bounded, single-object capability handed to another service so it talks to the store
  directly"). Bits flow peer↔substrate; the harness mediates only the grant.

The lineage to mine if this is pursued: Plan 9 namespaces, `SCM_RIGHTS`, Capsicum
rights-limited fds, the object-capability model (attenuation + the caretaker/proxy pattern for
**revocation** — you cannot un-send an fd, so use short-lived leases or a closable proxy), seL4 /
KeyKOS for the theory, and NFSv4 / WebDAV / presigned URLs for the boring-but-shipping distributed
realizations.

Honest hard parts: revocation (leases expire; fds don't — design for it up front); the trust
gradient (a *direct* handle to an untrusted service is direct substrate access — for untrusted
peers you may deliberately *proxy* and eat the cost rather than hand a real handle); and the fact
that the "handle" has several irreconcilable representations (kernel fd / lease token / presigned
URL / 9P fid), so the capability is itself an abstraction the substrate negotiates per backend.

**Why this is bigger than the filesystem.** The same machinery — *attenuate a capability and
pass the handle, don't proxy the resource or grant ambient authority* — is how the harness should
delegate **every** scoped resource to a peer: a sub-namespace to a subagent or team member running
in another process/host (subsystems 3 and 4), the working directory a remote `CommandRunner`
operates in, a network-egress scope, a secret, a compute lease. The filesystem is the first and
most concrete instance, but the right artifact is a harness-wide **capability / handle-passing
layer** — the Plan-9-meets-object-capability insight applied beyond files. Worth a dedicated
design spike of its own; recorded here because the filesystem is where it first bites.

### 3. Workspace isolation / forking — `tool.WorkspaceForker`

**Where:** `engine/tool/isolation.go`; adapter `internal/adapter/forker`.

**Purpose.** Fork-join parallelism (and subagent/team isolation). Produces an isolated
**child** `Workspace` from a base so a forked agent loop can read — and, when its catalog
allows, write — without racing on or mutating the shared base tree. This is what lets
Parallel run N branches, and lets read-only subagents run a shell (build/test/git-inspect)
without their writes touching the parent's working tree.

**Core method:**
```go
Fork(ctx, base Workspace, label string) (child Workspace, cleanup func() error, err error)
```
`label` is a human-meaningful tag (branch idea / task name) the impl *may* fold into the
child path; it is not load-bearing or required to be unique. `cleanup` tears the child down
and is safe to call once.

**Implementations today (proof the seam holds):**
- **git-worktree** (default): `git worktree add --detach <child> HEAD` — cheap, isolates the
  working tree + index, *shares* the base `.git` object store.
- **force-copy** (`WithForceCopy`): recursive copy of the whole tree including `.git` — fully
  isolated so a child's `git commit/push` cannot reach the base repo. Used for mutating
  forks.

**Where to explore.** A remote / non-git isolation adapter (overlay FS, object-store
snapshot, copy-on-write volume). The interface is genuinely agnostic — "the agent never
knows which." **Caveat:** the git-worktree *threat model* is NOT in this adapter — it lives in
`governance.IsolationApprovable`, which assumes the fork shares the base `.git`. A different
isolation kind needs that domain logic to become posture-aware (3.3 below; one of the two
domain-logic touch points in *Where to focus* #3).

#### 3.1 Forking is a special case of filesystem descoping

`Fork(base, label) → (child, cleanup)` expresses exactly *one* sharing policy:
a full, opaque copy-on-write copy whose merge strategy is hardwired to **discard** (`cleanup`
removes the child; nothing flows back to the base). The richer model the controlling agent
actually wants is a *menu* of sharing policies, chosen per child:

- **(a) a descoped view** — a single file, or a read-only slice of a subtree. (Bind-mount /
  9P-attach of a sub-path with a read-only flag. The base tree is untouched by construction;
  no merge needed.)
- **(b) a copy-on-write fork** — today's behavior, but with an explicit **merge-back
  strategy** instead of always-discard: promote-on-success, hand-back-as-diff-for-review,
  3-way merge, or discard.
- **(c) the shared tree** — no isolation; accept that agents may step on each other.

Only (b)-with-discard exists today. (a) and (c), and *any* merge-back, are genuine new
capability. This is not an accident: mecatl's subagent/team model is **read-only children
precisely to dodge the merge problem**, and Parallel's "merge" is to surface the fork-root
*paths* in the result text and let the parent agent read them manually (subsystem 4). The
moment mutating forks merge back, the harness owns
**conflict resolution** — the hard distributed-systems cost the read-only design was built to
avoid. "Various strategies on how to merge it back" is where the real work and the real risk
live; the strategy must be an explicit, named choice, not a default.

#### 3.2 Read-only peers — a distinct primitive (coordination, not isolation)

Separate from CoW-fork-then-merge: a *shared* tree where each child gets its own
writable file/directory and a **read-only view of its peers'** writable regions. This is a
shared mount namespace with per-agent write scoping — a blackboard pattern. It trades
isolation for **live coordination**: peers observe each other's progress with no merge step,
but there is no rollback and collisions are real (mitigated, not eliminated, by the
write-scoping). It is a different point on the isolation↔coordination axis than (b), and the
controlling agent should pick per task. Plan 9's `rfork` + union mounts is the cleanest prior
art: fork the namespace, bind one writable dir, attach peers read-only.

#### 3.3 git-worktree is embedded in the threat model, not the data path

Swapping the isolation mechanism is *proven, not hypothetical* — the forker is behind
`tool.WorkspaceForker` with two working implementations, and the agent references git only in
*comments*; the actual call goes through the interface. So at the `Workspace` interface,
git-worktree is pure CoW mechanism, and a remote/overlay/object-store isolation adapter would
drop in cleanly. Two couplings are *not* in the adapter, though:

- **The threat model, in `governance`.** `governance.IsolationApprovable` auto-approves
  `{go test, build, vet, list}` and *rejects* `git push/remote/fetch/config/worktree` in an
  isolated child **because a worktree shares the base `.git` object store** — the threat it
  defends is "a child commit/push reaches the base repo's remote." Force-copy severs `.git`, so
  that same posture is *wrong* (needlessly restrictive) for it; a read-only-subtree view (a) has
  yet another posture. So the isolation **kind** must select the governance posture —
  `IsolationApprovable` has to become isolation-kind-aware rather than worktree-assuming. This
  is core domain logic, not wiring — handle with care; it is one of the two domain-logic touch
  points in *Where to focus* #3.
- **Paths-as-strings.** The dropped `Root()` (#2), the `CommandRunner` `workdir` (#4), and the
  surfaced fork-root paths all assume a mountable local path. The moment tool execution and the
  filesystem are separate services, that path must be a shared resolvable *handle* — the
  central design constraint of the whole direction (*Where to focus* #1), quietly assumed true
  in several places.

#### 3.4 "Fork a workspace" should mean "fork the environment" — the container model

Forking is naturally bigger than the filesystem. A
container is a composition of *independent* isolation choices — mount namespace, dropped
capabilities, user/credential namespace, cgroups — and a forked workspace is the same shape on
four axes:

| Axis | Today | Container analog |
|---|---|---|
| **Filesystem view** | full CoW only (case (b)/discard above) | mount namespace / overlay |
| **Tool catalog** | exists, but harness-decided (read-only children get a narrower catalog), not a per-fork choice | dropped capabilities |
| **Credentials / auth** | **absent** — MCP/tool auth and git credentials are ambient; no per-fork scoping | user namespace / token scoping |
| **Budget / limits** | **mature** — TIGHTEN-only `MaxTurns` / `MaxToolCalls` / `MaxRunTokens` / timeout (the per-engine budget brake) | cgroups |

Budget is the *most* developed descoping axis; auth is the missing one. The unifying move is
to make `Fork` take a **spec, not a `label`** — an environment-descent descriptor carrying the
FS sharing mode (a/b/c + merge strategy), a catalog delta, a credential scope, and a budget —
and let the *controlling agent* choose them per child. That turns "fork a workspace" into
"spawn a descoped environment" (chroot-but-flexible, exactly the framing), and unifies
isolation, the existing read-only-subagent narrowing, and the existing budget brake under one
coherent descent operation. The return also needs to outgrow `cleanup func()`: to express
merge-back it must be a handle with `Merge(strategy)` / `Discard()`, not a teardown closure.

### 4. Command / tool execution — `tool.CommandRunner` + the `tool.Tool` catalog

**Where:** `engine/tool/tool.go`; adapters `internal/adapter/{osfs (the runner),tools,toolkit}`.

**Purpose.** Two related seams. `CommandRunner` is the shell-execution seam — the *only*
thing the Bash tool depends on, which is what makes shell (and therefore arbitrary command
execution) optional in the catalog. `Tool` is the seam every capability implements; the
**catalog** is a registry assembled per session, so "a limited set of tools" is just a
different catalog assembly. This is the "tool execution as a separate service" piece of the
cloud-native vision.

**Core methods:**
```go
// CommandRunner
Run(ctx, command string, workdir string) (CommandResult, error)   // ErrNoShell when none

// Tool
Spec() ToolSpec
ReadOnly() bool                                                    // drives read-parallel/mutate-serial dispatch
Execute(ctx, in session.ToolCall, ws Workspace) (session.ToolResult, error)
```

**Supporting type — `ToolSpec`.** The model-facing description of a tool:
```go
type ToolSpec struct {
    Name        string          // catalog name
    Description string          // model-facing documentation
    Schema      json.RawMessage // JSON schema for the tool's Args
}
```
This is what gets rendered into the per-turn tool inventory the model sees. A
`Disclosable` tool can advertise a cheap metadata-only `Advertised()` spec and hydrate the
full `Spec()` on demand (progressive disclosure). For a remote-tool world, `ToolSpec` is the
contract a tool-execution *service* would publish so the harness can advertise it without
hosting the implementation.

**Supporting types — `session.ToolCall` / `session.ToolResult`.** The call/result pair that
crosses the execution seam:
```go
ToolCall   { ID ToolCallID; Name string; Args json.RawMessage }   // Args validated against ToolSpec.Schema by the Tool
ToolResult { CallID ToolCallID; Content string; IsError bool }    // Content already token-shaped/truncated by the tool
```
Note `IsError` is *not* a harness error — an error result is still fed back to the model so
it can recover. This call/result shape is exactly what a remote tool RPC would carry.

**Supporting type — `CommandResult`.** `{Stdout, Stderr (both truncated by the adapter),
ExitCode}`. A non-zero exit is reported via `ExitCode`, not a Go error; error is reserved for
execution faults (cancellation, timeout, missing shell).

**Where to explore.** Two directions, and they are different. (a) A **remote
`CommandRunner`** that executes in a dev-environment which mounts the shared FS — the
interface already anticipates this ("run it locally, in a remote environment, or refuse").
(b) **Structured tools** as new `Tool`s with their own typed interfaces, instead of routing
everything through arbitrary shell. The architecture supports both; the key insight is that
`CommandRunner.Run(command string, workdir)` is **shell-shaped by construction** — it is the
wrong seam to carry structured capabilities. Limited, capability-scoped execution belongs in
new `Tool`s, not in a richer `CommandRunner`.

#### 4.1 Split the data plane from the environment plane

`CommandRunner.Run(command, workdir)` collapses two things that
want to be separate: the *data* (the governed, forkable workspace of #2/#3) and the
*environment* (the OS image — installed tools, their config, `PATH`, CLI credentials). The
cloud-native cut is the container model: **image vs. volume.** The workspace is a bind-mounted
data-only volume; the toolchain, tool config, and credentials are the *image* and live
**outside** the data dir. The honest scope warning: this is the single largest expansion in
this doc — everything *behind* the `Run` seam grows from "shell out locally" into remote
sandbox orchestration (the devcontainer / Codespaces / Coder / e2b / Fly-Machines space). The
`Run` *seam itself* stays stable and narrow; `CommandRunner` becomes a **client of a new
environment-provisioning subsystem**, which is where construction, lifetime, and reuse live.

#### 4.2 Declarative immutable environments — support a spectrum, default low

An immutable,
content-addressed environment gives reproducibility → **auditability**: "what environment did
this command run in?" answers with a closure/image hash, not "whatever was on the box." That
environment description is also a **provenance fact to persist with the session** (ties to #1).
Bad news: Nix is the gold standard *and* a footgun — raw flakes handed to an agent are a
mistake, cold builds are slow, flakes are still "experimental." The realistic design is a
spectrum, picked by how much rigor a task needs:

- **Floor — digest-pinned OCI image (+ lockfile, + SBOM).** Most of the immutability/audit
  benefit comes from content-addressing alone; you do not need Nix for it.
- **Middle — image + a declarative package delta** (devcontainer features, a `nix profile`
  add-list) drawn from a **curated catalog**, not free-form.
- **Ceiling — a full Nix/flake closure** for closure-level audit when it's warranted.

Default to the floor; lead the *agent* to good environment defaults the same way the harness
should lead non-expert users to good ones.

#### 4.3 Environment lifetime is three tiers of state, not two

"The env is ephemeral and
reconstructible; durable state lives only in the mounted FS" is the right thesis (the
stateless-process discipline applied to execution), but two tiers is a trap — real toolchains
keep load-bearing state *outside* the data dir (`~/.cache/go-build`, `node_modules`, `~/.m2`,
LSP indexes, the Nix store). Reset all of it per call and every build is cold. The honest model:

1. **Durable / governed / shared** — the data FS (#2). Source of truth; persisted, audited,
   forkable.
2. **Cache — reconstructible, best-effort** — build caches, indexes, scratch. Carried across
   calls *opportunistically* for speed; never guaranteed, never audited, safe to evict. Wants
   a **cache key** (env-hash + lockfiles) so it is safe to share/reuse/evict — which makes it a
   managed resource (→ #6).
3. **Environment image — immutable, declarative** — 4.2. Reconstructible from
   description; not "state."

The rule that answers *best-effort vs. guaranteed*: **correctness depends only on tier 1;
tiers 2 and 3 are pure performance/reproducibility.** The cache tier is best-effort *by
design* — guaranteeing it reintroduces exactly the snapshot-fidelity problem this approach
exists to avoid. If you can drop tier 2 at any moment and pay only latency, the line is drawn
correctly.

The closest mature prior art for this whole shape is the **CI/CD runner** (GitHub Actions and
peers): an ephemeral, declaratively-described execution environment, a checked-out workspace
as the durable artifact, and an explicit **best-effort cache** keyed on lockfiles/hashes as a
pure optimization — with exactly the same **security and auditability** concerns (what ran, in
what environment, with what credentials, against what inputs). It is worth borrowing their
vocabulary and their hard-won boundaries wholesale rather than reinventing them; the
agent-execution environment is a CI runner whose job list is decided by a model instead of a
YAML workflow.

#### 4.4 Long-lived / interactive processes break the `Run` contract

`Run(command, workdir) → (stdout, stderr, exit)` is a **batch** contract: the process
terminates and you collect output. A dev server, watch-mode compiler, REPL, or `tail -f` is
long-lived / streaming / interactive — a different lifecycle (start → handle → poll/stream/
signal → terminate). Don't widen `Run` to fit them; that corrupts the clean batch shape. The
right structure is a *sibling* `ProcessHost` port — and mecatl already has the lifecycle shape
to borrow one layer down in **background subagents** (detach → poll → drain). Connectivity
back to a long-lived process (reach a dev server from the desktop) is Tailscale / Cloudflare
Tunnel / ngrok territory, but it is a real security escalation: exposing an ingress from a
sandbox puts the sandbox's network surface in the threat model. For a knowledge-worker agent:
**out of scope.** For "run my app, show me a preview URL" (Codespaces / v0 / bolt): core,
later. Scope it out of v1 but **don't design it out** — name it a frontier with its own
auth/threat model.

#### 4.5 The unifying realization belongs in its own subsystem (→ #6)

The FS, the
execution environment, the cache tier, a long-lived process, a tunnel, a checked-out
credential lease, and an MCP connection are all **resources whose lifecycle does not nest
inside one tool call** — and they are instances of one missing abstraction. That abstraction
is promoted out of this note into **subsystem #6, resource-lifetime management**, because it is
cross-cutting and because each such resource is *more state the session must track* with a
disciplined model. See #6.

### 5. Cross-session memory — `tool.MemoryStore`

**Where:** `engine/tool/tool.go`; adapter `internal/adapter/memory` (flock + single JSON
document). Distilled in the background by `internal/adapter/dream`. A remote backend now also
exists as a gRPC driver client (`internal/adapter/grpcdriver`, `MemoryStoreService`).

**Purpose.** Conservative, cross-session ("tiered") memory exposed to the model as Remember
/ Recall. Scoped per project directory: facts written in one session are visible to later
sessions over the same project, isolated from other projects. The tier-0 *index* (keys +
one-line descriptions, values omitted) is cheap enough to keep always-in-context; values are
loaded on demand by key.

**Core methods:**
```go
RememberEntry(ctx, MemoryEntry) error              // the sole write; convenience Remember(key,value) was dropped
Recall(ctx, key) (MemoryEntry, bool, error)        // miss is (zero,false,nil), not an error
List(ctx, prefix) ([]MemoryEntry, error)           // FULL entries — value included
Forget(ctx, key) error                             // deleting a missing key is not an error
Index(ctx) ([]MemoryEntry, error)                  // tier-0 routing table, VALUE OMITTED
Search(ctx, query, k) ([]MemoryEntry, error)       // local BM25, VALUE OMITTED
```
(The interface was trimmed to consumer-driven methods when the port was finalized — the
convenience `Remember(key, value)` went away once its only caller was a wrapper over
`RememberEntry`.)

**Supporting type — `MemoryEntry`.**
```go
type MemoryEntry struct {
    Key         string    // opaque lookup key, e.g. "pref/test-runner"
    Value       string    // stored text; EMPTY in Index/Search results by design
    Description string    // optional one-line tier-0 hook; derived from value's first line if empty
    UpdatedAt   time.Time // wall-clock last write
}
```
`Index` and `Search` deliberately return entries with `Value` omitted — the tier-0 view is a
routing table, not the payload; the model `Recall`s a key to load the value. Note `UpdatedAt`
is *writer-supplied wall-clock*: cosmetic today (recency display), but a soft assumption to
avoid leaning on for conflict resolution in a distributed backend (clock skew).

**Where to explore.** bbolt or SQLite behind the interface. The interface is backend-neutral
in shape as written — any KV/SQL backend satisfies it, *and this is now demonstrated, not
asserted:* `tool.MemoryStore` was finalized as a first-class port with a `memconformance`
suite, and a gRPC `MemoryStoreService` driver client passes that same suite over the wire — a
remote memory backend in any language is a conformance run away (DRIVERS.md). Two contract
observations stand: (a) `Search`'s "local dependency-free BM25, deterministic order" is
*over-specified* for a pluggable backend (a SQLite FTS5 or vector store ranks differently);
loosen to "relevance-ranked" if it becomes a real seam. (b) The one change that touches the
*interface* (not just the adapter) is a transactional `Update(ctx, fn)` affordance to make
read-modify-write atomic — **still not added** (the port deliberately grew no method without a
consumer), so 5.1's TOCTOU note below is still live future work.

#### 5.1 Locking is single-host; the interface should outlast it (TOCTOU)

`flock` is used in exactly one place in the whole harness — this store: a whole-file BSD
`flock(2)` on a sentinel, guarding a whole-document read-modify-write of one JSON file, so the
lock granularity is *the entire project's memory*. That is a single-host dead-end **regardless
of granularity** — distribution is a *different* adapter (bbolt/SQLite, where the
backend-neutral interface makes it a drop-in and the flock-file stays a valid local adapter),
not finer file-locking. The latent correctness bug is **TOCTOU**: BSD locks don't compose
across calls, so the background "dream" consolidation's `List → decide → Forget/Remember`
sequence has a time-of-check-to-time-of-use window today. A transactional backend fixes it —
but *only* if the interface grows the `Update(ctx, fn)` affordance above. That is the one
change here that touches the interface, not just the adapter (and one of the two domain-logic
touch points in *Where to focus* #3).

### 6. Resource-lifetime management — *no seam yet (the one to build)*

**Where:** nowhere, today — and that absence is the point. The lifecycles are managed *by
hand*, scattered: the process-lifetime global MCP manager owned by `Build` (with the standing
warning *"never fold its `Close` into per-session cleanup"* — kill it and every other session
loses MCP), the process-wide preserved-fork LRU, the memory flock pair, run-scoped background
children drained by `drainChildren`. Each is correct in isolation; collectively they are the
same problem solved five times, and the pile of hand-written lifecycle warnings in `CLAUDE.md`
*is* the symptom of the missing abstraction.

**Purpose.** A first-class model for **resources whose lifecycle does not nest inside a single
tool call** — the workspace filesystem (#2), a fork/overlay (#3), the execution environment and
its best-effort cache (#4), a long-lived process or tunnel (#4), a checked-out credential
lease, an MCP connection. These are *more state the session must track*, and today that
tracking is implicit. The reason to promote it: the cloud-native thesis (near-stateless,
cheaply restartable) is only true if every outlives-a-call resource is **named, leased, and
either reconstructible or persisted** — so a restarted harness process can **re-attach** to
what it held. A resource that cannot be re-attached is a liability; this subsystem's job is to
make that liability *visible and disciplined* rather than silent.

**The model (sketch, not a design).** A resource is first-class with:
- an **owner** and a **scope** in the nesting hierarchy `call ⊂ run ⊂ session ⊂ team ⊂
  process` — it is reclaimed when its scope ends (the global MCP manager is `process`-scoped,
  a background child is `run`-scoped; getting *these two confused* is the exact bug the
  `CLAUDE.md` warnings guard against);
- a **lease** (renew / expire) so a crashed owner does not leak the resource forever;
- a **reclaim / finalizer**;
- a **name**, so resources are poolable and reusable (a warm execution environment shared
  across calls; a cache keyed by env-hash + lockfiles).

**Two properties this unlocks.** (1) **It unifies the axes** raised across #2–#4: the FS, the
environment, a process, and a credential are all instances of *one* abstraction — and a
resource handle *is* an object-capability (2.7 / the #3 auth-axis handle-passing), so
sharing or descoping a resource is passing an attenuated handle. (2) **It is the session-state
discipline** the rest of the doc keeps deferring to: what a session *holds* (its leases)
becomes an explicit, persisted part of session state (#1), not ambient process memory — which
is also where the audit story lives ("what resources did this session hold, and for how long").

**Where to explore.** Don't design it yet — track it. Prior art to steal from when the time
comes: **Kubernetes owner-references + finalizers + `Lease` objects** (the battle-tested
declarative version of scope-bound reclaim), **Chubby / ZooKeeper ephemeral-node leases** (the
distributed-lease case, with the crashed-owner expiry semantics), and **structured-concurrency
scopes** (the in-process case, where the scope hierarchy maps onto the `context` tree). The
near-term, low-cost first step is purely *descriptive*: enumerate every resource the harness
currently allocates, tag each with its de-facto scope, and confirm the hand-written lifecycle
matches — that inventory alone will tell us whether the abstraction earns a seam.

---

## Tier B — already-clean ports (remote adapter only)

These are narrow seams that don't need redesign. To distribute, wrap each in an RPC
client/server adapter; the interface stays as-is. *The driver-seams arc proved out exactly
this move* — it RPC-wrapped the Tier-A `SessionStore`/`MemoryStore` and the skill/soul/agent/
command source ports behind one uniform gRPC driver protocol with conformance suites
(DRIVERS.md). None of the Tier-B seams below have a driver yet (no consumer has asked), but the
pattern they were promised to fit is now load-bearing in the repo.

| Subsystem | Where | Core method(s) | Note |
|---|---|---|---|
| **Model provider** | `engine/port/llm.go` + `internal/adapter/{openai,anthropic,openrouter}`, `engine/adapter/mockllm` | `Stream(ctx, LLMRequest) (iter.Seq2[Chunk,error], error)`; `Capabilities() ProviderCapabilities` | **Already stateless full-replay — the disaggregation poster child.** `LLMRequest` is provider-neutral and guarded against widening. |
| **Permission policy** | `engine/port/permission.go` + `engine/adapter/permpolicy` | `Evaluate(ctx, sessionID, mode, call, ws) Decision`; `Learn(sessionID, call)` | Session-aware wrapper over the session-free `governance.Evaluator`. |
| **Learned-rule store** | `engine/port/permission.go` + `engine/adapter/permstore` | `Record(sessionID, rule)`; `Rules(sessionID) []Rule` | The small mutable seam for "allow always" verdicts. |
| **Lifecycle hooks** | `engine/port/hookrunner.go` + `internal/adapter/hookexec` | `Run(ctx, HookEvent) (HookOutcome, error)` | Exit code 0 = allow, 2 = block. Already a clean subprocess boundary. |
| **Event stream** | `engine/port/log.go` + server relays | `Emit(ctx, session.Event)` | **The structured-event spine — cloud-native observability is already here.** ctx is a trace carrier only, never a cancellation source. |
| **Tool-call audit** | `engine/port/log.go` + `internal/adapter/store/jsonlstore` | `ToolCall(id, call, result, queued, took)` | Distinct from diagnostics; carries dispatch-queue + execution timings. |
| **Diagnostics** | `engine/port/diagnostics.go` + `internal/adapter/slogdiag` | structured logger (`With`, level methods) | The only slog bridge; composition picks the sink. |
| **MCP federation** | `internal/adapter/mcp.Manager` | `Tools()`, `ListResources/ReadResource`, `ListPrompts/GetPrompt`, `Close()` | Already streaming-HTTP-transport only; no stdio, by design. |
| **Clock** | `engine/port/clock.go` + `engine/adapter/wallclock` (the injected production clock) | `Now() time.Time` | Abstracted for deterministic tests; trivially network-irrelevant. |

---

## Where to focus the exploration

This is the **priority synthesis** — of everything above, the three things that gate the
vision, in order. The reasoning lives in the numbered notes; this section adds only the
ranking and one piece of net-new advice (the spike in #1).

1. **The shared-handle problem — the architectural crux (2.1, 2.7, 3.3, 4.1; the first
   concrete instance of #6).** The filesystem, the forker, and the command runner are
   independently hostable only if a workspace "root" is a *handle* all three resolve to the
   same bytes; everything else is plumbing. Get the FS handle right and it becomes the template
   for the environment handle, the process handle, and the credential lease (#6). **Net-new —
   a minimal de-risking spike:** a memfs-backed *remote* `CommandRunner` + a non-git forker,
   all sharing one FS handle. It forces both the shared-handle question and the
   isolation-posture question into the open with little code. *(Now partly articulated: the
   driver arc shipped every other store/source seam and left the workspace/FS driver as an
   explicit proto **sketch** — `WorkspaceService` in DRIVERS.md — precisely because the
   shared-handle / real-exec problem makes it the hard one. That sketch is the natural home for
   this spike.)*

2. **Mid-turn checkpoint/resume (1.4).** Durable state is already correct; the gap is
   *liveness* — evicting an awaiting-approval run and rehydrating it on resume. This is what
   makes the process *genuinely* disposable rather than "disposable except while a human is
   mid-approval."

3. **The two domain-logic touch points that block clean adapter swaps (3.3, 5.1).**
   `governance.IsolationApprovable` (assumes git-worktree isolation) and the memory
   `Update(ctx, fn)` transactional affordance — the only two places where "add an adapter" is
   not sufficient, because they touch core logic, not just wiring.

Everything in Tier B is a "wrap in RPC when needed" exercise and gates nothing.
