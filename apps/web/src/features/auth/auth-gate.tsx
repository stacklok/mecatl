// SPDX-License-Identifier: Apache-2.0

import { getAuthSessionOptions, getPublicStatusOptions } from "@mecatl-studio/contracts/query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogIn, RefreshCw } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import {
  statusBannerMessages,
  statusBannerState,
} from "../../components/shell/connection-status-banner-state";
import { GlobalStatusSlot } from "../../components/shell/global-status-slot";
import { Button } from "../../components/ui/button";
import { accountStorageKey } from "../../lib/account-storage";
import { onAuthenticationRequired, setRequestRecoveryState } from "../../lib/api-client";
import { initializeProfilePreferences } from "../../lib/profile-preferences";
import { AuthRecoveryContext, useAuthRecovery } from "./auth-recovery-context";
import {
  acceptsPopupResult,
  commitRecoveryCheck,
  isPublicQuery,
  publicStatusFetchFailed,
  type RecoveryState,
  type SessionCheck,
} from "./auth-recovery-state";
import { PopupFallback } from "./popup-fallback";

const initialRecovery: RecoveryState = {
  identityEpoch: 0,
  phase: "checking",
  workspaceMounted: false,
};

function currentLoginUrl(popup: boolean): string {
  const returnTo = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  return authLoginUrl(returnTo, popup);
}

/** Keep the route while the arrival seed stays in the initiating tab. */
export function authLoginUrl(returnTo: string, popup = false) {
  // The arrival prompt belongs to the initiating tab. A login popup or new
  // tab returns to the chat route without carrying the seed through the
  // server's bounded return_to parameter.
  const target = new URL(returnTo, "https://studio.invalid");
  if (target.pathname === "/workspace/chat") {
    target.searchParams.delete("prompt");
    target.searchParams.delete("send");
  }
  const query = new URLSearchParams({
    return_to: `${target.pathname}${target.search}${target.hash}`,
  });
  if (popup) query.set("flow", "popup");
  return `/api/v1/auth/login?${query}`;
}

/** Public status is always fetched; authenticated feature queries mount only after verification. */
export function AuthGate({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const session = useQuery({
    ...getAuthSessionOptions(),
    refetchInterval: 60_000,
    retry: false,
    staleTime: 30_000,
  });
  const status = useQuery({
    ...getPublicStatusOptions(),
    refetchInterval: 15_000,
    retry: false,
    staleTime: 5_000,
  });
  const [recovery, setRecovery] = useState(initialRecovery);
  const recoveryRef = useRef(initialRecovery);
  const processedSession = useRef("");
  const [popupIssue, setPopupIssue] = useState<"blocked" | "closed" | "failed" | "waiting" | null>(
    null,
  );
  const popup = useRef<{ attempt: number; window: Window } | null>(null);
  const attempt = useRef(0);

  const applyCheck = useCallback(
    (check: SessionCheck) => {
      const transition = commitRecoveryCheck(recoveryRef.current, check, queryClient);
      // Apply account-scoped scale only after storage reconciliation, before
      // this identity's workspace renders. A sign-out resets it to default.
      if (transition.state.phase === "ready" || transition.clearAccount) {
        initializeProfilePreferences();
      }
      recoveryRef.current = transition.state;
      setRequestRecoveryState(transition.state);
      setRecovery(transition.state);
      if (transition.refetchReads) {
        void queryClient.invalidateQueries({
          predicate: (query) => !isPublicQuery(query.queryKey),
        });
      }
    },
    [queryClient],
  );

  useEffect(() => {
    if (session.isPending) return;
    const observation = `${session.status}:${session.dataUpdatedAt}:${session.errorUpdatedAt}`;
    if (processedSession.current === observation) return;
    processedSession.current = observation;
    if (session.isError || !session.data) {
      applyCheck({ kind: "session-check-failed" });
    } else if (session.data.mode !== "oidc") {
      applyCheck({ kind: "disabled" });
    } else if (session.data.status === "anonymous") {
      applyCheck({ kind: "anonymous" });
    } else {
      applyCheck({ kind: "authenticated", account: session.data.account });
    }
  }, [
    applyCheck,
    session.data,
    session.dataUpdatedAt,
    session.errorUpdatedAt,
    session.isError,
    session.isPending,
    session.status,
  ]);

  useEffect(
    () =>
      onAuthenticationRequired(() => {
        if (recoveryRef.current.phase !== "ready") return;
        applyCheck({ kind: "anonymous" });
        void session.refetch();
        void status.refetch();
      }),
    [applyCheck, session.refetch, status.refetch],
  );

  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== accountStorageKey || event.newValue === recoveryRef.current.account) return;
      // A peer tab explicitly signed out or changed account. Unlike an
      // expired session, its old workspace must disappear immediately.
      applyCheck({ kind: "signed-out" });
      void session.refetch();
      void status.refetch();
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [applyCheck, session.refetch, status.refetch]);

  const verifySession = useCallback(
    async (fallbackIssue: "closed" | "failed" = "closed", keepWaiting = false) => {
      const checked = await session.refetch();
      void status.refetch();
      if (checked.data?.status === "authenticated" && checked.data.account) {
        setPopupIssue(null);
      } else if (checked.data?.status === "disabled") {
        setPopupIssue(null);
      } else if (keepWaiting && popup.current && !popup.current.window.closed) {
        setPopupIssue("waiting");
      } else {
        setPopupIssue(fallbackIssue);
      }
    },
    [session.refetch, status.refetch],
  );

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      const active = popup.current;
      if (!active) return;
      if (
        !acceptsPopupResult(event, {
          activeAttempt: attempt.current,
          attempt: active.attempt,
          origin: window.location.origin,
          popup: active.window,
        })
      )
        return;
      popup.current = null;
      void verifySession(event.data.result === "failure" ? "failed" : "closed");
    };
    const onFocus = () => {
      if (recoveryRef.current.phase !== "ready" || popupIssue !== null)
        void verifySession("closed", true);
    };
    window.addEventListener("message", onMessage);
    window.addEventListener("focus", onFocus);
    const interval = window.setInterval(() => {
      if (!popup.current?.window.closed) return;
      popup.current = null;
      void verifySession();
    }, 500);
    return () => {
      window.removeEventListener("message", onMessage);
      window.removeEventListener("focus", onFocus);
      window.clearInterval(interval);
    };
  }, [popupIssue, verifySession]);

  const startPopupLogin = useCallback(() => {
    const nextAttempt = ++attempt.current;
    popup.current?.window.close();
    // This call stays in the initiating click stack so browser popup policy can allow it.
    const opened = window.open(currentLoginUrl(true), "_blank", "popup,width=520,height=720");
    if (!opened) {
      popup.current = null;
      setPopupIssue("blocked");
      return;
    }
    popup.current = { attempt: nextAttempt, window: opened };
    setPopupIssue("waiting");
  }, []);

  const bannerStatus =
    recovery.phase === "sign-in" && session.data?.mode === "oidc" && status.data
      ? { ...status.data, signInRequired: true }
      : status.data;
  const context = {
    banner: {
      authenticated: recovery.phase === "ready",
      publicStatus: status.isError ? undefined : bannerStatus,
      publicStatusFailed: publicStatusFetchFailed(status.isError, status.error),
      sessionCheckFailed: recovery.phase === "verification-unavailable",
    },
    loginUrl: currentLoginUrl(false),
    phase: recovery.phase,
    popupIssue,
    retrySession: () => {
      void session.refetch();
      void status.refetch();
    },
    startPopupLogin,
  };

  // A new or missing OIDC account must never render the old identity's
  // workspace while the effect below clears storage and authenticated queries.
  const unsafeIdentity =
    recovery.workspaceMounted &&
    session.isSuccess &&
    session.data.mode === "oidc" &&
    session.data.status === "authenticated" &&
    (!session.data.account || recovery.account !== session.data.account);

  return (
    <AuthRecoveryContext.Provider value={context}>
      {recovery.workspaceMounted && !unsafeIdentity ? (
        <div key={recovery.identityEpoch}>{children}</div>
      ) : (
        <PublicShell />
      )}
    </AuthRecoveryContext.Provider>
  );
}

function PublicShell() {
  const { banner, phase, popupIssue, retrySession, startPopupLogin } = useAuthRecovery();
  const state = statusBannerState(banner);
  const unavailable = state === "bff-unavailable" || state === "daemon-unavailable";
  return (
    <div className="flex min-h-dvh min-w-0 flex-col bg-[var(--shell-gradient-mid)] pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)]">
      <GlobalStatusSlot />
      <div
        className="flex min-h-0 min-w-0 flex-1 flex-col bg-[radial-gradient(120%_140%_at_20%_30%,var(--shell-gradient-start)_0%,var(--shell-gradient-mid)_50%,var(--shell-gradient-end)_100%)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)]"
        data-shell-gradient=""
      >
        <main className="flex min-h-0 min-w-0 flex-1 items-center justify-center p-6">
          <section className="w-full max-w-md rounded-2xl border bg-card p-6 text-card-foreground shadow-xl">
            <h1 className="text-xl font-semibold">Mecatl Studio</h1>
            <p aria-live="polite" className="mt-2 text-sm text-muted-foreground">
              {unavailable
                ? statusBannerMessages[state]
                : phase === "verification-unavailable"
                  ? "We couldn't verify your sign-in. Your workspace will appear after a successful check."
                  : state === "sign-in"
                    ? "Sign in to open this workspace."
                    : "Checking your sign-in and the Mecatl connection…"}
            </p>
            <div className="mt-5 flex flex-wrap gap-2">
              {state === "sign-in" && (
                <Button onClick={startPopupLogin} variant="action">
                  <LogIn aria-hidden="true" /> Sign in to Mecatl
                </Button>
              )}
              {(unavailable || phase === "verification-unavailable") && (
                <Button onClick={retrySession} variant="outline">
                  <RefreshCw aria-hidden="true" /> Try again
                </Button>
              )}
              {state === "sign-in" && popupIssue && <PopupFallback />}
            </div>
          </section>
        </main>
      </div>
    </div>
  );
}
