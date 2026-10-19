// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { Bug, Copy } from "lucide-react";
import type { ReactNode } from "react";
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

/** A copy item the daemon's `copyId` capability gates. Must render inside a `DropdownMenuContent`. */
function GatedCopyItem({
  icon,
  label,
  name,
  session,
  value,
}: {
  icon: ReactNode;
  label: string;
  /** The toast's name for what was copied. */
  name: string;
  session: SessionSummaryResponse;
  value: string;
}) {
  const denied = !session.capabilities.copyId;
  const reason = session.capabilities.copyIdReason || COPY_ID_NOT_OFFERED;
  return (
    <DropdownMenuItem
      aria-description={denied ? reason : undefined}
      disabled={denied}
      onSelect={() => void copyToClipboard(value, name)}
      title={denied ? reason : undefined}
    >
      {icon}
      <span className="min-w-0">
        {label}
        {denied && (
          <span aria-hidden="true" className="block truncate text-xs text-muted-foreground">
            {reason}
          </span>
        )}
      </span>
    </DropdownMenuItem>
  );
}

/** "Copy session ID". Must render inside a `DropdownMenuContent`. */
export function CopySessionIdMenuItem({ session }: { session: SessionSummaryResponse }) {
  return (
    <GatedCopyItem
      icon={<Copy aria-hidden="true" />}
      label={COPY_SESSION_ID_LABEL}
      name="Session ID"
      session={session}
      value={session.id}
    />
  );
}

/**
 * "Copy debug target ID" on a debug chat; nothing on an ordinary one. The
 * same `copyId` gate holds: a chat that withholds IDs withholds its target's.
 */
export function CopyDebugTargetMenuItem({ session }: { session: SessionSummaryResponse }) {
  const targetId = session.debugTargetSessionId;
  if (!targetId) return null;
  return (
    <GatedCopyItem
      icon={<Bug aria-hidden="true" />}
      label={COPY_DEBUG_TARGET_LABEL}
      name="Debug target ID"
      session={session}
      value={targetId}
    />
  );
}
