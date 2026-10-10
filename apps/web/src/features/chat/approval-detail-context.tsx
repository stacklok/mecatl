// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext } from "react";
import type { ApprovalRequest } from "./approval-panel";

/**
 * Opens an ask in the surface's detail panel. Only a surface with a side-panel
 * slot provides it; without a provider the card hides its detail button.
 */
export const ApprovalDetailContext = createContext<
  ((approval: ApprovalRequest) => void) | undefined
>(undefined);

export function useOpenApprovalDetail(): ((approval: ApprovalRequest) => void) | undefined {
  return useContext(ApprovalDetailContext);
}
