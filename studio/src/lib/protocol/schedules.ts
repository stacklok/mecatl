/**
 * Mappers between the SDK's generated schedule messages and the UI shapes.
 *
 * The daemon's schedule registry crosses the wire as the protobuf messages in
 * `@stacklok-oss/mecatl-sdk/gen` (`ScheduleSpec`, `ScheduleState`,
 * `ScheduleFire`). The SDK owns the JSON encoding — Timestamps, Durations and
 * enums are its concern — so this module only reshapes typed messages into
 * the rows the pages render and the drafts the form edits back into a spec.
 */

import type {
  Content,
  ListFiresResponse,
  ListSchedulesResponse,
  ScheduleFire,
  ScheduleSpec,
  ScheduleState,
} from "@stacklok-oss/mecatl-sdk/gen";

/** The well-known-type messages the spec carries, named off the spec itself so
 *  no protobuf runtime import is needed here. */
type Timestamp = NonNullable<ScheduleSpec["createdAt"]>;
type Duration = NonNullable<ScheduleSpec["fireTimeout"]>;

/**
 * Per-fire budgets (proto Limits). Zero DISABLES a limit rather than meaning
 * "unset", so these are plain numbers: the form, the wire, and the daemon all
 * read 0 as "no cap".
 */
type ScheduleLimits = {
  maxTurns: number;
  maxToolCalls: number;
  maxConsecutiveFailures: number;
};

/**
 * The spec fields Studio's form does not expose, carried VERBATIM across an
 * edit.
 *
 * `PUT /v1/schedules/{name}` REPLACES the whole spec: the daemon preserves
 * only the firing state, the creation timestamp, and the captured owner.
 * Every other field a request omits is therefore deleted from the schedule —
 * so a field this UI has no control for still has to make the round trip, or
 * editing a prompt would silently drop a provider selector an operator set
 * from the CLI.
 *
 * `parts` stays opaque on purpose. It is multimodal Content this client never
 * renders; the decoded messages are handed back unchanged.
 */
export type ScheduleCarriedSpec = {
  selectorProvider: string;
  selectorModel: string;
  misfire: number;
  singleton: boolean;
  carryContext: boolean;
  /** A proto Duration in seconds, fractions allowed. 0 = the deployment default. */
  fireTimeoutSeconds: number;
  parts: Content[];
};

export type ScheduleRow = {
  name: string;
  prompt: string;
  cron: string;
  oneShotAt: number | null;
  timezone: string;
  profile: string;
  mode: number;
  mutating: boolean;
  maxFires: number;
  limits: ScheduleLimits;
  oneShotRetry: boolean;
  oneShotMaxRetries: number;
  enabled: boolean;
  fireCount: number;
  nextFireAt: number | null;
  lastFireAt: number | null;
  fireStage: "idle" | "claimed" | "running";
  /** Prior fire's session id — "" while a fire is only claimed ("pending"). */
  lastFireSessionId: string;
  /**
   * Display label for the verified caller the schedule is attributed to, empty
   * when the daemon runs without caller enforcement. Read-only on the wire: a
   * create or update request naming an owner is ignored, so it is never part of
   * an edit draft.
   */
  owner: string;
  carried: ScheduleCarriedSpec;
};

/**
 * One fire record (`GET /v1/schedules/{name}/fires`, `…/fires/{id}`).
 *
 * A fire written by RecordFireStart is IN-FLIGHT — it has a `startedAt` and no
 * `stop`; a fire written by RecordFire is terminal. A record with neither is a
 * claim that never started its run (the crash-after-claim state), which is why
 * `inFlight` keys off the ABSENT stop rather than off `startedAt`.
 */
export type ScheduleFireRow = {
  id: string;
  scheduleName: string;
  sessionId: string;
  firedAt: number | null;
  startedAt: number | null;
  progressAt: number | null;
  deadline: number | null;
  stop: string;
  err: string;
  inFlight: boolean;
};

/**
 * The editable half of a spec: what the panel's form owns.
 *
 * The trigger is a SUM type here because it is one on the wire (cron XOR
 * one-shot, a cross-field rule the daemon enforces fail-closed). Modelling it
 * as two optional fields would let the form build a body the daemon must
 * reject.
 */
type ScheduleTriggerDraft =
  | { kind: "cron"; cron: string; timezone: string }
  | { kind: "one-shot"; at: number };

export type ScheduleSpecDraft = {
  name: string;
  prompt: string;
  trigger: ScheduleTriggerDraft;
  profile: "" | "no-fs";
  mode: number;
  mutating: boolean;
  maxFires: number;
  limits: ScheduleLimits;
  oneShotRetry: boolean;
  oneShotMaxRetries: number;
};

export const PERMISSION_MODES: Record<string, number> = {
  PERMISSION_MODE_UNSPECIFIED: 0,
  PERMISSION_MODE_DEFAULT: 1,
  PERMISSION_MODE_PLAN: 2,
  PERMISSION_MODE_ACCEPT_EDITS: 3,
};

// A Timestamp's zero value has no meaning on this surface — "no next fire" is
// a real state (a one-shot that fired, a cron past max_fires) and must not
// read as 1970 — so absent and zero both collapse to null.
function timestampMillis(value: Timestamp | undefined): number | null {
  if (!value) return null;
  const seconds = Number(value.seconds);
  if (!Number.isFinite(seconds) || seconds === 0) return null;
  return seconds * 1000 + Math.floor(value.nanos / 1e6);
}

function millisTimestamp(millis: number): Timestamp {
  const seconds = Math.floor(millis / 1000);
  return {
    $typeName: "google.protobuf.Timestamp",
    seconds: BigInt(seconds),
    nanos: (millis - seconds * 1000) * 1e6,
  };
}

// Returned in seconds because that is the unit the proto field is documented
// in and the unit the carried spec hands back.
function durationSeconds(value: Duration | undefined): number {
  if (!value) return 0;
  return Number(value.seconds) + value.nanos / 1e9;
}

function secondsDuration(seconds: number): Duration {
  const whole = Math.floor(seconds);
  return {
    $typeName: "google.protobuf.Duration",
    seconds: BigInt(whole),
    nanos: Math.round((seconds - whole) * 1e9),
  };
}

function decodeLimits(value: ScheduleSpec["limits"]): ScheduleLimits {
  return {
    maxTurns: value?.maxTurns ?? 0,
    maxToolCalls: value?.maxToolCalls ?? 0,
    maxConsecutiveFailures: value?.maxConsecutiveFailures ?? 0,
  };
}

// The owner's `name` is display-only by contract and `subject` is the
// identity, so the label prefers the name and falls back to the id rather than
// inventing a friendly string. An absent owner stays empty: an ownerless
// schedule is never rendered as an anonymous somebody.
function decodeOwner(owner: ScheduleSpec["owner"]): string {
  if (!owner) return "";
  return owner.name || owner.subject || "";
}

function decodeRow(
  spec: ScheduleSpec | undefined,
  state: ScheduleState | undefined,
): ScheduleRow {
  const lastFireSessionId = state?.lastFireSessionId ?? "";
  return {
    name: spec?.name ?? "",
    prompt: spec?.prompt ?? "",
    cron: spec?.trigger?.cron ?? "",
    oneShotAt: timestampMillis(spec?.trigger?.oneShot),
    timezone: spec?.timezone ?? "",
    profile: spec?.profile ?? "",
    mode: spec?.mode ?? 0,
    mutating: spec?.mutating ?? false,
    maxFires: spec?.maxFires ?? 0,
    limits: decodeLimits(spec?.limits),
    oneShotRetry: spec?.oneShotRetry ?? false,
    oneShotMaxRetries: spec?.oneShotMaxRetries ?? 0,
    enabled: state?.enabled ?? false,
    fireCount: state?.fireCount ?? 0,
    nextFireAt: timestampMillis(state?.nextFireAt),
    lastFireAt: timestampMillis(state?.lastFireAt),
    fireStage:
      timestampMillis(state?.lastFireStartedAt) !== null
        ? "running"
        : lastFireSessionId === "pending"
          ? "claimed"
          : "idle",
    lastFireSessionId: lastFireSessionId === "pending" ? "" : lastFireSessionId,
    owner: decodeOwner(spec?.owner),
    carried: {
      selectorProvider: spec?.selector?.providerId ?? "",
      selectorModel: spec?.selector?.modelId ?? "",
      misfire: spec?.misfire ?? 0,
      singleton: spec?.singleton ?? false,
      carryContext: spec?.carryContext ?? false,
      fireTimeoutSeconds: durationSeconds(spec?.fireTimeout),
      parts: spec?.parts ?? [],
    },
  };
}

/** `GET /v1/schedules`: every registry entry as a display row. */
export function decodeScheduleRows(
  response: ListSchedulesResponse,
): ScheduleRow[] {
  return (response.schedules ?? []).map((entry) =>
    decodeRow(entry.spec, entry.state),
  );
}

function decodeFire(fire: ScheduleFire): ScheduleFireRow | undefined {
  // A record with no id cannot be refreshed or correlated to a session; it is a
  // corrupt envelope rather than a fire, so it is dropped instead of rendered.
  if (!fire.id) return undefined;
  const stop = fire.stop ?? "";
  return {
    id: fire.id,
    scheduleName: fire.scheduleName ?? "",
    sessionId: fire.sessionId ?? "",
    firedAt: timestampMillis(fire.firedAt),
    startedAt: timestampMillis(fire.startedAt),
    progressAt: timestampMillis(fire.progressAt),
    deadline: timestampMillis(fire.deadline),
    stop,
    err: fire.err ?? "",
    inFlight: stop === "",
  };
}

/**
 * `GET /v1/schedules/{name}/fires`, newest first.
 *
 * The API documents the list as having NO guaranteed order, so the display
 * order is this decoder's job — an oversight surface that shows a random fire
 * first is worse than useless. A record with no `fired_at` sorts last rather
 * than first, which is where an un-clocked claim belongs.
 */
export function decodeScheduleFires(
  response: ListFiresResponse,
): ScheduleFireRow[] {
  return (response.fires ?? [])
    .map(decodeFire)
    .filter((fire): fire is ScheduleFireRow => fire !== undefined)
    .sort((left, right) => (right.firedAt ?? 0) - (left.firedAt ?? 0));
}

/** The edit prefill: the stored row split into the half the form owns. */
export function scheduleDraftFromRow(row: ScheduleRow): ScheduleSpecDraft {
  return {
    name: row.name,
    prompt: row.prompt,
    trigger:
      !row.cron && row.oneShotAt !== null
        ? { kind: "one-shot", at: row.oneShotAt }
        : { kind: "cron", cron: row.cron, timezone: row.timezone },
    profile: row.profile === "no-fs" ? "no-fs" : "",
    mode: row.mode,
    mutating: row.mutating,
    maxFires: row.maxFires,
    limits: row.limits,
    oneShotRetry: row.oneShotRetry,
    oneShotMaxRetries: row.oneShotMaxRetries,
  };
}

/**
 * The `spec` for `client.schedules.create` / `client.schedules.update`: a
 * complete `ScheduleSpec` message value the SDK serialises (proto3 omits
 * zero-valued fields, so an unset knob is simply absent on the wire).
 *
 * Trigger-conditional fields ride only the trigger they belong to: `maxFires`
 * bounds a cron's total fires and the daemon ignores it on a one-shot, while
 * the create-seam REJECTS a cron carrying `oneShotRetry` — so each stays at
 * its zero value on the other trigger.
 *
 * `carried` is omitted on a create (there is nothing to preserve yet) and
 * passed on an edit, where leaving it out would delete the fields this form
 * cannot edit. Server-owned fields — `createdAt` and `owner` — are
 * deliberately never set.
 */
export function encodeScheduleSpec(
  draft: ScheduleSpecDraft,
  carried?: ScheduleCarriedSpec,
): ScheduleSpec {
  const cron = draft.trigger.kind === "cron" ? draft.trigger : undefined;
  const oneShot = draft.trigger.kind === "one-shot" ? draft.trigger : undefined;
  return {
    $typeName: "mecatl.v1.ScheduleSpec",
    name: draft.name,
    prompt: draft.prompt,
    parts: carried?.parts ?? [],
    trigger: {
      $typeName: "mecatl.v1.TriggerSpec",
      cron: cron?.cron ?? "",
      oneShot: oneShot ? millisTimestamp(oneShot.at) : undefined,
    },
    selector:
      carried && (carried.selectorProvider || carried.selectorModel)
        ? {
            $typeName: "mecatl.v1.ScheduleProviderSelector",
            providerId: carried.selectorProvider,
            modelId: carried.selectorModel,
          }
        : undefined,
    profile: draft.profile,
    mode: draft.mode,
    limits: {
      $typeName: "mecatl.v1.Limits",
      maxTurns: draft.limits.maxTurns,
      maxToolCalls: draft.limits.maxToolCalls,
      maxConsecutiveFailures: draft.limits.maxConsecutiveFailures,
    },
    mutating: draft.mutating,
    maxFires: cron ? draft.maxFires : 0,
    misfire: carried?.misfire ?? 0,
    singleton: carried?.singleton ?? false,
    timezone: cron?.timezone ?? "",
    createdAt: undefined,
    oneShotRetry: oneShot ? draft.oneShotRetry : false,
    oneShotMaxRetries:
      oneShot && draft.oneShotRetry ? draft.oneShotMaxRetries : 0,
    carryContext: carried?.carryContext ?? false,
    fireTimeout:
      carried && carried.fireTimeoutSeconds > 0
        ? secondsDuration(carried.fireTimeoutSeconds)
        : undefined,
    owner: undefined,
  };
}
