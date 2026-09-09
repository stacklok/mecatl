# ADR 0318 — Headless mecatui credential backend selection

- Status: Proposed
- Date: 2026-09-09
- Scope: local remote-`mecatui` credential backend selection and persistence
- Supersedes: ADR 0277's keyring-only credential-backend selection clauses
- Superseded by: none

## Context

Remote `mecatui` currently assumes a desktop keyring for its encrypted credential
store. That is unsuitable for a first login on a headless Linux host, but silently
falling back after a keyring error could split or lose credentials. A plaintext
backend can improve portability only when its reduced confidentiality and its
selection authority are explicit.

The decision must preserve ADR 0277's target-bound credential lifecycle and its
local mutation guarantees. It must not broaden OAuth grants or make an ordinary
keyring initialization noninteractive.

## Decision

Select exactly one credential backend per canonical local clientauth root and pin
it in strict, non-secret versioned metadata before OAuth begins. The pin controls
login, connect, refresh, reauthentication, and logout; no later operation selects,
falls back, switches, or migrates a backend. A cancelled login retains its pin.

`mecatui login` alone accepts `--credential-store=auto|keyring|file`. On fresh
Linux `auto`, a private same-executable helper performs a 500 ms, read-only,
no-autostart D-Bus ownership check for Secret Service. Only absence or the joined
helper timeout selects the owner-only plaintext file backend; present proceeds to
ordinary keyring initialization. Cancellation, denied or malformed D-Bus work,
and helper failures fail closed. macOS `auto` retains keyring-default behavior;
explicit `file` bypasses keyring access on both platforms.

Store plaintext records only below the distinct `clientauth-plaintext/` directory
under the canonical root. Require `0700` directories and `0600` metadata, records,
temporaries, and locks, plus the existing local-file checks, atomic CAS, sync, and
lock protections. Disclose file selection once before OAuth with the approved
neutral line; documentation states that this is plaintext at rest and does not
protect against same-account access.

For a root without metadata, structurally valid registry-reachable legacy
connection evidence pins keyring before any secret read. Invalid metadata or
legacy evidence fails closed. File-backed roots require upgrade-only use: concurrent
older clients sharing that root are unsupported.

## Consequences

Headless Linux first use can enroll without requiring a running desktop keyring,
while established roots keep a deterministic backend even when their environment
changes. A keyring failure cannot silently downgrade confidentiality, and a file
failure cannot cause a keyring probe or migration.

The file backend deliberately weakens confidentiality at rest. Operators must
choose it knowingly, upgrade every client sharing its root, and rely on documented
owner-only filesystem protections rather than encryption. The private detector
adds a bounded internal helper protocol and platform-specific tests, but it never
becomes a public command surface or a keyring-health probe.

This ADR supersedes only ADR 0277's keyring-only backend-selection premise. ADR
0277 remains authoritative for remote OIDC enrollment, credential identity,
refresh, recovery, logout, and transport.

## See also

- [ADR 0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [ADR 0277 — Remote mecatui OIDC client authentication](./0277-remote-mecatui-oidc.md)
- [Headless mecatui credential storage acceptance plan](../acceptance/headless-client-credential-storage.md)
- [Architecture guide](../architecture.md)
- `user-docs/mecatui/remote-servers.md`
