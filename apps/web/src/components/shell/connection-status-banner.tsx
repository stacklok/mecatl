// SPDX-License-Identifier: Apache-2.0

import { AlertTriangle, LogIn, RefreshCw } from "lucide-react";
import { useAuthRecovery } from "../../features/auth/auth-recovery-context";
import { PopupFallback } from "../../features/auth/popup-fallback";
import { Button } from "../ui/button";
import { statusBannerMessages, statusBannerState } from "./connection-status-banner-state";

/**
 * The public connection and sign-in facts use the shell's global status band.
 * The transient band stays reserved for independent notices. No detailed
 * runtime query runs here.
 */
export function ConnectionStatusBanner({
  placement = "global",
}: {
  placement?: "global" | "transient";
}) {
  const { banner, phase, popupIssue, retrySession, startPopupLogin } = useAuthRecovery();
  const state = statusBannerState(banner);

  if (state === "hidden" || placement === "transient") return null;

  return (
    <div
      className="flex min-h-11 min-w-0 w-full shrink-0 flex-wrap items-center gap-2 bg-warning/90 py-2 pr-[max(1rem,env(safe-area-inset-right))] pl-[max(1rem,env(safe-area-inset-left))] text-xs font-medium text-black"
      role="status"
    >
      <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" />
      <span className="min-w-0 break-words">{statusBannerMessages[state]}</span>
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
