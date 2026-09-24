// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext } from "react";
import type { StatusBannerInput } from "../../components/shell/connection-status-banner-state";
import type { RecoveryPhase } from "./auth-recovery-state";

export interface AuthRecoveryContextValue {
  banner: StatusBannerInput;
  loginUrl: string;
  phase: RecoveryPhase;
  popupIssue: "blocked" | "closed" | "failed" | "waiting" | null;
  retrySession: () => void;
  startPopupLogin: () => void;
}

export const AuthRecoveryContext = createContext<AuthRecoveryContextValue | null>(null);

export function useAuthRecovery(): AuthRecoveryContextValue {
  const value = useContext(AuthRecoveryContext);
  if (!value) throw new Error("Studio auth recovery context is missing");
  return value;
}
