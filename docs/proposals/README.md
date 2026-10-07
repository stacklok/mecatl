# Proposals

Proposals are designs, not current behavior; don't rely on them for how Mecatl works.
When one is implemented, the implementing PR moves what is now true into
[`docs/`](../README.md) or [`user-docs/`](../../user-docs/intro.md) and deletes the
proposal; abandoned proposals are deleted too, and git history keeps them.

Every proposal starts with a three-line header: `Status: exploring | accepted |
implemented | abandoned`, `Owner: <name>`, and `Last updated: <YYYY-MM-DD>`. `task docs`
checks the header and fails on `Status: implemented`, because an implemented proposal
should already have been folded into the current docs and deleted.

| File | Owner | Status | Summary | Decide by (keep, move to an issue, or delete) |
|---|---|---|---|---|
| [agent-identity-model.md](agent-identity-model.md) | Juan Antonio Osorio | exploring | Strawman for agent identity: Mecatl as its own SPIFFE trust domain with JWT delegation chains. | **2026-11-06** |
| [agent-identity-outbound.md](agent-identity-outbound.md) | Juan Antonio Osorio | exploring | Part 2 of the identity model: how the harness reaches tools and gateways on the outbound hop. | **2026-11-06** |
| [cloud-native-harness-kit.md](cloud-native-harness-kit.md) | Juan Antonio Osorio | exploring | Positioning definition of the cloud-native harness kit and where its responsibility ends. | **2026-11-06** |
| [cloud-native-harness-systems.md](cloud-native-harness-systems.md) | Joe Beda | exploring | Speculative scoping of open questions for cloud-native harness systems. | **2026-11-06** |
| [scoped-resource-grants.md](scoped-resource-grants.md) | Joe Beda | exploring | Strawman for short-lived, attenuable grants and direct tool-to-service data paths. | **2026-11-06** |
| [session-state-and-read-ledger.md](session-state-and-read-ledger.md) | Jeremy Drouillard | exploring | Proposal to make the read-before-write ledger session-owned, extensible state. | **2026-11-06** |
| [agent-fabric-protocol.md](agent-fabric-protocol.md) | Dániel Kántor | exploring | Draft HTTP/JSON protocol for remote files, folders, and callable actions. | **2026-11-06** |
| [mecak8s-vmcp-delegation-contract.md](mecak8s-vmcp-delegation-contract.md) | Jakub Hrozek | exploring | Work record of the mecak8s and vMCP token-delegation contract qualification. | **2026-11-06** |
| [contextual-guardrails-capacity.md](contextual-guardrails-capacity.md) | Juan Antonio Osorio | exploring | Work record of capacity calibration evidence for contextual guardrails. | **2026-11-06** |
| [surface-migration-plan.md](surface-migration-plan.md) | Joe Beda | accepted | Template for moving `mecatui` overlays onto the `surface` interface; partly implemented. | **2026-11-06** |
