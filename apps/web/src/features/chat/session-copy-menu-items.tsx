// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { Bug, Copy } from "lucide-react";
import { DropdownMenuItem } from "../../components/ui/dropdown-menu";
import { copyToClipboard } from "../../lib/clipboard";

/**
 * A session's copy actions for its menus, ported from the prototype's
 * `session-copy-menu-items.tsx`. The daemon's `copyId` capability gates the
 * ID copy; a disabled item shows the daemon's reason inline, because a
 * disabled menu item cannot open a tooltip.
 */

export const COPY_SESSION_ID_LABEL = "Copy session ID";
export const COPY_DEBUG_TARGET_LABEL = "Copy debug target ID";
/** Shown under a disabled copy item when the daemon gave no closed reason. */
export const COPY_ID_NOT_OFFERED = "Not offered by this daemon";

/** "Copy session ID". Must render inside a `DropdownMenuContent`. */
export function CopySessionIdMenuItem({ session }: { session: SessionSummaryResponse }) {
  const denied = !session.capabilities.copyId;
  const reason = session.capabilities.copyIdReason || COPY_ID_NOT_OFFERED;
  return (
    <DropdownMenuItem
      aria-description={denied ? reason : undefined}
      disabled={denied}
      onSelect={() => void copyToClipboard(session.id, "Session ID")}
      title={denied ? reason : undefined}
    >
      <Copy aria-hidden="true" />
      <span className="min-w-0">
        {COPY_SESSION_ID_LABEL}
        {denied && (
          <span aria-hidden="true" className="block truncate text-xs text-muted-foreground">
            {reason}
          </span>
        )}
      </span>
    </DropdownMenuItem>
  );
}

/** "Copy debug target ID" on a debug chat; nothing on an ordinary one. */
export function CopyDebugTargetMenuItem({ session }: { session: SessionSummaryResponse }) {
  const targetId = session.debugTargetSessionId;
  if (!targetId) return null;
  return (
    <DropdownMenuItem onSelect={() => void copyToClipboard(targetId, "Debug target ID")}>
      <Bug aria-hidden="true" />
      {COPY_DEBUG_TARGET_LABEL}
    </DropdownMenuItem>
  );
}
