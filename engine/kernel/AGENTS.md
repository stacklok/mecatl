# AGENTS.md: isolated kernel

This is the isolated kernel experiment, not `agent.Engine`. Keep existing
production entry points unchanged unless Joe explicitly approves integration.

## Boundaries

- `history` owns public history values and persistence vocabulary, not execution
  or a storage backend. Its `history/id` values depend on the shared `engine/id`
  utility and the identity-free `codec` runtime. Resource and content identity
  owners may import `codec`, never the reverse.
- Do not wrap, embed, alias, or import the legacy `session.Session` aggregate.
- Review new public contracts in coherent, useful slices before adding consumers.
- Keep model/provider, host, learning, and delegation machinery out of this slice.
- Keep generated public API snapshots current. During this early kernel experiment,
  skip compatibility notes and changelog bookkeeping.

## Code organization and documentation

These conventions apply to `engine/kernel/` and kernel-specific changes in its
supporting adapters and tools, including `cborgen`. Apply them to new and changed
contracts and compare neighboring kernel types for coherence. Record established
patterns here as they emerge; keep unrelated legacy cleanup out of scope.

- Put the package overview in `doc.go`, not an arbitrary implementation file.
- Put persistence DTOs, versioned schemas, generated codecs and payload-local
  validation in the owning layer's `format` subpackage, as in `history/format`
  and `session/format`. Keep these packages independent of aggregates and their
  operational policy. Public domain values are separate from DTOs so persistence
  can omit information supplied by its enclosing context; validate that context
  at the conversion boundary rather than silently dropping conflicting values.
- Organize implementation files by owning type: `commit.go`, `record.go`, etc.
  Keep the declaration, methods, and type-specific helpers together, including
  validation, cloning, equality, and encoding/decoding. Avoid capability-wide
  files such as `dto.go` or `validate.go` that scatter each type's implementation.
- Keep a record DTO's exclusively owned sub-structs in the record's file, with
  their methods and helpers. Put types shared across records in clearly named
  files; generated codec references alone do not constitute sharing. Separate
  genuinely shared primitives or policy types by responsibility, not one file per
  method. Do not introduce generic frameworks just to share a few lines.
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
- Persistence DTOs document the long-term format. Document each type and field's
  meaning, identity scope, required/optional status, zero/empty behavior, ordering,
  and ownership where relevant. Distinguish payload-local validation from checks
  supplied by the enclosing context or aggregate.
- Mark speculative semantics beside the affected declaration: state the current
  guarantees, which design assumptions need usage experience, and when to revisit
  them. A provisional design note does not relax validation or permit silent
  reinterpretation of stored data.

## Contract and implementation discipline

- Public occurrence IDs are distinct, scoped identities for equality and
  correlation. Keep numeric allocation and successor arithmetic internal rather
  than exposing ordering as an identity contract. Use consistent identity naming
  across public values and persistence DTOs; document omitted enclosing scope.
- Use string constants for categorical fields, grouped by the field/type they
  describe. Document and validate closed vocabularies; preserve provider-owned
  open values without inventing a closed enum. Bound labels used in resource
  reservations and cover the largest supported encoded shape.
- Make mutable-storage ownership, aliasing, zero values, and failure outputs
  explicit where relevant. Prefer concrete type methods over speculative shared
  marker, cloning, or freezing interfaces.
- Keep data-contract validation distinct from operational resource policy.
  Validate lengths and counts before the allocations or conversions they govern;
  use overflow-safe arithmetic. Do not rely on mutable dependency-global limits.
- Preserve opaque payloads without interpreting schemas owned elsewhere.
- Keep generated persistence codecs bounded to their supported schemas. Preserve
  canonical bytes and trust-boundary checks when changing generator or runtime
  behavior.
- Prefer the simplest bounded implementation. An inexpensive sizing pass can be
  clearer than buffer-growth machinery; optimize demonstrated costs, not the
  number of loops in isolation.

See [README.md](README.md) for the implemented scope and focused checks.
