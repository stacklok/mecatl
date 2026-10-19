// SPDX-License-Identifier: Apache-2.0

import { Copy, TextSelect } from "lucide-react";
import { toast } from "sonner";
import { DropdownMenuItem } from "../../components/ui/dropdown-menu";
import { copyToClipboard } from "../../lib/clipboard";
import type { ChatMessage } from "./chat-state";
import { serializeTranscript } from "./transcript-text";

/**
 * The chat options' whole-conversation actions, ported from the prototype's
 * `transcript-actions.tsx`: "Select conversation" selects just the
 * transcript (never the list and navigation a page-wide select-all would
 * take), and "Copy conversation" puts the serialized transcript on the
 * clipboard.
 */

export const SELECT_CONVERSATION_LABEL = "Select conversation";
export const COPY_CONVERSATION_LABEL = "Copy conversation";
export const NOTHING_TO_COPY_MESSAGE = "Nothing to copy yet: the conversation has no messages";

/** Serializes and copies the conversation, reporting the outcome as a toast. */
export async function copyTranscript(
  messages: readonly ChatMessage[],
  names: { agentName: string; userName?: string },
): Promise<void> {
  const text = serializeTranscript(messages, names);
  if (!text) {
    toast.info(NOTHING_TO_COPY_MESSAGE);
    return;
  }
  await copyToClipboard(text, "Conversation");
}

/** Both conversation actions. Must render inside a `DropdownMenuContent`. */
export function TranscriptMenuItems({
  agentName,
  messages,
  onSelectTranscript,
  userName,
}: {
  agentName: string;
  messages: readonly ChatMessage[];
  onSelectTranscript: () => void;
  userName?: string;
}) {
  const empty = messages.length === 0;
  return (
    <>
      <DropdownMenuItem disabled={empty} onSelect={onSelectTranscript}>
        <TextSelect aria-hidden="true" />
        {SELECT_CONVERSATION_LABEL}
      </DropdownMenuItem>
      <DropdownMenuItem
        disabled={empty}
        onSelect={() => void copyTranscript(messages, { agentName, userName })}
      >
        <Copy aria-hidden="true" />
        {COPY_CONVERSATION_LABEL}
      </DropdownMenuItem>
    </>
  );
}
