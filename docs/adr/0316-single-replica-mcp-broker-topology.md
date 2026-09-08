# ADR 0314 — Single-replica production topology for the MCP broker

- Status: Accepted
- Date: 2026-09-07
- Scope: standalone MCP broker delivery, probes, draining, and Kubernetes topology
- Supersedes: None
- Superseded by: None

## Context

The process-bound remote broker in ADR 0313 has in-memory attachment handles and
outer OAuth callback correlation. A second replica cannot serve work admitted by
the first, and a replacement process cannot reconstruct that authority. Shipping a
normal rolling Deployment, a disruption budget, or an autoscaling control would
therefore imply availability and interchangeability that the implementation does
not have.

The broker also needs public workload-authenticated gRPC and browser callback
traffic, while health and drain controls must not become public application routes.
Kubernetes NetworkPolicy peers describe IP blocks and label selectors, not external
host names. Dynamic identity, OAuth, and MCP endpoints therefore need concrete
operator-provided policy rather than a claim the chart cannot enforce.

## Decision

Ship `cmd/mecabroker` and a dedicated Helm chart as exactly one replica with a
`Recreate` strategy. No PDB, scaling value, autoscaler, shared outer-broker Redis,
or HA mode is included. A rollout has a real outage: **restart interrupts** active
attachments, outer enrollment correlation, and callbacks. Mecak8s may begin a new
pre-prompt enrollment after replacement, but protected-call authorization never
rebinds and external effects are not exactly-once.

Use verified TLS and workload OIDC on the public gRPC listener. Publish only gRPC
and the fixed browser callback listener. Bind a second administration listener to
loopback and serve only liveness, bounded readiness, and drain there. In the
shell-less image, Kubernetes exec probes invoke the same binary's fixed local
`health`, `ready`, and `drain` operations; they accept no destination argument.

Readiness opens only after TLS identity, workload verifier initialization,
route/profile parsing, ToolHive construction, anonymous discovery, and static
protected-route validation succeed. Its bounded checks perform no user login or
tool execution. Readiness is a traffic signal, never an ownership or stale-worker
fence.

Drain uses one coordinator shared by gRPC and callback work. It closes admission
atomically, waits a non-negative endpoint-propagation interval, lets admitted work
finish until a finite deadline, cancels remaining operation contexts, stops public
listeners, and then closes the validator, RPC adapter, and ToolHive resources. The
default pod budget is 2 seconds propagation, 55 seconds active drain, 5 seconds
listener/resource shutdown, inside a 70 second termination grace.

Start from default-deny ingress and egress. The chart exposes separate
operator-supplied peer lists for mecak8s gRPC, browser callbacks, and egress.
Operators must provide DNS access plus concrete CIDR, namespace, and pod policy for
OIDC/JWKS, upstream OAuth, and MCP destinations. Dynamic external endpoints may
require an external policy controller or maintained IP ranges; the chart makes no
host-name enforcement claim.

### Resource ledger amendment to ADR 0027

| Resource | Owner and scope | Cleanup | Restart / reattach decision |
|---|---|---|---|
| Shared admission/drain coordinator and active-operation cancellation registry | one `mcpbrokerserver.Server`; one broker process | admission closes first; finite drain cancels remaining contexts; `Server.Close` completes teardown | **reset by design**; not durable authority and never an ownership fence |
| Public gRPC and callback listeners plus loopback admin listener | one `cmd/mecabroker` process | finite HTTP shutdown, gRPC stop, then listener close | **reset by design**; Service endpoints reconverge after the Recreate outage |
| Readiness checker set and timeout | one `mcpbrokerserver.Server` | discarded with the server | **derive on startup** from validated TLS/OIDC/profile/ToolHive construction |
| Local ToolHive process, outer callback correlation, confidential broker-client secret | one broker process | closed after admission and listeners; secret remains only in process memory | **reset by design**; restart interrupts outer correlation and never rebinds live authorization |

## Consequences

The deployment is production-hardened but deliberately not highly available.
Maintenance and pod replacement interrupt active broker work, and operators must
plan the downtime. The separate chart cannot accidentally inherit mecak8s replica,
PDB, Redis, or scaling assumptions. Network policy remains honest and portable,
while concrete egress maintenance stays an operator responsibility.

## See also

- [ADR 0027 — Cloud-native deployment](./0027-cloud-native.md)
- [ADR 0311 — ToolHive-owned multi-upstream MCP broker OAuth](./0311-per-upstream-mcp-broker-oauth-grants.md)
- [ADR 0313 — Process-bound remote MCP broker attachments](./0313-process-bound-remote-mcp-broker.md)
- [Architecture guide](../architecture.md)
- [Usage guide](../usage.md)
