# AGENTS.md: isolated kernel

This is the isolated kernel experiment, not `agent.Engine`. Keep existing
production entry points unchanged unless Joe explicitly approves integration.

- `history` owns public history values and persistence vocabulary, not execution
  or a storage backend. Its only current engine dependency is `id`.
- Do not wrap, embed, alias, or import the legacy `session.Session` aggregate.
- Review new public contracts in small steps before adding their consumers.
- Document every public declaration for Godoc. Also document pivotal internal
  types and functions: responsibility, invariants, ownership, and ordering or
  failure semantics where relevant. Keep comments concise; explain contracts
  and non-obvious rationale rather than narrating the code.
- Keep model/provider, host, learning, and delegation machinery out of this slice.
- Public exports require API snapshots and compatibility classification under
  the engine module's existing policy.

See [README.md](README.md) for the implemented scope and focused checks.
