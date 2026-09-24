// SPDX-License-Identifier: Apache-2.0

import { AlertTriangle, LogIn, RefreshCw } from "lucide-react";
import { useAuthRecovery } from "../../features/auth/auth-recovery-context";
import { PopupFallback } from "../../features/auth/popup-fallback";
import { Button } from "../ui/button";
import { statusBannerMessages, statusBannerState } from "./connection-status-banner-state";

/**
 * The public connection and sign-in facts share one banner contract with
 * the future shell layout (#1844). No detailed runtime query runs here.
 */
export function ConnectionStatusBanner() {
  const { banner, phase, popupIssue, retrySession, startPopupLogin } = useAuthRecovery();
  const state = statusBannerState(banner);

  if (state === "hidden") return null;

  return (
    <div
      className="flex shrink-0 flex-wrap items-center gap-2 bg-warning/90 px-4 py-1.5 text-xs font-medium text-black"
      role="status"
    >
      <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" />
      <span>{statusBannerMessages[state]}</span>
      {state === "session-check-failed" && (
        <Button onClick={retrySession} size="sm" variant="secondary">
          <RefreshCw aria-hidden="true" /> Retry session check
        </Button>
      )}
      {state === "sign-in" && (
        <>
          <Button onClick={startPopupLogin} size="sm" variant="secondary">
            <LogIn aria-hidden="true" /> {phase === "sign-in" ? "Sign in" : "Sign in again"}
          </Button>
          {popupIssue && <PopupFallback variant="secondary" />}
        </>
      )}
    </div>
  );
}
