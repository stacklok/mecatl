---
sidebar_position: 2
title: What is a cloud-native harness?
description:
  Understand how Mecatl separates the agent loop from the durable, governed
  services that let it run as a cloud-native application.
---

# What is a cloud-native harness?

A **cloud-native harness** is an agent runtime designed as a distributed
application, not a desktop tool copied into a VM.

A desktop harness commonly ties the client, agent loop, filesystem, tools,
credentials, and conversation state to one long-lived machine. Putting that
machine in a container changes where it runs, but not how it is built.

Mecatl takes a different approach. Its core engine owns the agent loop, while
clients, execution environments, model providers, tools, and durable state sit
outside it behind explicit boundaries. The same loop can run locally, as a
service, or on Kubernetes without being replaced.

## Explode the harness into its parts

A cloud-native harness separates the parts that a desktop harness often keeps
inside one process:

- **Agent loop:** reasoning, tool dispatch, permissions, hooks, and event
  emission.
- **Clients:** terminal, API, and application clients that interact with the
  loop without owning it.
- **Execution environments:** assigned workspaces and command runners.
- **Tool ecosystem:** built-in tools, streaming-HTTP MCP services, skills, and
  application-supplied integrations.
- **Supporting services:** model providers, session state, event history,
  identity, and coordination.

These boundaries let each part evolve on its own terms instead of requiring one
long-lived machine to be the client, sandbox, database, tool host, and agent
runtime.

## What the boundaries enable

### Run the loop like an application

The loop can be versioned, rolled out, logged, and observed like any other
application component. In a Kubernetes deployment, workers can be replaced
during normal operations without making durable sessions disappear. See
[Choose how to run Mecatl](/operating/choose-deployment.md) and
[deploy `mecak8s`](/operating/mecak8s.md).

### Keep sessions beyond a worker

A worker can be disposable while a session is not. With durable storage and
coordination, a replacement process resumes from the last persisted turn
boundary. It does not resume an in-flight operation, and work after the last
successful save can be lost. See
[Session continuity](/features/sessions/session-continuity.md) for the storage,
recovery, and single-writer model.

### Build a governed tool ecosystem

A cloud-native harness does not need an unrestricted remote desktop to be
useful. Mecatl can expose purpose-built tools, MCP services, skills, and
application integrations through an explicit catalog with permission, audit, and
execution-environment boundaries.

Shell remains an available, governed capability when it is needed. The direction
is to make it less necessary by expanding purpose-built, permissioned tools and
service integrations for common agent tasks. See
[Execution environments](/features/security-and-execution/execution-environments.md)
and [extension points](/building/extension-points/index.md).

### Serve more than one kind of client

The same loop can support local terminal work, remote services, embedded
applications, and Kubernetes deployments. A client does not need to own the
agent's filesystem, credentials, or durable session state to interact with it.

## Frequently asked questions

### Can another agent harness use Mecatl as its runtime?

Not as a drop-in backend. Mecatl is both the agent harness and the runtime, but
it separates the runtime from the client that you use to interact with it.

Tools such as Claude Code, Claude Desktop, and Codex generally bundle the user
interface, agent loop, context, and tools together on your machine. Mecatl
breaks those pieces apart. Its engine can run locally or remotely on Kubernetes,
while `mecatui` and future web or desktop interfaces act as clients connected to
the same runtime.

You can build another client on top of Mecatl using the
[gRPC or HTTP/SSE APIs](/building/grpc-http.md) or the
[TypeScript SDK](/building/getting-started/typescript-sdk.md). An existing
harness would need a dedicated integration to hand its agent loop over to
Mecatl.

## Where this is going

Mecatl is early, and the cloud-native harness is a direction as well as a
current architecture. We are working toward:

- **More clients.** Mecatl already supports the TUI, gRPC, HTTP/SSE, and the
  TypeScript SDK. Next are web, desktop, and mobile clients, along with new
  interaction models such as Slack and collaborative documents, all over the
  same client/server core. See the
  [TypeScript SDK](https://github.com/stacklok/mecatl/tree/main/sdk/typescript)
  and
  [gRPC and HTTP/SSE contracts](https://github.com/stacklok/mecatl/tree/main/contracts/proto).
- **Strong identity.** We want external systems to receive a verifiable
  delegation chain showing which user, agent, and subagent acted with which
  authority. The proposed design makes Mecatl its own SPIFFE trust domain and
  encodes that chain in JWTs. See the
  [agent identity model](https://github.com/stacklok/mecatl/blob/main/docs/agent-identity-model.md)
  working draft.
- **More tools.** We want a richer ecosystem beyond MCP, including tools that
  can call each other directly with narrowly scoped resources instead of copying
  large inputs through the harness and agent context.
  [Scoped Resource Grants](https://github.com/stacklok/mecatl/blob/main/docs/scoped-resource-grants.md)
  proposes short-lived, attenuable grants and direct tool-to-service data paths;
  it is also a working draft.
- **Cryptographic context attestation.** We want packaged agent context to be
  versioned, signed, attributable, distributable, and subject to policy, like
  modern software supply-chain artifacts, and to make that provenance available
  in the agent identity chain.

These are directions, not guarantees of current availability. The linked agent
identity and scoped-resource-grants documents are explicitly speculative; the
operator and capability guides describe what Mecatl supports today.

## Next steps

- [Choose how to run Mecatl](/operating/choose-deployment.md).
- [Deploy `mecak8s`](/operating/mecak8s.md) on Kubernetes.
- [Understand session continuity](/features/sessions/session-continuity.md).
- [Explore extension points](/building/extension-points/index.md) for your own
  integrations.
