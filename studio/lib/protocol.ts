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

export type ScheduleRow = {
  name: string;
  prompt: string;
  cron: string;
  oneShotAt: number | null;
  workspace: string;
  mode: number;
  mutating: boolean;
  enabled: boolean;
  fireCount: number;
  nextFireAt: number | null;
  lastFireAt: number | null;
  fireStage: "idle" | "claimed" | "running";
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

function protoMillis(value: unknown): number | null {
  const timestamp = asRecord(value);
  const seconds = Number(timestamp?.seconds ?? 0);
  if (!Number.isFinite(seconds) || seconds === 0) return null;
  const nanos = Number(timestamp?.nanos ?? 0);
  return seconds * 1000 + Math.floor((Number.isFinite(nanos) ? nanos : 0) / 1e6);
}

export function decodeScheduleRows(value: unknown): ScheduleRow[] {
  const body = asRecord(value);
  if (!Array.isArray(body?.schedules)) return [];
  return body.schedules.map((entryValue) => {
    const entry = asRecord(entryValue) ?? {};
    const spec = asRecord(entry.spec) ?? {};
    const state = asRecord(entry.state) ?? {};
    const trigger = asRecord(spec.trigger) ?? {};
    return {
      name: String(spec.name ?? ""),
      prompt: String(spec.prompt ?? ""),
      cron: String(trigger.cron ?? ""),
      oneShotAt: protoMillis(trigger.one_shot),
      workspace: String(spec.workspace ?? ""),
      mode: Number(spec.mode ?? 0),
      mutating: Boolean(spec.mutating),
      enabled: Boolean(state.enabled),
      fireCount: Number(state.fire_count ?? 0),
      nextFireAt: protoMillis(state.next_fire_at),
      lastFireAt: protoMillis(state.last_fire_at),
      fireStage: protoMillis(state.last_fire_started_at) !== null
        ? "running"
        : String(state.last_fire_session_id ?? "") === "pending" ? "claimed" : "idle",
    };
  });
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
