import "server-only";

/**
 * Remote OIDC login for external mode (requirement H3, #847; the client-side
 * pattern is mecatui's ADR 0277): Authorization Code + PKCE runs ENTIRELY in
 * the Next server tier — this module owns issuer discovery, the pending login
 * attempts, the in-memory token store, refresh, and revocation. Tokens never
 * reach the browser (rule 3) and are never written to disk: a Next server
 * restart requires one fresh sign-in (v1 deliberately defers durable storage;
 * ADR 0277's keyring-wrapped store is the eventual model).
 *
 * Env contract (all three unset → the static MECATL_AUTH_TOKEN path,
 * byte-identical to pre-H3 behavior):
 *
 * - MECATL_OIDC_ISSUER     — HTTPS issuer URL (required to enable)
 * - MECATL_OIDC_CLIENT_ID  — public client id (required to enable)
 * - MECATL_OIDC_AUDIENCE   — optional; forwarded as the `audience`
 *                            authorization parameter for providers that mint
 *                            audience-bound access tokens
 * - MECATL_OIDC_SCOPE      — optional; default
 *                            "openid profile email offline_access"
 * - MECATL_OIDC_REDIRECT_URI — optional; default = the first
 *                            MECATL_STUDIO_PUBLIC_ORIGIN +
 *                            "/api/auth/oidc/callback" (must be registered
 *                            with the provider)
 */
import {
  boundProviderError,
  buildAuthorizationUrl,
  evaluateCallback,
  pkceChallengeS256,
  randomUrlSafe,
  readCallbackParams,
} from "@/lib/oidc/pkce";
import {
  type ProxyAuthDecision,
  resolveProxyAuthorization,
} from "@/lib/oidc/resolve-authorization";
import {
  claimsFromIdToken,
  isInvalidGrantResponse,
  OidcTokenStore,
  type RefreshResult,
  type StoredTokens,
  tokenExpiryMs,
  tokensFromRefreshResponse,
} from "@/lib/oidc/token-store";
import { studioAllowedOrigins } from "@/lib/request-trust";

type OidcEnvConfig = {
  issuer: string;
  clientId: string;
  audience: string;
  scope: string;
  redirectUri: string;
};

function defaultRedirectUri(): string {
  const first = [...studioAllowedOrigins()][0] || "http://localhost:3000";
  return `${first}/api/auth/oidc/callback`;
}

function oidcConfig(): OidcEnvConfig | null {
  const issuer = process.env.MECATL_OIDC_ISSUER?.trim().replace(/\/+$/, "");
  const clientId = process.env.MECATL_OIDC_CLIENT_ID?.trim();
  if (!issuer || !clientId) return null;
  return {
    issuer,
    clientId,
    audience: process.env.MECATL_OIDC_AUDIENCE?.trim() || "",
    scope:
      process.env.MECATL_OIDC_SCOPE?.trim() ||
      "openid profile email offline_access",
    redirectUri:
      process.env.MECATL_OIDC_REDIRECT_URI?.trim() || defaultRedirectUri(),
  };
}

/** A half-set configuration is surfaced (never silently ignored) — but the
 * proxy stays on the static path so a typo cannot brick every request. */
function configProblem(): string {
  const issuer = process.env.MECATL_OIDC_ISSUER?.trim();
  const clientId = process.env.MECATL_OIDC_CLIENT_ID?.trim();
  if (issuer && !clientId)
    return "MECATL_OIDC_ISSUER is set but MECATL_OIDC_CLIENT_ID is missing.";
  if (clientId && !issuer)
    return "MECATL_OIDC_CLIENT_ID is set but MECATL_OIDC_ISSUER is missing.";
  return "";
}

type Discovery = {
  authorizationEndpoint: string;
  tokenEndpoint: string;
  /** "" when the provider does not advertise RFC 7009 revocation. */
  revocationEndpoint: string;
  issParameterSupported: boolean;
};

type LoginAttempt = { verifier: string; expires: number };

/** Stashed on globalThis so Next dev's module reloads don't sign the
 * operator out mid-session. Process memory only — see the module comment. */
type OidcRuntime = {
  store: OidcTokenStore;
  attempts: Map<string, LoginAttempt>;
  discovery: { issuer: string; doc: Discovery } | null;
};

const globalStash = globalThis as typeof globalThis & {
  __mecatlOidcRuntime?: OidcRuntime;
};
if (!globalStash.__mecatlOidcRuntime) {
  globalStash.__mecatlOidcRuntime = {
    store: new OidcTokenStore(),
    attempts: new Map(),
    discovery: null,
  };
}
const runtime: OidcRuntime = globalStash.__mecatlOidcRuntime;

const ATTEMPT_TTL_MS = 10 * 60_000;
const MAX_PENDING_ATTEMPTS = 32;

function pruneAttempts(now = Date.now()): void {
  for (const [state, attempt] of runtime.attempts) {
    if (attempt.expires < now) runtime.attempts.delete(state);
  }
  // Map iteration is insertion-ordered, so overflow drops the oldest.
  while (runtime.attempts.size >= MAX_PENDING_ATTEMPTS) {
    const oldest = runtime.attempts.keys().next().value;
    if (oldest === undefined) break;
    runtime.attempts.delete(oldest);
  }
}

function requireHttpsEndpoint(value: unknown, label: string): string {
  if (typeof value !== "string" || !value)
    throw new Error(`OIDC discovery does not include ${label}`);
  let endpoint: URL;
  try {
    endpoint = new URL(value);
  } catch {
    // Harness-authored message only — never echo the provider's value.
    throw new Error(`OIDC ${label} is not a valid URL`);
  }
  if (endpoint.protocol !== "https:")
    throw new Error(`OIDC ${label} must use HTTPS`);
  return endpoint.toString();
}

async function fetchDiscovery(
  issuer: string,
  signal?: AbortSignal,
): Promise<Discovery> {
  if (runtime.discovery?.issuer === issuer) return runtime.discovery.doc;
  const wellKnown = requireHttpsEndpoint(
    `${issuer}/.well-known/openid-configuration`,
    "issuer",
  );
  const response = await fetch(wellKnown, {
    headers: { accept: "application/json" },
    cache: "no-store",
    redirect: "manual",
    signal: signal ?? AbortSignal.timeout(8_000),
  });
  if (!response.ok)
    throw new Error(`OIDC discovery failed (HTTP ${response.status})`);
  const body = (await response.json().catch(() => null)) as Record<
    string,
    unknown
  > | null;
  if (!body) throw new Error("OIDC discovery returned malformed JSON");
  const documentIssuer =
    typeof body.issuer === "string" ? body.issuer.replace(/\/+$/, "") : "";
  if (documentIssuer !== issuer)
    throw new Error("OIDC discovery issuer does not match MECATL_OIDC_ISSUER");
  const doc: Discovery = {
    authorizationEndpoint: requireHttpsEndpoint(
      body.authorization_endpoint,
      "authorization endpoint",
    ),
    tokenEndpoint: requireHttpsEndpoint(body.token_endpoint, "token endpoint"),
    revocationEndpoint:
      typeof body.revocation_endpoint === "string" && body.revocation_endpoint
        ? requireHttpsEndpoint(body.revocation_endpoint, "revocation endpoint")
        : "",
    issParameterSupported:
      body.authorization_response_iss_parameter_supported === true,
  };
  runtime.discovery = { issuer, doc };
  return doc;
}

/** Begin one Authorization Code + PKCE attempt. Called ONLY from the login
 * route the UI's Sign in button opens — no browser window ever opens without
 * that explicit user action (H3.3). */
export async function beginOidcLogin(): Promise<{ authorizationUrl: string }> {
  const cfg = oidcConfig();
  if (!cfg)
    throw new Error(
      configProblem() ||
        "OIDC is not configured — set MECATL_OIDC_ISSUER and MECATL_OIDC_CLIENT_ID.",
    );
  const doc = await fetchDiscovery(cfg.issuer);
  const state = randomUrlSafe(24);
  const verifier = randomUrlSafe(48);
  pruneAttempts();
  runtime.attempts.set(state, {
    verifier,
    expires: Date.now() + ATTEMPT_TTL_MS,
  });
  return {
    authorizationUrl: buildAuthorizationUrl({
      authorizationEndpoint: doc.authorizationEndpoint,
      clientId: cfg.clientId,
      redirectUri: cfg.redirectUri,
      scope: cfg.scope,
      state,
      codeChallenge: pkceChallengeS256(verifier),
      audience: cfg.audience || undefined,
    }),
  };
}

export type CallbackOutcome = { ok: true } | { ok: false; error: string };

export async function completeOidcCallback(
  search: URLSearchParams,
): Promise<CallbackOutcome> {
  const cfg = oidcConfig();
  if (!cfg) return { ok: false, error: "OIDC is not configured." };
  const params = readCallbackParams(search);
  const attempt = params.state ? runtime.attempts.get(params.state) : undefined;
  if (params.state) runtime.attempts.delete(params.state);
  if (!attempt || attempt.expires < Date.now()) {
    return {
      ok: false,
      error:
        "This sign-in attempt is unknown or has expired. Start again from Settings.",
    };
  }
  let doc: Discovery;
  try {
    doc = await fetchDiscovery(cfg.issuer);
  } catch (error) {
    return { ok: false, error: safeMessage(error) };
  }
  const verdict = evaluateCallback(params, {
    expectedIssuer: cfg.issuer,
    issRequired: doc.issParameterSupported,
  });
  if (!verdict.ok) return { ok: false, error: verdict.reason };

  const body = new URLSearchParams({
    grant_type: "authorization_code",
    code: verdict.code,
    client_id: cfg.clientId,
    redirect_uri: cfg.redirectUri,
    code_verifier: attempt.verifier,
  });
  let response: Response;
  try {
    response = await fetch(doc.tokenEndpoint, {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body,
      cache: "no-store",
      signal: AbortSignal.timeout(15_000),
    });
  } catch {
    return {
      ok: false,
      error: "Could not reach the identity provider's token endpoint.",
    };
  }
  const result = (await response.json().catch(() => ({}))) as Record<
    string,
    unknown
  >;
  const accessToken =
    typeof result.access_token === "string" ? result.access_token : "";
  if (!response.ok || !accessToken) {
    // Never echo a token-endpoint body — the bounded machine `error` code is
    // the only fragment allowed out (ADR 0277's no-provider-bodies rule).
    const code =
      typeof result.error === "string" ? boundProviderError(result.error) : "";
    return {
      ok: false,
      error: code
        ? `The token exchange failed (${code}).`
        : "The token exchange failed.",
    };
  }
  const now = Date.now();
  runtime.store.setTokens({
    accessToken,
    refreshToken:
      typeof result.refresh_token === "string" ? result.refresh_token : "",
    expiresAt: tokenExpiryMs(result.expires_in, now),
    claims:
      (typeof result.id_token === "string" &&
        claimsFromIdToken(result.id_token)) ||
      {},
  });
  return { ok: true };
}

async function refreshExchange(
  cfg: OidcEnvConfig,
  prior: StoredTokens,
): Promise<RefreshResult> {
  const doc = await fetchDiscovery(cfg.issuer); // a throw is caught as transient
  // No `scope` on refresh: RFC 6749 section 6 makes it optional (the server
  // reuses the granted scopes) and some providers reject it as malformed —
  // the bug that silently killed the MCP gateway's auto-refresh once already
  // (see scripts/local-controller.mjs refreshGatewayAccessToken).
  const body = new URLSearchParams({
    grant_type: "refresh_token",
    refresh_token: prior.refreshToken,
    client_id: cfg.clientId,
  });
  const response = await fetch(doc.tokenEndpoint, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body,
    cache: "no-store",
    signal: AbortSignal.timeout(10_000),
  });
  const result = (await response.json().catch(() => ({}))) as Record<
    string,
    unknown
  >;
  if (!response.ok) {
    return isInvalidGrantResponse(response.status, result)
      ? { kind: "invalid-grant" }
      : { kind: "transient" };
  }
  const tokens = tokensFromRefreshResponse(result, prior, Date.now());
  return tokens ? { kind: "refreshed", tokens } : { kind: "transient" };
}

/**
 * The proxy's external-mode Authorization decision (H3.2): the current OIDC
 * access token when OIDC is configured (refreshed on demand inside the
 * 30-second refresh-ahead window), the static MECATL_AUTH_TOKEN when it is
 * not, and an honest 401/502 decision the proxy relays otherwise.
 */
export async function resolveExternalAuthorization(): Promise<ProxyAuthDecision> {
  const cfg = oidcConfig();
  const staticToken = process.env.MECATL_AUTH_TOKEN;
  if (!cfg) {
    return resolveProxyAuthorization({
      oidcConfigured: false,
      oidc: null,
      staticToken,
    });
  }
  const outcome = await runtime.store.bearer((prior) =>
    refreshExchange(cfg, prior),
  );
  return resolveProxyAuthorization({
    oidcConfigured: true,
    oidc: outcome,
    staticToken,
  });
}

export type OidcLoginStatus = {
  configured: boolean;
  state: "not-configured" | "signed-out" | "signed-in" | "expired";
  /** Set when the env configuration is half-complete. */
  problem?: string;
  issuer?: string;
  subject?: string;
  email?: string;
  /** ISO timestamp of the current access token's expiry (display only). */
  expiresAt?: string;
};

/** Status for the settings card. Never includes token material. */
export function oidcLoginStatus(): OidcLoginStatus {
  const cfg = oidcConfig();
  if (!cfg) {
    const problem = configProblem();
    return {
      configured: false,
      state: "not-configured",
      ...(problem ? { problem } : {}),
    };
  }
  const snapshot = runtime.store.snapshot();
  if (snapshot.state !== "signed-in") {
    return { configured: true, state: snapshot.state, issuer: cfg.issuer };
  }
  return {
    configured: true,
    state: "signed-in",
    issuer: cfg.issuer,
    subject: snapshot.claims.preferredUsername || snapshot.claims.sub,
    email: snapshot.claims.email,
    expiresAt: new Date(snapshot.expiresAt).toISOString(),
  };
}

const REVOCATION_BUDGET_MS = 5_000;

/**
 * Sign out: local credential removal FIRST, then bounded best-effort RFC 7009
 * revocation under one operation-wide budget (ADR 0277's logout shape; the
 * 5-second budget mirrors ADR 0274). Provider failure never restores local
 * state, and repeating logout while signed out succeeds.
 */
export async function oidcSignOut(): Promise<{ ok: true; revoked: boolean }> {
  const cfg = oidcConfig();
  const tokens = runtime.store.current();
  runtime.store.clear("signed-out");
  if (!cfg || !tokens) return { ok: true, revoked: false };
  const budget = AbortSignal.timeout(REVOCATION_BUDGET_MS);
  let revoked = false;
  try {
    const doc = await fetchDiscovery(cfg.issuer, budget);
    if (doc.revocationEndpoint) {
      const revoke = async (token: string, hint: string) => {
        if (!token) return true;
        const response = await fetch(doc.revocationEndpoint, {
          method: "POST",
          headers: { "content-type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({
            token,
            token_type_hint: hint,
            client_id: cfg.clientId,
          }),
          cache: "no-store",
          signal: budget,
        });
        return response.ok;
      };
      const refreshRevoked = await revoke(tokens.refreshToken, "refresh_token");
      const accessRevoked = await revoke(tokens.accessToken, "access_token");
      revoked = refreshRevoked && accessRevoked;
    }
  } catch {
    // Best-effort only — local sign-out already happened.
  }
  return { ok: true, revoked };
}

function safeMessage(error: unknown): string {
  return error instanceof Error && error.message
    ? error.message
    : "OIDC sign-in failed.";
}
