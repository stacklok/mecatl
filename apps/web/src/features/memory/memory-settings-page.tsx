// SPDX-License-Identifier: Apache-2.0

import type { ServerCapabilitiesResponse } from "@mecatl-studio/contracts";
import { ConsolidateMemoryCard } from "./consolidate-memory-card";
import { FactsAboutYou } from "./facts-about-you";
import { MemoryStores } from "./memory-stores";

/**
 * Settings → Memory: the read-only stores card (when the runtime's capabilities
 * are known), the remembered facts, then the consolidation review, which hides
 * itself when the agent reports no consolidation target at all.
 */
export function MemorySettingsPage({
  capabilities,
}: {
  capabilities?: Pick<ServerCapabilitiesResponse, "memory" | "userModel">;
}) {
  return (
    <>
      {capabilities && <MemoryStores capabilities={capabilities} />}
      <FactsAboutYou />
      <ConsolidateMemoryCard />
    </>
  );
}
