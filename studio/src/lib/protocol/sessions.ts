/**
 * Mappers from the SDK's session inventory and transcript shapes into the
 * UI's own summary types.
 *
 * The daemon — not this client — decides which actions a row supports: a
 * subagent or team member is inspect-only, a running or awaiting chat cannot
 * be renamed or deleted, and a store without pruning cannot delete at all.
 * Each capability therefore travels with a closed machine-readable reason, so
 * the UI explains a disabled action instead of re-deriving server eligibility
 * rules and drifting from them.
 */

import {
  type SessionTranscript as SdkSessionTranscript,
  SessionMode,
} from "@stacklok-oss/mecatl-sdk";
import type { ListSessionsResponse } from "@stacklok-oss/mecatl-sdk/gen";

export type SessionSummary = {
  sessionId: string;
  /** The server-held label: operator-authored, or seeded from the first prompt. */
  title: string;
  /**
   * Where `title` came from: "operator" (hand-set — an auto-rename must never
   * clobber it), "first-prompt" (seeded, safe to replace), or "" (legacy row /
   * older daemon — treat as unknown and do not auto-rename).
   */
  titleProvenance: string;
  /**
   * Non-empty on an AI-debug session (ADR 0254): the id of the stored session
   * this row was created to diagnose. The one relationship field a chat row
   * can carry — every other related kind is inspect-only.
   */
  debugTargetSessionId: string;
  /** Persisted lifecycle state (idle/running/awaiting/completed/failed/cancelled). */
  state: string;
  modelId: string;
  turns: number;
  /** Last write, epoch millis. Zero when the row carried no timestamp. */
  modifiedAt: number;
  /** Creation time, epoch millis. Zero when the snapshot could not be read. */
  createdAt: number;
  /**
   * Whether the row is an operator-facing chat at all. `inspect_only_kind` is
   * the ONE reason that means "not a chat" (a subagent, team member, parallel
   * branch, or scheduled fire) — EXCEPT for a debug session, whose kind is
   * stamped inspect-only but which the daemon drives as an ordinary chat (see
   * the mapper). Every other reason means "a chat, busy right now", which
   * still belongs in the list.
   */
  isChat: boolean;
  canRename: boolean;
  canDelete: boolean;
  canViewTranscript: boolean;
  renameReason: string;
  deleteReason: string;
};

export type SessionInventoryPage = {
  sessions: SessionSummary[];
  nextCursor: string;
};

/**
 * The closed session permission-mode vocabulary, spelled the way the daemon's
 * session aggregate spells it (session.PermissionMode: "default" / "plan" /
 * "acceptEdits").
 */
export type SessionPermissionMode = "default" | "plan" | "acceptEdits";

/**
 * Decodes a mode spelling into the closed vocabulary. Tolerant the same way
 * the daemon's own modeFromString is (case-insensitive,
 * "accept"/"accept-edits"/"accept_edits"/"acceptEdits" all mean accept-edits);
 * unknown or empty values fall through to "default" — the daemon applies the
 * default mode to a session created without one.
 */
export function decodeSessionPermissionMode(
  value: unknown,
): SessionPermissionMode {
  if (typeof value !== "string") return "default";
  switch (value.toLowerCase()) {
    case "plan":
      return "plan";
    case "accept":
    case "acceptedits":
    case "accept-edits":
    case "accept_edits":
      return "acceptEdits";
    default:
      return "default";
  }
}

/**
 * The wire spelling for requests that carry a mode — protojson snake_case,
 * per the requests-are-protojson rule. Never echo the decoded camelCase back.
 */
export function encodeSessionPermissionMode(
  mode: SessionPermissionMode,
): "default" | "plan" | "accept_edits" {
  return mode === "acceptEdits" ? "accept_edits" : mode;
}

/** The SDK's numeric mode → Studio's vocabulary (unspecified reads as default). */
export function sessionPermissionModeFromSdk(
  mode: SessionMode,
): SessionPermissionMode {
  switch (mode) {
    case SessionMode.Plan:
      return "plan";
    case SessionMode.AcceptEdits:
      return "acceptEdits";
    default:
      return "default";
  }
}

/** Studio's vocabulary (either spelling) → the SDK's numeric mode. */
export function sessionPermissionModeToSdk(
  mode: SessionPermissionMode | "accept_edits",
): SessionMode {
  switch (decodeSessionPermissionMode(mode)) {
    case "plan":
      return SessionMode.Plan;
    case "acceptEdits":
      return SessionMode.AcceptEdits;
    default:
      return SessionMode.Default;
  }
}

// int64 unix seconds cross the SDK as bigint; the UI keeps epoch millis.
const unixSecondsToMillis = (value: bigint | number | undefined) => {
  if (value === undefined) return 0;
  const seconds = Number(value);
  return Number.isFinite(seconds) ? Math.trunc(seconds) * 1000 : 0;
};

/** Maps one `ListSessions` page (the SDK's generated response) onto the UI rows. */
export function sessionInventoryFromResponse(
  response: ListSessionsResponse,
): SessionInventoryPage {
  const sessions: SessionSummary[] = [];
  for (const row of response.sessions ?? []) {
    // A row with no id cannot be opened, renamed, or deleted. That is a corrupt
    // envelope rather than a session, so it is dropped instead of rendered as an
    // inert chat the operator can never act on.
    if (!row.sessionId) continue;
    const capabilities = row.capabilities;
    const reasons = capabilities?.reasons;
    const debugTargetSessionId = row.relationship?.debugTargetSessionId ?? "";
    sessions.push({
      sessionId: row.sessionId,
      title: row.title ?? "",
      titleProvenance: row.titleProvenance ?? "",
      debugTargetSessionId,
      state: row.state ?? "",
      modelId: row.modelId ?? "",
      turns: row.turns ?? 0,
      modifiedAt: unixSecondsToMillis(row.modifiedAtUnix),
      createdAt: unixSecondsToMillis(row.createdAtUnix),
      // A debug session (ADR 0254) is the one non-main kind that IS a chat:
      // the daemon's run entry drives it like any main session, but its
      // inventory row is stamped `inspect_only_kind` because the KIND is not
      // main. The relationship is the honest chat signal there; its per-action
      // capabilities (rename/delete denied) still bind below.
      isChat:
        (reasons?.publicChat ?? "") !== "inspect_only_kind" ||
        debugTargetSessionId !== "",
      canRename: capabilities?.rename === true,
      canDelete: capabilities?.delete === true,
      canViewTranscript: capabilities?.viewTranscript === true,
      renameReason: reasons?.rename ?? "",
      deleteReason: reasons?.delete ?? "",
    });
  }
  return { sessions, nextCursor: response.nextCursor ?? "" };
}

/** One conversation entry from the session transcript. */
type TranscriptMessage = {
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

/** Maps the SDK's transcript projection onto the UI's transcript. */
export function sessionTranscriptFromSdk(
  transcript: SdkSessionTranscript,
): SessionTranscript {
  return {
    sessionId: transcript.sessionId,
    complete: transcript.complete === true,
    messages: transcript.messages.map((message) => ({
      role: message.role,
      text: message.text,
      toolCalls: message.toolCalls.map((call) => ({
        id: call.id,
        name: call.name,
        args: call.args,
      })),
      toolResult: message.toolResult
        ? {
            callId: message.toolResult.callId,
            content: message.toolResult.content,
            isError: message.toolResult.isError === true,
          }
        : undefined,
    })),
  };
}
