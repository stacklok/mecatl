---
sidebar_position: 3
title: Mecatl roadmap
sidebar_label: Roadmap
description:
  See what Mecatl has shipped, what is in progress, and where the project is
  heading across identity, autonomy, durability, clients, and extensibility.
---

# Mecatl roadmap

Agents are becoming coworkers that act across your systems, often with no one
watching. Mecatl's bet is that every agent action should be **attributable and
verifiable**: which user, which agent, which helper agent, with what authority,
for which call.

Identity is the differentiator. It doesn't make agents dependable, contained,
or useful on its own, though, so the work runs across five themes:

- **Identity and accountability:** know which agent acted, on whose behalf, and
  with what authority.
- **Bounded autonomy:** give agents room to work without giving them
  unrestricted access.
- **Dependable, long-running work:** make agent work something teams can
  operate, inspect, and recover when interrupted.
- **Where you work:** bring agents to the tools people already use, from the
  terminal to team chat.
- **An open foundation:** make Mecatl easy to build on and integrate with.

The roadmap is grouped as Done (shipped), Now (in progress), Soon (next) and
Later (further out). These describe direction, not delivery dates or
commitments, and current focus changes as we learn. For
what Mecatl supports today, see
[What is a cloud-native harness?](./cloud-native-harness.md) and the
[Mecatl documentation](./intro.md).

A few terms used below:

- **`mecak8s`:** the Mecatl server for Kubernetes, which runs many users' agent
  sessions.
- **Agent host:** the server process that runs the agent loop and talks to the
  model.
- **Gateway:** the MCP gateway that proxies agents' tool calls to MCP servers.
  It holds users' credentials for outside services, so the agent host never
  sees them.
- **Identity broker:** a separate component from the gateway. It issues and
  checks agent identities, and decides what each agent may call before the
  gateway receives the call.

## Done

A multi-user Kubernetes deployment where each user's work stays theirs, agents
act within limits the operator sets, tools use each user's own accounts, and
developers can build on Mecatl.

### Identity and accountability

- **Signed-in users whose work stays theirs.** When the operator connects an
  OIDC identity provider, users sign in through it and the server validates
  their token on every request. Each session, schedule and memory store then
  belongs to the user who created it, and another user's session or schedule
  reads as if it doesn't exist.

### Bounded autonomy

- **Delegating work never widens access.** A subagent's tools and permissions
  are the intersection of what the agent that started it has, what its own
  definition allows, and what the call asks for. Resuming or restarting a
  subagent can't widen them either.
- **Agents stay in their workspace.** Each session runs in a workspace the
  server assigns; clients can't pick another root. Operators choose how
  strictly paths outside it are gated, from asking for approval to allowing
  reads.
- **Untrusted text is labeled as data.** Web pages, search results and output
  from other agents reach the model wrapped in markers that identify them as
  untrusted data, not instructions.
- **Isolation to match the task.** Sessions can run with no file access or
  shell at all, and locally they can run in an opt-in microVM.

### Dependable, long-running work

- **Run it like any other service.** `mecak8s` ships as a Helm chart, and each
  release's image and chart are signed. It runs across several replicas, and
  operators can turn on metrics and traces for runs, tool calls and token use.
- **Sessions survive restarts and crashes.** With the Redis store, sessions
  live outside the pod, so a restart or pod move keeps their history and
  pending approvals.
- **Agents work on a schedule.** Users schedule a prompt to run on a cron
  schedule or once, from chat, the terminal or the API. Schedules survive
  restarts, and each scheduled run fires at most once, however many replicas
  are running.

### Where you work

- **Agents remember, and learn only with approval.** Each user has a personal
  memory that agents read and add to, and each project workspace has one too.
  Where the store keeps history, facts can be inspected, forgotten or
  restored, and facts that look like credentials are refused. Operators can
  also let agents propose new skills and facts from finished work.
- **Tools connect with each user's own accounts.** Operators list MCP servers
  once for the deployment. A user connects their accounts for a session in one
  browser trip, and the gateway keeps the tokens away from the agent and the
  model.

### An open foundation

- **Build apps on Mecatl.** A TypeScript SDK on npm lets apps start sessions,
  stream runs and answer approvals. Clients check what a server supports before
  they use a feature, and errors carry stable codes. See the
  [TypeScript SDK guides](/building/typescript-sdk/index.md).
- **Embed the engine in Go.** The engine, each model provider, and the session
  storage adapters are separate Go modules, so a Go application can use the
  same parts Mecatl does. See the
  [embedding guide](/building/go/embed-engine.md).

## Now

In progress: agents with their own identities and derived authority, isolated execution, and
session history that supports inspection and safe recovery.

### Identity and accountability

- **Delegated access, with authority derived from user and agent.** An agent
  acts on the user's behalf, not as the user. Its authority is what the user
  may do, limited to what the agent's definition allows, and a helper agent
  never gets more than the agent that started it. The user's provider
  credentials stay at the gateway and never reach the agent host or the model.
  Closing the session cuts off access.
- **Every agent gets its own identity.** Mecatl gives each agent its own SPIFFE
  identity (an open standard for workload identity) that other systems can
  verify. The identity broker ties it to the session, the agent that started
  it, and the user, so every action names the agent that took it. Subagents get
  their own identities, linked to the agent that started them.
- **An attributed audit trail.** Every authorization decision and gateway tool
  call is recorded and can be traced to the agent, the agents that started it,
  the session, and the user, including unattended runs. When a result is known
  only from the agent host's own report, the record says so, and a result that
  can't be known is recorded as unknown. Records carry the agent, session and
  user identifiers, so they can be matched to the session transcript, and go to
  storage the agent host can't change.

### Bounded autonomy

- **Isolated execution on Kubernetes.** Shell commands and file edits run in a
  separate executor that holds only the access it's granted, not cluster-wide
  privileges.
- **Per-agent tool limits and call-bound tokens.** Each agent's tool limit
  comes from its definition, narrowed by the agent that started it, and a call
  outside that limit is refused before any token is issued. Each gateway token
  is approved for one call (this tool, with these arguments) and expires within
  seconds, so a token taken from the agent host can't be used for any other
  call.

### Dependable, long-running work

- **Recovering without blindly repeating actions.** Reconstruct accepted
  session state after an interruption and distinguish known outcomes from
  actions whose effects are uncertain. A command or API call that may already
  have happened isn't automatically run again.
- **History that explains the work.** Mecatl keeps the instructions, context,
  tool definitions and model calls behind each action, so you can later see
  why an agent did what it did, even after its configuration has changed. When
  older turns are summarized to fit the model's context window, the full
  conversation and tool activity are still kept.
- **Scheduled agents with their own identity.** A recurring job, such as a
  nightly review, runs unattended on a time-limited grant from the user who
  scheduled it. The user's provider credentials stay at the gateway.

### Where you work

- **A terminal that keeps up with long sessions.** The terminal UI keeps memory
  and CPU flat in long sessions, and large lists such as MCP tools and
  resources stay fast to browse.

## Soon

Production hardening for identity, stronger isolation on Kubernetes, and agents
in Slack.

### Identity and accountability

- **Identity that survives restarts and scales out.** The new identity flow
  currently keeps its state in memory and runs on one replica. Its state
  becomes durable, so a restart no longer signs users out, and `mecak8s` runs
  more than one replica with identity turned on.
- **The identity broker as its own service.** The broker moves out of `mecak8s`
  with its signing keys, refresh tokens and audit records, so a compromised
  agent host can't use them. Records come from the components that make each
  decision, the authorization servers and the gateway, and are cross-checked
  against each other.
- **Works with gateways you already run.** The same flow works with a gateway
  Mecatl doesn't operate. That gateway keeps users' provider credentials under
  its own administrators, and the user consents once to Mecatl acting through
  it.

### Bounded autonomy

- **MicroVM isolation on Kubernetes.** Sessions on the shared executor can run
  in lightweight virtual machines with their own kernel when containers aren't
  enough, as local sessions already can.
- **Sign-out and revocation that reach every agent.** Signing out everywhere
  ends the user's sessions and schedule grants, and an account the company
  identity provider disables loses access at the next check.
- **Policy tied to the exact agent version.** The broker loads definitions only
  from sources the operator controls and records where each came from and
  which version was used. A session's definitions can't change once selected,
  and the operator decides which definitions may start a session, which caps
  what any agent in it can reach. Tool limits become Cedar policy, a readable
  policy language security teams can review, tied to the exact definition
  version.

### Dependable, long-running work

- **Pod replacement without downtime.** Pods can be drained and replaced with
  no user-facing interruption. After a crash, users and agents are told which
  work finished, which didn't, and which is unknown.

### Where you work

- **Agents in Slack.** Mention an agent and it works in the thread, within a
  spend budget, and picks up where it left off after restarts.

### An open foundation

- **SDK testkit.** A fake server lets apps built on the SDK be tested offline.
- **Specialized agents on a shared runtime.** Cleaner explicit extension points
  let developers add or replace capabilities without forking the engine. The
  execution kernel keeps permission checks, cancellation, and history rules
  consistent as those capabilities change.

## Later

Controlling where data flows between agents and tools, and working on agents
together.

### Identity and accountability

- **An audit ledger you can verify, not just trust.** Audit records form an
  append-only, hash-chained ledger with signed checkpoints kept outside Mecatl.
  Anyone can check offline that the record set is complete and unaltered.

### Bounded autonomy

- **Confidentiality across delegation.** A sensitivity label follows data into
  every delegated agent, and sharing work becomes an explicit act.
- **Direct tool-to-tool data exchange.** Tools pass data to each other through
  narrowly scoped grants instead of routing it through the model's context.
  This saves tokens and limits data exposure.

### Where you work

- **Multiplayer sessions.** Several people work with the same agents in one
  session, with shared controls and a clear record of who asked for what.

## Being explored

Not scheduled, and may not happen in this form.

- **Identity and accountability:** external systems verifying the full
  user → agent → helper delegation chain themselves; provenance for agent
  definitions, prompts and skills; extending authorization and audit beyond
  MCP tool calls to file tools, shell and agent spawning.
- **Where you work:** web, desktop and mobile clients.
