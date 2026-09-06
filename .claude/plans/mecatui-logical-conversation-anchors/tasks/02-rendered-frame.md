---
id: 02-rendered-frame
title: Cached rendered frame provenance
blocked_by: [01-conversation-state]
status: done
attempt: 1
branch: plan-mecatui-logical-conversation-anchors/02-rendered-frame-attempt-1
worktree: .scratch/worker-mecatui-logical-conversation-anchors-02-rendered-frame-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Make `renderer` produce a concrete cached `renderedFrame` with lockstep lines
and forward/reverse semantic provenance. Preserve the incremental cached-prefix
fast path; provenance must follow the same invalidation lifetime and must not
add a full rendered transcript. Instrument the current renderer sufficiently
for chrome, body, reasoning, arguments, result, artifact, and appendix regions.

## Acceptance criteria

- AC1.2: A rendered frame carries lockstep provenance and preserves byte-identical visible output for cached/fresh collapsed/expanded renders.
  - verify: `TestADR_0301_RenderedFrameProvenanceMatchesLines`
- AC1.3: Text-bearing regions map canonical visible ANSI-free grapheme offsets through reflow; derived rows use row fallback.
  - verify: `TestADR_0301_ReflowRestoresCanonicalVisibleTextOffset`
- AC1.8: Cached-prefix/frame-coalescing paths retain their complexity and no second transcript.
  - verify: `TestADR_0301_ScrollbackFrameRetainsLinearMetadataOnly`
