# mecatl

<p align="center">
  <img src="./assets/mecatito.png" alt="Mecatito, the mecatl mascot" width="260">
</p>

**mecatl is a cloud-native agent kit for Go.** It gives you the parts around a
model that turn it into an agent: a streaming loop, tools, permissions, hooks,
delegation, durable state, and the service boundaries needed to run it seriously.

Use the importable engine to build your own agent runtime, or run the supplied
cloud-native service on a workstation or Kubernetes. An embedding becomes as
cloud native as the state and execution services you wire behind its ports. The
UI is a client. The agent runtime is the server. They do not have to share a
process.

## What are you building?

| I want to... | Start here |
| --- | --- |
| **Embed an agent in a Go application** | Import [`github.com/stacklok/mecatl/engine`](./engine) and follow the [embedding guide](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/embed-engine.md). |
| **Run an agent service** | Start with [`mecated`](./cmd/mecated) and the [operator guide](./docs/usage.md). |
| **Run agents on Kubernetes** | Use [`mecak8s`](./cmd/mecak8s), which wires Redis-backed state and Kubernetes session leases. |
| **Use an agent locally** | Run the offline demo, then try the [`mecatui`](./cmd/mecatui) terminal client. |
| **Build unattended automation** | Use [`mecatequi`](./cmd/mecatequi) for a single prompt that produces a patch and machine-readable result. |

## Cloud native is how the kit is built

Putting a local agent in a container does not make it cloud native. The process
has to be replaceable, state has to live somewhere else, and another replica has
to be able to continue the work.

mecatl was designed around those constraints:

- One service can host many independent sessions and agent loops concurrently.
- Session snapshots, event logs, memory, schedules, skills, agent definitions,
  and other sources sit behind ports. In-process adapters are convenient
  defaults; a versioned gRPC driver protocol moves the durable stores and content
  sources into separate services. Learned approvals are rebuilt from the durable
  event log rather than stored by a separate permission service.
- Sessions survive process replacement, including a session parked on a human
  approval. An append-only event log retains prompts, approval decisions, and
  pre-compaction history.
- Optional session leases enforce one writer across replicas. The supplied
  `mecak8s` Helm chart combines Redis state, Kubernetes leases, drain handling,
  and disposable agent replicas—two by default—without putting mecatl state on
  a PVC.
- The execution environment is its own per-session capability boundary: a
  workspace, a command runner bound to the same namespace, and a durable
  environment reference. Custom deployments can reattach a non-local environment
  through the resolver seam. No production remote or microVM-backed environment
  implementation ships yet.

The loop itself does not know about Redis, Kubernetes, gRPC, OpenAI, a local
filesystem, or a particular UI. It depends on small interfaces and emits one
typed event model.

```text
 clients                         agent service                    replaceable backends
 ┌───────────────┐       ┌──────────────────────────┐       ┌────────────────────────┐
 │ mecatui       │       │ many sessions / loops    │       │ model providers        │
 │ web client *  │──────▶│ tools · policy · hooks   │◀─────▶│ sessions · event log   │
 │ your client   │ gRPC  │ subagents · teams        │       │ memory · schedules     │
 └───────────────┘ HTTP  └──────────────────────────┘       │ execution environments │
                                                           └────────────────────────┘
 * in active development
```

Read [Cloud-native kit properties](https://github.com/stacklok/mecatl/blob/main/user-docs/building/cloud-native-kit.md) for the
shipped state and restart model, and the [architecture guide](./docs/architecture.md)
for the boundaries and ports.

## Serious deployments, explicit authority

Agents call tools with somebody's authority. Once a service runs work for more
than one person, "the agent did it" is not a useful identity model.

mecatl already ships deny-dominant permissions, human approval over the wire,
model-backed guardrails, bounded delegation, OIDC caller verification, durable
owner and actor attribution, secret-scrubbed command environments, and an audit
log. Delegated runs carry a derived capability set that can only narrow at each
in-process hop, with local or Cedar-backed enforcement at the execution boundary.
Caller identity still is not, by itself, a complete tenant-isolation boundary.

The next step is cryptographic proof across process and service boundaries: trace
an action through the user, agent, and subagent that caused it, and let downstream
services verify the narrowed authority instead of trusting the harness's log. We
are working toward a mecatl-issued SPIFFE trust domain for that purpose. The
signing path, attenuation model, and key custody are active design work, not a
feature we claim to have finished. Follow the [agent identity
tracker](https://github.com/stacklok/mecatl/issues/377) and the [working identity
model](./docs/agent-identity-model.md).

## A client for humans

The client/server split is deliberate. `mecatui` renders the same gRPC event
stream that another client can consume. It can embed a local server for a
single-binary experience or connect to a remote one. A web client is [in active
development](https://github.com/stacklok/mecatl/pull/618) on the same principle:
the service remains the source of truth, and the browser is not where agent
state or provider credentials live.

This gives a tinkerer a friendly local experience without turning the UI into a
runtime dependency. It also lets a platform team put the service somewhere else
and build the client its users need.

## A kit for experimenting with agents

The operational pieces are only half the project. mecatl also includes the
things we want when building a personal assistant:

- Project memory plus a cross-project user model, with versioned inspect, forget,
  and undo operations.
- Optional dream consolidation. Automatic schedules retire only byte-identical
  duplicates; manual `/dream` lets a human review and apply plans, including
  synthesized replacements.
- An operator-owned, agent-read-only soul for persistent persona and style.
- Subagents, parallel fan-out, and coordinating teams with bounded budgets and
  inspectable child sessions.
- Progressive skills and completed-trajectory reflection. Learning is off by
  default; review and auto modes stage evidence-backed proposals, and learned
  procedures pass through the versioned skill lifecycle before activation.

These features are conservative on purpose. The agent can propose what it
learned, but durable memory and reusable instructions have provenance, review,
and rollback paths. See [Memory and learning](./docs/architecture/memory.md) and
[Skills and extensibility](./docs/architecture/extensibility.md).

## Try it without an API key

The offline demo runs a complete scripted session with tool calls, a permission
approval, delegation, and usage accounting:

```sh
go run ./cmd/mecademo
```

To build every supplied runtime and client:

```sh
task build
```

To embed the engine:

```sh
go get github.com/stacklok/mecatl/engine@latest
```

The engine is a separate Go module with a guarded public API and a deliberately
small dependency closure. Reference adapters let it run offline, then you can
replace only the ports your application owns. See the [engine compatibility
contract](./engine/COMPATIBILITY.md).

## Runtime shapes

| Shape | What it is | Learn more |
| --- | --- | --- |
| `engine` | Importable Go core for your own composition | [Embed the engine](https://github.com/stacklok/mecatl/blob/main/user-docs/deployment/embed-engine.md) |
| `mecated` | gRPC + HTTP/SSE agent service | [Run mecated](./docs/usage/mecated.md) |
| `mecak8s` | Kubernetes-native runtime with Redis and leases | [Run mecak8s](./docs/usage/mecak8s.md) |
| `mecatequi` | Single-shot headless automation | [Run mecatequi](./docs/usage/mecatequi-ci.md) |
| `mecatui` | Terminal client for a local or remote service | [Use mecatui](./docs/tui.md) |

See [Pick your deployment shape](https://github.com/stacklok/mecatl/blob/main/user-docs/getting-started/deployment-decision.md)
for the trade-offs.

> **Security:** `mecated` is unauthenticated by default and intended for
> loopback, single-user use. Configure authentication and transport protection
> before binding it off-loopback. The [operator guide](./docs/usage.md) covers
> bearer auth, TLS/mTLS, OIDC, rate limits, and deployment posture.

## Documentation

- [Architecture guide](./docs/architecture.md)
- [Usage and operator guide](./docs/usage.md)
- [Client integration guide](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/grpc-http.md)
- [Documentation index](./docs/README.md)
- [User documentation](https://github.com/stacklok/mecatl/blob/main/user-docs/intro.md)
- [Production-readiness tracker](./docs/design/PRODUCTION-READINESS.md)
- [Engine compatibility contract](./engine/COMPATIBILITY.md)
- [Contributing to mecatl](./CONTRIBUTING.md)

## Contributing, security, and license

Contributions are welcome through pull requests. Start with
[CONTRIBUTING.md](./CONTRIBUTING.md); coding agents should read
[AGENTS.md](./AGENTS.md) before changing the repository.

Report vulnerabilities privately through [SECURITY.md](./SECURITY.md).

Licensed under the [Apache License 2.0](./LICENSE). Community participation is
governed by the [Code of Conduct](./CODE_OF_CONDUCT.md).
