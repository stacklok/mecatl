"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { RuntimeStatusLine } from "../_components/runtime-status-line";
import { StorageMaintenanceSection } from "../_components/storage-maintenance-section";

/**
 * Storage: one card — what the agent has saved, and a clean-up of old runs.
 * The store location, the retention limits and the layout migration are
 * operator configuration and are deliberately not offered here.
 */
export default function StorageSettingsPage() {
  const runtime = useHarnessRuntime();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <StorageMaintenanceSection runtime={runtime} />
    </>
  );
}
