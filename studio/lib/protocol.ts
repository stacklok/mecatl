export type MecatlEvent = {
  type: string;
  seq?: string | number;
  text?: string;
  tool_call?: { id?: string; name?: string; args?: string; tool?: string; call_id?: string };
  tool_result?: { call_id?: string; content?: string; result?: string; is_error?: boolean; tool?: string };
  ask?: { ask_id?: string; tool?: string; args?: string; reason?: string };
  result?: { text?: string; stop?: string; error?: string; usage?: { input_tokens?: string | number; output_tokens?: string | number } };
  subagent?: { parent_call_id?: string; child_id?: string; goal?: string; routed_category?: string; routed_model?: string; model?: string };
  team?: { parent_call_id?: string; roster?: Array<{ name?: string; role?: string; routed_category?: string; routed_model?: string; model?: string }> };
  parallel?: { parent_call_id?: string; kind?: string; branch_index?: number; branch_label?: string; goal?: string; routed_category?: string; routed_model?: string; model?: string };
};

/**
 * Per-fire budgets (proto Limits). Zero DISABLES a limit rather than meaning
 * "unset", so these are plain numbers: the form, the wire, and the daemon all
 * read 0 as "no cap".
 */
export type ScheduleLimits = { maxTurns: number; maxToolCalls: number; maxConsecutiveFailures: number };

/**
 * The spec fields Studio's form does not expose, carried VERBATIM across an edit.
 *
 * `PUT /v1/schedules/{name}` REPLACES the whole spec: the daemon preserves only
 * the firing state, the creation timestamp, and the captured owner. Every other
 * field a request omits is therefore deleted from the schedule — so a field this
 * UI has no control for still has to make the round trip, or editing a prompt
 * would silently drop a provider selector an operator set from the CLI.
 *
 * `parts` stays opaque on purpose. It is multimodal Content this client never
 * renders, and decoding it into a typed shape only to re-encode it would be a
 * second mapping of a message we need only hand back unchanged.
 */
export type ScheduleCarriedSpec = {
  selectorProvider: string;
  selectorModel: string;
  misfire: number;
  singleton: boolean;
  carryContext: boolean;
  /** A proto Duration in seconds, fractions allowed. 0 = the deployment default. */
  fireTimeoutSeconds: number;
  parts: unknown[];
};

export type ScheduleRow = {
  name: string;
  prompt: string;
  cron: string;
  oneShotAt: number | null;
  timezone: string;
  workspace: string;
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
 * one-shot, a cross-field rule the daemon enforces fail-closed). Modelling it as
 * two optional fields would let the form build a body the daemon must reject.
 */
export type ScheduleTriggerDraft =
  | { kind: "cron"; cron: string; timezone: string }
  | { kind: "one-shot"; at: number };

export type ScheduleSpecDraft = {
  name: string;
  prompt: string;
  trigger: ScheduleTriggerDraft;
  profile: "" | "no-fs";
  workspace: string;
  mode: number;
  mutating: boolean;
  maxFires: number;
  limits: ScheduleLimits;
  oneShotRetry: boolean;
  oneShotMaxRetries: number;
};

type UnknownRecord = Record<string, unknown>;

const asRecord = (value: unknown): UnknownRecord | undefined =>
  typeof value === "object" && value !== null && !Array.isArray(value)
    ? value as UnknownRecord
    : undefined;
const optionalString = (value: unknown) => typeof value === "string" ? value : undefined;
const optionalNumber = (value: unknown) => typeof value === "number" ? value : undefined;

function stringFields(value: unknown, names: string[]) {
  const source = asRecord(value);
  if (!source) return undefined;
  return Object.fromEntries(names.map((name) => [name, optionalString(source[name])])) as Record<string, string | undefined>;
}

export function parseMecatlEvent(data: string): MecatlEvent {
  const raw = asRecord(JSON.parse(data));
  if (!raw || typeof raw.type !== "string" || !raw.type) throw new Error("event.type is required");
  const event: MecatlEvent = {
    type: raw.type,
    seq: typeof raw.seq === "string" || typeof raw.seq === "number" ? raw.seq : undefined,
    text: optionalString(raw.text),
  };
  event.tool_call = stringFields(raw.tool_call, ["id", "name", "args", "tool", "call_id"]);
  const toolResult = asRecord(raw.tool_result);
  if (toolResult) event.tool_result = {
    ...stringFields(toolResult, ["call_id", "content", "result", "tool"]),
    is_error: typeof toolResult.is_error === "boolean" ? toolResult.is_error : undefined,
  };
  event.ask = stringFields(raw.ask, ["ask_id", "tool", "args", "reason"]);
  const result = asRecord(raw.result);
  if (result) event.result = {
    ...stringFields(result, ["text", "stop", "error"]),
    usage: asRecord(result.usage) as MecatlEvent["result"] extends { usage?: infer U } ? U : never,
  };
  event.subagent = stringFields(raw.subagent, ["parent_call_id", "child_id", "goal", "routed_category", "routed_model", "model"]);
  const team = asRecord(raw.team);
  if (team) event.team = {
    parent_call_id: optionalString(team.parent_call_id),
    roster: Array.isArray(team.roster) ? team.roster.map((member) => stringFields(member, ["name", "role", "routed_category", "routed_model", "model"]) ?? {}) : undefined,
  };
  const parallel = asRecord(raw.parallel);
  if (parallel) event.parallel = {
    ...stringFields(parallel, ["parent_call_id", "kind", "branch_label", "goal", "routed_category", "routed_model", "model"]),
    branch_index: optionalNumber(parallel.branch_index),
  };
  return event;
}

const numeric = (value: unknown) => {
  const parsed = typeof value === "number" ? value : typeof value === "string" ? Number(value) : 0;
  return Number.isFinite(parsed) ? parsed : 0;
};

// An enum arrives as a NUMBER under stdlib encoding/json (what mecated's HTTP
// surface uses) and as its SCREAMING_CASE name under protojson (what a relay in
// front of it may use). Both normalise to the number the UI switches on, so a
// proxied deployment does not render every posture as "unset".
const enumNumber = (value: unknown, names: Record<string, number>) =>
  typeof value === "string" ? names[value] ?? 0 : numeric(value);
const PERMISSION_MODES: Record<string, number> = {
  PERMISSION_MODE_UNSPECIFIED: 0,
  PERMISSION_MODE_DEFAULT: 1,
  PERMISSION_MODE_PLAN: 2,
  PERMISSION_MODE_ACCEPT_EDITS: 3,
};
const MISFIRE_POLICIES: Record<string, number> = {
  MISFIRE_POLICY_UNSPECIFIED: 0,
  MISFIRE_FIRE_ONCE_NOW: 1,
  MISFIRE_SKIP: 2,
};

// A Timestamp is {seconds,nanos} under stdlib encoding/json and an RFC 3339
// string under protojson. The zero time has no wire form, so absent, zero, and
// unparseable all collapse to null — "no next fire" is a real state here (a
// one-shot that fired, a cron past max_fires) and must not read as 1970.
function protoMillis(value: unknown): number | null {
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) && parsed !== 0 ? parsed : null;
  }
  const timestamp = asRecord(value);
  const seconds = Number(timestamp?.seconds ?? 0);
  if (!Number.isFinite(seconds) || seconds === 0) return null;
  const nanos = Number(timestamp?.nanos ?? 0);
  return seconds * 1000 + Math.floor((Number.isFinite(nanos) ? nanos : 0) / 1e6);
}

// A Duration is {seconds,nanos} under stdlib encoding/json and a "1.5s" string
// under protojson. Returned in seconds because that is the unit the proto field
// is documented in and the unit the request form has to send back.
function protoSeconds(value: unknown): number {
  if (typeof value === "string") return numeric(value.replace(/s$/, ""));
  const duration = asRecord(value);
  if (!duration) return 0;
  return numeric(duration.seconds) + numeric(duration.nanos) / 1e9;
}

function decodeLimits(value: unknown): ScheduleLimits {
  const limits = asRecord(value) ?? {};
  return {
    maxTurns: numeric(limits.max_turns),
    maxToolCalls: numeric(limits.max_tool_calls),
    maxConsecutiveFailures: numeric(limits.max_consecutive_failures),
  };
}

// The owner's `name` is display-only by contract and `subject` is the identity,
// so the label prefers the name and falls back to the id rather than inventing a
// friendly string. An absent owner stays empty: an ownerless schedule is never
// rendered as an anonymous somebody.
function decodeOwner(value: unknown): string {
  const owner = asRecord(value);
  if (!owner) return "";
  return optionalString(owner.name) || optionalString(owner.subject) || "";
}

export function decodeScheduleRows(value: unknown): ScheduleRow[] {
  const body = asRecord(value);
  if (!Array.isArray(body?.schedules)) return [];
  return body.schedules.map((entryValue) => {
    const entry = asRecord(entryValue) ?? {};
    const spec = asRecord(entry.spec) ?? {};
    const state = asRecord(entry.state) ?? {};
    const trigger = asRecord(spec.trigger) ?? {};
    const selector = asRecord(spec.selector) ?? {};
    return {
      name: String(spec.name ?? ""),
      prompt: String(spec.prompt ?? ""),
      cron: String(trigger.cron ?? ""),
      oneShotAt: protoMillis(trigger.one_shot),
      timezone: String(spec.timezone ?? ""),
      workspace: String(spec.workspace ?? ""),
      profile: String(spec.profile ?? ""),
      mode: enumNumber(spec.mode, PERMISSION_MODES),
      mutating: Boolean(spec.mutating),
      maxFires: numeric(spec.max_fires),
      limits: decodeLimits(spec.limits),
      oneShotRetry: Boolean(spec.one_shot_retry),
      oneShotMaxRetries: numeric(spec.one_shot_max_retries),
      enabled: Boolean(state.enabled),
      fireCount: numeric(state.fire_count),
      nextFireAt: protoMillis(state.next_fire_at),
      lastFireAt: protoMillis(state.last_fire_at),
      fireStage: protoMillis(state.last_fire_started_at) !== null
        ? "running"
        : String(state.last_fire_session_id ?? "") === "pending" ? "claimed" : "idle",
      owner: decodeOwner(spec.owner),
      carried: {
        selectorProvider: String(selector.provider_id ?? ""),
        selectorModel: String(selector.model_id ?? ""),
        misfire: enumNumber(spec.misfire, MISFIRE_POLICIES),
        singleton: Boolean(spec.singleton),
        carryContext: Boolean(spec.carry_context),
        fireTimeoutSeconds: protoSeconds(spec.fire_timeout),
        parts: Array.isArray(spec.parts) ? spec.parts : [],
      },
    };
  });
}

function decodeFire(value: unknown): ScheduleFireRow | undefined {
  const fire = asRecord(value);
  const id = optionalString(fire?.id) ?? "";
  // A record with no id cannot be refreshed or correlated to a session; it is a
  // corrupt envelope rather than a fire, so it is dropped instead of rendered.
  if (!fire || !id) return undefined;
  const stop = optionalString(fire.stop) ?? "";
  return {
    id,
    scheduleName: optionalString(fire.schedule_name) ?? "",
    sessionId: optionalString(fire.session_id) ?? "",
    firedAt: protoMillis(fire.fired_at),
    startedAt: protoMillis(fire.started_at),
    progressAt: protoMillis(fire.progress_at),
    deadline: protoMillis(fire.deadline),
    stop,
    err: optionalString(fire.err) ?? "",
    inFlight: stop === "",
  };
}

/**
 * `GET /v1/schedules/{name}/fires`, newest first.
 *
 * The API documents the list as having NO guaranteed order, so the display order
 * is this decoder's job — an oversight surface that shows a random fire first is
 * worse than useless. A record with no `fired_at` sorts last rather than first,
 * which is where an un-clocked claim belongs.
 */
export function decodeScheduleFires(value: unknown): ScheduleFireRow[] {
  const body = asRecord(value);
  const rows = Array.isArray(body?.fires) ? body.fires : [];
  return rows
    .map(decodeFire)
    .filter((fire): fire is ScheduleFireRow => fire !== undefined)
    .sort((left, right) => (right.firedAt ?? 0) - (left.firedAt ?? 0));
}

/** `GET /v1/schedules/{name}/fires/{id}` — one fire, re-read on demand. */
export function decodeScheduleFire(value: unknown): ScheduleFireRow | null {
  const body = asRecord(value);
  return decodeFire(body?.fire) ?? null;
}

/** The edit prefill: the stored row split into the half the form owns. */
export function scheduleDraftFromRow(row: ScheduleRow): ScheduleSpecDraft {
  return {
    name: row.name,
    prompt: row.prompt,
    trigger: !row.cron && row.oneShotAt !== null
      ? { kind: "one-shot", at: row.oneShotAt }
      : { kind: "cron", cron: row.cron, timezone: row.timezone },
    profile: row.profile === "no-fs" ? "no-fs" : "",
    workspace: row.workspace,
    mode: row.mode,
    mutating: row.mutating,
    maxFires: row.maxFires,
    limits: row.limits,
    oneShotRetry: row.oneShotRetry,
    oneShotMaxRetries: row.oneShotMaxRetries,
  };
}

/**
 * The body for `POST /v1/schedules` and `PUT /v1/schedules/{name}`.
 *
 * Request bodies are decoded with PROTOJSON; responses are encoded with stdlib
 * `encoding/json`. The two disagree on every well-known type — a Timestamp reads
 * back as `{seconds,nanos}` but must be sent as RFC 3339, a Duration reads back
 * as `{seconds}` but must be sent as `"5s"` — so a response body can never be
 * echoed back as a request body. This function IS that conversion, which is why
 * the edit path decodes to a view model first rather than round-tripping raw
 * JSON.
 *
 * `carried` is omitted on a create (there is nothing to preserve yet) and passed
 * on an edit, where leaving it out would delete the fields this form cannot edit.
 * Server-owned fields — `created_at` and `owner` — are deliberately never sent.
 */
export function encodeScheduleSpec(draft: ScheduleSpecDraft, carried?: ScheduleCarriedSpec): UnknownRecord {
  const spec: UnknownRecord = {
    name: draft.name,
    prompt: draft.prompt,
    profile: draft.profile,
    workspace: draft.workspace,
    mode: draft.mode,
    mutating: draft.mutating,
    limits: {
      max_turns: draft.limits.maxTurns,
      max_tool_calls: draft.limits.maxToolCalls,
      max_consecutive_failures: draft.limits.maxConsecutiveFailures,
    },
  };
  if (draft.trigger.kind === "cron") {
    // max_fires bounds a cron's total fires; a one-shot fires once by definition
    // and the daemon ignores it there. one_shot_retry is the mirror image — the
    // create-seam REJECTS a cron that carries it, so neither field is sent on
    // the trigger it does not belong to.
    spec.trigger = { cron: draft.trigger.cron };
    spec.timezone = draft.trigger.timezone;
    spec.max_fires = draft.maxFires;
  } else {
    spec.trigger = { one_shot: new Date(draft.trigger.at).toISOString() };
    spec.one_shot_retry = draft.oneShotRetry;
    if (draft.oneShotRetry) spec.one_shot_max_retries = draft.oneShotMaxRetries;
  }
  if (!carried) return spec;
  spec.singleton = carried.singleton;
  spec.misfire = carried.misfire;
  spec.carry_context = carried.carryContext;
  if (carried.selectorProvider || carried.selectorModel) {
    spec.selector = { provider_id: carried.selectorProvider, model_id: carried.selectorModel };
  }
  if (carried.fireTimeoutSeconds > 0) spec.fire_timeout = `${carried.fireTimeoutSeconds}s`;
  if (carried.parts.length) spec.parts = carried.parts;
  return spec;
}
/**
 * A row of the daemon's stored-session inventory (`GET /v1/sessions`).
 *
 * The daemon — not this client — decides which actions a row supports: a
 * subagent or team member is inspect-only, a running or awaiting chat cannot be
 * renamed or deleted, and a store without pruning cannot delete at all. Each
 * capability therefore travels with a closed machine-readable reason, so the UI
 * explains a disabled action instead of re-deriving server eligibility rules and
 * drifting from them.
 */
export type SessionSummary = {
  sessionId: string;
  /** The server-held label: operator-authored, or seeded from the first prompt. */
  title: string;
  /** Persisted lifecycle state (idle/running/awaiting/completed/failed/cancelled). */
  state: string;
  workspace: string;
  modelId: string;
  turns: number;
  /** Last write, epoch millis. Zero when the row carried no timestamp. */
  modifiedAt: number;
  /** Creation time, epoch millis. Zero when the snapshot could not be read. */
  createdAt: number;
  /**
   * Whether the row is an operator-facing chat at all. `inspect_only_kind` is
   * the ONE reason that means "not a chat" (a subagent, team member, parallel
   * branch, or scheduled fire). Every other reason means "a chat, busy right
   * now", which still belongs in the list.
   */
  isChat: boolean;
  canRename: boolean;
  canDelete: boolean;
  canViewTranscript: boolean;
  renameReason: string;
  deleteReason: string;
};

export type SessionInventoryPage = { sessions: SessionSummary[]; nextCursor: string };

const booleanFlag = (value: unknown) => value === true;
// int64 fields cross encoding/json as numbers. A value that arrives as a string
// (a protojson-shaped proxy, a hand-written stub) must still not become NaN.
const unixSecondsToMillis = (value: unknown) => {
  const seconds = typeof value === "number" ? value : typeof value === "string" ? Number(value) : 0;
  return Number.isFinite(seconds) ? Math.trunc(seconds) * 1000 : 0;
};

export function decodeSessionInventory(value: unknown): SessionInventoryPage {
  const body = asRecord(value);
  const rows = Array.isArray(body?.sessions) ? body.sessions : [];
  const sessions: SessionSummary[] = [];
  for (const rowValue of rows) {
    const row = asRecord(rowValue);
    const sessionId = optionalString(row?.session_id) ?? "";
    // A row with no id cannot be opened, renamed, or deleted. That is a corrupt
    // envelope rather than a session, so it is dropped instead of rendered as an
    // inert chat the operator can never act on.
    if (!sessionId) continue;
    const capabilities = asRecord(row?.capabilities) ?? {};
    const reasons = asRecord(capabilities.reasons) ?? {};
    sessions.push({
      sessionId,
      title: optionalString(row?.title) ?? "",
      state: optionalString(row?.state) ?? "",
      workspace: optionalString(row?.workspace) ?? "",
      modelId: optionalString(row?.model_id) ?? "",
      turns: optionalNumber(row?.turns) ?? 0,
      modifiedAt: unixSecondsToMillis(row?.modified_at_unix),
      createdAt: unixSecondsToMillis(row?.created_at_unix),
      isChat: (optionalString(reasons.public_chat) ?? "") !== "inspect_only_kind",
      canRename: booleanFlag(capabilities.rename),
      canDelete: booleanFlag(capabilities.delete),
      canViewTranscript: booleanFlag(capabilities.view_transcript),
      renameReason: optionalString(reasons.rename) ?? "",
      deleteReason: optionalString(reasons.delete) ?? "",
    });
  }
  return { sessions, nextCursor: optionalString(body?.next_cursor) ?? "" };
}

/** One conversation entry from `GET /v1/sessions/{id}/transcript`. */
export type TranscriptMessage = {
  role: string;
  text: string;
  toolCalls: Array<{ id: string; name: string; args: string }>;
  toolResult?: { callId: string; content: string; isError: boolean };
};

export type SessionTranscript = {
  sessionId: string;
  /**
   * The daemon's completeness attestation. A successful load is complete even
   * for a genuinely empty conversation, so `false` means the transcript could
   * NOT be proven whole — the UI says so rather than presenting a partial
   * history as the whole one.
   */
  complete: boolean;
  messages: TranscriptMessage[];
};

export function decodeSessionTranscript(value: unknown): SessionTranscript {
  const body = asRecord(value);
  const rows = Array.isArray(body?.messages) ? body.messages : [];
  const messages: TranscriptMessage[] = [];
  for (const messageValue of rows) {
    const message = asRecord(messageValue);
    if (!message) continue;
    const calls = Array.isArray(message.tool_calls) ? message.tool_calls : [];
    const result = asRecord(message.tool_result);
    messages.push({
      role: optionalString(message.role) ?? "",
      text: optionalString(message.text) ?? "",
      toolCalls: calls.flatMap((callValue) => {
        const call = asRecord(callValue);
        if (!call) return [];
        return [{
          id: optionalString(call.id) ?? "",
          name: optionalString(call.name) ?? "",
          args: optionalString(call.args) ?? "",
        }];
      }),
      toolResult: result
        ? {
          callId: optionalString(result.call_id) ?? "",
          content: optionalString(result.content) ?? "",
          isError: booleanFlag(result.is_error),
        }
        : undefined,
    });
  }
  return {
    sessionId: optionalString(body?.session_id) ?? "",
    complete: booleanFlag(body?.complete),
    messages,
  };
}
