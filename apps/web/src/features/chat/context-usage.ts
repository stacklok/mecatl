// SPDX-License-Identifier: Apache-2.0

import type { SessionUsageResponse } from "@mecatl-studio/contracts";

export function contextUtilization(
  usage: Pick<SessionUsageResponse, "inputTokens" | "outputTokens">,
  contextWindow: string,
): number | null {
  const window = Number(contextWindow);
  if (!Number.isFinite(window) || window <= 0) return null;
  const used = Math.max(0, Number(usage.inputTokens)) + Math.max(0, Number(usage.outputTokens));
  if (!Number.isFinite(used)) return null;
  return Math.min(1, used / window);
}
