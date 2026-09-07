---
id: 08-panel-canonical-provenance
title: Map rendered rows to canonical visible grapheme offsets
blocked_by: [07-panel-semantic-tool-regions]
status: in-progress
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/08-panel-canonical-provenance-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-08-panel-canonical-provenance-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Repair the final-panel blocker: row provenance must map each text-bearing
rendered row to its location in canonical ANSI-free semantic-region text,
excluding block indentation, borders, presentation padding, and soft-wrap
layout. A resize must restore an anchor captured on a wrapped continuation to
the same grapheme in the canonical text. Preserve cached-prefix behavior and
add real reflow regression coverage.

## Acceptance criteria

- AC1.3: Text-bearing regions map grapheme-safe offsets in canonical visible ANSI-free text through reflow using existing x/ansi grapheme semantics.
  - verify: `TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset`
