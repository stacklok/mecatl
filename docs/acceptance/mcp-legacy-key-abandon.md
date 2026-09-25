# Abandon unavailable legacy MCP custody — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this adds one narrowly eligible recovery operation to the established host-local MCP settings workflow without changing credential formats, lifecycle schemas, or subsystem boundaries.
**Decision record:** None — the change composes existing strict profile validation and atomic settings mutation; its local-only exception and retained-state limits are fully specified here.
**Phase:** direct-MCP local lifecycle recovery
**Status:** in-progress, 2026-09-25. The approved bounded contract is being implemented on this branch.
**Delivery:** Split. The directing human explicitly waived a separate Plan / Interface PR; contract review and implementation remain sequential checkpoints on this implementation branch.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1921](https://github.com/stacklok/mecatl/issues/1921).

Allow a local operator who permanently lacks a legacy direct-DCR profile's external wrapping key to stop configuring that profile. The explicit operation removes only the selected settings entry and leaves the inaccessible encrypted records untouched.

Ordinary removal remains the recoverable lifecycle operation. Abandonment is not credential cleanup or upstream revocation; it is a narrow host-availability escape hatch for absent or empty legacy `key_env` custody.

## Human decisions

- [x] Treat the operation as a bounded exception rather than a new durable lifecycle design. — Decision: the directing human explicitly overrides ADR 0345 Decision 5 and direct-onboarding AC6.3 only for the selected settings-only abandonment defined here; ordinary removal and every other key-unavailable, corrupt, mismatched, pending, uncertain, or read-only state remain unchanged.
- [x] Name the exceptional CLI switch `--force`. — Decision: the directing human chose the familiar force spelling; eligibility remains limited to absent or empty legacy `key_env` custody and does not become a general removal bypass.
- [x] Keep plan and implementation on one branch. — Decision: the directing human explicitly waives a separate Plan / Interface PR while retaining plan review before implementation.
- [x] Retain inaccessible ciphertext without defining migration. — Decision: ordinary re-enrollment under new native custody may create a new registration; manually restoring the exact legacy custody and profile remains existing recovery behavior and is not performed or prevented by abandonment.

## Interface contract

- **gRPC / protobuf:** None — abandonment remains host-local administration and adds no daemon RPC, message, or generated contract.
- **Exported Go APIs / interfaces:** None — the importable `engine/` module and all exported interfaces remain unchanged; implementation stays in root-internal settings and command code.
- **Tool schemas:** None — no model-facing tool gains profile-administration authority or a schema change.
- **CLI / config:** `mecated mcp remove NAME [--file PATH]` gains the boolean flag `--force`. Despite its general name, the flag is accepted only when the selected entry is a structurally valid direct OAuth DCR profile using local legacy `key_env` custody and that exact environment variable is absent or has an empty value both during eligibility inspection and on a final recheck immediately before settings publication. Nonempty malformed values are not eligible. The command rejects native `local.key` custody, environment/read-only credentials, preregistered and CIMD OAuth clients, non-OAuth profiles, malformed settings, missing profiles, and profiles eligible for ordinary removal. Without the flag, removal behavior is unchanged.
- **Events / persistence:** The eligible operation atomically removes only the selected MCP settings entry through the existing narrow, stale-write-detecting settings mutation. It does not open, decrypt, enumerate, migrate, tombstone, overwrite, or delete any credential, grant, lifecycle, marker, or wrapping-key record. Existing encrypted state remains at its current path.
- **Security / authority:** The existing local operator authority and exact writable settings target apply. Eligibility inspection reads only validated non-secret profile metadata and whether the referenced environment variable is absent or empty; it never exposes or interprets a nonempty value. The operation makes no network, browser, keyring, OAuth, MCP, or upstream-revocation call. Output contains no environment value, key, client ID, token, registration body, or encrypted record content.
- **Compatibility / migration:** Existing profiles and ordinary removal retain their current behavior. Abandonment performs no migration and does not make retained ciphertext readable under new custody. Normal re-enrollment under native custody uses its separate namespace and may create a new upstream client registration because abandonment did not revoke the old one. If an operator later reconstructs the exact legacy profile and restores its external key, existing legacy recovery behavior is unchanged; abandonment neither promises to prevent nor attempts that manual recovery.

## In scope — 1 scenario, in implementation order

### Scenario 1 — Explicitly abandon an unavailable legacy direct-DCR profile

The host-local lifecycle and atomic settings ownership remain those of the [direct MCP onboarding plan](direct-mcp-onboarding.md). The directing human explicitly authorizes this settings-only exception to the key-unavailable prohibition in [ADR 0345 Decision 5](../adr/0345-direct-mcp-onboarding.md#decision) and direct-onboarding AC6.3. The exception does not enter or alter encrypted lifecycle state, and all ordinary removal cases continue to follow those contracts.

**Acceptance:**
- AC1.1: `mecated mcp remove NAME --force` succeeds only for the selected, structurally valid direct OAuth DCR profile with local legacy `key_env` custody when that exact reference is absent or empty at eligibility inspection and remains absent or empty on a final recheck immediately before settings publication. If the final observation is nonempty, the command rejects the operation without changing settings.
  - verify: `TestMCPAbandonUnavailable_Scenario1_FinalEligibilityRecheck`
- AC1.2: The operation rejects readable legacy custody, a nonempty malformed legacy key, native custody, non-DCR clients, non-local credentials, non-OAuth profiles, missing profiles, malformed settings, and every state where ordinary removal can proceed; rejection leaves settings and credential state unchanged.
  - verify: `TestMCPAbandonUnavailable_Scenario1_RejectionsPreserveState`
- AC1.3: An eligible operation atomically removes only the selected settings entry while preserving unrelated settings, comments, MCP entries, and stale-write protection.
  - verify: `TestMCPAbandonUnavailable_Scenario1_Eligibility`
- AC1.4: Eligibility and execution do not open or enumerate the credential store and make no lifecycle, grant, key, marker, network, browser, keyring, MCP, or upstream-revocation call.
  - verify: `TestMCPAbandonUnavailable_Scenario1_Eligibility`
- AC1.5: Success states that the profile was abandoned locally, encrypted records were retained, no upstream client was revoked, and later re-enrollment may create a new registration, without disclosing secret or opaque OAuth values.
  - verify: `TestMCPAbandonUnavailable_Scenario1_Eligibility`
- AC1.6: Ordinary `mecated mcp remove NAME [--file PATH]` retains its existing lifecycle-driven behavior and output when the new flag is absent.
  - verify: `TestDirectMCPOnboarding_Scenario5_BoundedTruthfulStatus`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Recovering, decrypting, migrating, or deleting retained legacy records | Separate credential-migration work | Missing custody grants no authority over encrypted state. |
| Revoking the upstream OAuth client | Separate networked revocation design | The operation is strictly local and works without credentials or network access. |
| Abandoning native keyring or file custody | Separate recovery design | Native custody has different markers and recovery invariants. |
| General force-removal for corrupt or uncertain lifecycle state | Separate contract | This exception is limited to absent or empty legacy `key_env` custody. |

## Definition of done

1. Every named Scenario 1 proof passes with offline, test-owned settings, environment, credential, and external-call fakes.
2. Focused `cmd/mecated` and affected settings-adapter tests pass.
3. `task lint`, `task test:race`, `task docs`, and `go run ./cmd/mecademo` pass on the final candidate.
4. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- None. Any request to abandon readable, native, corrupt, or otherwise uncertain custody is contract drift and requires a separate decision.
