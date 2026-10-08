// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext } from "react";
import type { ApprovalRequest } from "./approval-panel";

/** The visible ask chosen by the surface's Escape handler, never a verdict authority. */
export const EscapeHintContext = createContext<ApprovalRequest | undefined>(undefined);

export function useActiveEscapeAsk(approval: ApprovalRequest): boolean {
  return useContext(EscapeHintContext) === approval;
}
