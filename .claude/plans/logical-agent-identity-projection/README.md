# Logical-agent identity projection plan state

**Acceptance plan:** `docs/acceptance/logical-agent-identity-projection.md`  
**ADR:** `docs/adr/0252-logical-agent-identity-projection.md`  
**Accumulator:** `acc/logical-agent-identity-projection`  
**Base:** `acc/identity-issuer-substrate`

Tasks are ordered by the typed-value → issuance → verification dependency chain. Rotation/leak evidence and the vertical authorization proof may proceed after typed verification lands.

**Gate policy:** workers run only focused package tests. Repository-wide `task lint`, `task test`, `task docs`, and aggregate checks run once on the assembled accumulator after every task is merged.
