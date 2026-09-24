// SPDX-License-Identifier: Apache-2.0

import { getAuthSessionOptions, getPublicStatusOptions } from "@mecatl-studio/contracts/query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogIn, RefreshCw } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { ConnectionStatusBanner } from "../../components/shell/connection-status-banner";
import { Button } from "../../components/ui/button";
import { onAuthenticationRequired, setRequestRecoveryState } from "../../lib/api-client";
import { AuthRecoveryContext, useAuthRecovery } from "./auth-recovery-context";
import {
  acceptsPopupResult,
  commitRecoveryCheck,
  isPublicQuery,
  type RecoveryState,
  type SessionCheck,
} from "./auth-recovery-state";
import { PopupFallback } from "./popup-fallback";

const initialRecovery: RecoveryState = {
  identityEpoch: 0,
  phase: "checking",
  workspaceMounted: false,
};

function currentLoginUrl(): string {
  const returnTo = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  return `/api/v1/auth/login?${new URLSearchParams({ flow: "popup", return_to: returnTo })}`;
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
    const opened = window.open(currentLoginUrl(), "_blank", "popup,width=520,height=720");
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
      publicStatus: bannerStatus,
      publicStatusFailed: status.isError,
      sessionCheckFailed: recovery.phase === "verification-unavailable",
    },
    loginUrl: currentLoginUrl(),
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

/** Keep the full requested browser URL through the interactive sign-in round trip. */
export function authLoginUrl(returnTo: string) {
  return `/api/v1/auth/login?${new URLSearchParams({ return_to: returnTo }).toString()}`;
}

function PublicShell() {
  const { phase, popupIssue, retrySession, startPopupLogin } = useAuthRecovery();
  return (
    <div className="flex min-h-dvh flex-col bg-[radial-gradient(120%_140%_at_20%_30%,var(--shell-gradient-start)_0%,var(--shell-gradient-mid)_50%,var(--shell-gradient-end)_100%)]">
      <ConnectionStatusBanner />
      <header className="px-6 py-5 text-sm font-semibold text-white">Mecatl Studio</header>
      <main className="flex flex-1 items-center justify-center p-6">
        <section className="w-full max-w-md rounded-2xl border bg-card p-6 text-card-foreground shadow-xl">
          <h1 className="text-xl font-semibold">Mecatl Studio</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {phase === "checking"
              ? "Checking your sign-in and the Mecatl connection…"
              : phase === "verification-unavailable"
                ? "We couldn't verify your sign-in. Your workspace will appear after a successful check."
                : "Sign in to open this workspace."}
          </p>
          <div className="mt-5 flex flex-wrap gap-2">
            {phase === "sign-in" && (
              <Button onClick={startPopupLogin} variant="action">
                <LogIn aria-hidden="true" /> Sign in to Mecatl
              </Button>
            )}
            {phase === "verification-unavailable" && (
              <Button onClick={retrySession} variant="outline">
                <RefreshCw aria-hidden="true" /> Try again
              </Button>
            )}
            {popupIssue && <PopupFallback />}
          </div>
        </section>
      </main>
    </div>
  );
}
