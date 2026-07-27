---
id: 01-spec-field-and-validation
title: ScheduleSpec.OriginSessionID field + create-seam validation
blocked_by: []
status: done
branch: "plan-fire-result-delivery/01-spec-field-and-validation"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

Add the `OriginSessionID string` field to `port.ScheduleSpec` (engine/port/schedule.go)
with the field-by-field contract doc (empty = no delivery; metadata-only, never
rendered into a prompt). Wire create-seam validation in
`internal/adapter/server/schedule.go` (`validateScheduleSpec`): a non-empty
`OriginSessionID` that names a NON-existent session is rejected fail-closed (the
same `ErrInvalidArgument` class as the other spec rejections). The field must
round-trip through the store (jsonlstore/redisstore/memschedulestore persistence —
it is part of the Spec, so it serialises with the rest; confirm the store
conformance still passes with the new field populated).

This touches the engine's exported surface — run `task api:update`, commit the
changed `engine/api/*.txt`, and add an `engine/CHANGELOG.md` note (Added = minor)
per ADR 0037.

## Acceptance criteria

- AC1.2: A schedule created out-of-band (REST/gRPC, no conversation) persists an
  empty `OriginSessionID` and behaves exactly as before.
  - verify: `TestFireDelivery_Scenario1_OutOfBandCreateHasEmptyOrigin`
- AC1.3: A create whose `OriginSessionID` names a non-existent session is
  rejected fail-closed by the create-seam (the same class as the other spec
  rejections).
  - verify: `TestFireDelivery_Scenario1_UnknownOriginRejected`
