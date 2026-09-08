# Mecatui server-owned discovery scopes — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** Remote mecatui OAuth bootstrap correction
**Status:** proposed, 2026-09-08. Planned from [stacklok/mecatl#1261](https://github.com/stacklok/mecatl/issues/1261).
**Delivery:** Split. The change resolves a discovered-login CLI contract and OAuth consent/authority policy before implementation.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#1261](https://github.com/stacklok/mecatl/issues/1261).
**Plan PR:** absent until opened
**Approved baseline:** absent until approved

Protected-resource discovery makes the remote server, not a mecatui user flag, the authority for the requested scope set. A server that publishes `scopes_supported` supplies the exact set requested by discovered login. A server that deliberately omits it selects the existing fixed compatibility baseline, `openid,profile,offline_access`, so the first-use confirmation remains safe and enrollment proceeds.

The server's configured `oidc.scopes` is the only way to select non-baseline discovered-login scopes. Legacy explicit OIDC enrollment retains its existing `--scopes` behavior because it has no discovered server profile. This corrects the contradictory scope statements in the historical ADR-0305 record and its acceptance record without weakening discovery's confirmation or terminal-output safety checks.

## Human decisions

- [x] The scope authority and omission fallback are server-owned for discovered login. — Decision: discovery rejects `--scopes`; a present `scopes_supported` is requested exactly, while an absent member selects only `openid,profile,offline_access`. Administrators configure `oidc.scopes` to select other scopes. Legacy explicit login keeps `--scopes`.

## Interface contract

- **gRPC / protobuf:** None — protected-resource metadata and the mecatui command flow change without a gRPC or protobuf contract change.
- **Exported Go APIs / interfaces:** None — the affected helpers in `cmd/mecatui` are command-private.
- **Tool schemas:** None — this is not an agent tool surface.
- **CLI / config:** `mecatui login ADDRESS --scopes ...` is rejected on the protected-resource discovery route; its legacy explicit-identity behavior is unchanged. Server `oidc.scopes` remains the administrator-controlled way to advertise a non-baseline discovery scope set.
- **Events / persistence:** None — the selected scopes continue through the existing confirmed `clientauth.Connection` and enrollment persistence format; no new event or field is introduced.
- **Security / authority:** A discovery user cannot broaden, narrow, or otherwise override server-selected scopes. A present `scopes_supported` is the exact operator allowlist; its omission means only the fixed, non-empty OIDC compatibility baseline, never an arbitrary caller-provided set. Existing strict confirmation-display sanitization remains unchanged.
- **Compatibility / migration:** Existing servers that omit `scopes_supported` become enrollable with the documented baseline. Callers currently supplying discovery-mode `--scopes` receive a deliberate actionable rejection; legacy explicit OIDC login remains compatible. This decision supersedes only ADR 0305's scope-selection clauses.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — Deterministic server-owned discovered scope selection

`mecatui login ADDRESS` determines its requested scopes from the protected-resource profile and passes the exact confirmed connection to the existing browser-login seam. It must distinguish an omitted `scopes_supported` member from a present member so that omission selects the compatibility baseline while a configured member retains its exact-set authority. The existing profile and confirmation boundary remain governed by [ADR 0305](../adr/0305-oauth-protected-resource-discovery.md), as clarified by its successor, and by the protected-resource implementation in [`cmd/mecatui/login.go`](../../cmd/mecatui/login.go).

**Acceptance:**
- AC1.1: Discovery metadata with a non-empty `scopes_supported` list causes mecatui to request exactly that validated, deterministically ordered list.
  - verify: `TestADR_0305_DiscoveredScopeSelection`
- AC1.2: Discovery metadata that omits `scopes_supported` causes mecatui to request exactly `openid,profile,offline_access`, display that resolved set at confirmation, and pass the same set unchanged to the login seam.
  - verify: `TestMecatuiServerOwnedDiscoveryScopes_Scenario1_OmittedMetadataUsesBaseline`
- AC1.3: A discovery-mode `--scopes` flag is rejected before protected-resource discovery, browser launch, credential-store initialization, registry mutation, or enrollment; it cannot override either an advertised set or the omission baseline.
  - verify: `TestMecatuiServerOwnedDiscoveryScopes_Scenario1_RejectsDiscoveryScopesFlag`
- AC1.4: An omitted `scopes_supported` member is distinct from a present empty array: the former selects the baseline, while the latter and malformed, duplicate, comma-smuggled, or otherwise invalid advertised values fail closed. Unsafe confirmation-display fields remain rejected; no empty-scope exception weakens that boundary.
  - verify: `TestMecatuiServerOwnedDiscoveryScopes_Scenario1_ScopesMemberPresenceMatrix`, `TestADR_0305_DiscoveredScopeRejectsCommaSmuggling`, `TestConfirmDiscoveredLoginRejectsTerminalControls`
- AC1.5: Explicit identity login (`--issuer`, `--client-id`, and `--audience`) retains its current `--scopes` override behavior.
  - verify: `TestADR_0277_ExplicitEnrollmentCompatibility`

### Scenario 2 — One authoritative scope-policy narrative

The living guides describe the final behavior, while a new ADR records why scope selection is server-owned and supersedes only the contradictory ADR-0305 clauses. The historic acceptance record is corrected so its scope-selection criterion no longer conflicts with the frozen ADR. This follows the documentation lifecycle in [ADR 0002](../adr/0002-documentation-lifecycle.md), the acceptance-plan contract in [`README.md`](README.md), and the protected-resource documentation in [`architecture.md`](../architecture.md).

**Acceptance:**
- AC2.1: A successor ADR records the server-owned scope authority, absent-member baseline, discovery-mode flag rejection, legacy-mode compatibility, and the limited supersession of ADR 0305.
  - verify: inspection — the successor ADR has a precise `Supersedes` clause and cites ADR 0305
- AC2.2: Architecture, operator usage, and public remote-server documentation consistently state that discovered login follows advertised scopes or the fixed omission baseline, and that administrators configure `oidc.scopes` for non-baseline scopes.
  - verify: `task docs`, `task site:build`
- AC2.3: The historical protected-resource acceptance record no longer promises caller-selected discovered scope subsets.
  - verify: inspection — `docs/acceptance/oauth-protected-resource-discovery.md` AC4.5 matches the successor ADR

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Dynamic client registration and arbitrary user-selected OAuth scopes | Future protected-resource profile work | Discovery scope selection remains server-owned. |
| A new server configuration mode for an empty scope request | Future explicit product decision | Omission selects the non-empty compatibility baseline; empty scopes remain invalid. |
| Changes to server-side scope-based authorization | Future authorization work | `oidc.scopes` remains a public-client request profile, not server authorization policy. |
| Changes to legacy explicit OIDC enrollment flags | None | Preserve current compatibility behavior. |

## Definition of done

1. The successor ADR, this plan, and the acceptance-plan index pass `bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/mecatui-server-owned-discovery-scopes.md` and `bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh`.
2. The targeted offline scope-selection and shorthand-enrollment tests pass, including the omission-baseline and discovery-flag-rejection proofs.
3. `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass.
4. `go run ./cmd/mecademo` remains green.
5. The implementation PR links the approved Plan / Interface PR and reports no unwaived `/panel-review` blockers.

## Deferred decisions and known risks

- The baseline includes optional `profile` and `offline_access`; an issuer can deny either request. Existing user documentation must retain the honest refresh consequence: an initial login may work without a refresh token, but a later expiry requires login again.
- The metadata parser must represent member presence explicitly: omission selects the baseline, while a present empty array, `null`, non-array shape, invalid element, or duplicate field fails closed. It must not silently treat malformed present metadata as omission.
