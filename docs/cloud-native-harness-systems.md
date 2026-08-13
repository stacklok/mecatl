# Cloud-Native Harness Systems — future work scoping

*Status: speculative scoping only. Nothing in this doc is a committed direction, a
design decision, or implemented. It exists to name questions worth exploring later,
not to answer them. Not a design record under [ADR 0002](adr/0002-documentation-lifecycle.md)
(no frozen decision to supersede) — closer in kind to
[`docs/cloud-native-harness-kit.md`](cloud-native-harness-kit.md)'s strawman status,
one level more speculative still.*

## Already resolved — out of scope here

Two things this doc could have covered are deliberately left out because real work
already resolved them:

- **Session / event state.** [ADR 0027 — Cloud-native arc](adr/0027-cloud-native.md)
  (Accepted, Phases 0–5 shipped) closes the session/event-state gaps: a durable
  event log (`port.EventLog`), non-destructive compaction, verdict replay, mid-turn
  awaiting-approval evict/rehydrate, and cross-process single-writer leasing
  (`port.SessionLease`).
- **Resource-lifetime management.** ADR 0027 also directly answers "does
  resource-lifetime management earn a seam yet?" (not yet; a trip-wire condition is
  recorded for when to revisit) — so it isn't repeated here as an open question.
- **A concrete demonstration.** [ADR 0048 — mecak8s](adr/0048-mecak8s.md) (shipped
  MVP) is the k8s-native proof of these properties: a storage-free agent binary, a
  Redis-backed `port.SessionStore`/`port.EventLog`/`port.PrunableStore` adapter, and
  a kind-based e2e proof of multi-replica disposability.

Everything below is what's left — confirmed still unaddressed against current
`main` as of 2026-07-08. If a future audit finds one of these has since shipped,
delete the section rather than update it; this doc's only job is to name what's
still unscoped.

## 1. The shared-handle problem

The filesystem (`tool.FileSystem`/`Workspace`), the environment forker
(`tool.EnvironmentForker`), and the command runner (`tool.CommandRunner`) are
independently swappable interfaces already — each is a real port met by an adapter
only in composition. But all three quietly assume a workspace "root" is a *local
mountable path*, and the moment tool execution becomes a separate service that
assumption breaks: the filesystem, the forker, and the runner all need to resolve to
the *same bytes* without transiting them through the harness process.

This is confirmed still unresolved: the driver protocol
([ADR 0005 — driver seams](adr/0005-driver-seams.md)) shipped a gRPC driver for
every other stateful port (sessions, memory, skills, soul, agent defs, commands),
and deliberately left the workspace/FS driver as a **proto sketch only**
(`WorkspaceService` in `docs/adr/0005-driver-seams.md`) — because it breaks the
64 MiB unary rule (needs chunked/streamed reads, paged Glob/Grep), because Bash
needs real exec against real files (a remote FS can serve the read side only, or
must pair with a local materialization step), and because part of the `Workspace`
surface (the Edit read-ledger) is harness state, not storage, and shouldn't cross
with the rest.

Sub-questions worth scoping, roughly in order of how directly they gate the rest:

- **Drop `Root()`.** It conflates containment (paths confined, escapes rejected — a
  security property) with mount location (an environment concern). Only three
  consumers need "where am I" today (the permission-config resolver, surfaced
  fork-root paths, the `CommandRunner` workdir) — that's leakage, not a real
  requirement. The Unix-VFS answer: a filesystem doesn't know its mount point, the
  mount table does; move "which filesystem is mounted where" to the composition
  layer that owns both the filesystem(s) and the runner.
- **A composed namespace, not a singleton.** Today there is one `Workspace` per
  session. A mount table / composite filesystem (Unix VFS, overlayfs union mounts,
  Plan 9 namespaces, FUSE) would route path prefixes to backend filesystems — a
  git-backed project repo, a read-only reference tree, a writable scratch area, a
  synthetic FS over MCP resources or memory. The existing `osfs.WithReadRoots` skill
  carve-out is already an ad-hoc special case of exactly this.
- **The capability surface is past POSIX already, unevenly.** `Grep` is minimal
  (pattern + path only — no output modes, context lines, case control, type
  filters); there's no directory manipulation as first-class tools (mkdir / rename
  / delete), which matters specifically because mecatl's shell-less posture
  (`--no-bash`) has *no other way* to rename or delete a file. Beyond that: unified
  diff / multi-edit, multimodal/binary reads, and an LSP-backed tier (go-to-def,
  find-references, rename-symbol). Guiding principle: shape tool operations around
  what an LLM can reliably address — content anchors, line ranges, names/symbols —
  never byte offsets, for the same reason raw version tokens are brittle for models.
- **The Edit read-ledger should be explicit at the interface, implicit to the
  model.** The read-before-edit / unchanged-since check is optimistic concurrency
  control, currently internalized inside the osfs adapter (sha256 fingerprints; the
  `version` parameter in the interface is vestigial). It should become explicit at
  the `FileSystem` interface (read returns a token; write is conditional on it — an
  etag/If-Match/CAS shape) so the FS adapter is stateless and distributable, while
  the harness stores and supplies the tokens out of session state so the *model*
  still experiences "read, then edit" with no token in its mental model. Pair with
  content-match as a second, token-free edit method (Edit's `old_string` requirement
  is itself a stateless optimistic check) — offering the model multiple edit
  methods and letting the situation pick is likely better than one scheme.
- **The filesystem is an ecosystem substrate, not just an agent seam.** A generic
  bash environment and specialized non-LLM tools (PDF processors, compilers, an LSP
  server) want a POSIX-superset, byte-level, streaming substrate to mount directly
  — the LLM tool layer (Read/Edit/Grep) is just one narrowing consumer on top.
  Design the substrate for the most demanding consumer and let the LLM tools narrow
  it, not the reverse. This also makes multi-writer concurrency first-class (once a
  shell command and the LLM can write the same tree, the read-ledger is a real
  concurrency primitive, not an edge case) and opens watch/change-notification,
  snapshots, and atomic multi-file commit as substrate-level capabilities.
- **Existing governed stores as backends is the enterprise story.** The highest-value
  substrate adapters aren't new filesystems, they're existing ones wrapped —
  Google Drive, SharePoint/OneDrive, S3, Box, a DMS, NFS — so the agent inherits the
  corporate controls (legal holds, retention, DLP, residency, audit) those stores
  already enforce instead of mecatl reinventing or bypassing them. This needs a
  capability-negotiated substrate interface (a backend declares what it supports:
  random write, rename, watch, versioning, holds) and creates two hazards to design
  against up front: forks/scratch copies can break governance (a held or
  residency-bound file copied into an ungoverned `/scratch` is a real compliance
  violation, not hypothetical), and the agent's actions on governed content need to
  feed the enterprise audit trail.
- **Handing a descoped filesystem to another service.** Once the filesystem is a
  shared substrate, the load-bearing question is how the harness lets another
  service (a PDF processor, say) operate on a file or subtree directly — without
  transiting bytes through the harness, and never through the model. The right
  mental model is object-capability passing: the handle is the authority, attenuate
  it before handing it out (read-only, subtree-only, time-boxed), never pass "a
  path + ambient authority." Two regimes: same-host trusted-ish services can get a
  real attenuated fd (Unix `SCM_RIGHTS`, Capsicum-style rights-limiting, `os.Root`
  is already the local-containment piece of this); cross-host or non-POSIX backends
  need a signed, scoped, expiring lease (a gRPC/9P grant, or a presigned URL — S3/GCS
  signed URLs and Drive resumable-upload sessions are already exactly this pattern).
  Revocation is the honest hard part (leases expire; fds don't). This generalizes
  past the filesystem: the same attenuate-and-pass-the-handle idea is how the
  harness should delegate *any* scoped resource to a peer — a subagent's
  sub-namespace, a remote runner's working directory, a network-egress scope, a
  secret, a compute lease.

**Net-new de-risking spike, if this is ever picked up:** a memfs-backed *remote*
`CommandRunner` plus a non-git forker, sharing one FS handle. Small amount of code,
forces both the shared-handle question and the isolation-posture question (below)
into the open.

## 2. Forking as environment descent

`tool.EnvironmentForker.Fork(ctx, base Environment, label) (child Environment, cleanup, advisory, err)` — two
working implementations exist today (git-worktree default, force-copy for mutating
forks), proving the interface is genuinely swappable. Merge-back is **not**
hardwired to discard across the board any more: a diff-apply `tool.EnvironmentMerger`
(`internal/adapter/forker.Merger`, wrapped in a process-wide `SerializingMerger`)
promotes a preserved fork's changes into the parent, default-on when wired
([ADR 0039](adr/0039-parallel-auto-merge.md)) — but it is scoped narrowly, to
Parallel's single-branch fast path (`join=first`/`join=judge`, exactly one
branch), and it only ever does one strategy: apply the fork's `git diff HEAD` and
fail loud on conflict (no force, no three-way merge). Every other fork path still
discards: Team members and multi-branch Parallel fan-out stay read-only-by-design
specifically to dodge the merge-conflict problem, and the writable Subagent
(`mode:"read-write"`) deliberately bypasses forking altogether — it edits the
parent tree directly during the run, no fork and no merge involved
([ADR 0077](adr/0077-direct-write-subagent.md), which superseded an earlier
fork-and-merge design for it in [ADR 0040](adr/0040-writable-subagent-and-serialized-merge.md)).

- **A menu of sharing policies**, chosen per child, is the richer model: (a) a
  descoped read-only view of a subtree (bind-mount / 9P-attach style, no merge
  needed because the base is untouched by construction), (b) a copy-on-write fork
  with an explicit merge-back strategy — today that's exactly one variant
  (promote-on-success via diff-apply, one call site), with hand-back-as-diff-for-review
  and three-way merge still unimplemented, and still not generalized past
  Parallel's single-branch path — (c) a shared tree with no isolation at all. The
  remaining question is less "should mutating forks merge back" (they now do, for
  one path) and more "should every fork path get a choice of merge-back strategy,
  not just Parallel's winner" — which is still the harness taking on conflict
  resolution as a general capability, so it shouldn't be generalized lightly.
- **Read-only peers are a distinct primitive** from CoW-fork-then-merge: a shared
  tree where each child gets its own writable region and a read-only view of its
  peers' regions — a blackboard pattern trading isolation for live coordination
  (no rollback, real collisions, mitigated not eliminated by write-scoping). Plan
  9's `rfork` + union mounts is the cleanest prior art.
- **git-worktree is embedded in the threat model, not the data path** — swapping
  the isolation *mechanism* is proven (the forker interface holds), but
  `governance.IsolationApprovable` (`engine/governance/bash.go`) hardcodes a
  git-worktree assumption: it auto-approves `{go test, build, vet, list}` and
  rejects `git push/remote/fetch/config/worktree` (among other subcommands that,
  per its comment, "reach outside the throwaway checkout") specifically *because*
  a worktree shares the base `.git` object database and refs (spelled out in
  `internal/adapter/forker`'s package doc). Force-copy severs `.git`, so that posture is
  needlessly restrictive for it; a read-only-subtree view would need a third
  posture again. **Confirmed still unaddressed** — `IsolationApprovable` still
  takes a single `cmd string` with no isolation-kind parameter. Making this
  isolation-kind-aware is core domain logic, not wiring, and is one of the two
  concrete near-term items in this doc (see §4).
- **"Fork a workspace" should mean "fork the environment."** A forked workspace
  already varies on four independent axes the way a container does: filesystem
  view (today: CoW, with promote-on-success merge-back wired for exactly one path
  — Parallel's single-branch winner), tool catalog (today: harness-decided per
  role, not per-fork), credentials/auth (today: **absent** — MCP/tool auth and git
  credentials are ambient, no per-fork scoping), and budget/limits (today:
  genuinely mature — tighten-only `MaxTurns`/`MaxToolCalls`/`MaxRunTokens`/timeout).
  Auth is the one completely missing axis. The unifying move, if pursued, is
  making `Fork` take a descent spec instead of a `label` — FS sharing mode +
  merge strategy, catalog delta, credential scope, budget — chosen by the
  controlling agent per child, with the return outgrowing a bare `cleanup func()`
  into a handle exposing `Merge(strategy)`/`Discard()`.

## 3. Command/tool execution as a separate service

`tool.CommandRunner.Run(ctx, command, workdir) (CommandResult, error)`
(`engine/tool/tool.go`) is deliberately shell-shaped — the seam that makes shell
execution optional in a catalog at all. Splitting it out as a remote service is a
much bigger move than the filesystem/forker swap, worth scoping in pieces:

- **Split the data plane from the environment plane.** `Run(command, workdir)`
  collapses the governed, forkable workspace (data) with the OS image — installed
  tools, config, `PATH`, CLI credentials (environment). The container framing:
  workspace as a bind-mounted data-only volume, toolchain/config/credentials as an
  image living outside the data dir. Honest scope warning: everything behind `Run`
  grows from "shell out locally" into remote sandbox orchestration (the
  devcontainer/Codespaces/Coder/e2b/Fly-Machines space) — the `Run` seam itself
  should stay narrow; `CommandRunner` becomes a client of a new
  environment-provisioning subsystem.
- **Declarative immutable environments, as a spectrum, default low.** Content-
  addressed environments buy auditability ("what ran this, in what environment" =
  an image hash, not "whatever was on the box") and that description is itself a
  provenance fact worth persisting with the session. A realistic spectrum: floor =
  a digest-pinned OCI image + lockfile + SBOM (most of the benefit, no Nix
  required); middle = image + a declarative package delta from a curated catalog;
  ceiling = a full Nix/flake closure for when closure-level audit is warranted.
  Default to the floor.
- **Environment lifetime is three tiers, not two.** "Ephemeral env, durable state
  only in the mounted FS" is the right thesis but a two-tier model resets real
  toolchain caches (`~/.cache/go-build`, `node_modules`, LSP indexes) every call,
  making every build cold. Three tiers: (1) durable/governed data FS — correctness
  depends on this only; (2) a best-effort, evictable-at-any-moment cache keyed on
  env-hash + lockfiles — pure performance, never guaranteed or audited; (3) the
  immutable environment image — reconstructible from description, not "state" at
  all. The CI/CD runner (GitHub Actions and peers) is the closest mature prior art
  for this whole shape, vocabulary included.
- **Long-lived/interactive processes break the batch `Run` contract.** A dev
  server, watch-mode compiler, REPL, or `tail -f` needs start → handle →
  poll/stream/signal → terminate, not "terminates and you collect output." Don't
  widen `Run` to fit this — it's a sibling `ProcessHost` port, and mecatl already
  has a lifecycle shape one layer down to borrow from (background subagents:
  detach → poll → drain). Reaching a long-lived process from outside the sandbox
  (a preview URL) is a real security escalation (exposing an ingress puts the
  sandbox's network surface in the threat model) — out of scope for a
  knowledge-worker agent, but name it as a frontier with its own auth/threat model
  rather than designing it out.

## 4. Two small domain-logic gaps

Two places where "just add an adapter" isn't sufficient because the change touches
core logic, not wiring. Both confirmed still open against current `main`:

- **`governance.IsolationApprovable` is worktree-only** (§2 above) — it needs to
  become isolation-kind-aware before a non-git forker can carry the same
  auto-approve posture safely.
- **`tool.MemoryStore` has no transactional `Update(ctx, fn)` affordance.** The
  current `internal/adapter/memory` store uses a single-host BSD `flock` guarding a
  whole-document read-modify-write, which is a dead end for any distributed backend
  regardless of lock granularity — but the concrete latent bug is TOCTOU: the
  background "dream" consolidation's `List → decide → Forget/Remember` sequence has
  a time-of-check-to-time-of-use window today, and only a transactional interface
  affordance (not just a new backend) fixes it. The port has deliberately grown no
  method without a consumer so far, which is why this is still unbuilt rather than
  an oversight.

## Everything else is a narrow port already

Model provider, permission policy, hooks, the event log, tool-call audit,
diagnostics, MCP federation, and the clock are each already a clean, narrow
interface met by an adapter only in composition. Distributing any of them is "wrap
it in an RPC client/server," proven out by the driver-seams arc doing exactly that
for sessions, memory, and the skill/soul/agent/command sources
([ADR 0005](adr/0005-driver-seams.md)). None of them need scoping work; nothing to
track here.

## Where this leaves it

In priority order, if any of this is picked up:

1. **The shared-handle problem (§1)** is the architectural crux — it gates the
   filesystem, the forker, and the command runner all at once, and the workspace/FS
   driver sketch in [ADR 0005](adr/0005-driver-seams.md) is the natural place to
   spike it.
2. **The two domain-logic touch points (§4)** are small, concrete, and don't
   require the shared-handle problem to be solved first — either could be picked up
   independently.
3. **Forking-as-environment-descent and the execution-environment split (§2, §3)**
   are real but larger and more speculative; they're recorded here mainly so the
   shape (merge-back strategies, the container-model axes, the three-tier
   environment lifetime) doesn't have to be rediscovered later.

## Relationship to other docs

- [`docs/cloud-native-harness-kit.md`](cloud-native-harness-kit.md) — the kit
  definition this doc's open threads eventually feed, if pursued.
- [`docs/adr/0027-cloud-native.md`](adr/0027-cloud-native.md) — the shipped arc that
  resolved the session/event-state questions this doc doesn't cover; also where
  "does resource-lifetime management earn a seam" was directly answered.
- [`docs/adr/0048-mecak8s.md`](adr/0048-mecak8s.md) — the concrete k8s-native
  demonstration of the properties this whole line of thinking is in service of.
- [`docs/adr/0005-driver-seams.md`](adr/0005-driver-seams.md) — the driver protocol
  arc; its still-sketch-only `WorkspaceService` is where §1's spike would land.
