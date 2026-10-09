---
title: Configure mecak8s state and execution
description: Choose durable state and filesystem access for Kubernetes sessions.
sidebar_position: 1
---

# Configure mecak8s state and execution

Redis owns durable session state. Choose filesystem access separately, based on
the work sessions must perform.

## Choose filesystem access

`mecak8s` uses the same server-owned placement contract as `mecated`. Clients do
not send workspace paths or private environment references. By default, new
sessions have no filesystem access. Schedules retain that placement, and
delegation cannot add filesystem access that the parent lacks.

See
[Execution environments](/features/security-and-execution/execution-environments.md)
for the shared placement, no-FS, child-environment, and reattachment model.

## Native Kubernetes execution

For per-session PVC-backed workspaces,
[configure the execution provider](native-execution.md). Follow its
[authority and retained-state lifecycle](execution-provider-lifecycle.md) for
rotation, upgrades, retirement, and deletion.

When `execution.enabled` is `false`, the chart mounts no execution mTLS Secret
and passes no execution-provider flags. The separate provider and its existing
environments continue independently. Clients can explicitly request
`execution: {none: {}}` to keep a session filesystem-free.

## Mounted workspace (shared filesystem root)

To give sessions a real filesystem, mount a volume into the pod and point
`--workspace` at it (for example a PVC mounted at `/workspace`). A configured
root turns `mecak8s` into a **server-assigned filesystem deployment** rooted
there: every session is assigned that single root, the filesystem tools and
Shell operate on it, and clients have no field with which to select another
root. The path must be absolute and clean; a relative value is refused at
startup.

This does not change `mecak8s`'s storage-free posture: harness and session state
still live in Redis and the Kubernetes API, and the mounted volume holds only
agent working files. A root shared across the two default replicas needs a
`ReadWriteMany` volume; a `ReadWriteOnce` PVC binds to a single node, so scale
to one replica or use a per-pod volume if your storage class cannot do RWX. The
operator vouches for the mount, so scope it deliberately. Caller identity does
not isolate one caller's files from another caller using the same mount.

## Redis virtual workspace

Set `redis.filesystem.enabled=true` (or pass `--redis-filesystem`) to provide
persistent Read/ListDir/Edit/Write/Copy/Move/Remove/Grep/Glob files without
mounting a volume. Files are partitioned by the session owner's exact OIDC
issuer/subject pair; same-owner sessions share a namespace, while ownerless
sessions share a reserved anonymous namespace. The persisted placement is
revalidated on every run. Redis failures, missing namespace markers, and corrupt
records fail closed rather than appearing as an empty filesystem.

This mode is deliberately file-lite: it has no Shell, executable-file semantics,
git worktrees, or filesystem branch/merge workflow. It is mutually exclusive
with `workspace`. Set `redis.readLedger.enabled=true` independently to persist
each session's read-before-write evidence; deleting a session deletes that
ledger but not the principal's shared files. `mecak8s` sets no TTL on either
representation. Redis durability, backups, capacity, and eviction policy remain
operator concerns.

## State topology

`mecak8s` keeps session data in Redis and session ownership in the Kubernetes
API:

|State|Service|
|-|-|
|Session snapshots, retention, and cleanup|Redis|
|Durable event log and resume cursors|Redis Streams|
|Single-writer session lease|Kubernetes `coordination.k8s.io` Lease|

## Secure Redis credentials and TLS

Redis credentials reach `mecak8s` as **paths to files** projected from a
Kubernetes Secret volume. They never appear as container arguments, environment
variables, or ConfigMap entries. Mount the Secret read-only (`defaultMode: 0440`
is a good default), then point the flags at the mounted paths:

```text
--redis-url=redis.example.internal:6379                     # bare host:port, never a redis:// URL
--redis-username-file=/var/run/secrets/redis/username       # optional ACL username
--redis-password-file=/var/run/secrets/redis/password       # optional ACL password
--redis-tls-ca=/var/run/secrets/redis/ca.pem                # private CA...
--redis-tls                                                 # ...or verify against the system trust store
```

Every credential requires verified TLS, from one of two sources: the host's
system trust store (`--redis-tls`) for a managed Redis whose certificate chains
to a public CA, or a mounted PEM CA bundle (`--redis-tls-ca`) for a private one.
`--redis-tls-ca` **replaces** the system trust store rather than adding to it.
Both modes verify the server certificate against the hostname in `--redis-url`
(including IP SAN rules); hostname verification is never disabled and TLS 1.2 or
newer is required. TLS with no ACL is valid. ACL is optional: a password without
a username uses Redis's default ACL user, while a username requires a password.

For a managed Redis certificate that chains to a public CA, set
`redis.caKey: ""`. The chart then uses the system trust store and does not mount
a Redis Secret volume when the username and password keys are also empty.

`mecak8s` watches the lexical parent directories of every configured Redis CA,
username, and password file, so Kubernetes projected-Secret `..data` swaps are
observed. One coalesced event re-reads the **complete** configured file set. The
process builds fresh durability and follow clients through the same validation
and verified-TLS path, and publishes the pair only after bounded successful
PING/TLS/auth probes. Invalid or partially projected material leaves the last
valid client active; bounded single-flight retries cover the window where the
Secret projection and Redis-side ACL/trust update settle in different orders.
New operations use the replacement pair, while in-flight operations and
migration locks finish on their original durability client before it closes. No
Redis files configured means no reload watcher. Credential files may end in one
newline, as Kubernetes Secret projections commonly do; other whitespace remains
part of the credential.

`--redis-url` takes a bare `host:port`. A `redis://` or `rediss://` URL is
rejected on every path, plaintext included, and the rejection never repeats the
address back because a URL's userinfo can carry a password, and these errors
land in the operator's log.

Redis client-certificate (mTLS) authentication is not supported
and is tracked upstream at
[toolhive-core#240](https://github.com/stacklok/toolhive-core/issues/240).

:::warning[Plaintext Redis is fixture-only]

An address-only `--redis-url` is plaintext and unauthenticated, and is
**rejected at startup** unless you also pass `--redis-allow-plaintext`. That
opt-in exists for the disposable `values-kind.yaml` profile only (its in-chart
Redis fixture, with the mock provider and no auth). Production installs must not
set it: use verified TLS, and add ACL credentials when the managed Redis service
requires them.

:::

## Next steps

- [Configure caller access](/operating/mecak8s/identity-and-client-access.md).
- [Observe the deployment](/operating/mecak8s/observe-and-troubleshoot.md).
