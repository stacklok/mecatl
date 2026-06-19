# `engine/api/` — committed public-API snapshots

The `*.txt` files in this directory are the committed, human-readable text
snapshots of the engine module's **public API surface** — the exported
identifiers of the seven COMPATIBILITY-COMMITTED core packages
(`session`, `governance`, `tool`, `prompt`, `port`, `team`, `agent`).

They exist so external consumers can read the surface they depend on, and so a
change to that surface is impossible to land unnoticed: the `api-compat` gate
(`task api:check`) re-derives the surface on every PR and fails if it differs
from what is committed here.

## Workflow

- **Do not hand-edit these files.** They are generated.
- After an intentional change to a core package's exported API, regenerate with
  `task api:update` and commit the changed `*.txt`.
- Classify the change in [`../CHANGELOG.md`](../CHANGELOG.md) per the policy in
  [`../COMPATIBILITY.md`](../COMPATIBILITY.md) (Added = minor, Changed/Removed =
  breaking).

The dumper tooling itself lives in the **root module** (`internal/apicheck`) so
the engine module's `go.mod` stays free of the `go/tools` dependency
(ADR 0036); only these text baselines ship inside the engine module.

See [`../COMPATIBILITY.md`](../COMPATIBILITY.md) and
[ADR 0037](../../docs/adr/0037-engine-stability-contract.md).
