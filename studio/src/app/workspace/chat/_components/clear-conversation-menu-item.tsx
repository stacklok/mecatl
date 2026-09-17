"use client";

import { Eraser } from "lucide-react";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { CLEAR_CONVERSATION_LABEL } from "./clear-conversation";

/**
 * The chat ··· menu's Clear conversation entry (desktop dropdown): asks the
 * workspace to run the ClearSession handoff (use-clear-conversation.ts).
 * Must render inside a `DropdownMenuContent`. A `disabledReason` renders the
 * item disabled with the daemon's plain reason under the label, the same
 * shape as the sidebar's Rename/Delete denials.
 */
export function ClearConversationMenuItem({
  onSelect,
  disabledReason,
}: {
  onSelect: () => void;
  /** Non-empty disables the item and explains why. */
  disabledReason?: string;
}) {
  const disabled = !!disabledReason;
  return (
    <DropdownMenuItem
      disabled={disabled}
      onClick={onSelect}
      title={disabled ? disabledReason : undefined}
    >
      <Eraser className="size-4 mr-2 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        {CLEAR_CONVERSATION_LABEL}
        {disabled && (
          <span className="block truncate text-xs text-muted-foreground">
            {disabledReason}
          </span>
        )}
      </span>
    </DropdownMenuItem>
  );
}

/**
 * The same entry point for the mobile bottom-sheet menu, styled like its
 * sibling rows. `onDone` lets the sheet close itself after starting.
 */
export function ClearConversationSheetItem({
  onSelect,
  disabledReason,
  onDone,
}: {
  onSelect: () => void;
  disabledReason?: string;
  onDone?: () => void;
}) {
  const disabled = !!disabledReason;
  return (
    <button
      type="button"
      disabled={disabled}
      title={disabled ? disabledReason : undefined}
      onClick={() => {
        onSelect();
        onDone?.();
      }}
      className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors disabled:opacity-50"
    >
      <Eraser className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 text-left">
        {CLEAR_CONVERSATION_LABEL}
        {disabled && (
          <span className="block truncate text-xs text-muted-foreground">
            {disabledReason}
          </span>
        )}
      </span>
    </button>
  );
}
