---
matlatl: orphan-intentional
paths:
  - "contracts/proto/**"
  - "internal/adapter/server/**"
  - "engine/session/**"
  - "engine/agent/**"
  - "engine/learning/**"
  - "cmd/mecatui/client/**"
  - "sdk/typescript/src/**"
  - "adapters/grpcdriver/**"
---
# Wire API compatibility

Before changing remote-observable behavior, follow the [wire API policy](../../docs/architecture/api-surface.md#wire-api-compatibility).
Engine and mapper semantics count; unchanged protobuf fields do not prove compatibility.

If an existing conforming client would need changes to remain correct, stop and tell the operator
what contract breaks, which clients are affected, the proposed major bump, and upgrade implications.
Obtain explicit approval before implementing the break or changing the major.

Use stable feature identifiers for discoverable additions; client-required features alone do not require a bump.
Disabled capabilities are not version skew. Assess Go source and stored-data compatibility separately.

Every wire-affecting PR must include an **API compatibility** statement: assessment, rationale, major/feature changes, approval for any break, and upgrade implications. Record it in a plan when used; plans are optional.
