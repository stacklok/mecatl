/**
 * Remote OIDC login routes (requirement H3) — the server-tier half of
 * Authorization Code + PKCE against an OIDC-protected external mecated.
 * Tokens live in `src/lib/oidc-session.ts` (process memory) and never reach
 * the browser (rule 3). Same origin-trust table as the proxy routes (rule 4's
 * browser-facing half).
 *
 *   GET  /api/auth/oidc/status   → sign-in state for the settings card
 *   GET  /api/auth/oidc/start    → 302 to the issuer's authorize URL
 *                                  (opened by the card's Sign in button —
 *                                  never without that user action, H3.3)
 *   GET  /api/auth/oidc/callback → the registered redirect URI; verifies
 *                                  state + PKCE server-side, exchanges the
 *                                  code, renders a close-this-tab page
 *   POST /api/auth/oidc/logout   → local sign-out + best-effort revocation
 */
import {
  beginOidcLogin,
  type CallbackOutcome,
  completeOidcCallback,
  oidcLoginStatus,
  oidcSignOut,
} from "@/lib/oidc-session";
import { requestIsTrusted } from "@/lib/request-trust";

type Context = { params: Promise<{ action: string }> };

const noStore = { "cache-control": "no-store" };

const forbidden = () =>
  Response.json({ error: "request origin is not allowed" }, { status: 403 });

const unknownAction = () =>
  Response.json({ error: "unknown auth action" }, { status: 404 });

export async function GET(request: Request, context: Context) {
  if (!requestIsTrusted(request)) return forbidden();
  const { action } = await context.params;
  if (action === "status") {
    return Response.json(oidcLoginStatus(), { headers: noStore });
  }
  if (action === "start") {
    try {
      const { authorizationUrl } = await beginOidcLogin();
      return new Response(null, {
        status: 302,
        headers: { ...noStore, location: authorizationUrl },
      });
    } catch (error) {
      const message =
        error instanceof Error && error.message
          ? error.message
          : "Could not start OIDC sign-in.";
      return Response.json(
        { error: message },
        { status: 400, headers: noStore },
      );
    }
  }
  if (action === "callback") {
    const outcome = await completeOidcCallback(
      new URL(request.url).searchParams,
    );
    return callbackPage(outcome);
  }
  return unknownAction();
}

export async function POST(request: Request, context: Context) {
  if (!requestIsTrusted(request)) return forbidden();
  const { action } = await context.params;
  if (action === "logout") {
    return Response.json(await oidcSignOut(), { headers: noStore });
  }
  return unknownAction();
}

/** Mirrors the controller's gateway-OAuth callback page: a tiny same-origin
 * HTML page that notifies the opener (the settings card listens for the
 * `mecatl-oidc` message) and asks to be closed. The failure message is
 * harness-authored or provider-bounded upstream, and escaped here anyway. */
function callbackPage(outcome: CallbackOutcome): Response {
  const headers = {
    "content-type": "text/html; charset=utf-8",
    // The authorization code rides this page's URL; keep it out of referrers.
    "referrer-policy": "no-referrer",
    ...noStore,
  };
  const notify = (ok: boolean) =>
    `<script>window.opener?.postMessage({type:"mecatl-oidc",ok:${ok ? "true" : "false"}},window.location.origin);setTimeout(()=>window.close(),700)</script>`;
  if (outcome.ok) {
    return new Response(
      `<!doctype html><title>Signed in to Mecatl</title><style>body{font:16px system-ui;padding:40px;color:#25231f}</style><h1>Signed in</h1><p>You can close this tab and return to Mecatl Studio.</p>${notify(true)}`,
      { headers },
    );
  }
  const message = outcome.error.replace(/[<>&"']/g, "");
  return new Response(
    `<!doctype html><title>Mecatl sign-in failed</title><style>body{font:16px system-ui;padding:40px;color:#25231f}</style><h1>Could not sign in</h1><p>${message}</p>${notify(false)}`,
    { status: 400, headers },
  );
}
