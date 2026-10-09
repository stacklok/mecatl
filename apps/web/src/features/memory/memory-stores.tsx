// SPDX-License-Identifier: Apache-2.0

import type { ServerCapabilitiesResponse } from "@mecatl-studio/contracts";
import { Note, SettingsCard } from "../settings/settings-card";

/**
 * Settings → Memory: which memory stores the running agent has, as a
 * read-only report. Memory is configured where the agent runs; there is no
 * write path for it in Studio, so this card offers no controls.
 */
const stores = [
  { capability: "memory", label: "Project memory", testId: "memory-status-project" },
  { capability: "userModel", label: "Facts about you", testId: "memory-status-user-model" },
] as const;

export function MemoryStores({
  capabilities,
}: {
  capabilities: Pick<ServerCapabilitiesResponse, "memory" | "userModel">;
}) {
  return (
    <SettingsCard title="Memory">
      <div className="space-y-4" data-testid="memory-stores">
        <Note>Memory is set where the agent runs and can&rsquo;t be changed here.</Note>
        <div className="divide-y divide-border/60 rounded-lg border px-4">
          {stores.map((store) => (
            <div
              className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2.5"
              key={store.capability}
            >
              <p className="text-sm">{store.label}</p>
              <span className="text-sm text-muted-foreground" data-testid={store.testId}>
                {capabilities[store.capability] ? "On" : "Off"}
              </span>
            </div>
          ))}
        </div>
      </div>
    </SettingsCard>
  );
}
