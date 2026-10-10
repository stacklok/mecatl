// SPDX-License-Identifier: Apache-2.0

import type { ChatMessage } from "./chat-state";

/**
 * Whole-conversation selection and copy, ported from the prototype's
 * `transcript-text.ts`, for the chat options' "Select conversation" and
 * "Copy conversation". Pure DOM and data: nothing here reaches the daemon.
 */

export interface TranscriptSerializeOptions {
  agentName: string;
  /** The label for the user's turns; defaults to "You". */
  userName?: string;
}

/**
 * The conversation as plain text: for every user or assistant message with
 * any content, a bold line naming the author and then the body, separated
 * by blank lines. Empty turns (a failed turn with no text, a reasoning-only
 * turn) are skipped.
 */
export function serializeTranscript(
  messages: readonly ChatMessage[],
  options: TranscriptSerializeOptions,
): string {
  const userName = options.userName?.trim() || "You";
  const blocks: string[] = [];
  for (const message of messages) {
    if (message.role !== "user" && message.role !== "assistant") continue;
    const content = message.content.trim();
    if (!content) continue;
    const author = message.role === "user" ? userName : options.agentName;
    blocks.push(`**${author}**\n\n${content}`);
  }
  return blocks.join("\n\n");
}

/** Selects everything rendered inside `element`: a select-all scoped to the transcript. */
export function selectElementContents(element: HTMLElement): void {
  const selection = window.getSelection();
  if (!selection) return;
  const range = document.createRange();
  range.selectNodeContents(element);
  selection.removeAllRanges();
  selection.addRange(range);
}
