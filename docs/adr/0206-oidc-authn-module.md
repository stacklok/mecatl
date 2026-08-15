# ADR 0206 — Ship reusable OIDC caller identity as an opt-in module

- Status: Accepted
- Date: 2026-08-12
- Scope: Engine caller-identity projection, OIDC validation, module and release boundaries
- Supersedes: None
- Superseded by: None

## Context

The engine accepts a verified principal through context but deliberately has no JWT,
OIDC, HTTP, or ToolHive dependency. The server already validates OIDC tokens with
`toolhive-core/authn`; embedders need the same security behavior without importing the
root module's full adapter cone. Putting that validator in the engine would destroy the
engine's small, provider-neutral dependency closure.

## Decision

Keep claim projection in the stdlib-only `engine/session` package. It accepts only an
already-verified claims map, requires non-empty string `iss` and `sub`, and can never
mint a system or anonymous principal.

Ship the ToolHive-backed verifier as the separate opt-in Go module
`github.com/stacklok/mecatl/authn/oidc`. Its public API exposes only its own narrow
configuration, validator, errors, and engine principal; ToolHive types do not cross the
boundary. The root CLI adapter delegates to this module and retains its existing flags,
server error contract, system context, and close lifecycle. Release it under
`authn/oidc/vX.Y.Z`; it is not an LLM provider module.

## Consequences

Engine-only consumers retain the dependency-free identity seam. OIDC embedders opt into
the validator's network and cryptographic closure explicitly. Engine and authn module
releases must be coordinated when the authn module begins using a newly added engine
API, and each module is tested, linted, tidied, and vulnerability-scanned separately.
The engine release must come first: `authn/oidc` cannot be released or pass its
`GOWORK=off` standalone gate until a published engine minor includes the exported
`PrincipalFromClaims` and `GrantTypeFromClaims` APIs it imports. The authn manifest must
not hide that ordering with a local `replace`; workspace builds remain the pre-release
integration proof, while the failing standalone check is the explicit release gate.

## See also

- [Architecture guide](../architecture.md)
- [ADR 0204 — Caller identity threading](./0204-caller-identity-threading.md)
- [ADR 0205 — Bounded JWKS staleness](./0205-bounded-jwks-staleness.md)
