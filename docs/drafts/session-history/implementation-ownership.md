# Implementation note: snapshots, ownership, and cloning

**Status:** Candidate implementation principles, not a change to the session-history contracts or an approved analyzer specification or implementation plan.

The implementation will pass rich state between execution, history, projections, and adapters.
Readers need to know whether that state can change underneath them.
Without a consistent ownership model, each caller must compensate with defensive checks or ad hoc clones, and it becomes difficult to establish whether accepted facts still describe the data being used.
Related work in [#2404](https://github.com/stacklok/mecatl/pull/2404) has raised the same concern around mutable aliases and independent snapshot capture and restore.

As implementation begins, look for repeatable patterns that give readers stable snapshots or, where mutation is necessary, explicit ownership and cloning boundaries.
This is an engineering consideration across the implementation, not another history layer, a requirement to make every object immutable, or authorization for a repository-wide refactor.
The [complete logical-input and transformation contract](session-commit-history.md#complete-logical-model-inputs-and-transformation-evidence) is one place where stable ownership matters; use the [review map](README.md) for the broader design.

## The guarantee readers need

Three guarantees are different:

| Guarantee | Meaning |
| --- | --- |
| Read-only reference | This caller cannot mutate through this reference. |
| Immutable value | Nobody can mutate the value or mutable state reachable from it. |
| Stable snapshot | Changes elsewhere cannot alter this reader's view. |

Read-only access alone does not establish snapshot stability.
Another owner may retain a writable alias to a map, slice, pointer, or nested object.
For example, returning a state struct by value does not isolate its nested argument map: a later mutation through the original map can still change what the reader sees.
A detached snapshot that also leaks a writable map back to callers has the opposite hole.

Go does not provide a general immutable-object qualifier.
`const` does not apply to structs, maps, or slices; a struct copy preserves aliases in reference-bearing fields; `maps.Clone` and `slices.Clone` are shallow.
An interface narrows the ordinary access path but does not establish immutable storage behind it.
Unexported fields provide a package boundary, not a boundary between files in the same package.

## Construct, detach, publish

For data that requires stable snapshots, prefer a lifecycle with a clear publication point:

1. Construct and validate privately.
2. Detach caller-owned mutable inputs, including nested mutable state.
3. End mutation of the snapshot and everything mutable reachable from it before publication.
4. Expose queries, safe value copies, or iterators that do not leak writable aliases, including aliases in yielded values.
5. Construct a new snapshot for updates rather than changing a published one.

Prefer value-only leaf types where practical.
Copying a collection of such values can establish independence without bespoke recursive cloning.
Sharing genuinely immutable substructure can also be safe; copying every reachable value is not the goal.
The goal is to eliminate writable aliases across the boundary, using an explicit clone contract where copies are needed rather than scattering defensive copies among callers.

Atomic publication can make a new snapshot reference available to concurrent readers.
It does not freeze the referenced object graph or coordinate concurrent writers' read-modify-publish sequences.
Writer coordination remains a separate responsibility.

Where mutable state is the better fit, make its owner and access rules explicit: who may mutate it, whether a returned value is borrowed or detached, and where ownership transfers or clones occur.
Synchronization must cover the period in which a reader relies on mutable data; locking only while returning an aliased reference does not give that reader a stable view.
Do not substitute an undocumented ownership transfer for detaching caller inputs.

## Consistency makes ownership locally understandable

Correctness alone is not enough if every type achieves it differently.
A reader should be able to recognize an adopted ownership pattern from its API and a short contract, without tracing callers and nested helpers to discover whether the data is shared, copied, or still mutable.
Deep analysis belongs in establishing and verifying the pattern; ordinary use should not require repeating that analysis.

During implementation, converge on a small set of recognizable patterns for immutable snapshots, detached mutable copies, and borrowed access to owner-controlled state.
Use consistent naming and semantics for construction, snapshot capture, cloning, and accessors wherever the guarantees are the same.
For each pattern, make input retention, nested-copy depth, returned aliases, and the permitted lifetime of borrowed access explicit.
A method named `Clone` should not leave readers guessing which nested mutable fields remain shared; distinguish weaker copy operations when needed.

Consistency does not require one generic clone helper or a common interface for unrelated types.
Type-specific implementations can follow the same ownership contract and regression-test shape.
Exceptions should be explicit and locally explained rather than silently weakening a familiar convention.
Select the initial patterns against representative boundaries, then reuse them as implementation grows instead of inventing a new ownership scheme for each component.

## Representation boundaries worth enforcing

Use a real package when the abstraction has a coherent responsibility.
Creating a package for every immutable type would fragment the code without necessarily improving ownership.
Within a larger package, consider a mechanically enforced implementation boundary, informally a "soft package":

```text
routing_snapshot.go       owns the concrete representation
routing_snapshot_test.go  may inspect that representation
other files               use only the designated API
```

An interface alone is not that boundary.
Same-package code can assert the concrete type and access its representation.
A downcast used to recover access deliberately omitted from an API should prompt a contract or design review, not become an accepted workaround.
This is particularly important when coding agents can discover and use the hidden representation despite comments asking them not to.

A narrow `go/analysis` checker is one candidate for making these boundaries enforceable.
It could resolve symbols and reject references to designated implementation types and members outside explicitly owning files, including concrete type assertions, type switches, and other representation access.
Representation access in tests would be limited to explicitly designated implementation tests, not allowed in every test file.
The public API would have to remain usable without naming restricted concrete types.
The exact designation mechanism, API shape, exceptions, and integration remain to be designed; no checker is selected or implemented by this note.

Such a checker would enforce visibility and abstraction integrity, **not prove deep immutability**.
Its value is confining representation access to a small area whose ownership can be reviewed and tested.
Detecting obvious alias leaks or mutations might be useful later, but general ownership inference across arbitrary Go is a much larger project.
Prefer a restrictive, checkable pattern over relying on that inference.

## Evidence for snapshot independence

For each adopted snapshot boundary, tests should show that:

- Mutating constructor inputs after construction does not change the snapshot.
- Mutating returned collections, where copies are returned, does not change it.
- Creating or publishing a later version leaves earlier snapshots unchanged.
- Nested mutable values satisfy the same guarantees, not just the outer map or slice.

Use the race detector as supplementary evidence.
Sequential aliasing bugs can violate snapshot stability without a race, so race-free execution alone does not establish the contract.

The direction to evaluate during implementation is to enforce where representation access is allowed, then establish and verify ownership within that boundary.
Choose concrete patterns against actual implementation needs before standardizing helpers or committing to a custom analyzer.
