# Isolated execution kernel

This directory contains the isolated kernel experiment alongside the existing
runtime. Its public package is currently `history`, which supplies the strongly
typed `HistoryID` using the shared [`id`](../id/id.go) utility. There is no execution
facade or storage implementation here yet; production entry points continue to
use the existing engine.

`history` depends on `id` and the standard library. Neither package depends on the
legacy session aggregate, model providers, or host composition. The
[kernel design note](../../docs/drafts/session-history/implementation-kernel.md)
explains the intended execution and history boundaries. Follow the scoped
[implementation guidance](AGENTS.md) when changing this directory.

Run the focused offline checks from the repository root:

```sh
cd engine && go test -race ./id ./kernel/...
```
