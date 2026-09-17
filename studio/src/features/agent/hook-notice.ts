/**
 * Hook decision notices — the daemon's `hook` event, rendered instead of
 * dropped. The pure rules live here: attribute a fire to the tool call it
 * names, format a call-less fire as a marked transcript notice, parse that
 * marker back, and pick one tone per decision. Shared by the live prompt
 * reducer and the durable-watch reducer so a rebuilt transcript reads
 * exactly like the live one did.
 */

import type {
  AgentMessage,
  HookDecision,
  HookNotice,
  StreamEvent,
} from "./types";

/** The `hook` StreamEvent variant. */
export type HookStreamEvent = Extract<StreamEvent, { type: "hook" }>;

/** The marker a call-less hook notice carries in `AgentMessage.notices`. */
const HOOK_NOTICE_MARKER =
  /^\[hook:(info|blocked|modified|advisory)\] ?([\s\S]*)$/;

/** What each decision did, for a fire whose daemon message is empty. */
const DECISION_OUTCOME: Record<HookDecision, string> = {
  blocked: "blocked the action",
  modified: "rewrote the action",
  advisory: "flagged the action",
  info: "ran",
};

/** Short label per decision: the chip text and the panel column. */
export const HOOK_DECISION_LABEL: Record<HookDecision, string> = {
  blocked: "Blocked",
  modified: "Modified",
  advisory: "Advisory",
  info: "Info",
};

/** The decision glyph: ✗ blocked, ✎ modified, ⚠ advisory, ℹ info. */
export const HOOK_DECISION_GLYPH: Record<HookDecision, string> = {
  blocked: "✗",
  modified: "✎",
  advisory: "⚠",
  info: "ℹ",
};

/**
 * The human-readable body of one fire: the phase (and the tool, when the
 * fire had one) followed by the daemon's message — or, when the daemon
 * sent none, what the decision means. A message that already opens with
 * the phase name ("PreToolUse hook rewrote tool arguments for Shell") is
 * kept verbatim rather than prefixed twice.
 */
export function hookNoticeBody(notice: HookNotice): string {
  const text = notice.text.trim();
  if (
    notice.phase &&
    text.toLowerCase().startsWith(notice.phase.toLowerCase())
  ) {
    return text;
  }
  const phase = notice.phase ? `${notice.phase} hook` : "Hook";
  const subject = notice.tool ? `${phase} (${notice.tool})` : phase;
  return text
    ? `${subject}: ${text}`
    : `${subject} ${DECISION_OUTCOME[notice.decision]}`;
}

/** The daemon's message, or what the decision did when it sent none. */
export function hookOutcomeText(notice: HookNotice): string {
  const text = notice.text.trim();
  return text || `The hook ${DECISION_OUTCOME[notice.decision]}.`;
}

/** A call-less hook fire as the marked transcript notice the bubble parses. */
export function formatHookNotice(notice: HookNotice): string {
  return `[hook:${notice.decision}] ${hookNoticeBody(notice)}`;
}

/** Parses a `[hook:<decision>] …` notice; null for every other notice. */
export function parseHookNotice(
  text: string,
): { decision: HookDecision; text: string } | null {
  const match = HOOK_NOTICE_MARKER.exec(text);
  if (!match) return null;
  return { decision: match[1] as HookDecision, text: match[2] };
}

/** Whether a transcript notice is a marked hook notice. */
export function isHookNotice(text: string): boolean {
  return HOOK_NOTICE_MARKER.test(text);
}

/**
 * Chip tone per decision: blocked destructive, modified info (a change was
 * applied), advisory warning (needs a look), info muted.
 */
export function hookChipClass(decision: HookDecision): string {
  switch (decision) {
    case "blocked":
      return "border-destructive/40 bg-destructive/10 text-destructive";
    case "modified":
      return "border-info/40 bg-info/10 text-info";
    case "advisory":
      return "border-warning/40 bg-warning/10 text-warning";
    default:
      return "border-border bg-muted/40 text-muted-foreground";
  }
}

/** Text-only tone per decision, for the transcript line. */
export function hookTextClass(decision: HookDecision): string {
  switch (decision) {
    case "blocked":
      return "text-destructive";
    case "modified":
      return "text-info";
    case "advisory":
      return "text-warning";
    default:
      return "text-muted-foreground/70";
  }
}

const noticeOf = (event: HookStreamEvent): HookNotice => ({
  phase: event.phase,
  tool: event.tool,
  decision: event.decision,
  text: event.text,
});

/**
 * Applies one hook fire onto ONE message: attributed to the matching tool
 * call when the message carries it, else appended as a marked notice. The
 * call's status is deliberately left alone — a PreToolUse block is followed
 * by its own is_error tool result, which fails the call exactly once.
 */
export function attachHookToMessage(
  message: AgentMessage,
  event: HookStreamEvent,
): AgentMessage {
  const notice = noticeOf(event);
  if (
    event.callId &&
    message.toolCalls?.some((call) => call.callId === event.callId)
  ) {
    return {
      ...message,
      toolCalls: message.toolCalls.map((call) =>
        call.callId === event.callId
          ? { ...call, hooks: [...(call.hooks ?? []), notice] }
          : call,
      ),
    };
  }
  return {
    ...message,
    notices: [...(message.notices ?? []), formatHookNotice(notice)],
  };
}

/**
 * The watch reducer's arm: the fire lands on the LATEST message carrying
 * its call (a PostToolUse fire can arrive after a later bubble opened),
 * else on the trailing assistant message as a notice, opening one if
 * needed — the same shape as every other assistant-side activity.
 */
export function reduceHookEvent(
  messages: AgentMessage[],
  event: HookStreamEvent,
  onAssistant: (
    apply: (message: AgentMessage) => AgentMessage,
  ) => AgentMessage[],
): AgentMessage[] {
  if (event.callId) {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index];
      if (!message.toolCalls?.some((call) => call.callId === event.callId)) {
        continue;
      }
      return [
        ...messages.slice(0, index),
        attachHookToMessage(message, event),
        ...messages.slice(index + 1),
      ];
    }
  }
  return onAssistant((message) => attachHookToMessage(message, event));
}
