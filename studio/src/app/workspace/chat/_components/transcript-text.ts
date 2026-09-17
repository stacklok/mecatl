import type { AgentMessage } from "@/features/agent";

/**
 * Transcript selection and copy helpers (the TUI's ctrl+g select-all /
 * ctrl+y copy / esc-clears-selection, translated to a web page).
 *
 * Pure DOM + data: nothing here reaches the daemon. `serializeTranscript`
 * renders the SAME content the per-message Copy button copies (message
 * text only — tool calls, delegation cards and reasoning stay out), one
 * message after another, so "Copy conversation" is the whole-transcript
 * form of that button rather than a second export format.
 */

export interface TranscriptSerializeOptions {
  /** The chat's default assistant name; a message's own `agentName` wins. */
  botName: string;
  /** The label for the user's turns; defaults to "You". */
  userName?: string;
}

/**
 * The conversation as plain text: for every user/assistant message with any
 * content (or attachments), a bold header line naming the author, the
 * message body, and one `[attachment: name]` line per attachment; messages
 * are blank-line separated. Tool-role messages and empty turns (a failed
 * turn with no text, a reasoning-only turn) are skipped.
 */
export function serializeTranscript(
  messages: readonly AgentMessage[],
  opts: TranscriptSerializeOptions,
): string {
  const userName = opts.userName?.trim() || "You";
  const blocks: string[] = [];
  for (const message of messages) {
    if (message.role !== "user" && message.role !== "assistant") continue;
    const content = message.content.trim();
    const attachments = message.attachments ?? [];
    if (!content && attachments.length === 0) continue;
    const author =
      message.role === "user"
        ? userName
        : message.agentName?.trim() || opts.botName;
    const lines: string[] = [`**${author}**`];
    if (content) lines.push("", content);
    for (const attachment of attachments) {
      lines.push(`[attachment: ${attachment.name}]`);
    }
    blocks.push(lines.join("\n"));
  }
  return blocks.join("\n\n");
}

/** Select everything rendered inside `el` (a transcript-scoped select-all). */
export function selectElementContents(el: HTMLElement): void {
  const selection = window.getSelection();
  if (!selection) return;
  const range = document.createRange();
  range.selectNodeContents(el);
  selection.removeAllRanges();
  selection.addRange(range);
}

/**
 * True when the document carries a non-empty selection anchored inside
 * `el`. A collapsed selection (a bare caret) is not a selection: Esc must
 * fall through to the panel/run layers then.
 */
export function selectionInside(el: HTMLElement | null): boolean {
  if (!el) return false;
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0 || selection.isCollapsed) {
    return false;
  }
  return selection.anchorNode !== null && el.contains(selection.anchorNode);
}

/** The subset of a keyboard event the select-all chord test reads. */
export interface SelectAllKeyEvent {
  key: string;
  metaKey: boolean;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
  target: EventTarget | null;
}

function isEditable(target: EventTarget | null): boolean {
  const node = target as HTMLElement | null;
  return (
    !!node &&
    typeof node === "object" &&
    "tagName" in node &&
    (node.tagName === "INPUT" ||
      node.tagName === "TEXTAREA" ||
      node.isContentEditable === true)
  );
}

/**
 * True for a bare ⌘A / Ctrl+A (no shift, no alt) that did NOT originate in
 * an editable field. Fired on the transcript container, it scopes select-all
 * to the conversation; a text field rendered inside the transcript (a
 * clarification answer, say) keeps the browser's own select-all.
 */
export function isSelectAllChord(event: SelectAllKeyEvent): boolean {
  if (!(event.metaKey || event.ctrlKey)) return false;
  if (event.shiftKey || event.altKey) return false;
  if (event.key.toLowerCase() !== "a") return false;
  return !isEditable(event.target);
}

export type CopyFailureReason = "insecure-context" | "denied";

export type CopyOutcome =
  | { ok: true }
  | { ok: false; reason: CopyFailureReason };

/**
 * Write `text` to the clipboard. The async Clipboard API only exists on a
 * secure page (https or localhost) — an external-mode Studio served over
 * plain http has no `navigator.clipboard` at all, which is reported as its
 * own reason rather than a generic failure.
 */
export async function copyText(text: string): Promise<CopyOutcome> {
  const clipboard =
    typeof navigator === "undefined" ? undefined : navigator.clipboard;
  if (!clipboard || typeof clipboard.writeText !== "function") {
    return { ok: false, reason: "insecure-context" };
  }
  try {
    await clipboard.writeText(text);
    return { ok: true };
  } catch {
    return { ok: false, reason: "denied" };
  }
}

/** The user-facing sentence for a copy failure, naming the fix where one exists. */
export function copyFailureMessage(reason: CopyFailureReason): string {
  switch (reason) {
    case "insecure-context":
      return "Copy needs a secure page (https or localhost) — the browser blocks the clipboard here. Select the text and press ⌘C / Ctrl+C instead.";
    case "denied":
      return "The browser refused clipboard access.";
  }
}
