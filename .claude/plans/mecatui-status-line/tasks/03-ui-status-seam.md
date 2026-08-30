---
id: 03-ui-status-seam
title: Pure UI status seam and responsive shipped defaults
blocked_by: [01-status-protocol]
status: done
branch: plan-mecatui-status-line/03-ui-status-seam
worktree: ""
issue: ""
retries: 1
last_error: "worker reported missing prerequisite despite a547817c being reachable from accumulator"
accumulator: acc/mecatui-status-line
---

# Task brief

Add a pure injected local-status seam to `ui`; it must not read XDG/YAML or start processes. Route current header and footer behavior through shipped full/compact/minimal defaults while preserving current no-settings chrome and goldens. UI/layout remains sole authority for available widths, variant selection, alignment, clipping, posture/scroll/changed-file header lanes, footer activity lane, and keyboard help. Render parsed StatusML through active theme.

## Acceptance criteria

- AC2.1: With no status-line override, shipped templates reproduce the current header and status/usage row while keyboard help remains unchanged.
  - verify: `TestStatusLine_Scenario2_DefaultTemplatesPreserveChrome`
- AC2.3: Full, compact, and minimal variants are measured and selected independently per surface; custom header content gets only columns remaining after safety lanes, and custom footer content is right-justified after the renderer-owned activity lane.
  - verify: `TestStatusCustomization_Scenario2_ReservedLanesAndResponsiveSelection`
- AC2.4: Header posture, scroll, and changed-file indicators remain renderer-owned and visible when applicable, regardless of a custom header template.
  - verify: `TestStatusLine_Scenario2_HeaderSystemIndicatorsSurviveOverride`
- AC5.3: Existing behavior without settings and the default-template behavior stay covered by offline golden tests.
  - verify: `TestStatusLine_Scenario5_DefaultCompatibility`
