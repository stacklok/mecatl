# Kernel history adapters

These packages support the [isolated kernel history subsystem](../../kernel/README.md).
Existing production entry points do not use this subsystem.

[`history.History`](../../kernel/history/history.go) owns DTO validation,
canonical encoding and decoding, content identity checks, ordering, retry
classification, and read policy. Adapters implement the byte-facing
[`history.Backend`](../../kernel/history/backend.go) contract: atomic
expected-head appends, coherent indexed observations, and reads bounded before
allocating or reconstructing commit bytes. Keep the shared history protocol in
`History` so each backend only implements its storage guarantees.

[`mem`](mem/backend.go) retains complete histories in process-local memory and
provides no durability across process loss. [`conformance`](conformance/history.go)
exercises the backend contract and the real `History` implementation together.
Its optional allocation checks require a quiescent in-process fixture.
