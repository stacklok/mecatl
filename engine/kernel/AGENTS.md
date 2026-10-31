# AGENTS.md: isolated kernel

This is the isolated kernel experiment, not `agent.Engine`. Keep existing
production entry points unchanged unless Joe explicitly approves integration.

## Boundaries

- `history` owns public history values and persistence vocabulary, not execution
  or a storage backend. It imports the `history/id` identity leaf, which depends
  only on the shared `engine/id` utility and the standard library.
- Do not wrap, embed, alias, or import the legacy `session.Session` aggregate.
- Review new public contracts in coherent, useful slices before adding consumers.
- Keep model/provider, host, learning, and delegation machinery out of this slice.
- Public exports require API snapshots and compatibility classification under
  the engine module's existing policy.

## Code organization and documentation

These conventions apply only to `engine/kernel/`.

- Put the package overview in `doc.go`, not an arbitrary implementation file.
- Organize implementation files by owning type: `commit.go`, `record.go`, etc.
  Keep the declaration, methods, and type-specific helpers together, including
  validation, cloning, equality, and encoding/decoding. Avoid capability-wide
  files such as `dto.go` or `validate.go` that scatter each type's implementation.
- Separate genuinely shared primitives or policy types when they have a clear
  responsibility. Split a large file at a coherent ownership boundary, not one
  file per method. Do not introduce generic frameworks just to share a few lines.
- Organize tests by owning type too. Focused codec, malformed-input, or fuzz suites
  may be separate when that makes the cases easier to read.
- Code and code comments are authoritative for implemented behavior. Kernel design
  material under `docs/drafts/` is historical rationale, not a specification readers
  must consult to understand the implementation. Keep current contracts beside
  the owning code and prove them through tests.
- Document every public declaration for Godoc. Also document pivotal internal
  types and functions: responsibility, invariants, ownership, and ordering or
  failure semantics where relevant. Keep comments concise; explain contracts
  and non-obvious rationale rather than narrating the code.

## Contract and implementation discipline

- Make mutable-storage ownership, aliasing, zero values, and failure outputs
  explicit where relevant. Prefer concrete type methods over speculative shared
  marker, cloning, or freezing interfaces.
- Keep data-contract validation distinct from operational resource policy.
  Validate lengths and counts before the allocations or conversions they govern;
  use overflow-safe arithmetic. Do not rely on mutable dependency-global limits.
- Preserve opaque payloads without interpreting schemas owned elsewhere.
- Keep handwritten codecs bounded to the small schemas they serve; document
  interim limitations in code rather than silently copying the pattern to more
  persisted types. Preserve canonical bytes and trust-boundary checks when
  replacing the implementation.
- Prefer the simplest bounded implementation. An inexpensive sizing pass can be
  clearer than buffer-growth machinery; optimize demonstrated costs, not the
  number of loops in isolation.

See [README.md](README.md) for the implemented scope and focused checks.
