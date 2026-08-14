---
id: 03-authoritative-transcript
title: Snapshot-derived authoritative transcript surface
blocked_by: [01-session-taxonomy]
status: done
branch: "plan-session-continuity-ux/03-authoritative-transcript"
worktree: ".scratch/worktrees/session-continuity-ux-task-03"
issue: "471"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Add a pure, ownership-checked public transcript surface over one SessionStore.Load. Project the loaded Conversation into proto/client transcript data without Environment resolution, engine rebuild, lease acquisition, or persistence. Keep EventLog activity replay separate and explicitly non-authoritative. Add gRPC/HTTP and mecatui-client mappings but no overlay changes yet.

## Acceptance criteria

- AC3.1: The public transcript surface performs one ownership-checked SessionStore load and returns the exact human-displayable `Conversation.Messages` from that coherent aggregate, including a genuinely-empty idle session as complete with zero messages; it performs no environment resolution, engine rebuild, lease, or persistence.
  - verify: `TestSessionContinuityUX_Scenario3_AuthoritativeTranscript`
- AC3.2: Unknown, corrupt, and failed snapshot loads return typed errors; they never appear as a successful empty transcript.
  - verify: `TestADR_0108_TranscriptAbsenceIsNotEmptySuccess`
- AC3.3: Compacted sessions expose the current summary/tail the model will use, and provider-private reasoning replay blobs are neither displayed as human text nor required for transcript completeness.
  - verify: `TestSessionContinuityUX_Scenario3_CompactedAndReasoningTranscript`
- AC3.4: EventLog activity availability/completeness is reported separately; missing terminal events, append gaps, read failures, and EOF never upgrade an activity stream to an authoritative transcript.
  - verify: `TestInvariant_event_replay_never_attests_model_context`
