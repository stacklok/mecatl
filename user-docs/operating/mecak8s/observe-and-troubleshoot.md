---
title: Observe and troubleshoot mecak8s
description: Inspect durable events, logs, and readiness for a Kubernetes deployment.
sidebar_position: 5
---

# Observe and troubleshoot mecak8s

Start with pod readiness and Redis connectivity, then inspect logs and durable session events for the affected workflow.

## Inspect the event log

The durable event log at `mecatl:events:<SESSION_ID>` is a Redis
Stream. Read it with:

```sh
redis-cli XRANGE "mecatl:events:<SESSION_ID>" - +
```

Each entry's `r` field contains the event envelope, including its `Actor`
metadata. Do not modify the sibling cursor key,
`mecatl:events-gen:<SESSION_ID>`, independently of the event log.

## Size durable event followers

Each `mecak8s` process uses a dedicated Redis client for blocking durable-event
followers. This keeps session saves, event appends, and metadata operations on
the durability client when watches are idle.

|CLI flag|Helm value|Default|
|-|-|-|
|`--redis-follow-pool-size`|`redis.follow.poolSize`|`32`|
|`--redis-max-followers`|`redis.follow.maxFollowers`|`32`|

Both values must be positive integers, and the maximum follower count must not
exceed the follow pool size. `mecak8s` and the Helm chart reject invalid values
at startup or render time.

Keep the defaults unless one pod must serve more than 32 concurrent durable
watches. Size the follower limit for the expected per-pod watch concurrency,
then give the pool at least that many connections. Include every replica and
briefly overlapping credential generations in the Redis connection budget.

When the process has admitted the maximum number of followers, a new watch
ends with `watch_capacity`. gRPC reports `RESOURCE_EXHAUSTED`; HTTP retains its
status 200 event stream and sends a terminal `event: error` frame. The
TypeScript SDK reconnects from the last processed cursor with the same filter.
This code is separate from `watch_lagging`, which means a client did not consume
the server's bounded delivery buffer quickly enough.

## Configure logging

The chart exposes the process logging threshold separately from metrics and
OTLP:

```yaml
logging:
  level: debug
```

This renders `--log-level=debug`, including the embedded
ToolHive/authserver/vMCP `slog` records in the mecak8s container logs. The
default empty value preserves the binary's `INFO` default. Supported values are
`debug`, `info`, `warn`, and `error`. `extraArgs` remains available for flags
that are not modeled by the chart; if it also contains `--log-level`, its later
argument takes precedence.

## Readiness and health

mecak8s exposes two unauthenticated probe endpoints on the HTTP port (default
`0.0.0.0:8081`). A separate plaintext, Pod-only drain listener defaults to
`0.0.0.0:8082` and serves only `GET /drain`:

|Endpoint|Purpose|
|-|-|
|`GET /healthz`|Liveness, returns 200 unless the process is hung|
|`GET /readyz`|Readiness, returns 200 only when `!draining && redisOK`; flips to 503 on drain or Redis failure|
|`GET /drain` on port 8082|preStop hook target, arms the drain gate, blocks ~3s for endpoint propagation, returns 200|

The Service exposes only ports 8080 and 8081, so normal Service/gateway API
traffic cannot invoke `/drain`. Direct Pod-IP access to 8082 remains an operator
network-isolation responsibility. The `readyz` probe is dynamic: it calls
`svc.StorageReady`, which pings the Redis store with a 2-second timeout. A Redis
failure shows up as not-ready and removes the pod from Service endpoints without
a restart. The startup and readiness probes use a 3-second kubelet timeout so the
2-second Redis bound can complete before Kubernetes abandons the request.

For collector configuration, use [observability](/operating/observability.md). Diagnose identity failures in [client access](identity-and-client-access.md#troubleshooting-start-here).

## Configure model recovery

Use `extraArgs` to set the server-owned recovery policy. Defaults are a 30-minute
recovery window and at most 60 calls for each model step. For example:

```yaml
extraArgs:
  - --llm-recovery-budget=10m
  - --llm-max-attempts=12
```

Recovery ends when semantic output becomes visible; a later failure is terminal.
Review [per-step limits and provider costs](/features/sessions/choose-models.md#a-provider-error-ended-a-model-step)
and the remaining [resilience controls](/features/runtime/observability-and-resilience.md#model-call-resilience)
before changing this policy.

## Next steps

- [Scale, recover, and upgrade](/operating/mecak8s/scale-recover-and-upgrade.md).
