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
  operate, not babysit.
- **Collaboration where work happens:** make agents useful to teams, not just
  to one person at a terminal.
- **An open foundation:** make Mecatl easy to build on and integrate with.

The roadmap is grouped as Done, Now, Soon and Later. These describe direction,
not delivery dates or commitments, and current focus changes as we learn. For
what Mecatl supports today, see
[What is a cloud-native harness?](./user-docs/cloud-native-harness.md) and the
[Mecatl documentation](https://mecatl.dev/docs/).

## Done

A multi-user Kubernetes deployment where each user's work stays theirs, agents
act within limits the operator sets, tools use each user's own accounts, and
developers can build on Mecatl.

### Identity and accountability

- **Every action belongs to a signed-in user.** When the operator connects an
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
  from other agents reach the model inside fences that mark them as untrusted
  data, not instructions.
- **Isolation to match the task.** Sessions can run with no file access or
  shell at all.

### Dependable, long-running work

- **Run it like any other service.** mecak8s ships as a Helm chart, and each
  release's image and chart are signed. Operators can turn on metrics and
  traces for runs, tool calls and token use.
- **Work survives restarts and crashes.** With the Redis store, sessions live
  outside the pod, so a restart or pod move keeps their history and pending
  approvals.
- **Agents work on a schedule.** Users schedule a prompt to run on a cron
  schedule or once, from chat, the terminal or the API. Schedules survive
  restarts, and each slot fires at most once however many replicas run.

### Collaboration where work happens

- **Agents remember, and learn only with approval.** Each user has a personal
  memory that agents read and add to, and each project workspace has one too.
  Where the store keeps history, facts can be inspected, forgotten or
  restored, and facts that look like credentials are refused. Operators can
  also let agents propose new skills and facts from finished work.
- **Tools connect with each user's own accounts.** Operators list MCP servers
  once for the deployment. A user connects their accounts for a session in one
  browser trip, and a credential broker keeps the tokens away from the agent
  and the model.

### An open foundation

- **Build apps on Mecatl.** A TypeScript SDK on npm lets apps start sessions,
  stream runs and answer approvals. Clients check what a server supports before
  they use a feature, and errors carry stable codes.
- **Embed the engine in Go.** The engine, each model provider, and the session
  storage adapters are separate Go modules, so a Go application can use the
  same parts Mecatl does.

## Now

Agents with their own identities and derived authority, safe execution, and
sessions that survive restarts.

### Identity and accountability

- **Delegated access, with authority derived from user and agent.** An agent
  acts on the user's behalf, not as the user. Its authority is what the user
  may do, limited to what the agent's definition allows, and a helper agent
  never gets more than the agent that started it. The user's provider
  credentials stay at the gateway and never reach the agent host or the model.
  Closing the session cuts off access.
- **Every agent gets its own identity.** Mecatl gives each agent its own SPIFFE
  identity that other systems can verify. The identity broker ties it to the
  session, the agent that started it, and the user, so every action names the
  agent that took it. Subagents get their own identities, linked to the agent
  that started them.
- **An attributed audit trail.** Every authorization decision and gateway tool
  call is recorded and can be traced to the agent, the agents that started it,
  the session, and the user, including unattended runs. Outcomes the agent host
  only reports are marked as reported, and an unknown result is recorded as
  unknown. Records carry the agent, session and user identifiers, so they can
  be matched to the session transcript, and go to storage the agent host can't
  change.

### Bounded autonomy

- **Isolated execution.** Shell commands and file edits run in a separate
  executor that holds only the access it's granted, not cluster-wide
  privileges.
- **Per-agent tool limits and call-bound tokens.** Each agent's tool limit
  comes from its definition, narrowed by the agent that started it, and a call
  outside that limit is refused before any token is issued. Each gateway token
  is approved for one call (this tool, with these arguments) and expires within
  seconds, so a token taken from the agent host is no good for anything else.

### Dependable, long-running work

- **Durable sessions.** Sessions survive restarts, pod moves and crashes. When
  a session resumes, actions that may already have happened, like a command or
  API call, are handled rather than blindly repeated.
- **Scheduled agents with their own identity.** A recurring job, such as a
  nightly review, runs unattended on a time-limited grant from the user who
  scheduled it. The user's provider credentials stay at the gateway.

### Collaboration where work happens

- **A terminal that keeps up with long sessions.** The terminal UI keeps memory
  and CPU flat in long sessions, and large lists such as MCP tools and
  resources stay fast to browse.

## Soon

Production hardening for identity, stronger isolation, and agents in Slack.

### Identity and accountability

- **Identity that survives restarts and scales out.** Identity state is
  durable, so a restart no longer signs users out, and mecak8s runs more than
  one replica with identity on.
- **The identity broker as its own service.** The broker moves out of mecak8s
  with its signing keys, refresh tokens and audit records, so a compromised
  agent host can't use them. Records come from the components that make each
  decision, the authorization servers and the gateway, and are cross-checked
  against each other.
- **Works with gateways you already run.** The same flow works with a gateway
  Mecatl doesn't operate. That gateway keeps users' provider credentials under
  its own administrators, and the user consents once to Mecatl acting through
  it.

### Bounded autonomy

- **MicroVM isolation for untrusted work.** Sessions can run in lightweight
  virtual machines with their own kernel when containers aren't enough.
- **Sign-out and revocation that reach every agent.** Signing out everywhere
  ends the user's sessions and schedule grants, and an account the company
  identity provider disables loses access at the next check.
- **Policy tied to the exact agent version.** The broker loads definitions only
  from sources the operator controls and records where each came from and
  which version was used. A session's definitions can't change once selected,
  and the operator decides which definitions may start a session, which caps
  what any agent in it can reach. Tool limits become reviewable Cedar policy
  tied to the exact definition version.

### Dependable, long-running work

- **Kubernetes-native operations.** Graceful handoff with no user-facing
  downtime, and well-tested crash recovery that tells users and agents
  honestly what happened.

### Collaboration where work happens

- **Agents in Slack.** Mention an agent and it works in the thread, within a
  spend budget, and picks up where it left off after restarts.

### An open foundation

- **SDK testkit.** A fake server lets apps built on the SDK be tested offline.
- **Kernel architecture.** A minimal core agent loop with well-defined
  extension points, with many core features refactored as extensions.

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

### Collaboration where work happens

- **Multiplayer sessions.** Several people work with the same agents in one
  session, with shared controls and a clear record of who asked for what.
- **Studio web workspace.** A browser workspace for teams, with chat, agent
  mentions and commands, alongside the terminal.

### An open foundation

- **Direct tool-to-tool data exchange.** Tools pass data to each other through
  narrowly scoped grants instead of routing it through the model's context.
  This saves tokens and limits data exposure.

## Being explored

Not scheduled, and may not happen in this form.

- **Identity and accountability:** external systems verifying the full
  user → agent → helper delegation chain themselves; provenance for agent
  definitions, prompts and skills; extending authorization and audit beyond
  MCP tool calls to file tools, shell and agent spawning.
- **Collaboration where work happens:** desktop and mobile clients.
