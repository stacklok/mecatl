# ADR 0233 — Secure external Redis on the shared connection layer

- Status: Accepted
- Date: 2026-08-19
- Scope: Redis session-store adapter, mecak8s composition and Helm deployment
- Supersedes: none
- Superseded by: none

## Context

`mecak8s` keeps session snapshots and the durable event log in Redis. The original
address-only adapter served the disposable Kind fixture, but a managed Redis deployment
needs verified TLS and Secret-mounted connection material without placing credential
values in flags, chart values, diagnostics, or errors. Managed services do not all
authenticate the same way: some require a default ACL password, some a named ACL user,
and some none at all. Treating a password as mandatory for every secure connection
excludes TLS-only services; accepting credentials without verified TLS silently
downgrades a production connection.

A shared client layer already sits in mecatl's dependency graph:
`github.com/stacklok/toolhive-core/redis` (Apache-2.0), pinned at `v0.0.39` and already a
direct dependency of the in-tree `authn/oidc` module via `toolhive-core/authn`. Its
`Config` + `NewClient` cover the mechanical half — TLS assembly, client construction,
connectivity Ping — and additionally supply standalone/cluster/sentinel topologies and
explicit dial/read/write timeout defaults. The enterprise distribution's own Redis
session storage delegates to it rather than hand-rolling a client. Adopting it costs no
new modules: the package's transitive imports are go-redis (already a direct dependency),
`cespare/xxhash`, `go.uber.org/atomic`, and stdlib — `go.sum` is unchanged, only
`go.mod`'s indirect marker moves.

The one capability the shared layer lacks is client-certificate authentication: its
`TLSConfig` carries `InsecureSkipVerify` and `CACert` but no client certificate, so
mutual TLS cannot be expressed through it.

## Decision

Delegate Redis client construction, TLS assembly, timeout defaults, and the fail-fast
connectivity Ping to `github.com/stacklok/toolhive-core/redis`. Keep in this adapter only
what that layer leaves to its callers: reading credentials from mounted files, and the
policy that binds them.

**Credentials require verified TLS.** Verified TLS is satisfied two ways: the host's
system trust store (`--redis-tls`, chart `redis.caKey: ""`), or a PEM CA bundle that
replaces it (`--redis-tls-ca`, chart `redis.caKey`). Certificate verification is never
disabled — `InsecureSkipVerify`, which the shared layer exposes, must not be set — TLS
1.2 is the floor, and hostname verification binds the certificate to the configured
endpoint host: go-redis derives `ServerName` from the dial address, so an IP endpoint
requires an IP SAN.

Keep address-only Redis as the explicit plaintext compatibility path for local and Kind
fixtures, opted into with `AllowPlaintext` (`--redis-allow-plaintext`). The default path
must never infer plaintext from an address alone.

Read username, password, and CA only from configured file paths. Do not accept secret
values as CLI or Helm values, and do not include their contents in errors. Remove one
terminal LF or CRLF from username/password files, but preserve all other whitespace.

Make ACL authentication optional: a password without a username uses the default Redis
ACL user; a supplied username requires a non-empty password; supplied but empty username
or password files are invalid.

**No client-certificate (mTLS) support.** There are no `--redis-tls-cert` /
`--redis-tls-key` flags, no `redis.clientCertKey` / `redis.clientKeyKey` chart values,
and no `ClientCertFile` / `ClientKeyFile` adapter fields. Restoring it is tracked
upstream at [stacklok/toolhive-core#240](https://github.com/stacklok/toolhive-core/issues/240);
mecatl carries no local reimplementation in the meantime.

Validate `host:port` on **every** construction path, plaintext included, and reject any
address containing `@` or `/`. Never echo the address in that rejection: `--redis-url`
invites a URL, and a URL's userinfo can carry a password into a diagnostics log. Once
validated the address is safe to name, so the connect failure still identifies the
endpoint.

The Helm chart's external profile requires an endpoint; its Secret projection follows the
configured keys. An install using system-trust TLS with no ACL keys mounts no Secret at
all, and `credentialsSecret` is required exactly when some key needs reading. Only the
configured keys are projected, at `/var/run/secrets/redis` with `defaultMode: 0440` —
never the whole Secret. The local profile renders neither the Secret volume nor the
secure-Redis flags, and carries the explicit plaintext opt-in.

## Consequences

One Redis client construction path across the Stacklok Go estate, with timeout defaults
and cluster/sentinel topologies now reachable should mecak8s need them — no flags expose
them yet, so today's deployments are standalone.

Managed Redis with a publicly-rooted certificate is deployable with verified TLS and no
CA bundle to source. A CA-file requirement would have left those operators — Azure Cache
for Redis and ElastiCache in-transit encryption among them — with no verified-TLS
configuration available and the plaintext opt-in as the path of least resistance.

A Redis requiring client-certificate authentication is **not** deployable until
toolhive-core#240 lands.

Address-only Redis remains insecure and must stay confined to the explicit local/Kind
compatibility path.

Credential files are read once at construction, so rotation requires a restart — and the
shared layer forecloses the cheap fix, because its `Config` takes credential values
rather than a go-redis `CredentialsProviderContext` hook. Rotation without restart would
need that hook upstream too.

## See also

- [mecak8s operator guide](../usage/mecak8s.md)
- [ADR 0048 — mecak8s](./0048-mecak8s.md)
- [Cloud-native resource inventory](./0027-cloud-native.md)
