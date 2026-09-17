"use client";

import { GitBranch } from "lucide-react";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";

/** The one label both entry points share (the `/worktrees` picker's). */
export const SWITCH_WORKTREE_LABEL = "Switch worktree…";

/**
 * The chat ··· menu's entry point to the worktree picker (desktop dropdown):
 * the web analogue of the TUI's `/worktrees` built-in. Must render inside a
 * `DropdownMenuContent`. The dialog itself is owned by the workspace
 * (use-worktree-switch.tsx), so this only asks for it. The workspace offers
 * the item only when the daemon advertises `worktrees` AND the row's `fork`
 * capability (both a clear and a fork mint a successor).
 */
export function SwitchWorktreeMenuItem({ onSelect }: { onSelect: () => void }) {
  return (
    <DropdownMenuItem onClick={onSelect}>
      <GitBranch className="size-4 mr-2 text-muted-foreground" />
      {SWITCH_WORKTREE_LABEL}
    </DropdownMenuItem>
  );
}

/**
 * The same entry point for the mobile bottom-sheet menu, styled like its
 * sibling rows. `onDone` lets the sheet close itself after opening the dialog.
 */
export function SwitchWorktreeSheetItem({
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
      <GitBranch className="size-4 text-muted-foreground" />
      {SWITCH_WORKTREE_LABEL}
    </button>
  );
}
