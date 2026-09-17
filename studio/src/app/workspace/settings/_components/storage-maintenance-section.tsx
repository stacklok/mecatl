"use client";

import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useStorageHealth } from "@/features/agent/hooks/use-storage-health";
import { useStorageMaintenance } from "@/features/agent/hooks/use-storage-maintenance";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { StorageCleanup } from "./storage-cleanup-card";
import { StorageHealthCard } from "./storage-health-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;

/**
 * The Storage page's one card: the health summary with the clean-up block
 * beneath it, sharing ONE storage-health read so a finished clean-up's
 * `refreshHealth` updates the numbers the person is looking at. The
 * maintenance hook's migration controller is not surfaced — the layout
 * migration is operator maintenance, not something an office user runs.
 */
export function StorageMaintenanceSection({ runtime }: { runtime: Runtime }) {
  const { serverCapabilities } = useRuntimeStatus();
  const storage = useStorageHealth();
  const maintenance = useStorageMaintenance({
    refreshHealth: storage.refresh,
  });
  return (
    <StorageHealthCard
      live={runtime.live}
      supported={storage.supported}
      health={storage.health}
      onRefresh={storage.refresh}
    >
      <StorageCleanup
        supported={serverCapabilities.storage_cleanup === true}
        cleanup={maintenance.cleanup}
      />
    </StorageHealthCard>
  );
}
