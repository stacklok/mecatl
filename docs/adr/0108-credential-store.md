# ADR 0108 — Internal encrypted credential-store substrate

- Status: Accepted
- Date: 2026-08-14
- Scope: host-internal opaque credential persistence
- Supersedes: none
- Superseded by: none

## Context

Future OAuth and MCP work needs durable secret material, but coupling storage to token
formats, provider configuration, implicit XDG paths, or a particular key source would
make the security boundary difficult to reuse and audit. Plain files do not protect an
offline copy. Locking the data file itself is insufficient because atomic replacement
changes its inode. Password derivation and environment fallback would invent unsafe key
acquisition policy in a storage adapter.

The first consumer and key-acquisition path are deliberately outside this decision. The
adapter therefore must be useful without changing default runtime behaviour or exposing
a new engine API.

## Decision

Create the host-internal `internal/adapter/credentialstore` port package; the engine is
not its consumer, so this boundary does not belong in `engine/port`. Its format-agnostic
`Reader` exposes read, capabilities, and lifecycle operations. `ConditionalWriter`
exposes only create-only, version-matched replace, and version-matched delete, and
mutable `Store` embeds both. Versions are opaque and a zero version is never a wildcard.
Mutability is expressed by interface implementation rather than a capability flag that
could contradict the method set. Ship a deterministic memory Store and a hardened local
encrypted-file Store adapter behind the same mutable-Store conformance suite.

Future environment or Kubernetes Secret-backed sources may implement Reader only.
Consumers that perform durable refresh-token rotation require a mutable CAS Store; they
must not silently accept a Reader and lose refreshed credentials. No such source or
consumer is implemented by this decision.

The file constructor requires an explicit absolute owner-only root, a validated
namespace, and exactly 32 injected key bytes. It performs no environment, XDG, config,
prompt, password, KDF, or keyring lookup. It uses AES-256-GCM with random 12-byte nonces.
A strict bounded versioned envelope authenticates its header and location-bound AAD
(namespace plus complete arbitrary-byte record key). The SHA-256 digest of the complete
persisted envelope is the CAS version, so a fresh nonce prevents identical-value and
delete/recreate ABA.

Hash namespaces and record keys into full SHA-256 physical names. Serialize every full
read/create/replace/delete transaction with an exclusive, stable, never-unlinked
per-record flock sentinel. Authenticate an existing record before evaluating a conflict
or mutation. Commit writes through an owner-only exclusive same-directory temporary
file, file sync, atomic rename, and directory sync where supported. Delete retains the
lock sentinel. Cancellation observed at the final pre-commit gate prevents commit; an
atomic rename or remove already in progress cannot be canceled.

On supported local Unix targets, reject symlinks, special files, hard-linked files,
wrong ownership, and non-exact modes (`0700` directories, `0600` files). Unsupported
platforms fail before filesystem side effects rather than selecting a weaker fallback.
`Close` serializes with operations and clears the store-owned long-lived key copy.

## Consequences

Copied record and crash-temporary files do not reveal plaintext without the injected
key, tampering and wrong-key use fail as corruption, and cooperating local processes get
atomic CAS. No new crypto, keyring, cloud, or locking dependency is introduced; the
existing flock dependency and standard-library cryptography are sufficient. The package
is not wired into composition, so it creates no default files and acquires no key.

The guarantee is deliberately local and limited. Root and malicious same-UID processes,
process-memory inspection, file access patterns and lengths, secure SSD/journal erasure,
and unreliable non-local filesystem flock/rename semantics are outside the threat model.
An attacker can replay an older valid envelope because there is no external monotonic
ledger. A crash may leave an encrypted owner-only temporary file. A directory-sync error
after rename/remove means the mutation may already have committed, so callers must read
before retrying. Clearing the owned key does not prove erasure of prior Go or AEAD copies.
OAuth integration, per-client routing, key acquisition, and remote/Kubernetes stores
remain deferred.

## See also

- [Architecture overview](../architecture.md#internal-credential-store)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md#credential-store)
- [Production readiness](../design/PRODUCTION-READINESS.md)
- [ADR 0027 resource inventory](./0027-cloud-native.md#list-1-resource-inventory)
