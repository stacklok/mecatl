"use client";

/**
 * The sign-in card for an external deployment behind an identity provider,
 * mounted by the provider settings page in external mode only. It is also
 * where the workspace's auth-recovery banner sends a signed-out or expired
 * session ("Open sign-in settings"). Talks only to the server-tier auth
 * routes (`/api/auth/oidc/*`); no token ever reaches this component (rule 3)
 * and no deployment address is rendered.
 *
 * A DISCOVERED profile stays default-deny until "Continue to sign in"
 * confirms it — nothing reaches the identity provider for an unconfirmed
 * profile. Sign in opens the authorize redirect in a popup (the explicit
 * user action H3.3 requires) and the card refreshes on the callback page's
 * `mecatl-oidc` message and on window focus.
 */
import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Note, SettingsCard, SettingsRow } from "./settings-card";

/** `/api/auth/oidc/status` — never token material, never an address. */
export type OidcStatus = {
  configured: boolean;
  state:
    | "not-configured"
    | "discovered"
    | "signed-out"
    | "signed-in"
    | "expired";
  problem?: string;
  source?: "env" | "discovery";
  issuer?: string;
  clientId?: string;
  audience?: string;
  scopes?: string[];
  profileHash?: string;
  subject?: string;
  email?: string;
  expiresAt?: string;
  authMode?: "oidc" | "static" | "anonymous";
  store?: { kind: "memory" | "file"; problem?: string };
  transport?: {
    tlsCa: boolean;
    insecure: boolean;
    privateIssuer: boolean;
    problem?: string;
  };
  callbackTimeoutSeconds?: number;
};

export const OIDC_START_URL = "/api/auth/oidc/start";
const POPUP_NAME = "mecatl-oidc-login";
const POPUP_FEATURES = "width=520,height=680";

type Notice = { tone: "info" | "error"; text: string };

export function OidcLoginCard() {
  const [status, setStatus] = useState<OidcStatus | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);

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
    window.open(OIDC_START_URL, POPUP_NAME, POPUP_FEATURES);
  };

  /** The review step's "yes": confirm the discovered profile the card shows,
   * then sign in. The popup opens SYNCHRONOUSLY on the click (popup-blocker
   * safe) but is pointed at the authorize redirect only once the server
   * accepted the confirmation — nothing reaches the issuer for an
   * unconfirmed profile. */
  const continueToSignIn = async () => {
    if (!status?.profileHash) return;
    const popup = window.open("about:blank", POPUP_NAME, POPUP_FEATURES);
    setBusy(true);
    setNotice(null);
    try {
      const response = await fetch("/api/auth/oidc/confirm-discovery", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ profileHash: status.profileHash }),
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as {
          error?: string;
        };
        popup?.close();
        setNotice({
          tone: "error",
          text: body.error || "The sign-in details could not be confirmed.",
        });
        return;
      }
      if (popup) popup.location.href = OIDC_START_URL;
      else window.open(OIDC_START_URL, POPUP_NAME, POPUP_FEATURES);
    } catch {
      popup?.close();
      setNotice({
        tone: "error",
        text: "The sign-in details could not be confirmed right now.",
      });
    } finally {
      setBusy(false);
      void refresh();
    }
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
      title="Sign in"
      description="Sign in with your organisation's account to use the agent."
    >
      {failed ? (
        <Note>The sign-in status could not be read right now.</Note>
      ) : !status ? (
        <Note>Checking sign-in status…</Note>
      ) : status.state === "not-configured" ? (
        <Note>Sign-in is not set up for this agent.</Note>
      ) : status.state === "discovered" ? (
        <div className="divide-y divide-border/60">
          <SettingsRow
            label="Review before signing in"
            description="The agent uses the identity provider below to sign you in. Nothing is sent until you continue."
          >
            <Button
              type="button"
              variant="action"
              className="rounded-full"
              disabled={busy}
              onClick={() => void continueToSignIn()}
            >
              {busy ? "Confirming…" : "Continue to sign in"}
            </Button>
          </SettingsRow>
          <IdentityProviderRow status={status} />
        </div>
      ) : status.state === "signed-in" ? (
        <SettingsRow
          label="Signed in"
          description={status.email || status.subject || "You are signed in."}
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
        <div className="divide-y divide-border/60">
          <SettingsRow
            label={
              status.state === "expired" ? "Sign-in expired" : "Not signed in"
            }
            description={
              status.state === "expired"
                ? "Sign in again to keep using the agent."
                : "Sign in opens a new window."
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
          <IdentityProviderRow status={status} />
        </div>
      )}
      {notice ? (
        <p
          role="status"
          className={
            notice.tone === "error"
              ? "mt-3 text-xs text-destructive"
              : "mt-3 text-xs text-muted-foreground"
          }
        >
          {notice.text}
        </p>
      ) : null}
    </SettingsCard>
  );
}

/** Where a sign-in goes — the one detail worth reviewing before it opens. */
function IdentityProviderRow({ status }: { status: OidcStatus }) {
  if (!status.issuer) return null;
  return (
    <SettingsRow label="Identity provider">
      <span
        data-testid="oidc-issuer"
        className="max-w-[28rem] break-all text-right font-mono text-xs"
      >
        {status.issuer}
      </span>
    </SettingsRow>
  );
}
