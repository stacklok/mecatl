# Legacy status-line parity migration

## Goal

Replace the legacy default header/right-footer generators with shipped static
source templates while preserving behavior for the same UI state, width, and
theme. The differential oracle permits only the agreed omission of the dynamic
agents shortcut binding.

## Task sequence

### P1 — Legacy contract harness

Create test-only legacy output fixtures and a differential harness. Cover header
identity/mode/connection, footer activity separation, context pressure, cache
facets, team/parallel/subagent state, widths, and themes. Assert plain text,
semantic spans, selected responsive tier, and single-line output.

### P2 — Display atoms and context-meter primitive

Expose narrow delegation display atoms needed by static templates (running,
finished, total); retain detailed raw state counts. Add a semantic context-meter
primitive that resolves bar glyphs and pressure tokens without ANSI/OSC.

### P3 — Static defaults and parity

Express default header/right-footer as static source templates plus P2 atoms.
Restore session digest, provider/model/route, mode, muted connection, delegation
summaries, context bar, and usage facets. Make the differential harness green.

### P4 — Legacy deletion

Delete old UI default header/footer generators, context/usage/delegation candidate
selection, and any duplicate default rendering path. Retain renderer-owned left
activity lane, mandatory header chrome, stable geometry, and final theme render.
Add a structural anti-regression test.

### P5 — Final documentation and review

Reconcile operator/public docs with final Input atoms and StatusML primitive.
Run aggregate gates and panel review.

## Invariants

- Generated right/status surfaces never wrap.
- Left activity lane is not customizable.
- Header safety/navigation lanes are renderer-owned.
- Source defaults own all default status behavior after P4.
- No shortcut bindings appear in Input.
- Context meter is semantic/theme-resolved; usage facets are static template
  composition.
- Preserve the operator's unstaged `user-docs/mecatui/status-line.md` edit.
