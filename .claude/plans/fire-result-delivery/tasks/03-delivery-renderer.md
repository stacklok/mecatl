---
id: 03-delivery-renderer
title: Fenced-untrusted delivery renderer (renderFireDelivery)
blocked_by: []
status: done
branch: "plan-fire-result-delivery/03-delivery-renderer"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

A pure renderer sibling to `renderCarriedContext`
(`internal/app/scheduler_fire.go:257`): given a fire's terminal `EvResult`
(schedule name, fire id, stop reason, final text), produce a FENCED-UNTRUSTED
harness note using the EXACT `agent.FenceUntrusted` + `agent.NeutraliseFraming`
discipline carried context uses. The note is framed as data ("a scheduled task
reported"), never as a live instruction.

Contract:
- Provenance header names the schedule name + fire id; the WHOLE header (incl.
  the attacker-influenceable schedule name, which is model-authored at create and
  only validated non-empty) is passed through `NeutraliseFraming` and placed
  INSIDE the `FenceUntrusted` block — the header's trusted framing and the
  attacker-chosen name are defanged alike.
- The fire's final text is rune-clamped to a bounded budget (a new
  `fireDeliveryMaxRunes`, mirroring `carriedContextMaxRunes`).
- A fire that ended with no meaningful text (empty terminal) still renders a note
  carrying the stop reason — never a silent blank.
- A forged `<<<UNTRUSTED` closing marker or harness section header inside the
  fire's text is neutralised (stays inside the fence).

This is a pure function — test it directly (the domain layer takes no fakes).
Plant-and-watch-fail for the absence assertions (forged framing, clamp).

## Acceptance criteria

- AC2.1: The rendered note wraps the fire's outcome in `agent.FenceUntrusted`
  with a provenance header naming the schedule and fire id.
  - verify: `TestFireDelivery_Scenario2_RenderFencesOutcome`
- AC2.2: A fire text containing a forged `<<<UNTRUSTED` closing marker or a
  harness section header is neutralised — the rendered note keeps it inside the
  fence.
  - verify: `TestFireDelivery_Scenario2_NeutralisesForgedFraming`
- AC2.3: An over-long fire text is clamped to the delivery rune budget (the note
  never blows the origin conversation's context window).
  - verify: `TestFireDelivery_Scenario2_ClampsToRuneBudget`
- AC2.4: A fire that ended with no meaningful text (an empty terminal) still
  renders a note carrying the stop reason — the delivery is never a silent blank.
  - verify: `TestFireDelivery_Scenario2_EmptyTerminalStatesStopReason`
- AC2.5: The provenance header's model-authored fields are neutralised. A
  schedule name containing a forged `Tool:`/`Policy:`/`<<<UNTRUSTED` line is
  passed through `NeutraliseFraming` and the whole header is placed INSIDE the
  `FenceUntrusted` block.
  - verify: `TestFireDelivery_Scenario2_ProvenanceHeaderNeutralised`
