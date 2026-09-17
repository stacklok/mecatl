"use client";

import { type ReactElement, useCallback, useState } from "react";
import { WorktreePickerDialog } from "@/features/agent/components/worktree-picker-dialog";

/** The chat-workspace state the worktree switch reads. */
export interface WorktreeSwitchDeps {
  /** The selected chat's row; null for a draft, the mock tour, or an AI-debug
   *  chat (which keeps its binding — no successor of any shape). */
  session: { id: string; title: string; canFork?: boolean } | null;
  /** The daemon's `worktrees` capability (compatibility document). */
  supported: boolean;
  /** Adopts the new chat: refresh the list, move the UI there. */
  onSwitched: (newSessionId: string) => void | Promise<void>;
}

/**
 * Switch worktree — the TUI's `/worktrees` placement picker, for the
 * browser. Owns the picker dialog's open state and renders it; the header
 * menu (desktop and mobile) gets `openWorktreePicker`, which is undefined
 * whenever the item must not be offered: the daemon does not advertise
 * `worktrees`, there is no daemon session (a draft — ListWorktrees is keyed
 * by session id), or the row's `fork` capability is off (both a clear and a
 * fork mint a successor, so the same daemon verdict gates the picker).
 */
export function useWorktreeSwitch(deps: WorktreeSwitchDeps): {
  openWorktreePicker: (() => void) | undefined;
  worktreePickerDialog: ReactElement;
} {
  const [open, setOpen] = useState(false);
  const { session, supported, onSwitched } = deps;
  const offered = supported && session !== null && session.canFork === true;

  const openWorktreePicker = useCallback(() => setOpen(true), []);

  const worktreePickerDialog = (
    <WorktreePickerDialog
      sessionId={offered ? session.id : null}
      sessionTitle={session?.title ?? ""}
      open={open && offered}
      onOpenChange={setOpen}
      onSwitched={(newId) => onSwitched(newId)}
    />
  );

  return {
    openWorktreePicker: offered ? openWorktreePicker : undefined,
    worktreePickerDialog,
  };
}
