---
id: 02-persisted-resources
title: Persisted session schedule team and derived-resource enforcement
blocked_by: [01-ownership-core]
status: done
branch: "plan-caller-separation/02-persisted-resources"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Persisted session, schedule, team, and derived-resource enforcement

Apply the shared decision to application-facing persisted resources, including ownerless
records, forks/carryover, schedule fires, lists, and event streams. Deny as absence and
calculate list metadata after filtering.

## Acceptance criteria

- AC1.1: An OIDC-authenticated caller can access a session, schedule, team, or memory record whose durable `(issuer, subject)` owner equals the caller.
  - verify: `TestCallerSeparation_Scenario1_OwnerCanAccessOwnedResources`
- AC1.2: In OIDC mode, every ownerless pre-identity resource—including a session, schedule, team, user-model memory entry, project-memory entry, and its derived handle/event—is unavailable to every caller and is never adopted or rewritten on access.
  - verify: `TestCallerSeparation_Scenario1_OwnerlessResourcesAreNotAdopted`
- AC1.6: Bob cannot fork Alice's session or create a session with Alice's `source_session_id`; both source references return absence, create no destination, and leave Alice's owner and history unchanged. Alice's same-owner fork/carryover remains available.
  - verify: `TestCallerSeparation_Scenario1_ForkAndCarryoverAuthorizeSource`
- AC2.3: A list of sessions, schedules, teams, or schedule fires contains all and only resources owned by the requesting caller; its count, cursor, page boundary, and empty-page behaviour are computed from that caller-owned set, not a globally paginated set.
  - verify: `TestCallerSeparation_Scenario2_ListMetadataIsOwnerScoped`
- AC2.4: A direct load, update, or delete of another caller's persisted resource, including a schedule fire, is indistinguishable from loading a missing resource.
  - verify: `TestCallerSeparation_Scenario2_OwnerMismatchIsNotFound`
- AC2.5: Event-log access resolves through the session owner; Bob cannot retrieve Alice's session or schedule-fire event stream by any handle, while Alice can retrieve her own.
  - verify: `TestCallerSeparation_Scenario2_EventStreamsResolveParentOwner`
