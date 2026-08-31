# ADR 0258 — Bounded scoped HTTPS keep-alive reuse for OIDC

- Status: Accepted
- Date: 2026-08-30
- Scope: private HTTPS OIDC transport connection reuse and remote mecatui recovery
- Supersedes: ADR 0235 (keep-alive policy); ADR 0260 (credential-recovery storage classification only)

## Context

ADR 0235 required a fresh DNS-pinned check for every request by disabling HTTP
keep-alives. That preserved the private-address boundary, but made repeated OIDC
refresh, validation, and revocation requests pay a new connection setup cost. Reuse
is safe only if an idle connection cannot turn a previously approved address into a
permanent routing decision, and if the transport has a bounded lifetime and cleanup
owner.

ADR 0260 describes recovery for an unusable local credential, but its broad recovery
wording can conflate repairable encrypted-record corruption with unavailable local
storage or issuer trust. Those failures have different safe next actions.

## Decision

Replace the no-keep-alive policy in ADR 0235 with bounded scoped HTTPS keep-alive
reuse. The transport may maintain a finite idle connection pool owned by the OIDC
client, and the owner must close it when the client is closed. Pool and idle
lifetimes are finite; the transport must not create an unbounded process-lifetime
resource.

Every new dial re-resolves the approved hostname and connects only to the
intersection of the newly resolved addresses and the approved address set. Reusing
an existing connection does not widen that set or bypass the transport's scoped
origin checks. Retain HTTPS-only admission, same-origin and endpoint validation,
redirect refusal, explicit CA roots, and Go TLS hostname verification. Caller-owned
HTTP clients remain rejected in private HTTPS mode.

Supersede only ADR 0260's broad credential-recovery classification as follows:
repairable, atomically proven corruption of the local credential record may use the
same-target browser-free reauthentication path while its transaction and identity
preconditions hold. Unavailable or unreadable local storage, missing keyring access,
or unavailable issuer trust are not repairable corruption: preserve the credential
and target metadata, report the corresponding closed recovery reason, and require
storage/issuer remediation or a browser-free retry as appropriate. Browser-free
retry means rerunning the existing `mecatui login ADDRESS --no-browser` flow; it is
not device flow and must not silently overwrite an unavailable record.

## Consequences

Repeated OIDC requests can reuse a bounded number of authenticated HTTPS connections,
while DNS admission remains enforced whenever a new connection is established. A
connection retained in the idle pool may outlive a DNS answer, so pool bounds and
owner cleanup are mandatory operational behavior. Existing TLS, origin, CA, and
redirect protections remain in force.

Recovery can distinguish a record that is safe to repair from infrastructure that is
not safe to overwrite. Operators may need to fix local keyring/storage or issuer CA
trust before retrying; no recovery path turns an unavailable backend into evidence
of corruption.

## See also

- [ADR 0235 — Scoped private HTTPS OIDC transport](./0235-scoped-private-https-oidc-transport.md)
- [ADR 0260 — Remote mecatui OIDC client authentication](./0260-remote-mecatui-oidc.md)
- [ADR 0257 — Remote mecatui logout budget](./0257-remote-mecatui-logout-budget.md)
- [TUI guide](../tui.md)

---
