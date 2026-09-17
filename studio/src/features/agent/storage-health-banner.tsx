"use client";

import { TriangleAlert } from "lucide-react";
import { useStorageHealth } from "./hooks/use-storage-health";

/**
 * The degraded-store banner (ADR 0226): when the daemon reports its session
 * store unavailable, corrupt families, or a failed background job, chats can
 * silently vanish from the sidebar — this says why. A healthy store (or a
 * daemon without `capabilities.storage_health`) renders nothing.
 *
 * Mounted in the workspace shell alongside the runtime status banners;
 * exported standalone so other surfaces can adopt it later.
 */
export function StorageHealthBanner() {
  const { health, degraded } = useStorageHealth();
  if (!degraded || health === null) return null;

  const detail = !health.available
    ? health.unavailableReason || "The session store cannot be read."
    : health.corruptCount > 0
      ? `${health.corruptCount} stored session${
          health.corruptCount === 1 ? "" : "s"
        } can no longer be loaded.`
      : health.lastFailure;

  return (
    <div
      role="alert"
      className="flex items-center justify-center gap-2 border-b border-amber-500/40 bg-amber-500/10 px-4 py-1.5 text-xs text-amber-700 dark:text-amber-400"
    >
      <TriangleAlert className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="font-medium">
        Session storage is degraded — some chats may be missing.
      </span>
      {detail && <span className="hidden truncate sm:inline">{detail}</span>}
    </div>
  );
}
