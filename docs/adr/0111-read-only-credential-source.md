# ADR 0111 — Read-only environment credential source and OAuth refresh posture

- Status: Accepted
- Date: 2026-08-14
- Scope: explicit Kubernetes-friendly credential restore for embedding hosts
- Supersedes: none
- Superseded by: none

## Context

ADRs 0108 and 0110 established a host-internal split between credential reads and
conditional writes, then initially required MCP OAuth to receive one mutable Store.
Kubernetes commonly projects a Secret into a pod environment, but that projection is not
a compare-and-swap persistence surface. Treating it as mutable would either lose refresh
rotation at restart or imply Kubernetes API authority that mecatl does not have.

## Decision

Add an explicit environment `Reader` under `internal/adapter/credentialstore`. Its
constructor receives one namespace, one opaque record key, one environment variable name,
and one lookup function. Construction performs no lookup, and default composition never
selects `os.LookupEnv`. Names use the `MECATL_` prefix plus the ASCII grammar
`[A-Z_][A-Z0-9_]{0,127}`. The selected
value is strict canonical padded base64, decoded within the existing one-MiB record bound.
The adapter serves only the configured exact key, returns owned copies, derives a stable
opaque SHA-256 version from namespace, key, and value, and exposes only typed, value-free
errors. It cannot list or mutate environment state and has an idempotent close.

MCP OAuth persistence injection accepts exactly one persistence mode: either one mutable
credential `Store`, whose reads and conditional writes necessarily share a CAS domain, or
one read-only credential `Reader`. The two options are mutually exclusive; an independently
supplied writer is not accepted. A reader-only controller can warm-restore a valid
credential. New authorization and reset require a mutable Store and fail before presenter,
network, or persistence side effects.
Lazy refresh is fail-closed before network by default because the result cannot be made
durable. An explicit in-memory-refresh option may use a refreshed token for only the
current controller lifetime; the source record remains authoritative and a restart sees
its old value. A refresh `invalid_grant` clears only controller memory and returns the
redacted login-required category when no writer exists. It never attempts to mutate the
environment.

No command flag, settings schema, `app.Build` wiring, browser, ACP surface, Kubernetes API
client, or mounted-file source is added. Embedding hosts must opt in explicitly.

## Consequences

A host can inject a Kubernetes Secret-backed environment value for warm OAuth restore
without granting write authority or pretending refresh rotation is durable. Secret-backed
environment variables are immutable for a running pod. Rotation persistence therefore
requires an external controller to update the Secret and restart the pod, or a future
Kubernetes Secret backend that conditionally writes against `resourceVersion`.

The opt-in in-memory refresh posture trades restart continuity for current-process
availability and is named as such. It does not update the Secret, survive replacement, or
coordinate replicas. The default posture remains fail-closed.

## See also

- [ADR 0108 — Internal encrypted credential store](./0108-credential-store.md)
- [ADR 0110 — Adapter-local MCP OAuth controller](./0110-mcp-oauth-controller.md)
- [Architecture overview](../architecture.md#internal-credential-store)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md#credential-store)
