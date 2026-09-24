// SPDX-License-Identifier: Apache-2.0

import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { LogIn, RefreshCw } from "lucide-react";
import { type ReactNode, useEffect, useRef } from "react";
import { GlobalStatusSlot } from "../../components/shell/global-status-slot";
import { Button } from "../../components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "../../components/ui/card";
import { reconcileAccount } from "../../lib/account-storage";
import { authGateState } from "./auth-gate-state";

export function AuthGate({ children }: { children: ReactNode }) {
  const session = useQuery({
    ...getAuthSessionOptions(),
    refetchInterval: 60_000,
    retry: false,
    staleTime: 30_000,
  });

  // Tracks whether this tab has ever seen an authenticated session, so a
  // credential that expires mid-use reads as "sign in again" rather than
  // the same generic first-visit copy — the session silently dropping
  // should never look identical to never having signed in.
  const wasAuthenticated = useRef(false);
  useEffect(() => {
    if (session.data?.status === "authenticated") wasAuthenticated.current = true;
  }, [session.data?.status]);

  const state = authGateState(session);
  // Before any child reads browser storage: drop what a previous account left
  // behind on this origin. Idempotent, so running it on every render is safe.
  if (state === "ready") reconcileAccount(session.data?.account);

  if (state === "checking") {
    return <GateFrame message="Checking the Mecatl connection…" />;
  }

  if (state === "error") {
    return (
      <GateFrame message="We couldn't check your sign-in status. Try again.">
        <Button onClick={() => session.refetch()} variant="outline">
          <RefreshCw aria-hidden="true" />
          Try again
        </Button>
      </GateFrame>
    );
  }

  if (state === "sign-in") {
    const returnTo = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    const loginUrl = authLoginUrl(returnTo);
    const message = wasAuthenticated.current
      ? "Sign in again to keep using the agent."
      : "Sign in with the identity provider configured by this Mecatl deployment.";
    return (
      <GateFrame message={message}>
        <Button asChild className="w-full" variant="action">
          <a href={loginUrl}>
            <LogIn aria-hidden="true" />
            {wasAuthenticated.current ? "Sign in again" : "Sign in to Mecatl"}
          </a>
        </Button>
      </GateFrame>
    );
  }

  return children;
}

/** Keep the full requested browser URL through the interactive sign-in round trip. */
export function authLoginUrl(returnTo: string) {
  return `/api/v1/auth/login?${new URLSearchParams({ return_to: returnTo }).toString()}`;
}

function GateFrame({ children, message }: { children?: ReactNode; message: string }) {
  return (
    <div className="flex min-h-dvh min-w-0 flex-col bg-[var(--shell-gradient-mid)] pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)]">
      <GlobalStatusSlot />
      <div
        className="flex min-h-0 min-w-0 flex-1 flex-col bg-[radial-gradient(120%_140%_at_20%_30%,var(--shell-gradient-start)_0%,var(--shell-gradient-mid)_50%,var(--shell-gradient-end)_100%)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)]"
        data-shell-gradient=""
      >
        <main className="flex min-h-0 min-w-0 flex-1 items-center justify-center p-5">
          <Card className="w-full max-w-sm border-white/10 bg-background/95 shadow-2xl backdrop-blur">
            <CardHeader className="text-center">
              <span
                aria-hidden="true"
                className="mx-auto mb-2 block size-10 bg-brand [mask-image:url(/stacklok-logo-mark.svg)] [mask-position:center] [mask-repeat:no-repeat] [mask-size:contain]"
              />
              <CardTitle>Mecatl</CardTitle>
              <CardDescription>{message}</CardDescription>
            </CardHeader>
            {children === undefined ? null : (
              <CardContent>
                <CardFooter className="p-0">{children}</CardFooter>
              </CardContent>
            )}
          </Card>
        </main>
      </div>
    </div>
  );
}
