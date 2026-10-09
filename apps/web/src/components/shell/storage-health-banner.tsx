// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions, getStorageHealthOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { useAuthRecovery } from "@/features/auth/auth-recovery-context";
import { storageHealthStatus } from "@/features/settings/storage-settings";

/**
 * The degraded-store notice, ported from the prototype's
 * `features/runtime/storage-health-banner.tsx`. When the agent cannot read
 * its saved chats, some can no longer be opened, or an automatic clean-up
 * failed, chats can be missing from the chat list, and this says why.
 *
 * It reads what the BFF already serves (the runtime's `storageHealth`
 * capability and `/api/v1/storage/health`, the same queries Settings uses)
 * and asks for nothing until the identity is verified, the agent is online,
 * and it reports storage health. Otherwise, or with a healthy store, it
 * renders nothing. The detail line is Settings → Storage's own wording.
 */
export function StorageHealthBanner() {
  const { phase } = useAuthRecovery();
  const ready = phase === "ready";
  const runtime = useQuery({ ...getRuntimeOptions(), enabled: ready });
  const reported =
    runtime.data?.connection === "online" && runtime.data.capabilities.storageHealth === true;
  const health = useQuery({ ...getStorageHealthOptions(), enabled: ready && reported });

  if (!ready || !reported || !health.data?.supported) return null;
  const status = storageHealthStatus(health.data);
  if (status.label === "Healthy") return null;

  return (
    <div
      className="flex min-h-11 min-w-0 flex-wrap items-center justify-center gap-x-3 gap-y-1 bg-warning px-4 py-2 text-xs text-warning-foreground"
      role="status"
    >
      <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" />
      <span className="font-medium">Session storage is degraded. Some chats may be missing.</span>
      <span className="hidden min-w-0 break-words sm:inline">{status.detail}</span>
    </div>
  );
}
