# Mecatl Studio schedules — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — adds the schedules product surface (`/api/v1/schedules…`) to the Studio BFF and the schedules workspace to the web app, inside the boundary ADR 0351 fixed; no new durable architecture decision.
**Decision record:** None — the boundary, the published-SDK rule, and the session model are ADR 0351's; the daemon-side scheduling contract is the existing ScheduleService (see [architecture](../architecture.md#scheduled-tasks)); this plan only projects it through the BFF.
**Phase:** capability — second feature layer of the Studio stack
**Status:** proposed, 2026-09-21. Fifth layer of the Studio `gh stack`; scope decisions were taken by the assistant under the directing human's go-ahead and are recorded below for review.
**Delivery:** Split, under the same stacking exception as [the bootstrap plan](studio-bootstrap.md): this plan is a stack layer, the implementation is the next layer, and nothing merges until the whole series is reviewed.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1736](https://github.com/stacklok/mecatl/issues/1736).
**Plan PR:** [stacklok/mecatl#1757](https://github.com/stacklok/mecatl/pull/1757)
**Approved baseline:** absent until the stack root merges

Port the prototype's schedules feature onto Studio: the BFF projects the daemon's schedules
(cron and one-shot triggers, permission mode, tool profile, fire history) into a product
contract that is capability-gated on the deployment's `scheduling` flag, and the web app gains
the schedules workspace — a list, a create/edit form with a cron builder and a natural-language
phrase parser, and a detail page with the fire history.

## Human decisions

- [x] Scope of the port. — Decision: the whole prototype schedules feature, BFF and web, including the browser-side cron builder and phrase parser; nothing daemon-side changes.
- [x] Contract provenance. — Decision: schemas, paths, and operation ids are ported byte-for-byte from the prototype; the `profile` field reuses the chat contract's `sessionToolAccess` enum (`all` | `noFilesystem` → the daemon's `no-fs` profile).
- [x] Capability gating. — Decision: `GET /api/v1/schedules` never fails on an unsupported deployment; it returns `supported: false` with a human-readable `reason` and an empty list, and every mutation answers `501` `schedule_unsupported`. Support is read live from the negotiated runtime snapshot's `capabilities.scheduling`, not cached at startup.
- [x] Mutations and the bootstrap's security gates. — Decision: create, update, actions, and delete sit behind the bootstrap's same-origin + double-submit CSRF check and, with interactive login active, the `401` session gate; the web app's request interceptor supplies the header.
- [x] Update semantics. — Decision: `PUT` re-sends the request's fields and preserves, from the daemon's current spec, the fields the product contract does not expose (`carryContext`, `fireTimeout`, `limits`, `misfire`, `parts`, `selector`, `singleton`); a one-shot trigger zeroes `maxFires`, a cron trigger zeroes the one-shot retry fields. The trigger itself is immutable on update: the daemon keeps a schedule's stored next fire when its spec is updated, so a changed trigger would be accepted and never honoured. The BFF compares the request's trigger (kind, cron expression and timezone, or the one-shot instant compared as an instant) with the stored one and refuses a difference with `409` `schedule_trigger_immutable`; the form locks the trigger of an existing schedule and says that changing when it runs means creating a new schedule.

## Interface contract

- **gRPC / protobuf:** None — the BFF drives the daemon exclusively through the published SDK's `schedules` namespace (`list`, `get`, `create`, `update`, `delete`, `pause`, `resume`, `fireNow`, `listFires`).
- **Exported Go APIs / interfaces:** None — no Go source changes.
- **Tool schemas:** None — Studio adds no model-facing tool.
- **CLI / config:** None — no new variable, flag, task, or compose setting.
- **Events / persistence:** No streams and no browser persistence. Routes under `/api/v1`: `GET /schedules` (`listSchedules` → `{supported, reason, items[]}`, items sorted by name), `POST /schedules` (`createSchedule` `{name, prompt, trigger, mode, mutating, profile, maxFires, oneShotRetry, oneShotMaxRetries}` → `201` schedule), `PUT /schedules/{name}` (`updateSchedule`, same body without `name` → `200` schedule), `POST /schedules/{name}/actions` (`actOnSchedule` `{action: fire|pause|resume}` → `204`), `DELETE /schedules/{name}` (→ `204`), `GET /schedules/{name}/fires` (`listScheduleFires` → `{items[]}` newest first). A `trigger` is `{kind: "cron", expression, timezone}` or `{kind: "once", at}` where `at` is an ISO 8601 instant carrying an explicit offset (`Z` or a numeric offset), the only one-shot representation that crosses the API, and `timezone` is an IANA zone name or empty for UTC. The browser edits that time through a `datetime-local` control, which has no offset and so cannot distinguish the two occurrences of an ambiguous local time; the form resolves the ambiguity itself rather than passing it on. For an existing schedule the trigger is locked and re-sent exactly as stored. In the create form it keeps the original instant and re-sends it unchanged while the displayed local value is unedited; once edited it CANONICALISES the local value with a fixed rule rather than restoring the previous instant — the earlier occurrence when a local time happens twice at a fall-back transition, and the first valid instant after the gap when a local time does not exist at a spring-forward transition — and shows the resolved absolute time with its offset beside the field, so the instant that will be saved is always the one displayed. Re-entering the wall clock of an instant at the later occurrence therefore yields the earlier one by design; only an unedited value preserves the original instant. A schedule row is `{name, prompt, trigger, mode, mutating, profile, maxFires, oneShotRetry, oneShotMaxRetries, enabled, status: claimed|completed|paused|running|scheduled, fireCount, lastFireAt, lastFireSessionId, nextFireAt, owner, providerId, modelId}`; `status` is `running` while a fire has started, else `claimed` while the daemon's last-fire session reads `pending` (the row then shows an empty session id), else `completed` when the daemon reports no next fire and the schedule has fired at least once (a one-shot that has run, or a cron that has reached `maxFires`; the daemon's claim disables the schedule in the same write, so `enabled` is then `false`), else `paused` when disabled, else `scheduled`. Run now is offered only for `scheduled` and `claimed` schedules; Pause and Resume are not offered for a `completed` one. A fire row is `{id, scheduleName, sessionId, firedAt, startedAt, progressAt, deadline, stop, error, inFlight}` with `inFlight = stop is empty`; fires without an id are dropped. Timestamps are ISO 8601 strings or `null` when the daemon's value is zero. Problem codes: `runtime_unavailable` (`503`), `schedule_unsupported` (`501`), `invalid_schedule` (`400`, the detail naming the offending field) for a create or update body that fails validation, `schedule_trigger_immutable` (`409`), plus the daemon's own codes relayed by the bootstrap's error mapping.
- **Security / authority:** Mutations inherit the CSRF and session gates. Field bounds are enforced before any SDK call: `name` and `prompt` trimmed, `name` 1–160 and `prompt` 1–1,000,000 characters, cron expression non-empty, cron `timezone` empty or a zone `Intl` resolves (the daemon silently replaces an unknown zone with UTC, so the BFF refuses one with `400` rather than let a schedule run at the wrong time), one-shot `at` a valid ISO datetime with an offset, `maxFires` and `oneShotMaxRetries` integers in `0..2147483647` (the daemon's fields are signed 32-bit, so a larger or non-safe value is refused with `400` before any SDK call rather than coerced during serialisation). The BFF never widens what the daemon authorises: ownership, owner scoping, and the daemon's own schedule limits are relayed, and the unexposed spec fields are carried over untouched on update so a browser cannot alter them.
- **Compatibility / migration:** Additive on the BFF (six routes, regenerated OpenAPI and client). The web nav gains `Scheduled` after `Chats`. No existing route or schema changes.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — the inventory is honest about deployments without scheduling

Scheduling is an operator-enabled capability ([architecture](../architecture.md#scheduled-tasks)); Studio reads the negotiated capability and degrades the whole surface rather than failing requests ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC1.1: on a deployment whose snapshot reports `scheduling: true`, `GET /api/v1/schedules` returns `supported: true`, an empty `reason`, and the normalised rows sorted by name.
  - verify: vitest:apps/server/src/routes/schedules.test.ts#bGlzdHMgbm9ybWFsaXplZCBzY2hlZHVsZXM — `apps/server/src/routes/schedules.test.ts :: "lists normalized schedules"`
- AC1.2: on a deployment without scheduling the list returns `supported: false`, a non-empty `reason`, and no items, while create, update, actions, delete, and fires answer `501` `schedule_unsupported`.
  - verify: vitest:apps/server/src/routes/schedules.test.ts#Y2FwYWJpbGl0eS1kaXNhYmxlcyB1bnN1cHBvcnRlZCBtdXRhdGlvbnM — `apps/server/src/routes/schedules.test.ts :: "capability-disables unsupported mutations"`
- AC1.3: without a runtime every schedules route answers `503` `runtime_unavailable`.
  - verify: vitest:apps/server/src/routes/schedules.test.ts#YW5zd2VycyA1MDMgcnVudGltZV91bmF2YWlsYWJsZSBvbiBldmVyeSBzY2hlZHVsZXMgcm91dGUgd2l0aG91dCBhIHJ1bnRpbWU — `apps/server/src/routes/schedules.test.ts :: "answers 503 runtime_unavailable on every schedules route without a runtime"`

### Scenario 2 — create and update map the product contract onto the SDK spec

The BFF translates the product's mode, profile, and trigger vocabulary into the SDK spec and
back, and an update never silently rewrites fields the product does not expose — the BFF
relays and never widens the daemon's authority ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md);
the spec fields are the daemon's, see [architecture](../architecture.md#scheduled-tasks)).

**Acceptance:**
- AC2.1: `profile: noFilesystem` becomes the daemon's `no-fs` profile on create; the default `all` becomes the empty profile; the daemon's profile decodes back into the enum.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#Zm9yd2FyZHMgdGhlIG5vLWZpbGVzeXN0ZW0gcHJvZmlsZSB0byB0aGUgU0RLIHNwZWMgb24gY3JlYXRl — `apps/server/src/mecatl/schedules.test.ts :: "forwards the no-filesystem profile to the SDK spec on create"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#ZGVmYXVsdHMgdG8gdGhlIGFsbC10b29scyBwcm9maWxlIG9uIGNyZWF0ZQ — `apps/server/src/mecatl/schedules.test.ts :: "defaults to the all-tools profile on create"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#ZGVjb2RlcyB0aGUgU0RLJ3MgcHJvZmlsZSBiYWNrIGludG8gdGhlIHJlc3BvbnNlIGVudW0 — `apps/server/src/mecatl/schedules.test.ts :: "decodes the SDK's profile back into the response enum"`
- AC2.2: a cron trigger carries its expression and timezone and zeroes the one-shot retry fields; a one-shot trigger converts its ISO instant to a protobuf timestamp, zeroes `maxFires`, and honours `oneShotRetry`/`oneShotMaxRetries`; both decode back to the same trigger shape.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#bWFwcyBjcm9uIGFuZCBvbmUtc2hvdCB0cmlnZ2VycyB0byB0aGUgU0RLIHNwZWMgYW5kIGJhY2s — `apps/server/src/mecatl/schedules.test.ts :: "maps cron and one-shot triggers to the SDK spec and back"`
- AC2.3: `PUT` re-sends the request's profile rather than the schedule's prior value, and carries over `carryContext`, `fireTimeout`, `limits`, `misfire`, `parts`, `selector`, and `singleton` from the daemon's current spec.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cmUtc2VuZHMgdGhlIHJlcXVlc3QncyBwcm9maWxlIG9uIHVwZGF0ZSByYXRoZXIgdGhhbiB0aGUgc2NoZWR1bGUncyBwcmlvciB2YWx1ZQ — `apps/server/src/mecatl/schedules.test.ts :: "re-sends the request's profile on update rather than the schedule's prior value"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cHJlc2VydmVzIHRoZSBzY2hlZHVsZSdzIGV4aXN0aW5nIGxpbWl0cywgc2VsZWN0b3IsIGFuZCBwYXJ0cyBvbiB1cGRhdGU — `apps/server/src/mecatl/schedules.test.ts :: "preserves the schedule's existing limits, selector, and parts on update"`
- AC2.5: `maxFires` or `oneShotMaxRetries` above `2147483647`, or any non-safe integer, is refused with `400` before any SDK call, so no value reaches a signed 32-bit daemon field that cannot represent it.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cmVmdXNlcyBjb3VudHMgYSBzaWduZWQgMzItYml0IGRhZW1vbiBmaWVsZCBjYW5ub3QgcmVwcmVzZW50 — `apps/server/src/mecatl/schedules.test.ts :: "refuses counts a signed 32-bit daemon field cannot represent"`
- AC2.6: an update whose trigger differs from the stored one is refused with `409` `schedule_trigger_immutable` before the SDK's `update` is called; an update that names the stored trigger, including the same one-shot instant written with another offset, is applied.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cmVmdXNlcyBhbiB1cGRhdGUgdGhhdCBjaGFuZ2VzIHRoZSB0cmlnZ2VyIHdpdGhvdXQgY2FsbGluZyB0aGUgU0RLIHVwZGF0ZQ — `apps/server/src/mecatl/schedules.test.ts :: "refuses an update that changes the trigger without calling the SDK update"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#YXBwbGllcyBhIG5vbi10cmlnZ2VyIGVkaXQgd2hlbiB0aGUgdHJpZ2dlciBuYW1lcyB0aGUgc3RvcmVkIGluc3RhbnQ — `apps/server/src/mecatl/schedules.test.ts :: "applies a non-trigger edit when the trigger names the stored instant"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#YXBwbGllcyBhIG5vbi10cmlnZ2VyIGVkaXQgdG8gYSByZWN1cnJpbmcgc2NoZWR1bGU — `apps/server/src/mecatl/schedules.test.ts :: "applies a non-trigger edit to a recurring schedule"`
  - verify: vitest:apps/server/src/routes/schedules.test.ts#YW5zd2VycyA0MDkgc2NoZWR1bGVfdHJpZ2dlcl9pbW11dGFibGUgd2hlbiBhbiB1cGRhdGUgY2hhbmdlcyB0aGUgdHJpZ2dlcg — `apps/server/src/routes/schedules.test.ts :: "answers 409 schedule_trigger_immutable when an update changes the trigger"`
  - verify: vitest:apps/server/src/routes/schedules.test.ts#c3RpbGwgYXBwbGllcyBhbiB1cGRhdGUgdGhhdCBrZWVwcyB0aGUgdHJpZ2dlcg — `apps/server/src/routes/schedules.test.ts :: "still applies an update that keeps the trigger"`
- AC2.7: a cron timezone the daemon could not load is refused with `400` `invalid_schedule` before any SDK call, an empty timezone is accepted as UTC, and a one-shot `at` with a numeric offset is accepted while a bare local time is not.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#YWNjZXB0cyBJQU5BIHRpbWUgem9uZXMgYW5kIHRoZSBlbXB0eSBVVEMgY2hvaWNl — `apps/server/src/mecatl/schedules.test.ts :: "accepts IANA time zones and the empty UTC choice"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cmVmdXNlcyBhIHRpbWUgem9uZSB0aGUgZGFlbW9uIHdvdWxkIHNpbGVudGx5IHJlcGxhY2Ugd2l0aCBVVEM — `apps/server/src/mecatl/schedules.test.ts :: "refuses a time zone the daemon would silently replace with UTC"`
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#YWNjZXB0cyBhIG9uZS1zaG90IGluc3RhbnQgd2l0aCBhIG51bWVyaWMgVVRDIG9mZnNldCBidXQgbm90IGEgYmFyZSBsb2NhbCB0aW1l — `apps/server/src/mecatl/schedules.test.ts :: "accepts a one-shot instant with a numeric UTC offset but not a bare local time"`
  - verify: vitest:apps/server/src/routes/schedules.test.ts#cmVmdXNlcyBhbiB1bmtub3duIGNyb24gdGltZSB6b25lIHdpdGggYSA0MDAgcHJvYmxlbSBiZWZvcmUgcmVhY2hpbmcgTWVjYXRs — `apps/server/src/routes/schedules.test.ts :: "refuses an unknown cron time zone with a 400 problem before reaching Mecatl"`
  - verify: vitest:apps/server/src/routes/schedules.test.ts#YWNjZXB0cyBhbiBlbXB0eSB0aW1lIHpvbmUgYW5kIGEgb25lLXNob3QgaW5zdGFudCB3aXRoIGEgbnVtZXJpYyBvZmZzZXQ — `apps/server/src/routes/schedules.test.ts :: "accepts an empty time zone and a one-shot instant with a numeric offset"`
- AC2.4: the permission mode round-trips (`default`, `plan`, `acceptEdits`) with `plan` as the create default.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cm91bmQtdHJpcHMgdGhlIHBlcm1pc3Npb24gbW9kZSB3aXRoIHBsYW4gYXMgdGhlIGRlZmF1bHQ — `apps/server/src/mecatl/schedules.test.ts :: "round-trips the permission mode with plan as the default"`

### Scenario 3 — lifecycle actions and fire history

Actions go through the SDK's named operations; fire history is a projection with a derived
in-flight flag and newest-first order. The daemon owns fire semantics
([architecture](../architecture.md#scheduled-tasks)); Studio only projects them
([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC3.1: `fire`, `pause`, and `resume` call `fireNow`, `pause`, and `resume` respectively and return `204`; delete returns `204`.
  - verify: vitest:apps/server/src/routes/schedules.test.ts#cm91dGVzIGxpZmVjeWNsZSBhY3Rpb25z — `apps/server/src/routes/schedules.test.ts :: "routes lifecycle actions"`
- AC3.2: a schedule's `status` derives from enabled / in-flight / pending-claim state as the contract defines, and a pending claim hides the placeholder session id.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#ZGVyaXZlcyBzY2hlZHVsZSBzdGF0dXMgZnJvbSBlbmFibGVkLCBpbi1mbGlnaHQsIGFuZCBwZW5kaW5nIGNsYWltcw — `apps/server/src/mecatl/schedules.test.ts :: "derives schedule status from enabled, in-flight, and pending claims"`
- AC3.5: a schedule whose daemon state reports no next fire after at least one fire is `completed`, not `scheduled` or `paused`, although the daemon has disabled it: a one-shot that has run and a cron that has reached `maxFires` both report it, while a never-fired schedule with no next fire stays `scheduled` and an operator-paused schedule stays `paused`.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#cmVwb3J0cyBhIGZpcmVkIG9uZS1zaG90IGFuZCBhbiBleGhhdXN0ZWQgY3JvbiBhcyBjb21wbGV0ZWQ — `apps/server/src/mecatl/schedules.test.ts :: "reports a fired one-shot and an exhausted cron as completed"`
- AC3.6: Run now is offered only for `scheduled` and `claimed` schedules, and neither Pause nor Resume for a `completed` one; running Run now refreshes that schedule's fire history.
  - verify: vitest:apps/web/src/features/schedules/schedule-detail.test.ts#b2ZmZXJzIFJ1biBub3cgb25seSBmb3IgYSBzY2hlZHVsZSB3aXRoIHdvcmsgbGVmdCB0byBydW4 — `apps/web/src/features/schedules/schedule-detail.test.ts :: "offers Run now only for a schedule with work left to run"`
  - verify: vitest:apps/web/src/features/schedules/schedule-detail.test.ts#b2ZmZXJzIG5laXRoZXIgUGF1c2Ugbm9yIFJlc3VtZSBmb3IgYSBjb21wbGV0ZWQgc2NoZWR1bGU — `apps/web/src/features/schedules/schedule-detail.test.ts :: "offers neither Pause nor Resume for a completed schedule"`
- AC3.3: fires list newest first by `firedAt`, drop rows without an id, and mark `inFlight` when `stop` is empty.
  - verify: vitest:apps/server/src/mecatl/schedules.test.ts#bGlzdHMgZmlyZXMgbmV3ZXN0IGZpcnN0IHdpdGggaW4tZmxpZ2h0IGRlcml2ZWQgZnJvbSBhIG1pc3Npbmcgc3RvcA — `apps/server/src/mecatl/schedules.test.ts :: "lists fires newest first with in-flight derived from a missing stop"`
- AC3.4: every schedules mutation is refused with `403` `cross_site_request` without the CSRF pair and with `401` `unauthenticated` without a session when interactive login is active.
  - verify: vitest:apps/server/src/routes/schedules.test.ts#c2NoZWR1bGUgbXV0YXRpb25zIHJlcXVpcmUgdGhlIENTUkYgcGFpciBhbmQgYSBzZXNzaW9uIHdoZW4gaW50ZXJhY3RpdmUgbG9naW4gaXMgYWN0aXZl — `apps/server/src/routes/schedules.test.ts :: "schedule mutations require the CSRF pair and a session when interactive login is active"`

### Scenario 4 — the browser builds and explains triggers

The cron builder and the natural-language phrase parser are pure functions with ported
tests; the form renders from them ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md), decision 3).

**Acceptance:**
- AC4.1: the builder derives each repeat shape and the interval shapes, defaults a missing weekday or interval, clamps out-of-range picks, never emits a malformed cron from a blank time, and is stable through cron → builder → cron for builder-shaped strings.
  - verify: vitest:apps/web/src/features/schedules/cron-builder.test.ts#ZGVyaXZlcyBlYWNoIHJlcGVhdCBzaGFwZQ — `apps/web/src/features/schedules/cron-builder.test.ts :: "derives each repeat shape"`
  - verify: vitest:apps/web/src/features/schedules/cron-builder.test.ts#bmV2ZXIgZGVyaXZlcyBhbiBlbXB0eSBvciBtYWxmb3JtZWQgY3JvbiBmcm9tIGEgYmxhbmsvaW52YWxpZCB0aW1l — `apps/web/src/features/schedules/cron-builder.test.ts :: "never derives an empty or malformed cron from a blank/invalid time"`
  - verify: vitest:apps/web/src/features/schedules/cron-builder.test.ts#Y3JvbiDihpIgYnVpbGRlciDihpIgY3JvbiBpcyBzdGFibGUgZm9yIGJ1aWxkZXItc2hhcGVkIHN0cmluZ3M — `apps/web/src/features/schedules/cron-builder.test.ts :: "cron → builder → cron is stable for builder-shaped strings"`
- AC4.2: the builder describes its own shapes in plain English and falls back to the raw expression otherwise.
  - verify: vitest:apps/web/src/features/schedules/cron-builder.test.ts#ZGVzY3JpYmVzIHRoZSBidWlsZGVyJ3Mgb3duIHNoYXBlcyBpbiBwbGFpbiBFbmdsaXNo — `apps/web/src/features/schedules/cron-builder.test.ts :: "describes the builder's own shapes in plain English"`
  - verify: vitest:apps/web/src/features/schedules/cron-builder.test.ts#ZmFsbHMgYmFjayB0byB0aGUgcmF3IGV4cHJlc3Npb24gZm9yIGFueXRoaW5nIGl0IGRvZXNuJ3QgcmVjb2duaXNl — `apps/web/src/features/schedules/cron-builder.test.ts :: "falls back to the raw expression for anything it doesn't recognise"`
- AC4.3: the phrase parser handles `next <weekday> at …`, `tomorrow at …`, `in N units`, the 12-hour clock, five-field cron strings, and never throws on odd input. It makes no claim about preserving an edited instant: an unedited form is AC4.5's subject and an edited one is AC4.6's.
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#bmV4dCA8ZG93PiByb2xscyBhIHdlZWsgZm9yd2FyZCB3aGVuIHRvZGF5J3MgdGltZSBoYXMgcGFzc2Vk — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "next <dow> rolls a week forward when today's time has passed"`
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#bm9ybWFsaXNlcyB0aGUgMTItaG91ciBjbG9jazogMTJhbSBpcyAwaCwgMTJwbSBpcyAxMmg — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "normalises the 12-hour clock: 12am is 0h, 12pm is 12h"`
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#bmV2ZXIgdGhyb3dzIG9uIG9kZCBpbnB1dA — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "never throws on odd input"`
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#cm91bmQtdHJpcHMgdGhlIGNvbXBpbGVyJ3Mgb25lLXNob3QgaW5zdGFudCB0byB0aGUgbWludXRl — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "round-trips the compiler's one-shot instant to the minute"`
- AC4.5: an unedited one-shot form re-sends the schedule's original instant unchanged, so saving an untouched schedule never moves its fire time.
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#a2VlcHMgdGhlIG9yaWdpbmFsIGluc3RhbnQgd2hlbiB0aGUgZGlzcGxheWVkIGxvY2FsIHZhbHVlIGlzIHVuZWRpdGVk — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "keeps the original instant when the displayed local value is unedited"`
- AC4.6: in the create form (an existing schedule's trigger is locked, AC4.8), editing or reconfirming a one-shot wall clock CANONICALISES it, and does not round-trip the previous instant. A local time that occurs twice at a fall-back transition resolves to the earlier occurrence, so an instant at the later occurrence yields the earlier one once its wall clock is re-entered — the documented rule, visible in the resolved absolute time the form displays before saving. A local time that does not exist at a spring-forward transition resolves to the first valid instant after the gap.
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#cmVzb2x2ZXMgYW4gYW1iaWd1b3VzIGZhbGwtYmFjayBsb2NhbCB0aW1lIHRvIHRoZSBlYXJsaWVyIG9jY3VycmVuY2U — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "resolves an ambiguous fall-back local time to the earlier occurrence"`
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#Y2Fub25pY2FsaXNlcyBhbiBlZGl0ZWQgbGF0ZXItb2NjdXJyZW5jZSBpbnN0YW50IHRvIHRoZSBlYXJsaWVyIG9uZQ — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "canonicalises an edited later-occurrence instant to the earlier one"`
  - verify: vitest:apps/web/src/features/schedules/schedule-phrase.test.ts#cmVzb2x2ZXMgYSBub25leGlzdGVudCBzcHJpbmctZm9yd2FyZCBsb2NhbCB0aW1lIHRvIHRoZSBmaXJzdCBpbnN0YW50IGFmdGVyIHRoZSBnYXA — `apps/web/src/features/schedules/schedule-phrase.test.ts :: "resolves a nonexistent spring-forward local time to the first instant after the gap"`
- AC4.7: saving an unchanged form sends exactly the stored mode and write flag, `plan` on a writing schedule included, and an existing schedule's trigger exactly as stored; an empty cron timezone is labelled UTC.
  - verify: vitest:apps/web/src/features/schedules/schedule-form-value.test.ts#c2F2aW5nIGFuIHVuY2hhbmdlZCBmb3JtIHNlbmRzIGV4YWN0bHkgdGhlIHN0b3JlZCBtb2RlIGFuZCB3cml0ZSBmbGFn — `apps/web/src/features/schedules/schedule-form-value.test.ts :: "saving an unchanged form sends exactly the stored mode and write flag"`
  - verify: vitest:apps/web/src/features/schedules/schedule-form-value.test.ts#c2VuZHMgYW4gZXhpc3Rpbmcgc2NoZWR1bGUncyB0cmlnZ2VyIGV4YWN0bHkgYXMgc3RvcmVk — `apps/web/src/features/schedules/schedule-form-value.test.ts :: "sends an existing schedule's trigger exactly as stored"`
  - verify: vitest:apps/web/src/features/schedules/schedule-detail.test.ts#bGFiZWxzIGFuIGVtcHR5IGNyb24gdGltZSB6b25lIGFzIFVUQw — `apps/web/src/features/schedules/schedule-detail.test.ts :: "labels an empty cron time zone as UTC"`
- AC4.8: for an existing schedule every trigger control (kind, cron builder and expression, timezone, the one-shot date and time, the phrase field) is locked, the trigger shown is exactly the trigger sent, and fields the server does not compare as part of the trigger (the fire limit and one-shot retries) stay editable; a new schedule's trigger stays fully editable.
  - verify: vitest:apps/web/src/features/schedules/schedule-form-value.test.ts#bG9ja3MgZXZlcnkgdHJpZ2dlciBjb250cm9sIG9mIGFuIGV4aXN0aW5nIHNjaGVkdWxl — `apps/web/src/features/schedules/schedule-form-value.test.ts :: "locks every trigger control of an existing schedule"`
  - verify: vitest:apps/web/src/features/schedules/schedule-form-value.test.ts#ZGlzcGxheXMgZXhhY3RseSB0aGUgdHJpZ2dlciBhbiBleGlzdGluZyBzY2hlZHVsZSBzZW5kcw — `apps/web/src/features/schedules/schedule-form-value.test.ts :: "displays exactly the trigger an existing schedule sends"`
  - verify: vitest:apps/web/src/features/schedules/schedule-form-value.test.ts#bGVhdmVzIHRoZSByZXRyeSBmaWVsZHMgb2YgYW4gZXhpc3Rpbmcgb25lLXNob3Qgc2NoZWR1bGUgZWRpdGFibGU — `apps/web/src/features/schedules/schedule-form-value.test.ts :: "leaves the retry fields of an existing one-shot schedule editable"`
  - verify: vitest:apps/web/src/features/schedules/schedule-form-value.test.ts#a2VlcHMgdHJpZ2dlciBjb250cm9scyBlZGl0YWJsZSBmb3IgYSBuZXcgc2NoZWR1bGU — `apps/web/src/features/schedules/schedule-form-value.test.ts :: "keeps trigger controls editable for a new schedule"`
- AC4.4: the detail page orders fires by duration with the newest fire as the tie-break and measures an in-flight fire against the current time.
  - verify: vitest:apps/web/src/features/schedules/schedule-detail.test.ts#c29ydHMgYnkgZHVyYXRpb24gd2l0aCBuZXdlc3QgZmlyZSBhcyB0aGUgdGllIGJyZWFr — `apps/web/src/features/schedules/schedule-detail.test.ts :: "sorts by duration with newest fire as the tie break"`
  - verify: vitest:apps/web/src/features/schedules/schedule-detail.test.ts#dXNlcyB0aGUgY3VycmVudCB0aW1lIGZvciBhbiBpbi1mbGlnaHQgZHVyYXRpb24 — `apps/web/src/features/schedules/schedule-detail.test.ts :: "uses the current time for an in-flight duration"`

### Scenario 5 — schedules join the workspace

Navigation and routing follow the chat layer's pattern ([studio-chat](studio-chat.md)) inside
the shell the bootstrap fixed ([ADR 0351](../adr/0351-mecatl-studio-in-repo-web-ui.md)).

**Acceptance:**
- AC5.1: the nav's second item is `Scheduled`; `/workspace/schedules` lists schedules (with `?schedule=` opening the editor) and `/workspace/schedules/{name}` shows the detail page; on an unsupported deployment the workspace shows the daemon's `reason` and disables creation.
  - verify: inspection — route files, `nav-items.ts`, and the workspace's `supported` branch; `pnpm --filter @mecatl-studio/web typecheck` proves the typed links resolve.
- AC5.2: `task studio:check` passes with the regenerated OpenAPI document and client committed; `task ac-trace` resolves every proof named here.
  - verify: inspection — the CI `studio` job.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Knowledge, settings, search, shortcuts reference page | later stack layers, one Bounded plan each | bootstrap plan's per-feature decision |
| Exposing `limits`, `carryContext`, `singleton`, `misfire`, or `selector` in the product contract | a later plan if the product asks | update-semantics decision above |
| Timezone pickers beyond the browser's own zone | a later plan | prototype behaviour kept |
| `user-docs/` pages for Studio | the top layer of the stack | bootstrap plan's documentation decision |

## Definition of done

1. `task studio:check`, `task lint:actions`, `task docs`, and `task ac-trace` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `docs/architecture.md`'s Studio section names the schedules surface and its capability gate.
4. The implementation layer records the exact commit of this plan it built against.

## Deferred decisions and known risks

- The form's timezone for cron triggers defaults to the browser's `Intl` zone. An empty zone means UTC and the UI labels it so.
- Review origin: the one-shot representation, the `completed` status, and the numeric bounds above answer three review findings against an earlier draft of this plan, which round-tripped a one-shot instant through an offset-less local value, labelled a finished schedule `scheduled`, and bounded the two counts only as non-negative.
- Risk: the `claimed` status relies on the daemon's `pending` placeholder session id; if the daemon changes that convention the row degrades to `scheduled`, never to an error.
