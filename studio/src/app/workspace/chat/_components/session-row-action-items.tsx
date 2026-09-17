"use client";

import { GitFork, ScrollText } from "lucide-react";
import { DropdownMenuItem } from "@/components/ui/dropdown-menu";
import type { AgentSession } from "@/features/agent";
import { isMockTourSession } from "@/features/agent/mock-tour";
import { capabilityReasonLabel } from "@/lib/session-kinds";

/**
 * The two per-row actions the TUI's /sessions overlay has and the sidebar
 * lacked: `v` (view a chat's transcript read-only, without making it the live
 * chat) and `f` (fork the chat as-is — a copy on the same model, provider and
 * placement). Eligibility comes from the daemon row's capabilities, never
 * re-derived here: an omitted capability is a denial, and a disabled item
 * shows the daemon's own reason inline (disabled menu items swallow pointer
 * events, so a tooltip could not open). Neither is offered for the Labs mock
 * tour row, which the daemon knows nothing about.
 */

export const VIEW_TRANSCRIPT_LABEL = "View transcript";
export const FORK_CHAT_LABEL = "Fork chat";
/** Under a disabled View transcript when the daemon gave no closed reason. */
export const TRANSCRIPT_NOT_OFFERED = "Transcript unavailable";
/** Under a disabled Fork chat when the daemon gave no closed reason. */
export const FORK_NOT_OFFERED = "Not offered by this daemon";

export interface RowActionGate {
  disabled: boolean;
  /** The plain-words reason shown under a disabled item ("" when enabled). */
  reason: string;
}

/** The row's `view_transcript` capability, with its reason in plain words. */
export function viewTranscriptGate(session: AgentSession): RowActionGate {
  if (session.canViewTranscript === true)
    return { disabled: false, reason: "" };
  return {
    disabled: true,
    reason:
      capabilityReasonLabel(session.viewTranscriptReason) ||
      TRANSCRIPT_NOT_OFFERED,
  };
}

/** The row's `fork` capability (the successor offer), reason in plain words. */
export function forkGate(session: AgentSession): RowActionGate {
  if (session.canFork === true) return { disabled: false, reason: "" };
  return {
    disabled: true,
    reason: capabilityReasonLabel(session.forkReason) || FORK_NOT_OFFERED,
  };
}

/** True for a row the daemon knows (never the Labs mock tour). */
function viewTranscriptOffered(session: AgentSession): boolean {
  return !isMockTourSession(session.id);
}

/**
 * Whether Fork is offered on a row at all: a real chat that is not an
 * AI-debug session (ADR 0254). A debug chat's copy would drop the binding to
 * the session it diagnoses, so — like Clear conversation and Switch worktree
 * — the successor-minting actions stay off it.
 */
export function forkOffered(session: AgentSession): boolean {
  return !isMockTourSession(session.id) && !session.debugTargetSessionId;
}

function reasonLine(reason: string) {
  return (
    <span className="block truncate text-xs text-muted-foreground">
      {reason}
    </span>
  );
}

/** Sidebar row / chat header "View transcript" (desktop dropdown). Must render
 *  inside a `DropdownMenuContent`. */
export function ViewTranscriptMenuItem({
  session,
  onSelect,
}: {
  session: AgentSession;
  onSelect: () => void;
}) {
  if (!viewTranscriptOffered(session)) return null;
  const gate = viewTranscriptGate(session);
  return (
    <DropdownMenuItem
      disabled={gate.disabled}
      onClick={onSelect}
      title={gate.disabled ? gate.reason : undefined}
    >
      <ScrollText className="size-4 mr-2 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        {VIEW_TRANSCRIPT_LABEL}
        {gate.disabled && reasonLine(gate.reason)}
      </span>
    </DropdownMenuItem>
  );
}

/** Sidebar row / chat header "Fork chat" (desktop dropdown). Must render
 *  inside a `DropdownMenuContent`. */
export function ForkChatMenuItem({
  session,
  onSelect,
}: {
  session: AgentSession;
  onSelect: () => void;
}) {
  if (!forkOffered(session)) return null;
  const gate = forkGate(session);
  return (
    <DropdownMenuItem
      disabled={gate.disabled}
      onClick={onSelect}
      title={gate.disabled ? gate.reason : undefined}
    >
      <GitFork className="size-4 mr-2 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        {FORK_CHAT_LABEL}
        {gate.disabled && reasonLine(gate.reason)}
      </span>
    </DropdownMenuItem>
  );
}

const SHEET_ROW =
  "flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

/** The mobile bottom-sheet twin of `ViewTranscriptMenuItem`. */
export function ViewTranscriptSheetItem({
  session,
  onSelect,
  onDone,
}: {
  session: AgentSession;
  onSelect: () => void;
  /** Lets the sheet close itself once the action started. */
  onDone?: () => void;
}) {
  if (!viewTranscriptOffered(session)) return null;
  const gate = viewTranscriptGate(session);
  return (
    <button
      type="button"
      className={SHEET_ROW}
      disabled={gate.disabled}
      title={gate.disabled ? gate.reason : undefined}
      onClick={() => {
        onDone?.();
        onSelect();
      }}
    >
      <ScrollText className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 text-left">
        {VIEW_TRANSCRIPT_LABEL}
        {gate.disabled && reasonLine(gate.reason)}
      </span>
    </button>
  );
}

/** The mobile bottom-sheet twin of `ForkChatMenuItem`. */
export function ForkChatSheetItem({
  session,
  onSelect,
  onDone,
}: {
  session: AgentSession;
  onSelect: () => void;
  onDone?: () => void;
}) {
  if (!forkOffered(session)) return null;
  const gate = forkGate(session);
  return (
    <button
      type="button"
      className={SHEET_ROW}
      disabled={gate.disabled}
      title={gate.disabled ? gate.reason : undefined}
      onClick={() => {
        onDone?.();
        onSelect();
      }}
    >
      <GitFork className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 text-left">
        {FORK_CHAT_LABEL}
        {gate.disabled && reasonLine(gate.reason)}
      </span>
    </button>
  );
}
