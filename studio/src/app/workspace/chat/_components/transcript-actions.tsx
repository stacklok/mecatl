"use client";

import { Copy, TextSelect } from "lucide-react";
import { toast } from "sonner";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import type { AgentMessage } from "@/features/agent";
import {
  copyFailureMessage,
  copyText,
  serializeTranscript,
} from "./transcript-text";

/**
 * The chat ··· menus' whole-conversation actions (the TUI's ctrl+g
 * select-all and ctrl+y copy): "Select conversation" selects just the
 * transcript — never the sidebar and navigation a page-level ⌘A would take
 * — and "Copy conversation" puts the serialized transcript on the
 * clipboard. Desktop dropdown items, a thread-panel copy item and the
 * mobile bottom-sheet rows all share the one copy path, so the toasts (and
 * the insecure-page explanation) can't drift between them.
 */

export const SELECT_CONVERSATION_LABEL = "Select conversation";
export const COPY_CONVERSATION_LABEL = "Copy conversation";
export const CONVERSATION_COPIED_MESSAGE = "Conversation copied";
export const NOTHING_TO_COPY_MESSAGE =
  "Nothing to copy yet — the conversation has no messages";

export interface TranscriptActionProps {
  messages: readonly AgentMessage[];
  botName: string;
  /** Select the rendered transcript (the caller owns the DOM node). */
  onSelectTranscript: () => void;
}

/**
 * Serialize and copy the conversation, reporting the outcome as a toast.
 * A failed copy says WHY: the Clipboard API is absent on a plain-http page
 * (external-mode deployments), or the browser refused it.
 */
export async function copyTranscript(
  messages: readonly AgentMessage[],
  botName: string,
  successMessage: string = CONVERSATION_COPIED_MESSAGE,
): Promise<void> {
  const text = serializeTranscript(messages, { botName });
  if (!text) {
    toast.info(NOTHING_TO_COPY_MESSAGE);
    return;
  }
  const outcome = await copyText(text);
  if (outcome.ok) toast.success(successMessage);
  else toast.error(copyFailureMessage(outcome.reason));
}

/** A dropdown item that copies `messages`. Must render inside a `DropdownMenuContent`. */
export function CopyTranscriptMenuItem({
  messages,
  botName,
  label = COPY_CONVERSATION_LABEL,
  successMessage = CONVERSATION_COPIED_MESSAGE,
}: {
  messages: readonly AgentMessage[];
  botName: string;
  label?: string;
  successMessage?: string;
}) {
  return (
    <DropdownMenuItem
      disabled={messages.length === 0}
      onClick={() => void copyTranscript(messages, botName, successMessage)}
    >
      <Copy className="size-4 mr-2 text-muted-foreground" />
      {label}
    </DropdownMenuItem>
  );
}

/** Both conversation actions for the desktop ··· dropdown. */
export function TranscriptMenuItems({
  messages,
  botName,
  onSelectTranscript,
}: TranscriptActionProps) {
  return (
    <>
      <DropdownMenuItem
        disabled={messages.length === 0}
        onClick={onSelectTranscript}
      >
        <TextSelect className="size-4 mr-2 text-muted-foreground" />
        {SELECT_CONVERSATION_LABEL}
      </DropdownMenuItem>
      <CopyTranscriptMenuItem messages={messages} botName={botName} />
    </>
  );
}

const SHEET_ROW_CLASS =
  "flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors disabled:opacity-50";

/**
 * The same two actions for the mobile bottom-sheet menu, styled like its
 * sibling rows. `onDone` lets the sheet close itself after an action.
 */
export function TranscriptSheetItems({
  messages,
  botName,
  onSelectTranscript,
  onDone,
}: TranscriptActionProps & { onDone?: () => void }) {
  const empty = messages.length === 0;
  return (
    <>
      <button
        type="button"
        disabled={empty}
        onClick={() => {
          onSelectTranscript();
          onDone?.();
        }}
        className={SHEET_ROW_CLASS}
      >
        <TextSelect className="size-4 text-muted-foreground" />
        {SELECT_CONVERSATION_LABEL}
      </button>
      <button
        type="button"
        disabled={empty}
        onClick={() => {
          void copyTranscript(messages, botName);
          onDone?.();
        }}
        className={SHEET_ROW_CLASS}
      >
        <Copy className="size-4 text-muted-foreground" />
        {COPY_CONVERSATION_LABEL}
      </button>
    </>
  );
}
