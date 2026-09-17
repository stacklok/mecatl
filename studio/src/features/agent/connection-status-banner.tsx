"use client";

/**
 * The live-feed connection banner — mecatui's "live feed reconnecting" and
 * live auth-recovery cues, as one strip in the workspace banner band.
 *
 * It reads the SDK client's connection status (`use-connection-status.ts`),
 * which the RuntimeStatusProvider's 5-second daemon probe cannot see: the
 * probe answers "is the daemon reachable", the SDK status answers "is the
 * SESSION FEED healthy". The two differ exactly when a durable watch (a run
 * driven by a schedule, another tab, a gRPC client — ADR 0250) drops and the
 * SDK reconnects it from the last cursor with backoff, or when the feed's
 * request is refused with a 401 while the probe still passes.
 *
 *  - `reconnecting`: an amber strip naming what is happening. The copy is
 *    the state, not a counter: the SDK's per-watch `AttachOptions.onReconnect`
 *    reports attempts for ONE watch, while this strip reads the client-wide
 *    status store, and a single number over several watches would mislead.
 *  - `unauthorized`: a destructive strip, and an IMMEDIATE runtime re-probe
 *    (once per transition) so the provider's own auth-recovery banner — the
 *    one that names the cause class and offers Sign in when OIDC is
 *    configured — takes over with the daemon's reason. Meanwhile this strip
 *    offers Retry, the sign-in settings page (external mode: the credential
 *    is server-held, so the copy never claims a browser login repairs an env
 *    token) or a daemon restart (managed mode: the controller re-spawns the
 *    daemon with a fresh token).
 *  - every other value renders nothing: `offline` is the runtime-status
 *    banner's fact, `incompatible` the API-major strip's, `connecting` and
 *    `online` say nothing worth a banner.
 *
 * It stays quiet while the runtime status is not "connected": the provider's
 * offline / auth-recovery banner already occupies the band, and two strips
 * saying the same thing in different words is worse than one.
 *
 * Out of scope here, by design: Studio's OWN prompt stream never reconnects
 * — `POST /prompt` ends its run on client disconnect, so a client-side
 * re-attach would find the run already over. That path keeps its error strip
 * with Retry (`chat-view.tsx`), which the reconnecting copy points at.
 */
import { TriangleAlert } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { restartHarnessDaemon } from "@/lib/harness/client";
import { SIGN_IN_SETTINGS_HREF } from "./auth-recovery-banner";
import { useConnectionStatus } from "./hooks/use-connection-status";
import { useRuntimeStatus } from "./runtime-status";

export const RECONNECTING_TITLE = "Live feed reconnecting…";
export const RECONNECTING_DETAIL =
  "The session event stream dropped; Studio is reconnecting from the last event it received. A run this tab started is not resumed — if it ended, the chat offers Retry.";
export const UNAUTHORIZED_TITLE =
  "The daemon refused Studio's credential on the live feed.";
export const UNAUTHORIZED_DETAIL_EXTERNAL =
  "Studio is re-checking the connection. If the deployment's sign-in expired, sign in again from Settings; a rejected static token needs the operator.";
export const UNAUTHORIZED_DETAIL_MANAGED =
  "Studio is re-checking the connection. Restarting the daemon issues it a fresh token.";

const amberAction =
  "rounded border border-amber-600/40 px-2 py-0.5 font-medium hover:bg-amber-500/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-amber-600";
const destructiveAction =
  "rounded border border-destructive/40 px-2 py-0.5 font-medium hover:bg-destructive/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-destructive disabled:opacity-60";

export function ConnectionStatusBanner() {
  const status = useConnectionStatus();
  const { state, mode, refresh } = useRuntimeStatus();
  const [restarting, setRestarting] = useState(false);
  const [actionError, setActionError] = useState("");
  // The re-probe fires once per transition INTO unauthorized, never per
  // render: the provider's probe is what decides which banner owns the band.
  const reprobed = useRef(false);

  useEffect(() => {
    if (status !== "unauthorized") {
      reprobed.current = false;
      return;
    }
    if (reprobed.current) return;
    reprobed.current = true;
    void refresh();
  }, [status, refresh]);

  const retry = useCallback(() => {
    setActionError("");
    void refresh();
  }, [refresh]);

  const restart = useCallback(async () => {
    setActionError("");
    setRestarting(true);
    try {
      await restartHarnessDaemon();
      await refresh();
    } catch (caught) {
      setActionError(
        caught instanceof Error ? caught.message : "The restart failed.",
      );
    } finally {
      setRestarting(false);
    }
  }, [refresh]);

  if (state !== "connected") return null;

  if (status === "reconnecting") {
    return (
      <div
        role="status"
        aria-live="polite"
        data-connection-status={status}
        className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 border-b border-amber-500/40 bg-amber-500/10 px-4 py-1.5 text-xs text-amber-800 dark:text-amber-300"
      >
        <TriangleAlert className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="font-medium">{RECONNECTING_TITLE}</span>
        <span className="hidden sm:inline">{RECONNECTING_DETAIL}</span>
        <button type="button" onClick={retry} className={amberAction}>
          Check connection
        </button>
      </div>
    );
  }

  if (status === "unauthorized") {
    return (
      <div
        role="alert"
        data-connection-status={status}
        className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 border-b border-destructive/30 bg-destructive/10 px-4 py-1.5 text-xs text-destructive"
      >
        <TriangleAlert className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="font-medium">{UNAUTHORIZED_TITLE}</span>
        <span className="hidden sm:inline">
          {mode === "managed"
            ? UNAUTHORIZED_DETAIL_MANAGED
            : UNAUTHORIZED_DETAIL_EXTERNAL}
        </span>
        {mode === "managed" ? (
          <button
            type="button"
            onClick={() => void restart()}
            disabled={restarting}
            className={destructiveAction}
          >
            {restarting ? "Restarting…" : "Restart daemon"}
          </button>
        ) : (
          <Link href={SIGN_IN_SETTINGS_HREF} className={destructiveAction}>
            Open sign-in settings
          </Link>
        )}
        <button type="button" onClick={retry} className={destructiveAction}>
          Retry
        </button>
        {actionError ? (
          <span className="basis-full text-center">{actionError}</span>
        ) : null}
      </div>
    );
  }

  return null;
}
