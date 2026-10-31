# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the existing
runtime. `history/id` supplies the strongly typed `HistoryID` using the shared
[`id`](../id/id.go) utility. `history` re-exports that type and retains its
constructor/parser entry points. `history/format` supplies opaque, comparable
commit CIDs with strict text parsing and formatting, plus mutable `Commit` and
`Record` persistence DTOs. Their `Clone` methods detach nested mutable storage;
`Equal` compares data with nil and empty slices equivalent. Neither operation
validates or freezes the DTOs. There is no commit codec, execution facade, or
storage implementation here yet; production entry points continue to use the
existing engine.

The dependency direction is `history` → `history/id` → `engine/id` and
`history/format` → `history/id`, with only standard-library imports alongside
those edges. The identity leaf supplies strongly typed history identities to
format DTOs without importing higher-level history contracts. These packages do
not depend on the legacy session aggregate, model providers, or host composition.
The [kernel design note](../../docs/drafts/session-history/implementation/kernel.md)
explains the intended execution and history boundaries. Follow the scoped
[implementation guidance](AGENTS.md) when changing this directory.

Run the focused offline checks from the repository root:

```sh
cd engine && go test -race ./id ./kernel/...
```
