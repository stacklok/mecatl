---
id: 04-template-overrides
title: StatusML template execution and partial surfaces
blocked_by: [01-status-protocol, 02-status-settings, 03-ui-status-seam, 08-status-contract-corrections, 09-generator-foundation]
status: done
branch: plan-mecatui-status-line/04-template-overrides
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Implement the source-owned, process-free template source using validated user-global settings and raw canonical `Input`. The `Source`, not `ui`, selects header/footer variants from submitted available widths, evaluates templates, parses StatusML, and publishes `Result`. Build a private automatic StatusML-escaped template projection from raw input; commands continue to receive raw JSON. Escape every textual interpolation—including clock formatting output—before StatusML parsing, without exposing a user-visible raw/safe bypass or arbitrary template helpers. Retain shipped defaults independently for omitted header/footer surfaces; templates cannot control renderer-owned lanes.

## Acceptance criteria

- AC2.2: A template can compose status input values and StatusML tags; substituted values cannot forge layout/style markup or terminal controls.
  - verify: `TestStatusLine_Scenario2_TemplateEscapesValues`
- AC2.6: An override of only `header` or only `footer` replaces only that surface; the omitted surface renders the matching shipped default templates.
  - verify: `TestStatusCustomization_Scenario2_PartialSurfaceOverrideKeepsDefault`
