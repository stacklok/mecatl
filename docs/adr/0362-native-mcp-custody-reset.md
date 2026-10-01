# ADR 0362 — Root-scoped native MCP custody reset

- Status: Proposed
- Date: 2026-09-25
- Scope: attended host-local recovery of native MCP credential custody
- Supersedes: ADR 0345 decision 5 and its wrapping-key garbage-collection deferral only for the explicit reset operation

## Context

The first native MCP custody prototype wrote a three-field `version: 1` marker. Later builds retained that version while adding pending recovery, namespace binding, and initialization identity. The strict current reader therefore rejects the prototype marker. A root move also does not reset a root-derived OS-keyring account, so manually moving files can leave a new pending marker paired with the old artifact.

Normal profile removal must continue to preserve shared custody and must not become a force-forget path. Recovery nevertheless needs an attended operation that can discard the complete native custody set without touching legacy `key_env` records or upstream registrations.

## Decision

`mecated mcp reset-custody NAME [--file PATH]` is the supported recovery operation. `NAME` selects a configured native local-key profile and therefore one canonical credential root; the operation is root-scoped and reports all configured native profiles sharing that root. It preserves settings and profiles — including a legacy `key_env` profile sharing the root, which reset never touches — performs no network or DCR operation, and requires an attended confirmation containing the exact anchor name.

The reset recognizes either the exact prototype marker (including its historical plain locator digest) or the current strict marker. It fails closed on malformed metadata, unsafe files, locator drift, or a root with no marker at all. Under the existing root lock it first rewrites the marker in place to a durable `resetting` state that carries the retired-namespace name it has committed to, having already proven the artifact's locator digest against the marker it is replacing. It then retires only the native namespace, removes the root-pinned keyring/file artifact, and removes and syncs the marker. Reset only removes: it never republishes custody itself, so the next `Resolve` (from `mcp login` or `mcp add`) creates it fresh through the existing pending/ready protocol. `Resolve` and `Open` refuse a `resetting` marker rather than treating it as ready, pointing the operator back at `reset-custody`.

A prototype marker predates namespace separation, so there is usually no native namespace directory to retire; retiring it is a no-op that only records the retired directory. Resetting a prototype marker never removes the shared pre-namespace directory: the old wrapping artifact is discarded, making the old native records unusable, while any legacy `key_env` records in that directory remain physically untouched and continue to use their independently supplied key. Current markers retire only the native namespace.

Retired namespace directories are intentionally retained: an already-running process may hold an open namespace after the rename. The old wrapping key is removed, so a new process cannot reopen it. Retired directories are owned by the custody root and may be cleaned by a later explicitly designed maintenance operation; reset does not claim upstream revocation or process-memory erasure.

## Consequences

Operators can recover incompatible or mismatched native custody without calculating a root hash, handling a secret, editing Keychain, or moving XDG roots. All affected native profiles require fresh login. Legacy namespaces (including a `key_env` profile sharing the reset root), unrelated roots, settings, provider credentials, and upstream DCR registrations remain outside the reset boundary.

The reset is destructive and intentionally attended. It resumes any interruption from the durable `resetting` marker rather than by counting retired directories on disk, so resume behaves identically whether this is the root's first reset or its fifth: the root lock and the marker's own recorded retired-directory name are what make interruption resumable, never leaving an unmarked artifact silently adoptable. The retained retired directory is a durable resource owned by the custody adapter; its cleanup and restart behavior are reset-by-design and must remain inventoried with the custody implementation.

## See also

- [ADR 0345 — Host-local direct MCP onboarding and credential custody](./0345-direct-mcp-onboarding.md)
- [Direct MCP onboarding acceptance plan](../acceptance/direct-mcp-onboarding.md)
- [MCP OAuth and credentials](https://github.com/stacklok/mecatl/blob/main/user-docs/features/mcp-oauth-and-credentials.md)
