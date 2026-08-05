# Cloud-Native Harness Kit — definition

Status: **strawman / working draft.**

This is the *conceptual / positioning* definition of the cloud-native harness kit. It
sits **above** `adr/0027-cloud-native.md`, which is the mecatl-internal engineering arc (make
*this process* disposable). This doc asks the broader question: what is the **kit**, what
makes a harness **cloud-native**, and where the kit's responsibility ends and a downstream
consumer's begins.

## Why a kit?

We call this a **kit** because we want a set of core components that hold up across more
than one context. Today we are designing for a knowledge worker running in the downstream consumer, but the
same project already does coding tasks on the desktop. Components that compose give us a
starting point for harnesses we have not built yet, in contexts we have not targeted yet.

**The test is reuse.** As we move from interactive chat toward something long-running and
self-triggering, how much of what we have carries over? The more the core stays intact
across that shift, the more the kit is earning its name. And if we open source it, that
same property decides whether anyone builds on top: the more places it works while still
adding value, the more likely others are to pick it up.

A good test for a platform is whether people can use it to do things you didn't imagine
when you built it. We need to leave room for people to put the kit parts together in
unexpected ways.

## 1. Definition

An **agentic coding harness** is the system around a model that lets it finish a software
task: the streaming agent loop, the tool kit, the permission model, hooks, and delegation,
behind a provider-agnostic port (see [architecture](architecture.md)).

> The **cloud-native harness kit** is the reusable substance of such a harness — the
> importable engine, the port/driver contract, and the reference adapters — built so that:
> its **process is disposable**, its **capabilities are resolved per session from a
> service** (not baked into a local filesystem), a **single instance serves multiple
> tenants** in isolation, and the whole thing is **operable over a network** under
> orchestration. mecatl is the kit's reference implementation; a downstream product is one
> consumer. The kit provides the seams; *how* a consumer scopes capabilities to its own
> concepts is the consumer's policy, not the kit's.

## 2. What makes a harness "cloud-native" (the properties)

1. **Disposable process.** Kill any instance mid-session, start another over the same
   stores, lose nothing the user cares about: externalized state, evict/rehydrate (resume
   a session parked mid-turn in a *different* process), and a durable record (events /
   approvals / history persisted, not emitted-and-discarded). Spanning *multiple*
   instances additionally needs a single-writer handoff per session (a session lease), so
   two processes never write one session — the multi-instance half, still ahead of us.
2. **Stateless provider replay.** No server-side conversation state; resume is
   load-snapshot-and-replay. Nothing provider-side to externalize.
3. **Capabilities resolved per session from a service.** Skills, tools, and agent
   definitions cross a port and are resolved when a session opens — from a service, not
   discovered from disk. A skill crosses as identity, body, and named assets, **never as a
   path**, so it needs no filesystem; progressive disclosure is the same tool-call
   mechanism mainstream harnesses use, so models drive it natively. The service is the
   **source of record**; any on-disk materialization is an optional last-mile delivery
   detail, never the source of truth. The kit resolves whatever capabilities the consumer
   supplies; the rule deciding *which* capabilities a session gets is the consumer's
   policy (§3).
4. **Multi-tenant within one instance.** A single instance serves sessions for different
   principals concurrently, isolated from one another — per-session workspace containment,
   a secret-scrubbed shell environment, per-session trust and posture, untrusted-input
   fencing, and redacted cross-agent surfaces. Multi-tenancy is a property of the
   **harness**, *not* something a deployment bolts on.
5. **Operable over a network.** The harness exposes a network-drivable control surface (a
   streaming API) and the operability a runtime under orchestration needs: health /
   readiness, metrics, structured diagnostics, graceful drain. "Cloud-native" implies
   *operable*, not merely stateless-and-resumable.
6. **Everything behind a port.** The core is importable and provider/transport-agnostic;
   concrete adapters meet ports only in a composition layer. This is what keeps both the
   "build our own" and the "shim onto someone else's harness" paths viable at the same
   cost.

## 3. What is in the kit (and what is not)

The kit is the reusable harness substance; mecatl is its reference implementation; a
downstream product is one consumer.

**In the kit:**

- **The engine** — the loop, permissions, hooks, and delegation (subagents / teams),
  behind a provider-agnostic port. The importable core.
- **The tool interface and common tools** — the `Tool` contract and catalog over three
  sources (built-in, model-defined, and MCP), with a shared set of common tools. A tool
  executes in-process (a built-in tool may itself spawn a subagent) or out-of-process (an
  MCP server) behind the same interface, so a consumer composes tools without caring where
  they run. The kit inventories and presents the tools; *which* tools a session gets is
  the consumer's capability-scoping policy (§3).
- **The port / driver protocol** — the contract a remote driver implements for sessions,
  memory, skills, soul, agent definitions, and commands.
- **The reference adapters** — offline defaults so a consumer can run without standing up
  infrastructure.
- **The skills-as-a-service model** (§4) — progressive disclosure, no-FS delivery, served
  over the driver protocol.

**Not in the kit (a consumer's concern):**

- **Capability-scoping policy** — how a consumer decides which skills/tools a session
  gets. A consumer may bind a capability set to its own domain concepts (a tenant, a
  project, a product-specific "agent template"); that model lives in the consumer, not in
  the kit and not in mecatl's domain. The kit only resolves capabilities at session time
  (§2, property #3).
- **Agent-composition policy (nesting / inheritance)** — the kit builds an *agent
  environment* (mecatl's `agent.Deps` + the `Engine` assembled from it) and uses it
  recursively to launch sub-agents: a child environment is itself an environment, inheriting
  a strictly-less-privileged subset of the parent's tools, permissions, and trust. What is
  *not* in the kit is the rule governing that recursion — how deep agents may nest, what a
  child inherits, whether a child may itself delegate. mecatl currently caps this at one
  level as a policy, not a structural limit; the kit exposes the seam so a consumer can
  experiment with other compositions.

## 4. Skills as a service

The kit-level mechanism, independent of any consumer's capability-scoping policy:

- **Mechanism:** a skill is surfaced via a tool call that does progressive disclosure. It
  does **not** require filesystem access, so it can be served remotely over the driver
  protocol — a skill crosses as identity, body, and named assets, never as a path.
- **Source of record, FS-optional last-mile:** the service is authoritative. If an
  off-the-shelf harness is adopted later, a shim fetches a session's skills from the
  service and **materializes them to a directory** the harness consumes — the
  service-of-record stays, only the last-mile delivery changes. The kit is defined to
  preserve this exit, not to bet against it.
- **What the kit does not own:** the rule that decides *which* skills a session sees. A
  consumer supplies that (per tenant, per project, per its own template concept); the kit
  resolves whatever it is given.

## 5. Open questions

These are unresolved and shape the scope of everything above:

1. **The capability-resolution contract** — what a consumer hands the kit at session
   open, so capability-scoping stays the consumer's policy without the kit leaking a fixed
   model.
2. **Off-the-shelf adoption criteria** — what concretely would make us switch harnesses,
   so the reversibility shim (§4) stays a real option and not a comforting story.
3. **What using the kit looks like** — the consumer-side shape a level down from this doc:
   what "main" is, how a consumer wires and configures the kit (a dependency-injection
   composition root?), and whether there's room for a desktop kit runner that dynamically
   configures and launches subsystems (à la ToolHive). Out of scope for this definition,
   but it's the test of whether the kit idea is real.
4. **The agent environment as a first-class noun** — mecatl already builds an *agent
   environment* (`agent.Deps` + the `Engine`) and uses it to launch sub-agents, but it
   isn't a named entity in the domain model, and recursion is capped at one level. Should
   the kit promote it to a first-class noun (so a consumer composes environments
   explicitly) and separate the nesting-depth policy from the core kit parts, so consumers
   can experiment with compositions the reference implementation doesn't ship?

*Out of band (governance, not definitional):* the open-source posture — whether the kit
(or parts of it) is open-sourced, under what license and governance, and when.

## 6. Mapping to the domain model and architecture

Most properties already have a name in the mecatl [domain model](architecture/mecatl.modelith.md)
and a realization in the [architecture](architecture.md). The kit is largely a
*generalization* of what mecatl already models and builds.

| Kit property (§2) | Maps to (domain model entity / invariant) | Status |
|---|---|---|
| #1 Disposable process | `Process` (`process-disposable`); supporting: `snapshot-at-turn-boundary`, `rehydration-needs-snapshot-plus-log`. Multi-instance handoff: `SessionLease` (`single-writer-per-session`, `session-affinity-routing`) | single-process disposability shipped (mecatl Phases 1–3); lease/affinity **modeled only**, a later phase |
| #2 Stateless provider replay | `Provider` (`provider-stateless-replay`), `fixed-provider-per-session` | modeled + built |
| #3 Capabilities resolved per session from a service | `Skill` (`skill-crosses-as-bundle-not-path`), `Session }o--o{ Skill : "activates"`, `Tool`, `MCPServer`, `AgentDef`; the driver protocol (`adr/0005-driver-seams.md`) | modeled + built (ports cross gRPC) |
| (kit component) The tool interface and common tools | `Tool`/`ToolSpec`/`Catalog` and the `FileSystem`/`Workspace` interfaces; built-in + model-defined + MCP sources; in-proc (subagent-spawning) vs out-of-proc (MCP) execution | modeled + built |
| #4 Multi-tenant within one instance | `Workspace` (`workspace-contained`, `workspace-shell-env-scrubbed`), `Principal`, per-session trust/posture, `team-goal-trusted-peers-untrusted`, `child-ask-redacts-raw-args` | isolation seams built; cross-principal tenant boundary is partly a composition concern |
| #5 Operable over a network | the gRPC + HTTP/SSE API; the `Diagnostics` port; OTel telemetry; drain-to-discard relays | built |
| #6 Everything behind a port | the hexagonal core + `port` interfaces + the driver protocol | built |

**A boundary worth stating explicitly:** in mecatl's domain, skill activation is
**`Session`-scoped** and `AgentDef` is a **subagent specialist definition** (role prompt,
tool grants, model) that a `Subagent` or `TeamMember` instantiates. Neither is a
capability-scoping policy. A consumer that wants to bind a capability set to its own
concept (e.g. a product-specific "agent template") layers that on top via the
session-time resolution seam (§2, property #3) — it is **not** a change to mecatl's
domain model. Likewise the agent-composition policy (§3): the kit's agent environment
recurses to launch sub-agents, but *how deep* and *what a child inherits* is the
consumer's policy, not a structural property the kit bakes in.

## Relationship to other docs

- `architecture/mecatl.modelith.md`: the canonical domain model — the entities and invariants this
  definition maps onto (§6).
- `architecture.md`: the as-built architecture — the hexagonal core and ports that
  realize properties #2, #3, #5, and #6.
- `adr/0027-cloud-native.md`: the mecatl-internal engineering arc this definition generalizes;
  the source of properties #1–#2.
- `adr/0005-driver-seams.md`: the port / driver protocol that makes capabilities-as-a-service (#3)
  already real in mecatl.
- [`cloud-native-harness-systems.md`](cloud-native-harness-systems.md): speculative,
  unscoped future work (the shared-handle problem across the filesystem/forker/command-runner,
  environment-as-descent forking, execution-as-a-service) that isn't a decision yet and may
  never become one.
- [`scoped-resource-grants.md`](scoped-resource-grants.md): a strawman for the tool/filesystem
  substrate — scoped, leased, identity-bound service grants with a direct data path.
- [`agent-identity-model.md`](agent-identity-model.md): a strawman for agent identity —
  mecatl as its own SPIFFE trust domain issuer, definition/instance/run identity tiers,
  attenuating delegation, and the parkable credential lifecycle.
- [`agent-identity-outbound.md`](agent-identity-outbound.md): part 2 of the above — works out
  the outbound boundary, where mecatl's SVID meets its registered-OAuth-client identity.
