// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { GitFork, ScrollText } from "lucide-react";
import { DropdownMenuItem } from "../../components/ui/dropdown-menu";

/**
 * "View transcript" and "Fork chat" for a session's menus, ported from the
 * prototype's `session-row-action-items.tsx`. Eligibility comes from the
 * daemon row's capabilities, never re-derived here: a disabled item shows
 * the daemon's own reason inline.
 */

export const VIEW_TRANSCRIPT_LABEL = "View transcript";
export const FORK_CHAT_LABEL = "Fork chat";
export const TRANSCRIPT_NOT_OFFERED = "Transcript unavailable";
export const FORK_NOT_OFFERED = "Not offered by this daemon";

/** Whether Fork is offered at all: never on a debug chat, whose copy would lose its binding. */
export function forkOffered(session: SessionSummaryResponse): boolean {
  return !session.debugTargetSessionId;
}

/** The reason under a disabled item. It is the item's description, so its name stays the action. */
function ReasonLine({ reason }: { reason: string }) {
  return (
    <span aria-hidden="true" className="block truncate text-xs text-muted-foreground">
      {reason}
    </span>
  );
}

export function ViewTranscriptMenuItem({
  onSelect,
  session,
}: {
  onSelect: () => void;
  session: SessionSummaryResponse;
}) {
  const denied = !session.capabilities.viewTranscript;
  const reason = session.capabilities.viewTranscriptReason || TRANSCRIPT_NOT_OFFERED;
  return (
    <DropdownMenuItem
      aria-description={denied ? reason : undefined}
      disabled={denied}
      onSelect={onSelect}
      title={denied ? reason : undefined}
    >
      <ScrollText aria-hidden="true" />
      <span className="min-w-0">
        {VIEW_TRANSCRIPT_LABEL}
        {denied && <ReasonLine reason={reason} />}
      </span>
    </DropdownMenuItem>
  );
}

export function ForkChatMenuItem({
  disabled = false,
  onSelect,
  session,
}: {
  /** The caller's own gate (a fork already in flight, or no model to carry). */
  disabled?: boolean;
  onSelect: () => void;
  session: SessionSummaryResponse;
}) {
  if (!forkOffered(session)) return null;
  const denied = !session.capabilities.fork;
  const reason = session.capabilities.forkReason || FORK_NOT_OFFERED;
  return (
    <DropdownMenuItem
      aria-description={denied ? reason : undefined}
      disabled={denied || disabled}
      onSelect={onSelect}
      title={denied ? reason : undefined}
    >
      <GitFork aria-hidden="true" />
      <span className="min-w-0">
        {FORK_CHAT_LABEL}
        {denied && <ReasonLine reason={reason} />}
      </span>
    </DropdownMenuItem>
  );
}
