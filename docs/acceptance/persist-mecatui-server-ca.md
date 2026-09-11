# Persist saved mecatui server CA — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — persisting target-specific server trust and superseding ADR 0287 establishes a durable client transport and security decision.
**Decision record:** [ADR 0327](../adr/0327-persisted-mecatui-server-ca.md)
**Phase:** focused mecatui remote-login capability
**Status:** in-progress, 2026-09-08
**Delivery:** Split. A separate plan/interface PR is required by the Architectural workflow; no waiver has been authorized.
**Expected tasks:** 1
**Workflow waiver:** Pending explicit human authorization. This combined implementation candidate cannot be treated as an approved or complete contract until that waiver is granted (or the required plan/interface PR is completed).
**Issue:** [#886](https://github.com/stacklok/mecatl/issues/886)

A private gRPC server CA supplied as `mecatui login --server-tls-ca PATH` is stored with the target and restored by a later saved `mecatui connect ADDRESS`. Login `--tls-ca` remains issuer-only; an explicit `connect --tls-ca` wins for that invocation.

## Human decisions

- [x] Server CA flag name — Decision: `--server-tls-ca`, to distinguish it from login's issuer-only `--tls-ca`.

## Interface contract

- **gRPC / protobuf:** None — client-local registry metadata changes only.
- **Exported Go APIs / interfaces:** None — `clientauth` is an internal adapter.
- **Tool schemas:** None — no agent tool changes.
- **CLI / config:** `mecatui login --server-tls-ca PATH` saves an absolute, clean server CA path.
- **Events / persistence:** Saved connection rows gain optional `server_ca_file`; absent legacy rows use existing system-root behavior.
- **Security / authority:** Saved OIDC connections still require verified TLS; issuer and server CA roots remain separate, and `connect --tls-ca` overrides the saved server CA.
- **Compatibility / migration:** Existing rows without `server_ca_file` remain readable and usable without migration.

### Scenario 1 — save and restore server trust

Given a private-CA gRPC server and a successful remote OIDC login, preserving the
issuer/server trust split in [ADR 0287](../adr/0287-target-aware-mecatui-tls.md):

- AC1.1: login saves a separately named server CA path while retaining the issuer CA path for OIDC operations only.
  - verify: `TestRemoteLoginStoresAbsoluteCAReferencesAcrossCWDChanges`
- AC1.2: a saved connect restores the saved server CA, while a non-empty `connect --tls-ca` takes precedence.
  - verify: `TestResolveTransportUsesPersistedServerCAWithExplicitOverride`
- AC1.3: legacy registry rows with no server CA remain readable.
  - verify: `TestRegistryReadsLegacyIssuerCAAndMigratesOnMutation`

## Definition of done

- The focused login, saved-CA selection, and legacy registry regression tests pass.
- `task docs` and `task site:build` pass.

## Out of scope

| Item | Decision |
|---|---|
| CA contents | Never persist them; only validated path references are saved. |
| TLS verification policy | Unchanged. |
| OIDC issuer trust | Remains exclusively `login --tls-ca`. |
