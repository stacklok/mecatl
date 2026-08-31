---
id: 02-direct-draft-inactivity
title: Preserve inactive direct SkillDraft behavior after intent admission
blocked_by: [01-explicit-procedure-intent]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add integration coverage through the real direct SkillDraft and learning-admission paths proving that detecting a procedure request changes admission only. Preserve ADR-0111's body-only Draft lifecycle and prevent either admission or rejection from activating a direct draft.

## Acceptance criteria

- AC1.4: A direct `SkillDraft` remains Draft/inactive after this detector admits or rejects a procedure request.
  - verify: `TestCloudNativeLearning_Scenario1_DirectSkillDraftRemainsInactive`
