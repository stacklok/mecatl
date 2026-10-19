// SPDX-License-Identifier: Apache-2.0

import { AlertTriangle, LogIn, RefreshCw } from "lucide-react";
import { useAuthRecovery } from "../../features/auth/auth-recovery-context";
import { PopupFallback } from "../../features/auth/popup-fallback";
import { Button } from "../ui/button";
import { statusBannerMessages, statusBannerState } from "./connection-status-banner-state";

/**
 * The band's actions take the prototype's outlined strip-button look, drawn
 * in the band's own warning tokens so text and focus rings keep its contrast.
 */
const bandAction =
  "border border-warning-foreground/40 bg-transparent text-warning-foreground hover:bg-warning-foreground/10 focus-visible:ring-warning-foreground focus-visible:ring-offset-warning";

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
      className="relative z-[60] flex min-h-11 min-w-0 w-full shrink-0 flex-wrap items-center justify-center gap-x-3 gap-y-1 bg-warning py-2 pr-[max(1rem,env(safe-area-inset-right))] pl-[max(1rem,env(safe-area-inset-left))] text-xs text-warning-foreground"
      role="status"
    >
      <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" />
      <span className="min-w-0 break-words font-medium">{statusBannerMessages[state]}</span>
      {state === "session-check-failed" && (
        <Button className={bandAction} onClick={retrySession} size="sm" variant="secondary">
          <RefreshCw aria-hidden="true" /> Retry session check
        </Button>
      )}
      {state === "sign-in" && (
        <>
          <Button className={bandAction} onClick={startPopupLogin} size="sm" variant="secondary">
            <LogIn aria-hidden="true" /> {phase === "sign-in" ? "Sign in" : "Sign in again"}
          </Button>
          {popupIssue && <PopupFallback className={bandAction} variant="secondary" />}
        </>
      )}
    </div>
  );
}
