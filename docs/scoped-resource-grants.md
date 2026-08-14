# Scoped resource grants: a filesystem and tool substrate

*Status: strawman / working draft. Speculative scoping, not a design record under
[ADR 0002](adr/0002-documentation-lifecycle.md) (no frozen decision here). Same
tier as [`docs/cloud-native-harness-kit.md`](cloud-native-harness-kit.md). It
picks up the shared-handle problem (§1) from the companion
["Cloud-Native Harness Systems"](cloud-native-harness-systems.md) scoping doc.
If this direction is ever committed, it becomes one or more ADRs and this doc
gets superseded.*

This doc describes a possible future for how mecatl's tools talk to filesystems
and to each other. It is written to be concrete enough to argue with. Nothing
here is implemented, and several existing decisions would be reversed if we
pursue it (that list is explicit, below).

The inspiration is loose but real: Plan 9's per-process namespaces and
everything-is-a-file-server model, tempered by the caveat that Plan 9's
namespaces were a mechanism for cooperative scoping, not a security boundary
against an adversarial process. We borrow the composition idea and supply our
own enforcement.

## The idea in one page

In most languages you can pass an interface as a parameter. Even C passes
function pointers. Agent harnesses today mostly can't do the equivalent across
process boundaries: a tool either gets ambient access to a whole filesystem
(bash in a sandbox) or gets bytes shoveled through the harness and often through
the model's context window.

The proposal is two layers:

1. **A generic grant mechanism.** The harness constructs a scoped reference to
   a service and passes it to another service as data. A grant is a concrete
   tuple: service address, protocol schema, scope claims, and a token. No
   kernel handles, no broker doing handle-to-address mapping. The token is
   short-lived, renewable, bound to the callee's workload identity, and
   attenuable (a holder can obtain a narrower grant, never a wider one).
2. **A filesystem service as the first instance.** The harness composes narrow
   filesystem views (a read-only input directory, a read-write output
   directory) out of one or more backends and grants them to tools. The data
   path runs tool-to-filesystem-service directly. Bytes never transit the
   harness and never transit the model.

The long-term goal is to move away from open-ended sandbox/bash environments
and toward special-purpose, scoped tools. In some ways this is reinventing the
shell pipeline to be cloud-native: instead of "everything shares one mutable
directory tree and an environment," every step gets exactly the views it needs,
for exactly as long as it needs them.

At the same time, this fundamentally opens mecatl up to work over the network,
so the harness, potentially, becomes a distributed system that works well with
a cloud/k8s environment. Sandboxing technologies can still be used as a
security boundary where necessary, without needing long-lived processes or
co-locating too much into the same sandbox.

In the immediate term this is aimed at remote tools, where today there is no
good story at all. A remote MCP server that needs a PDF today gets it base64'd
through a tool result: the bytes cross the harness *and* the model's context.
Against that baseline, a scoped grant with a direct data path is a categorical
improvement, and latency is not the bar to clear. Local/stdio tools can keep
using the host filesystem as a shared substrate; the grant machinery engages
only where it pays for itself.

We would rather show than tell with respect to MCP. The MCP community's
center of gravity is simplicity; pushing a capability model through that
process now seems unlikely to move fast. Build it gRPC-native, demonstrate it
inside mecatl deployments, and let the ideas flow into MCP later, or become a
successor. The history here is on the side of demonstration: virtiofs, LISAFS,
and directfs each displaced a simpler incumbent protocol by working, not by
persuasion.

## The motivating scenario

An agent needs to download several PDFs and extract their text.

1. The agent calls a **fetch tool** several times. Each call includes a grant
   to a **byte-sink service**: the moral equivalent of an `io.Writer`. The
   fetch tool streams each PDF directly into the sink. The bytes never pass
   through the harness or the model.
2. The harness materializes those sinks as files in a directory it controls
   (the agent chose the layout).
3. The agent calls a **PDF decoder tool**. The harness composes a two-entry
   namespace for the call: `/input` (the downloaded PDFs, read-only, immutable
   snapshot) and `/output` (empty, read-write, exclusive to this callee). The
   decoder reads inputs and writes extracted text directly against the
   filesystem service.
4. The decoder closes its grant when done (releasing its write lease); the
   harness declines further renewal on the fetch grants it already retired.
   The agent reads the results through its own workspace view.

(A further scenario, at some point, might layer on multi-tool scripting. Right
now there is a split in the universe where it is easy for agents to script CLI
tools but very difficult to script MCP/remote tools. There are hard tradeoffs
and some features are mutually exclusive. We would look to build the best of
both worlds.)

A sketch of the grant the decoder receives (shapes illustrative, not a wire
format):

```yaml
grant:
  service: "fs.mecatl.internal:7443"        # where to connect
  protocol: "mecatl.grant.fs.v1"            # which proto it speaks
  audience: "spiffe://mecatl/tool/pdf-decoder"
  scope:                                    # opaque to the generic layer
    mounts:
      - path: "/input"
        verbs: [read, stat, list, glob, grep]
        posture: immutable-snapshot
      - path: "/output"
        verbs: [read, stat, list, write, mkdir, remove]
        posture: exclusive-write
  lease:
    usage_ttl: 60s        # requests fail after this without renewal
    renew_ttl: 600s       # renewal itself is refused after this
  delegation:
    allowed: false        # a leaf grant: cannot be re-delegated
  token: "<signed, audience-bound>"
```

Three properties to notice. The `scope` block is meaningful only to the
filesystem service; the generic layer treats it as opaque claims. The
`audience` binding means possession of the token is not enough: the caller
must prove it holds the named SPIFFE identity — a bit different from how
`audience` is used in JWT grants, and perhaps not the right term for it
long-term; whether proof-of-possession should be mandatory or optional here
is still open. And the lease is the whole revocation story for the common
case, described next.

## Design principles

- **One regime.** Grants are always concrete tuples, even when caller and
  callee share a host. No fd-passing fast path, no handle table, no
  handle-to-address broker. We give up "the reference is unforgeable by
  construction" and take on token engineering instead, deliberately.
- **The data path does not need to transit the harness.** The harness is a
  control plane: it composes views, mints and renews grants, and audits.
  Bytes flow between the granted service and the grantee.
- **Capability times identity.** A grant is capability-shaped (it names what
  you may do) and identity-bound (only the named workload can exercise it).
  This is a deliberate hybrid, not object-capabilities in the strict sense.
- **Enforce per operation, never per mode label.** "Read-only" is a summary,
  not an enforcement point. Every verb is checked against the grant. A 2026
  wasmtime CVE (a read-only preopen bypassed via `TRUNCATE` on open) is the
  freshest instance of a decades-old bug class; the conformance suite must
  probe every verb against every posture.
- **Verbs beyond POSIX.** The substrate is not a re-spelling of the POSIX
  file API. Higher-order, LLM-friendly verbs (glob, grep, anchored edits) are
  first-class and run server-side, where the data lives.
- **Own the metadata layer.** Provenance and information-security tags live in
  our directory layer, not in backend xattrs. No existing xattr or
  alternate-data-stream mechanism survives a backend hop; anything we need to
  survive a read-transform-write cycle has to be ours.
- **Single deployment first, federation-shaped from day one.** V1 is one trust
  domain, one issuer. The tuple shape and verification interface must make
  that the degenerate case of a federated design, not a different design.
- **No unfederatable center.** When this opens up, there must be no single
  resolution or key service that cannot be self-hosted or replaced. Upspin,
  the closest Plan 9-lineage ancestor of this design, died in May 2025 when
  its one central keyserver was turned off.

## The grant mechanism (generic layer)

### The envelope

The generic layer owns: the (address, protocol, audience, scope-claims, token,
lease) tuple; token verification; the lease lifecycle; attenuation; and the
resolver that turns a grant into a typed client. It does not know what a path
is. Scope claims are an opaque, service-interpreted payload. This is the OAuth
shape (generic token, resource-specific scopes) and the Fuchsia shape (generic
capability routing; a directory is just one capability type).

### The lease lifecycle: renew or die

Tokens carry two TTLs, both short:

- **Usage TTL** (order of a minute): requests presented after this fail.
- **Renew TTL** (order of ten minutes): the window within which the holder can
  refresh an expired-or-expiring token from the issuer. After this, the grant
  is dead.

The client renews continually while it works and **explicitly closes** the
grant when done. This inverts the revocation problem. Revocation, in the
common case, is the issuer declining the next renewal: the issuer sits on the
renewal path (control plane, one endpoint, infrequent) instead of the
validation path (data plane, every operation). The staleness window shrinks to
the usage TTL. Services verify tokens offline; nobody calls home per request.

The precedent is boring in the best way: OAuth access/refresh pairs, Kerberos
renewable tickets, Chubby sessions, Kubernetes leases. It also unifies with
write leases (below): renewing the token *is* the write-lock keepalive, and
close is both the lock release and a clean audit event. Renew, close, and
attenuate/delegate (next section) are the entire holder-facing control plane;
the data plane never sees any of them.

Push-based revocation lists (each service keeps its own; the issuer pushes a
revocation to the token's single audience) remain available as an escalation
for the emergency case, and audience binding makes them tractable: every token
has exactly one recipient, so the issuer always knows where to push. They are
deliberately **not** in v1. Short TTLs are the industry's converged answer
(Kerberos, JWT-SVID, and the current WIMSE drafts all state it outright), and
the renewal loop covers what TTLs alone don't.

One real operational parameter: sub-minute usage TTLs make verifier clock skew
matter. It needs a stated tolerance, not an assumption.

A related worry is session resumption: a user pauses a conversation for a
while, or schedules a task, well past any short lease's renew window. In the
common case this is benign — the TTL clock only needs to run while a tool
call is actually in flight, and it should not need to be held open across a
human-in-the-loop confirmation pause, so a resumed session simply re-mints
whatever grants it needs. The real risk window is a tool process that errors
out or is killed mid-call: the lease is left to expire on its own rather than
needing active cleanup, which is the renew-or-die model working as intended,
not a special case. Whether the renew TTL's order-of-ten-minutes figure is
right is still a guess.

### Attenuation, delegation, and audience

The audience is a workload identity (SPIFFE ID in our deployments).
Exercising a grant requires proof of possession of that identity's key,
DPoP-style, so a leaked token is not a usable token.

Attenuation is a control-plane method, not client-side token math. A holder
calls the issuer with its grant and gets back a narrower one: a subset of the
scope, a shorter lease, and optionally a **different audience**, so a tool can
delegate a slice of what it holds to another workload. Narrow-only is
enforced at the method; wider requests fail.

Making this a method buys three things. The issuer gets a policy point: it
can deny a delegation outright (this mount is non-delegable; that audience is
outside the deployment; the requested slice crosses a classification boundary
in the metadata layer). Every delegation is audited, so the authority tree is
observable instead of invisible (client-side macaroon attenuation leaves the
issuer unable to see delegation topology; Fly.io ended up centralizing
verification partly for this reason). And the issuer holds the delegation
tree as state, so declining a parent's renewal cascades to its children;
the "revoking a token must also revoke everything derived from it" obligation
becomes bookkeeping instead of a distributed-systems problem.

Proof-of-possession also forces this shape rather than merely favoring it. A
holder cannot mint a token that a different identity can use: the new
audience must prove possession of its own key, and only something with
issuing authority can re-bind. Offline attenuation and cross-identity
delegation are incompatible once tokens are identity-bound. The precedents
are OAuth token exchange
([RFC 8693](https://datatracker.ietf.org/doc/html/rfc8693): trade a token
for a narrower one, optionally for another actor) and Kerberos constrained
delegation (a service asks the KDC to delegate to a named service, under KDC
policy).

Grants carry a delegable bit and a maximum delegation depth in the envelope,
so the harness can mint leaf grants that cannot spawn others.

### Token format: deferred, with named requirements

Creating a new token format is not something to take lightly, so this doc
doesn't. It names the requirements:

- offline-verifiable (no issuer call on the data path),
- audience-bound with proof-of-possession,
- carrying opaque scope claims and the dual-TTL lease.

Attenuation is deliberately absent from that list. It is a control-plane
method (above), so the token needs no client-side derivation machinery, and
that removes the strongest reason to adopt
[Biscuit](https://www.biscuitsec.org/): its offline attenuation blocks solve
a problem this design routes through the issuer on purpose. The leading
candidate is therefore the boring one, a plain signed proto with an issuer
field, which is how HDFS block tokens have run at scale since 2010. Biscuit
stays on the shelf for a federated future where offline attenuation across
trust domains earns its keep. Nobody in the SPIFFE world has yet fused
capability-style grants with workload identity (the WIMSE drafts keep them
separate on purpose), so that fusion remains one of the genuinely new pieces
here, and it is small. What v1 must not do is anything that makes
single-issuer a special case rather than the degenerate case.

### Keeping gRPC out of the core

mecatl's layering rule is machine-enforced three ways: the engine's domain and
loop import no gRPC, ever. The domain-level representation of a grant is a
neutral value object (a `ResourceGrant` in `engine/tool`, next to `Workspace`
for the same cycle-avoidance reason), plus a resolver port that turns a grant
into a typed client. The gRPC specifics live in an adapter, wired in
composition. This is the existing `internal/adapter/grpcdriver` pattern one
level deeper: today composition resolves config-time endpoints into port
implementations once per process; this adds a runtime resolver that does it
per grant. The modulith's composition gets more complicated. That is the tax
for keeping the layering rule intact, and it is the same tax the driver seams
already paid.

### The rule of two

The generic layer is designed against exactly two concrete instances: the
byte-sink and the filesystem view. A database grant (scope claims naming
tables and verbs, pointing at a service speaking a query proto) is used as a
thought experiment to check the shape isn't filesystem-shaped in disguise, and
is otherwise not specced. No registry of resource types, no third instance,
until a real consumer demands one. Designs like this die of speculative
generality; this one is not allowed to.

## The filesystem service (first instance)

### Scope claims

A filesystem grant's scope is a set of mounts: path prefix, verb set, and a
concurrency posture. Verbs are enumerated individually (read, stat, list,
write, mkdir, remove, rename, ...) because per-operation enforcement demands
it. There is no "mode"; there are verbs.

The verb set is deliberately not POSIX. Byte-level primitives are the floor,
because demanding programmatic consumers (a compiler, a PDF processor) need
them. On top of them sit higher-order verbs that run where the data lives:
glob, grep/search (the caller gets matches back, not the tree they were found
in), and content-anchored conditional edits (the shape of mecatl's Edit tool:
match this exact string, replace it, fail on ambiguity). The interface is
called by tools and by agents both, and the higher-order verbs are the
LLM-friendly ones: content anchors, line ranges, and names, never byte
offsets. A grep pushed to the service is one request and a short reply; the
same grep implemented by the caller is a full tree transfer. mecatl's
in-process `Workspace` already puts Glob and Grep on the server side of the
seam for exactly this reason; the remote protocol keeps that and leaves room
for more. Batched reads belong in the same tier: several files, or several
ranges, in one request. gVisor built LISAFS because per-file round trips were
the bottleneck; this protocol should not re-learn that.

Higher-order verbs are still verbs: enumerated in scope claims, individually
enforced, and subject to the metadata layer. A grep must not surface matches
from files the mount's composition excluded or its tags forbid.

This also fixes an existing capability gap: mecatl's shell-less posture
(`--no-bash`) currently has no way to rename or delete a file at all.
Directory manipulation becomes first-class verbs on the service rather than a
side effect of having a shell.

### Concurrency postures and caching

The harness composes the namespace, so it knows at mint time who else can see
each mount. That knowledge is stamped into the grant as a posture, and the
posture is what unlocks caching:

- **`immutable-snapshot`**: the view cannot change for the life of the grant.
  Cache everything, forever, with no validation traffic. Content-addressed
  backends make this free.
- **`exclusive-write`**: the holder is the only writer of this mount.
  Write-back caching is safe and encouraged; the holder flushes at an
  explicit **commit/sync** verb. The write lease is not negotiated
  dynamically by the server watching access patterns (the NFSv4 delegation
  model, which operators routinely disable because recall is scary); it is
  issued up front by the party that owns the mount table, and recall is just
  lease non-renewal, a path that already has to work.

  The read contract for everyone else is **read-committed**, and write-back
  caching forces that rather than merely suggesting it: until the holder
  flushes, the freshest bytes live in the holder's cache, so an observer can
  only ever be served the state as of the last commit. This makes the
  observed-handoff case work: a parent grants a tree to a sub-agent
  `exclusive-write`, keeps a read grant for itself, and watches progress at
  commit granularity, with full state at close. Commit is the publication
  point, not just a flush.
- **`shared`**: multiple readers/writers are possible but expected to be rare.
  Per-operation semantics, conditional writes (etag/compare-and-swap), and
  honestly higher latency.

The PDF scenario uses the first two. Most flows will.

### Commit and close

`exclusive-write` mounts get an explicit commit verb: flush write-back state,
optionally as an atomic multi-file commit, and produce an audit event. Closing
the grant implies a final commit. This gives the substrate the atomic-commit
and audit hooks the companion scoping doc asked for, at a natural point in the
lifecycle rather than as a bolt-on.

The commit token also becomes the externalized form of mecatl's Edit
read-ledger: read returns a content token, conditional write requires it. The
optimistic-concurrency check the osfs adapter currently keeps as private
per-session state becomes an explicit, stateless part of the interface, which
is what lets the filesystem service be distributed at all. The *model* still
experiences "read, then edit"; the harness carries the tokens.

This mode should make it possible for a client to mount this into some sort
of traditional environment via FUSE or a sandbox's host-interface mechanisms.
It would essentially be write caching, since there is no assumption that
changes will be reflected to other clients immediately. We are explicitly
*not* reinventing NFS here.

## The byte-sink service (second instance)

The fetch tool's grant is not a filesystem. It is a write-only stream with a
declared destination the *harness* chose: scope claims are roughly
`{stream_id, max_bytes, content_type_allowlist}`, and the service's proto is a
chunked append plus a finalize. It exists in v1 for a structural reason: it is
the second, non-filesystem consumer that keeps the generic layer honest. (It
could be collapsed into "an FS grant scoped to one file," and then the generic
envelope would never have been tested against a second shape. Keeping it
distinct is deliberate.)

It also carries the provenance story: the sink knows the bytes came from
`https://example.com/foo.pdf` via the fetch tool at a given time, and stamps
that into the metadata layer at finalize. Files born from the network arrive
marked (see below).

## The namespace

The mount table lives in composition, where the workspace factory lives today.
A session's view, a child's view, and a tool-call's view are all compositions
over the same backends: a git-backed project tree, a scratch area, a read-only
reference tree, remote governed stores later. Skill assets deliberately do not
ride this namespace: ADR 0108 keeps them logical and on-demand through `Skill`.
The mount table is therefore a general namespace capability, not a skill-assets
replacement.

Prefix routing is the whole v1 composition model: the mount table maps path
prefixes to backends, and two mounts never claim the same path. Same-path
union or overlay layering (precedence orders, whiteouts, copy-up) is
deliberately not built in. It is a large implementation surface of debatable
value, and Plan 9 itself kept union directories shallow (top-level merge
only, no whiteouts) and treated the restraint as a feature. If a consumer
turns up whose need is genuinely overlay-shaped, the answer is an
overlayfs-style *backend* behind a single mount, not protocol-level layering
semantics.

Two extension families ride on the substrate. They are different ideas and
should not be conflated, even if they share plumbing:

### Metadata on real bytes

Files carry structured, non-POSIX metadata owned by the directory layer:
provenance (a Mark-of-the-Web equivalent: origin URL, fetching tool, time) and
information-security tags (classification pointers, never the sensitive
payload itself, per the S3-object-tagging discipline). Because the layer is
ours, the tags survive backend hops and read-transform-write cycles by
default, which no inherited mechanism provides: NTFS zone identifiers die at
the first non-NTFS boundary, xattrs die at backend hops, and content-embedded
labels (the Microsoft Purview approach, the strongest survivor found) only
work for container formats. Tag semantics need one early decision the macOS
quarantine flag gets right by being explicit: is a tag about *this instance*
of the bytes or about the *lineage* of the content? Provenance wants lineage;
some infosec tags want instance.

Enforcement can then hang off metadata: a mount composed for an
externally-facing tool can exclude files tagged above a classification, or a
guardrail can gate on provenance ("this config was written by a file fetched
from the internet this session").

The likely mechanism is taint propagation through tools: if a tool reads file
A carrying taint T and writes file B, B inherits T. A tool that is explicitly
taint-aware could carry extra verbs to remove a taint, but that should be
rare — removing a taint is an action worth tracing back to a human rather
than something a tool does unremarked.

### Synthetic namespaces

Anything the harness knows can be presented as files, without a traditional
filesystem behind it:

- A `/proc`-style live view of subagent status: roster, stop states, usage
  counters, presented as small readable files. Today this information rides
  tool-result text and events; as a mount it becomes greppable and composable.
  No shipped precedent for this exists (checked, absent, as of July 2026); it
  is genuinely new.
- A content-addressed store for skills, packages, and binaries, Nix-flavored:
  immutable, hash-named, served as an `immutable-snapshot` mount. OSTree's
  hardlink-checkout trick (present a content-addressed store as a plain
  directory tree) is the mature local materialization when a real process
  needs real files.

One honest caution from practitioners: synthetic filesystems over slow
backends turn many small reads into synchronous RPCs and hit a latency wall.
Synthetic mounts should serve small, harness-local state, or immutable content
that caches, and not proxy a slow remote API call per `stat`.

## The protocol

gRPC-native, under the same conventions as the existing driver protocol
([ADR 0005](adr/0005-driver-seams.md)): server-streamed chunked reads, capped
and paged list/glob/grep, `NOT_FOUND` mapped to sentinels, server-side caps,
non-local cleartext refused. This supersedes ADR 0005's deliberately-parked
`WorkspaceService` sketch, and its trigger condition ("a real consumer that
separates the harness process from where the code lives") is exactly what this
doc proposes to build. The two blockers the sketch recorded are answered
directly: streaming/paging is taken on as the cost of the feature, and the
Edit read-ledger objection dissolves once the ledger is an explicit content
token at the interface instead of harness-side state.

The deliverable, when this opens up, is the protocol plus exported conformance
suites, not a server. That is already this repo's discipline ("the conformance
suites are the contract"), and it is what an outside implementer or a public
service would build against. Bash-needs-real-exec remains true and remains out
of scope here: command execution against a granted view is the §3
execution-environment problem in the companion doc, and a remote runner would
be a *consumer* of these grants, not a feature of them.

## What this reverses in mecatl

An honest ledger. Each of these was a considered decision; a real design doc
for this direction would supersede them by new ADR, never by quiet edits.

- **`Root()` leaves `WorkspaceReader`** (`engine/tool/tool.go`). It conflates
  containment (a security property) with mount location (an environment
  concern). A filesystem doesn't know its mount point; the mount table does.
  The three current consumers (permission-config resolver, surfaced fork-root
  paths, the `CommandRunner` workdir) get what they need from composition.
- **The Edit read-ledger externalizes.** The current `FileVersion` +
  `RecordRead`/`RecordedVersion` + `CreateFile`/`ReplaceFile` protocol makes the
  token and conditional-write contract explicit. Ledger keying itself is I/O-free
  and lexical; a future remote environment supplies backend versions/CAS without
  changing the model's experience.
- **ADR 0005's workspace-driver deferral ends.** The sketch's trigger
  condition is met by this design; the 64 MiB unary rule gives way to
  streaming for this one service, as the sketch itself anticipated.
- **`governance.IsolationApprovable` becomes isolation-kind-aware**
  (`engine/governance/bash.go`). Its auto-approve/reject sets hardcode
  git-worktree sharing assumptions; a granted read-only view and a force-copy
  fork each need a different posture. This was already flagged as a
  domain-logic gap in the companion doc, and it lands here as a prerequisite.
- **`EnvironmentForker` is absorbed into grant composition.** Fork label becomes
  a descent spec: mounts with postures (an `immutable-snapshot` of the parent
  tree is a read-only fork; an `exclusive-write` overlay is a mutating one),
  a merge strategy, and a lease. The existing forkers become backends. The
  merge-back machinery ([ADR 0039](adr/0039-parallel-auto-merge.md),
  [ADR 0077](adr/0077-direct-write-subagent.md)) is not redesigned here; how
  merge strategies ride grants is an open question below.
- **Per-session engines gain a per-call dimension.** Today a workspace is
  fixed per session (and per fork). Grants are per call. The composition
  seams that assume one workspace per engine instance get revisited.

Not reversed: the layering rule (this design pays extra to keep it), the
provider-neutral `port.LLMRequest` discipline (untouched), the no-stdio-MCP
rule (untouched), and permissions deny-dominance (grants add a second,
complementary axis; they do not replace the permission fold).

## Security model

Stated plainly, because the Plan 9 lesson is that namespace composition is a
mechanism, not a boundary:

- **Trust.** The harness and the grant issuer are trusted (in v1 they are the
  same process or deployment). Granted services (the filesystem service, the
  byte-sink) are trusted to enforce. Grantee tools are semi-trusted: the
  whole point is that a compromised or confused tool can exercise only its
  grants. The model's tool calls are untrusted input throughout, unchanged.
- **Enforcement point.** The granted service, per operation, verifying the
  token offline and the caller's identity by proof-of-possession. Not the
  harness, which is not on the data path.
- **Confused deputy.** Largely closed by construction: a tool acts on explicit
  grants passed for this task, not on ambient authority, so a crafted input
  cannot redirect authority the tool wasn't handed. This is the classic
  capability argument and it is the security payoff of the whole design.
- **Residual risks, named.** A stolen token plus a stolen workload key within
  the usage TTL. Clock skew between issuer and verifier. A tool that copies
  governed bytes from a tagged mount into an untagged one (the metadata layer
  reduces this; a copy performed by the tool's own code launders anything, so
  egress-sensitive mounts should not be co-granted with unmonitored writable
  ones). Verb-coverage gaps of the wasmtime-truncate kind, which is why
  per-verb-per-posture probes are a named conformance obligation, not a test
  we hope someone writes.

## Prior art

*From a web research pass (July 2026, three parallel survey agents, per-claim
sources and confidence flags; claims below trace to fetched primary sources
unless flagged). Nobody was found to have built this combination. Every part
has real precedent, and the parts are converging.*

**The mechanism is proven, piecewise.**
[HDFS block access tokens](https://community.cloudera.com/t5/Community-Articles/The-Untold-Story-of-Block-Access-Token/ta-p/248625)
(2010) are the cleanest "control plane mints a scoped token, data plane goes
direct, verification is offline" precedent, run at scale for fifteen years,
for exactly one resource type.
[Fuchsia's component framework](https://fuchsia.dev/fuchsia-src/concepts/components/v2/capabilities/directory)
proves the composition half: typed directory/storage capabilities,
declaratively routed into a per-consumer namespace, resolved once, then used
directly. [gVisor's 9P-to-LISAFS-to-directfs arc](https://gvisor.dev/blog/2023/06/27/directfs/)
is the strongest recent validation of the direct-data-path thesis: an
RPC-mediated filesystem was abandoned twice in favor of direct access once a
safely scoped handle was mintable.
[Tahoe-LAFS](https://tahoe-lafs.org/trac/tahoe-lafs/wiki/Capabilities) proves
one-directional capability diminishment composing through a directory tree.
[Azure stored access policies](https://learn.microsoft.com/en-us/azure/storage/common/storage-sas-overview)
are the one clean revocable-grant-class mechanism found among the presigned-URL
family (AWS's answer is "rotate the signing credential," which revokes
everything).

**The token pieces exist.** [Biscuit](https://doc.biscuitsec.org/) (offline
verification, attenuation blocks, revocation IDs with distribution
deliberately unspecified). [DPoP (RFC 9449)](https://datatracker.ietf.org/doc/html/rfc9449)
for proof-of-possession binding. [SPIFFE](https://spiffe.io/) for workload
identity. The [WIMSE drafts](https://datatracker.ietf.org/doc/draft-ietf-wimse-arch/)
and [OAuth transaction tokens](https://datatracker.ietf.org/doc/draft-ietf-oauth-transaction-tokens/)
document the IETF's current consensus: short TTLs instead of revocation
infrastructure, identity kept separate from authorization context. Fusing
attenuation with workload identity, and push-per-audience revocation as a
named pattern, were both checked and found absent: small, real gaps.
[Fly.io's macaroon experience](https://fly.io/blog/macaroons-escalated-quickly/)
is the best operational account of running attenuation-based tokens in
production, including the pain (pure restrictive caveats express some roles
awkwardly; revocation was bespoke nonce lists).

**The agent world scopes at the sandbox, not below it.** E2B, Modal, Daytona,
GKE Agent Sandbox, Bedrock AgentCore, and
[Anthropic's sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime)
all isolate per-VM/container/session.
[MCP roots](https://modelcontextprotocol.io/specification/2025-06-18/client/roots)
are plain `file://` URIs: no read/write split, no TTL, no revocation, nothing
a remote server can use.
[Vercel's sandbox](https://vercel.com/blog/security-boundaries-in-agentic-architectures)
brokers credentials on network egress (the nearest shipping cousin) and even
states the narrow-tools principle, but never applies it to files.
["Lingering Authority"](https://arxiv.org/abs/2606.22504) (June 2026) names
the revoke-on-subgoal-closure problem and is weeks old with one citation;
[Tenuo](https://github.com/tenuo-ai/tenuo) and an A2A capability-authorization
draft are converging on task-scoped attenuable tokens for *tool calls*, not
for resource data planes. A `/proc`-style live subagent filesystem has no
shipped precedent found.

**Metadata survival is nobody's solved problem.** NTFS zone identifiers,
macOS quarantine xattrs, SELinux labels, and S3 object tags each break at a
backend or format boundary;
[Microsoft Purview's content-embedded labels](https://learn.microsoft.com/en-us/purview/sensitivity-labels)
survive best and only work for container formats. Owning the metadata layer is
forced, not chosen.

Net: this is assembling verified parts into a configuration nobody has
shipped. The two genuinely new pieces are small and identifiable: filesystem
grants with a direct data path applied to agent tools, and the subagent
status namespace.

## Deliberately out of scope

- **Command execution and environment provisioning.** The §3 problem in the
  companion doc (images, toolchains, `ProcessHost`). A remote runner consumes
  grants; it is not part of this design.
- **Cross-domain federation and public services.** The endgame, not v1. V1's
  only obligation to it is not painting the token and tuple shapes into a
  single-issuer corner.
- **MCP standardization.** Show, don't tell.
- **Multi-writer cache coherence.** `shared` posture gets conditional writes
  and honest latency, nothing fancier.
- **Push-based revocation lists.** Designed-for (audience binding keeps them
  tractable) but not built; renew-or-die covers v1.
- **A third resource type.** The rule of two, above.

## Open questions

- **Token format.** A bespoke signed proto versus Biscuit. With attenuation
  moved to the control plane, the Datalog machinery has no v1 consumer; the
  remaining deciding factors are proof-of-possession ergonomics with SPIFFE
  SVID keys and whether federation ever revives offline attenuation.
- **Where delegation is served.** The issuer owns renewal and holds the
  delegation tree, so it is the natural home for the attenuate/delegate
  method; a resource service narrowing grants locally would need signing
  authority. Is issuer-only good enough, or does a busy filesystem service
  want a local fast path?
- **Renewal cadence versus cache TTLs versus clock skew.** Three timers that
  interact; they need to be designed together, with stated tolerances.
- **Bash coexistence.** The long-term goal displaces open-ended shell, but
  Bash is load-bearing today and the migration is gradual. ADR 0108 rejected
  implicit skill-asset materialization: textual references stay logical, while
  workflows needing shell files must create or obtain them explicitly. Should a
  future granted filesystem view materialize locally for shell consumption, and
  how does `IsolationApprovable` classify commands against a grant posture?
- **Talking to OAuth'd remote services (MCP).** The MCP world is heavily
  OAuth-based; a grant token can't be presented to an MCP server as-is. The
  likely shape is a gateway that translates a grant (plus the human user's
  own credentials) into OAuth/bearer credentials for the far side —
  candidate machinery: [draft-ietf-oauth-spiffe-client-auth](https://datatracker.ietf.org/doc/draft-ietf-oauth-spiffe-client-auth/),
  which lets a SPIFFE SVID stand in for an OAuth client credential. That
  gateway would itself just be a tool from mecatl's point of view — likely a
  concrete application of the byte-sink instance rather than a third
  resource type (the rule of two, above). Where MCP's dynamically-typed
  tool-call shape (closer to COM's `IDispatch` than to a strongly-typed
  interface) meets this design's concrete, typed grants is exactly the
  boundary this doc doesn't resolve.
- **Merge strategies on forks-as-grants.** Does the descent spec carry a
  merge-back strategy (promote-on-success, hand-back-as-diff, discard), or
  does merging stay a separate seam consuming the fork's mount?
- **The v1 higher-order verb set.** Glob, grep, and batched reads are
  obvious; content-anchored edit is likely. Where to stop (structured tree
  summaries, an LSP-shaped symbol tier) is not obvious, and every verb added
  is protocol surface to enforce and conform forever.
- **Format-aware server-side queries.** Recorded for investigation, not
  decided: if the service knows a file's format (JSON, YAML, extensible to
  others), a query verb could evaluate a path/filter expression server-side
  ("the `.spec.replicas` of every `deployment.yaml` under `/manifests`"),
  collapsing what is otherwise a list-read-parse chain of round trips into
  one request. The precedent shape is S3 Select and jq. The costs are real: a
  query language is protocol surface forever, a hostile query is a
  denial-of-service vector that needs resource limits, and per-format parsers
  put content-shaped code on the enforcement path. Probably a post-v1 verb
  tier, gated on demonstrated need.
- **Scope-claim schema evolution.** Claims are opaque to the generic layer,
  so each service versions its own claim schema; the conformance suites need
  to pin how unknown claims fail (closed, loudly).
- **A git-shaped service.** Recorded, deliberately unanswered: what does a
  service that is explicitly git look like? VFS-shaped (mounts serving
  worktree views) with git-native verbs alongside (branch, diff, log, a real
  commit). The resonances are strong: `immutable-snapshot` is a pinned
  commit, `exclusive-write` with a lease is a branch checkout, the commit
  verb already smells like the git one, and merge-back strategies are
  branch-and-merge. Whether that convergence means the filesystem service is
  git underneath, or git is one backend with extended verbs, is exactly what
  this bullet refuses to decide today.
- **Key distribution for signing and verifying grants.** How do all the
  different identities actually get their keys? In the simplest shape there
  is one trust domain per deployment, the harness is the issuer, and
  subagents get SPIFFE sub-path identities — SPIFFE is the natural mechanism
  for distributing that key material. One simplification worth keeping: the
  signing key never has to leave the issuer if the same service that issues
  a grant also evaluates it, since then nothing else needs the key at all.
  The residual problem shrinks to key consistency across a horizontally
  replicated issuer, which is an easier problem than general key
  distribution.
- **Where the issuer lives.** In-process with the harness in v1, but the
  renewal endpoint is load-bearing for every live grant; its availability
  story needs a sentence more than "it's the harness." If it is the service
  itself that issues, does the harness itself delegate access to tools and
  view itself as a peer actor?

## The de-risking spike

The smallest thing that proves the shape, refining the spike the companion doc
already proposed:

1. A grant issuer (in-process with a mecatl composition root) minting
   dual-TTL, audience-bound grants, with renew/close/decline.
2. A memfs-backed filesystem service speaking the v1 proto: streamed reads,
   server-side glob/grep, per-verb enforcement, the three postures,
   conditional writes.
3. A byte-sink service, to keep the envelope honest.
4. One tool flow, end to end: the PDF scenario, with a stub decoder.
5. Conformance probes for every verb against every posture, including the
   truncate-shaped ones.

Success is the scenario running with bytes never transiting the harness, a
grant dying by non-renewal mid-run and the tool failing cleanly, and the
byte-sink working without any filesystem-shaped assumptions leaking into the
generic layer. Explicitly not in the spike: real tokens (a stub signer is
fine), the metadata layer, synthetic namespaces, and any mecatl interface
reversals. The spike is throwaway proof, not phase one.

## Relationship to other docs

- [**Cloud-Native Harness Systems**](cloud-native-harness-systems.md) — the
  scoping doc that names the shared-handle problem this doc takes up (§1), the
  execution-environment split this doc defers (§3), and the
  `IsolationApprovable` gap this doc absorbs (§4).
- [`docs/cloud-native-harness-kit.md`](cloud-native-harness-kit.md): the kit
  framing this substrate would slot into; the grant mechanism is exactly the
  kind of component the kit's reuse test is about.
- [ADR 0005 — driver seams](adr/0005-driver-seams.md): the protocol
  conventions this inherits and the `WorkspaceService` sketch this supersedes.
- [ADR 0027 — cloud-native arc](adr/0027-cloud-native.md) and
  [ADR 0048 — mecak8s](adr/0048-mecak8s.md): the disposable-process
  properties this extends from session state to the tool data plane.
- [ADR 0039](adr/0039-parallel-auto-merge.md) /
  [ADR 0077](adr/0077-direct-write-subagent.md): the fork/merge decisions the
  descent-spec question touches.
