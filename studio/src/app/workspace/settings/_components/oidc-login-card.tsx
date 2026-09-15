"use client";

/**
 * Remote sign-in card for OIDC-protected external deployments (requirement
 * H3). Self-contained and NOT yet mounted anywhere — the natural home is the
 * Settings runtime/connection section (beside the connection status), mounted
 * only when the deployment mode is external; the orchestrator wires it in
 * during integration.
 *
 * Talks only to the server-tier auth routes (`/api/auth/oidc/*`); no token
 * ever reaches this component (rule 3). Sign in opens the authorize redirect
 * in a popup — the explicit user action H3.3 requires — and the card refreshes
 * on the callback page's `mecatl-oidc` message and on window focus.
 */
import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Note, SettingsCard, SettingsRow } from "./settings-card";

type OidcStatus = {
  configured: boolean;
  state: "not-configured" | "signed-out" | "signed-in" | "expired";
  problem?: string;
  issuer?: string;
  subject?: string;
  email?: string;
  expiresAt?: string;
};

export function OidcLoginCard() {
  const [status, setStatus] = useState<OidcStatus | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const response = await fetch("/api/auth/oidc/status", {
        cache: "no-store",
      });
      if (!response.ok) throw new Error();
      setStatus((await response.json()) as OidcStatus);
      setFailed(false);
    } catch {
      setStatus(null);
      setFailed(true);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const onMessage = (event: MessageEvent) => {
      const data = event.data as { type?: unknown } | null;
      if (
        event.origin === window.location.origin &&
        data?.type === "mecatl-oidc"
      ) {
        void refresh();
      }
    };
    const onFocus = () => void refresh();
    window.addEventListener("message", onMessage);
    window.addEventListener("focus", onFocus);
    return () => {
      window.removeEventListener("message", onMessage);
      window.removeEventListener("focus", onFocus);
    };
  }, [refresh]);

  const signIn = () => {
    // The route 302s straight to the issuer's authorize URL; the callback
    // page notifies this card and closes itself.
    window.open(
      "/api/auth/oidc/start",
      "mecatl-oidc-login",
      "width=520,height=680",
    );
  };

  const signOut = async () => {
    setBusy(true);
    try {
      await fetch("/api/auth/oidc/logout", { method: "POST" });
    } catch {
      // Local sign-out state is authoritative server-side; refresh shows it.
    } finally {
      setBusy(false);
      void refresh();
    }
  };

  return (
    <SettingsCard
      title="Remote sign-in"
      description="OIDC login to the external mecated deployment. Tokens stay in the Studio server and never reach this browser."
    >
      {failed ? (
        <Note>The sign-in status could not be read right now.</Note>
      ) : !status ? (
        <Note>Checking sign-in status…</Note>
      ) : status.state === "not-configured" ? (
        <Note>
          {status.problem ||
            "Not configured. Set MECATL_OIDC_ISSUER and MECATL_OIDC_CLIENT_ID (and optionally MECATL_OIDC_AUDIENCE) in Studio's environment to sign in to an OIDC-protected deployment."}
        </Note>
      ) : (
        <div className="divide-y divide-border/60">
          {status.state === "signed-in" ? (
            <SettingsRow
              label="Signed in"
              description={
                status.email ||
                status.subject ||
                "Authenticated to the deployment."
              }
            >
              <Button
                type="button"
                variant="outline"
                className="rounded-full"
                disabled={busy}
                onClick={() => void signOut()}
              >
                {busy ? "Signing out…" : "Sign out"}
              </Button>
            </SettingsRow>
          ) : (
            <SettingsRow
              label={
                status.state === "expired" ? "Session expired" : "Not signed in"
              }
              description={
                status.state === "expired"
                  ? "The identity provider ended this session — sign in again."
                  : status.issuer
              }
            >
              <Button
                type="button"
                variant="action"
                className="rounded-full"
                onClick={signIn}
              >
                {status.state === "expired" ? "Sign in again" : "Sign in"}
              </Button>
            </SettingsRow>
          )}
        </div>
      )}
    </SettingsCard>
  );
}
