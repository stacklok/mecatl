"use client";

import { Info } from "lucide-react";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";

/** The one label both entry points share (the `/session` built-in's dialog). */
export const SESSION_DETAILS_LABEL = "Session details";

/**
 * The chat ··· menu's entry point to the session details dialog (desktop
 * dropdown): the exact id with Copy, lifecycle state, kind, model, placement,
 * timestamps, counts and relationships. Must render inside a
 * `DropdownMenuContent`. The dialog itself is owned by the workspace (it is
 * also what `/session` and ⌘I open), so this only asks for it.
 */
export function SessionDetailsMenuItem({ onSelect }: { onSelect: () => void }) {
  return (
    <DropdownMenuItem onClick={onSelect}>
      <Info className="size-4 mr-2 text-muted-foreground" />
      {SESSION_DETAILS_LABEL}
    </DropdownMenuItem>
  );
}

/**
 * The same entry point for the mobile bottom-sheet menu, styled like its
 * sibling rows. `onDone` lets the sheet close itself after opening the dialog.
 */
export function SessionDetailsSheetItem({
  onSelect,
  onDone,
}: {
  onSelect: () => void;
  onDone?: () => void;
}) {
  return (
    <button
      type="button"
      onClick={() => {
        onSelect();
        onDone?.();
      }}
      className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors"
    >
      <Info className="size-4 text-muted-foreground" />
      {SESSION_DETAILS_LABEL}
    </button>
  );
}
