---
matlatl: orphan-intentional
paths:
  - "**/*.modelith.yaml"
  - "**/*.modelith.md"
---
# Domain models contain domain definitions

Domain models contain definitions, relationships, invariants, and domain scenarios.
Never add implementation or delivery status: proposed, planned, unimplemented,
shipped, deferred, in progress, or awaiting review. This applies to definitions,
relationship notes, invariant statements, and scenario names and steps alike.
A model is not a plan or a report of what code has shipped.

Keep approval and delivery status in issues or PRs.
Edit the Modelith YAML source and re-render its Markdown; never hand-edit generated output.
