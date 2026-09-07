---
sidebar_position: 5
title: Standalone MCP broker
description: Deploy the process-bound MCP authorization broker safely as one Kubernetes replica.
---

# Standalone MCP broker

Use `cmd/mecabroker` when `mecak8s` needs the remote ToolHive authorization boundary.
It ships as `ghcr.io/stacklok/mecatl/mecabroker` and has its own
`deploy/helm/mecabroker/` chart; it is not part of the mecak8s chart.

```sh
task ko:build:broker
helm template broker deploy/helm/mecabroker \
  -f deploy/helm/mecabroker/ci/production-values.yaml
```

Replace all example endpoints, Secret names, and TEST-NET ranges before installing.
Create the TLS, workload-identity CA, and upstream OAuth client Secrets out of band;
do not put secret values in Helm values.

## Availability boundary

The chart always uses one replica and `Recreate`. It offers no PDB, autoscaling, HA
switch, or outer-broker Redis. Restart interrupts active attachments and outer OAuth
callback correlation. A replaced broker may support a fresh pre-prompt enrollment, but
cannot rebind an active protected call or promise exactly-once external effects.

If a tool reports an unknown outcome after losing the broker response:

1. Treat the operation as possibly completed.
2. Inspect provider state through a known-safe status or read operation, if one exists.
3. Do not automatically repeat the mutation.
4. If the outcome remains unknown, report it and obtain an explicit recovery decision.

The broker-enabled model receives the same guidance. It is not a hard reconciliation or
approval gate, and a later new invocation can still duplicate an external effect.

## Probes and drain

Only TLS gRPC and browser callbacks are published by the Service. The admin listener is
loopback-only; shell-free exec probes call the binary's fixed `health`, `ready`, and
`drain` operations. Readiness checks validated TLS identity, bounded OIDC verifier
initialization, route/profile configuration, ToolHive construction, anonymous discovery,
and protected-route declarations. It never performs login or tool execution and is not
an ownership fence.

Drain closes gRPC and callback admission together, waits 2 seconds for endpoint
propagation, permits existing work until the finite 55-second deadline, then cancels the
remainder and closes listeners and local ToolHive resources. The pod's 70-second
termination grace contains that budget.

## Network policy

Set `networkPolicy.publicFrom` to one union of exact namespace, pod, and CIDR peers for the multiplexed public listener. Vanilla NetworkPolicy cannot distinguish gRPC from browser callbacks on the shared port. Set `operatorEgress` to cluster DNS plus concrete destination rules for OIDC/JWKS, upstream OAuth, and MCP; external DNS names are not enforced.

For the complete configuration and resource-lifecycle boundary, see the
[usage guide](https://github.com/stacklok/mecatl/blob/main/docs/usage.md#standalone-mcp-broker-on-kubernetes)
and [ADR 0305](https://github.com/stacklok/mecatl/blob/main/docs/adr/0305-single-replica-mcp-broker-topology.md).
