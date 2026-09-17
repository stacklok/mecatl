"use client";

import { Badge } from "@/components/ui/badge";
import { modeBadgeVariant, permissionModeLabel } from "@/lib/permission-mode";
import type { SessionPermissionMode } from "@/lib/protocol";

/**
 * The chat header's permission-mode badge — mecatui's `mode <x>` header
 * segment, coloured by mode (plan = info, accept edits = success). Silent
 * for the everyday posture: Manual with nothing held renders NOTHING, so a
 * badge in the header always means the session is NOT ask-me-first, or is
 * about to change.
 *
 * A switch made mid-run is held until the run ends (`pendingMode`, the TUI's
 * `mode <target> pending`): the badge shows the TARGET with "· pending" so
 * the user sees their pick took, and the title says when it lands. Only
 * rendered where the mode can be set (`enabled`) — the read-only mock tour
 * and the AI-debug chat have no posture to show.
 */
export function PermissionModeBadge({
  mode,
  pendingMode = null,
  enabled = true,
}: {
  /** The daemon-confirmed mode. */
  mode: SessionPermissionMode;
  /** A held mid-run switch, or null. */
  pendingMode?: SessionPermissionMode | null;
  /** False on surfaces with no Mode selector: renders nothing. */
  enabled?: boolean;
}) {
  const pending = pendingMode != null && pendingMode !== mode;
  const shown = pending ? pendingMode : mode;
  if (!enabled || (shown === "default" && !pending)) return null;
  const label = permissionModeLabel(shown);
  const title = pending
    ? `Permission mode: ${label} — pending, applies when the run ends (currently ${permissionModeLabel(mode)})`
    : `Permission mode: ${label}`;
  return (
    <Badge
      variant={modeBadgeVariant(shown) ?? "muted"}
      className="shrink-0 select-none"
      title={title}
      data-testid="permission-mode-badge"
      data-mode={shown}
      data-pending={pending ? "true" : undefined}
    >
      <span className="sr-only">Permission mode: </span>
      {label}
      {pending ? " · pending" : ""}
    </Badge>
  );
}
