# AGENTS.md: DTO generation

This tool supports the isolated kernel experiment, not production agent execution.
Follow [kernel guidance](../../engine/kernel/AGENTS.md) for contract documentation,
ownership, code organization, and focused tests; it also applies across this module
boundary. The command subtree inherits this guidance.

- Keep capability policy separate from the shared typed whole-state graph.
- Never execute owner code. Load each module with `GOWORK=off` and load-only overlays.
- Render and typecheck the selected closure before installing any artifact. Preserve
  deterministic output, explicit owner provenance, and marked regular-file protection.
- View-only declarations select local handles via `-types`; infer private
  `value *DTO` backing and read default field policy from DTO `view` tags.
  Context-bearing children require handwritten navigation, never an automatic
  adapter with missing scope. Source/layout/tag/docs changes require regeneration.
- Regenerate through `task generate:data`; check with `task generate:data:check`.
  See [README.md](README.md) for supported shapes and declaration syntax.
