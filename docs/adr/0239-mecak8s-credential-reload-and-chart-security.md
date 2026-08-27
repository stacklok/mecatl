# ADR 0239 — mecak8s credential reload and chart security

- Status: Accepted
- Date: 2026-08-27
- Scope: mecak8s projected credentials, live TLS and Redis reload, and Helm security defaults

## Context

Kubernetes projected Secret volumes rotate by replacing a `..data` symlink. Watching a
resolved file or inode therefore misses later generations. A rotation can also expose a
brief mixed generation while related files are being projected, so replacing live TLS or
Redis state one field at a time can publish an invalid credential set. Restart-only
credentials avoid those races but force avoidable pod disruption and can leave a real
provider deployment using stale credentials.

The chart also needs a fail-closed distinction between its disposable mock-provider
fixture and a real provider. Zero-valued numeric limits are currently ambiguous between
"unset" and an invalid explicit ceiling, while requiring a metrics subsystem solely to
observe infrequent reloads would add a second operational contract.

## Decision

Use one secret-agnostic file-watching adapter for mounted credential files. It watches the
lexical parent directories of configured paths, never resolved symlink targets; deduplicates
parents; and coalesces event bursts with a resettable debounce bounded from the first event.
The adapter reports watcher errors without reading or logging file content. Its idempotent
close cancels a pending timer, closes the underlying watcher, and joins its goroutine.

For the mecak8s server certificate, synchronously load, key-match, and parse the initial
certificate before serving. Keep exactly one last-valid, fully parsed `tls.Certificate` in
an atomic pointer and serve it through `tls.Config.GetCertificate`; do not retain or mutate
a static `Certificates` slice. Each debounced notification transactionally loads and
validates the certificate and key together, then atomically publishes the pair. Invalid,
partial, or mismatched rotations are logged and leave the prior certificate active.
Existing TLS connections continue with their negotiated state; new handshakes read the
current pointer. The watcher closes after graceful serving stops. The client CA remains
restart-required because changing client trust is a separate revocation and rollout policy.

Apply the same last-valid principle to Redis credentials, replacing the whole client
transactionally rather than mutating its options. Every Redis operation first leases the
store's current generation; a swap publishes one verified generation atomically, while a
retired generation remains open until its operations, event iterators, and migration-lock
lifecycle release their leases. `Close` first stops and joins reload work, then rejects new
leases/swaps and waits for all generations to retire; repeated closes are safe.

When any Redis CA, username, or password file is configured, mecak8s enables reload without
a separate flag. A coalesced file event re-reads the complete configured set and runs the
existing validation and toolhive-core client construction path under a bounded probe. A
single reload worker retries with bounded exponential backoff because Secret projection and
Redis-side ACL/trust updates can settle independently; a newer event resets the attempt
series. Only a successful PING/TLS/auth candidate is published. Invalid or exhausted
candidates are closed and retain the previous client. With no configured Redis files there
is no watcher, goroutine, retry state, or behavior change.

The later chart security gate rejects `mockProvider=false` unless both server TLS and
OIDC are configured, except when the operator explicitly sets
`security.allowUnsafeRealProvider=true`. The exception is conspicuous, is stamped on the
pod as `mecatl.stacklok.com/unsafe-real-provider: "true"`, and is never inferred from
another value. TLS alone does not authenticate callers; OIDC alone does not encrypt the
transport. Chart token ceilings are nullable: omission means no chart-provided ceiling,
while an explicitly supplied value must be a positive integer. `defaultProvider` validates
against the built-in CLI provider IDs, while the opaque `model` accepts any non-blank value;
both flags render only when non-empty. Optional topology spread, affinity, node selector,
and toleration values map directly under the pod spec and remain absent by default.

Safe structured command-root logs are the required reload observability surface. They
identify component, operation, outcome, attempt, and a bounded non-secret reason, but never
credential/certificate content, PEM, file content, or authentication arguments. No reload-specific metrics are required by this decision.

## Consequences

Projected Secret rotation updates new mecak8s TLS handshakes without restarting the pod,
and a malformed intermediate projection cannot take down the listener or replace its
working identity. Directory-wide notifications can include unrelated writes, so reloads
may be attempted more often than strictly necessary; bounded debounce and transactional
validation make those attempts cheap and safe.

The file watchers and Redis generation manager are process-lifetime resources that their
owners must close. Redis reload can temporarily hold multiple clients: each displaced client
closes only after its last in-flight operation or migration lock lifecycle releases it.
Client-CA rotation still requires a rollout. Real-provider chart installs become secure by
default, with an intentionally named escape hatch for controlled development environments.

## See also

- [ADR 0048](./0048-mecak8s.md) — mecak8s deployment and shutdown contract
- [ADR 0206](./0206-oidc-authn-module.md) — OIDC authentication module
- [ADR 0233](./0233-secure-external-redis.md) — verified external Redis transport
- [Cloud-native resource inventory](./0027-cloud-native.md#list-1-resource-inventory)
