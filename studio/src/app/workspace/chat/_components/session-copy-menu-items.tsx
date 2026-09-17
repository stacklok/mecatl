"use client";

import { Bug, Copy } from "lucide-react";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import type { AgentSession } from "@/features/agent";
import { isMockTourSession } from "@/features/agent/mock-tour";
import { copyToClipboard } from "@/lib/clipboard";

export const COPY_SESSION_ID_LABEL = "Copy session ID";
export const COPY_DEBUG_TARGET_LABEL = "Copy debug target ID";
/** Shown under a disabled copy item when the daemon gave no closed reason. */
export const COPY_ID_NOT_OFFERED = "Not offered by this daemon";

/** The row's id copy: the daemon's `copy_id` capability gates it (omitted = denied). */
function copyIdGate(session: AgentSession): {
  disabled: boolean;
  reason: string;
} {
  if (session.canCopyId === true) return { disabled: false, reason: "" };
  return {
    disabled: true,
    reason: session.copyIdReason || COPY_ID_NOT_OFFERED,
  };
}

/** True for a row that has an id the daemon knows (never the mock tour). */
function realSession(session: AgentSession): boolean {
  return !isMockTourSession(session.id);
}

/**
 * "Copy session ID" for the sidebar row's desktop context menu (the TUI's
 * `c` on the `/session` overlay, one click from the list). Disabled, with
 * the daemon's reason, when the row's capabilities deny it. Renders nothing
 * for the mock tour row. Must render inside a `DropdownMenuContent`.
 */
export function CopySessionIdMenuItem({ session }: { session: AgentSession }) {
  if (!realSession(session)) return null;
  const gate = copyIdGate(session);
  return (
    <DropdownMenuItem
      disabled={gate.disabled}
      onClick={() => void copyToClipboard(session.id, "Session ID")}
      title={gate.disabled ? gate.reason : undefined}
    >
      <Copy className="size-4 mr-2 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        {COPY_SESSION_ID_LABEL}
        {gate.disabled && (
          <span className="block truncate text-xs text-muted-foreground">
            {gate.reason}
          </span>
        )}
      </span>
    </DropdownMenuItem>
  );
}

/**
 * "Copy debug target ID" for an AI-debug session's row menu (ADR 0254): the
 * id of the stored session this chat diagnoses, which otherwise lives only in
 * the Debug badge's hover text. Renders nothing on an ordinary row.
 */
export function CopyDebugTargetMenuItem({
  session,
}: {
  session: AgentSession;
}) {
  const targetId = session.debugTargetSessionId;
  if (!targetId) return null;
  return (
    <DropdownMenuItem
      onClick={() => void copyToClipboard(targetId, "Debug target ID")}
    >
      <Bug className="size-4 mr-2 shrink-0 text-muted-foreground" />
      <span className="min-w-0">{COPY_DEBUG_TARGET_LABEL}</span>
    </DropdownMenuItem>
  );
}

const SHEET_ROW =
  "flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

/** The mobile bottom-sheet twin of `CopySessionIdMenuItem`. */
export function CopySessionIdSheetItem({
  session,
  onDone,
}: {
  session: AgentSession;
  onDone?: () => void;
}) {
  if (!realSession(session)) return null;
  const gate = copyIdGate(session);
  return (
    <button
      type="button"
      className={SHEET_ROW}
      disabled={gate.disabled}
      onClick={() => {
        onDone?.();
        void copyToClipboard(session.id, "Session ID");
      }}
    >
      <Copy className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 text-left">
        {COPY_SESSION_ID_LABEL}
        {gate.disabled && (
          <span className="block truncate text-xs text-muted-foreground">
            {gate.reason}
          </span>
        )}
      </span>
    </button>
  );
}

/** The mobile bottom-sheet twin of `CopyDebugTargetMenuItem`. */
export function CopyDebugTargetSheetItem({
  session,
  onDone,
}: {
  session: AgentSession;
  onDone?: () => void;
}) {
  const targetId = session.debugTargetSessionId;
  if (!targetId) return null;
  return (
    <button
      type="button"
      className={SHEET_ROW}
      onClick={() => {
        onDone?.();
        void copyToClipboard(targetId, "Debug target ID");
      }}
    >
      <Bug className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 text-left">{COPY_DEBUG_TARGET_LABEL}</span>
    </button>
  );
}
