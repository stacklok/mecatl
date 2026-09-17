import "server-only";

/**
 * Remote OIDC login for external mode (requirement H3, #847; the client-side
 * pattern is mecatui's ADR 0277): Authorization Code + PKCE runs ENTIRELY in
 * the Next server tier — this module owns the sign-in profile (env-configured
 * or RFC 9728-discovered), issuer discovery, the pending login attempts, the
 * token store, refresh, and revocation. Tokens never reach the browser
 * (rule 3). By default they live in process memory and a Next server restart
 * requires one fresh sign-in; `MECATL_OIDC_TOKEN_STORE=file` mirrors them
 * into the encrypted file store (`oidc/file-token-store.ts`, the
 * `--credential-store file` analogue).
 *
 * Env contract — the `mecatui login` / `connect` flags as deployment knobs.
 * Everything unset → the static MECATL_AUTH_TOKEN path, byte-identical to
 * pre-H3 behavior:
 *
 * - MECATL_OIDC_ISSUER     — HTTPS issuer URL (enables the explicit-values
 *                            shape together with the client id)
 * - MECATL_OIDC_CLIENT_ID  — public client id
 * - MECATL_OIDC_AUDIENCE   — optional; forwarded as the `audience`
 *                            authorization parameter for providers that mint
 *                            audience-bound access tokens
 * - MECATL_OIDC_SCOPE      — optional; default
 *                            "openid profile email offline_access"
 * - MECATL_OIDC_REDIRECT_URI — optional; default = the first
 *                            MECATL_STUDIO_PUBLIC_ORIGIN +
 *                            "/api/auth/oidc/callback" (must be registered
 *                            with the provider)
 * - MECATL_OIDC_DISCOVERY=1 — with NO issuer / client id set: discover the
 *                            profile from the deployment's own RFC 9728
 *                            protected-resource metadata (`mecatui login
 *                            ADDRESS`). The discovered profile is
 *                            DEFAULT-DENY: a human reviews issuer / client
 *                            id / audience / scopes in Settings and confirms
 *                            (the TUI's "Continue with browser login? [y/N]")
 *                            before any sign-in starts. The confirmation
 *                            lives with the sign-in it produced — process
 *                            memory, or the file store — so a re-confirm is
 *                            owed after a restart unless the credential
 *                            itself survived it.
 * - MECATL_OIDC_PRIVATE_ISSUER=1 — allow plain-HTTP loopback / RFC 1918
 *                            deployments and issuers (`--private-issuer`)
 * - MECATL_OIDC_CALLBACK_TIMEOUT — seconds a started sign-in stays valid
 *                            (default 600; `--callback-timeout`)
 * - MECATL_OIDC_TOKEN_STORE / _KEY / _PATH — `oidc/file-token-store.ts`
 * - MECATL_AUTH_PREFER_STATIC=1 / MECATL_AUTH_ANONYMOUS=1 —
 *                            `oidc/resolve-authorization.ts`
 * - MECATL_TLS_CA / MECATL_TLS_INSECURE — `server-tls.ts`
 */
import { resolveTokenStore } from "@/lib/oidc/file-token-store";
import {
  callbackTimeoutSeconds,
  LoginAttempts,
} from "@/lib/oidc/login-attempts";
import {
  boundProviderError,
  buildAuthorizationUrl,
  evaluateCallback,
  pkceChallengeS256,
  randomUrlSafe,
  readCallbackParams,
} from "@/lib/oidc/pkce";
import {
  type DiscoveredProfile,
  discoverProtectedResource,
  isPrivateHost,
  profileHash,
} from "@/lib/oidc/protected-resource";
import {
  type AuthMode,
  authMode,
  normalizeStaticToken,
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
import {
  allowPrivateIssuer,
  type TransportPolicy,
  upstreamFetchInit,
  upstreamTransport,
} from "@/lib/server-tls";

type OidcConfig = {
  /** Where the profile came from: the MECATL_OIDC_* env, or the
   * deployment's RFC 9728 metadata. */
  source: "env" | "discovery";
  issuer: string;
  clientId: string;
  audience: string;
  scope: string;
  redirectUri: string;
};

const flag = (value: string | undefined) =>
  ["1", "true", "yes"].includes((value ?? "").trim().toLowerCase());

function defaultRedirectUri(): string {
  const first = [...studioAllowedOrigins()][0] || "http://localhost:3000";
  return `${first}/api/auth/oidc/callback`;
}

function redirectUri(): string {
  return process.env.MECATL_OIDC_REDIRECT_URI?.trim() || defaultRedirectUri();
}

/** The explicit-values shape (MECATL_OIDC_ISSUER + MECATL_OIDC_CLIENT_ID). */
function envConfig(): OidcConfig | null {
  const issuer = process.env.MECATL_OIDC_ISSUER?.trim().replace(/\/+$/, "");
  const clientId = process.env.MECATL_OIDC_CLIENT_ID?.trim();
  if (!issuer || !clientId) return null;
  return {
    source: "env",
    issuer,
    clientId,
    audience: process.env.MECATL_OIDC_AUDIENCE?.trim() || "",
    scope:
      process.env.MECATL_OIDC_SCOPE?.trim() ||
      "openid profile email offline_access",
    redirectUri: redirectUri(),
  };
}

/** RFC 9728 discovery is the fallback shape ONLY: explicit env values win,
 * and there must be a deployment to discover against. */
function discoveryEnabled(): boolean {
  return (
    flag(process.env.MECATL_OIDC_DISCOVERY) &&
    !process.env.MECATL_OIDC_ISSUER?.trim() &&
    !process.env.MECATL_OIDC_CLIENT_ID?.trim() &&
    Boolean(process.env.MECATL_BASE_URL?.trim())
  );
}

function discoveredConfig(profile: DiscoveredProfile): OidcConfig {
  return {
    source: "discovery",
    issuer: profile.issuer.replace(/\/+$/, ""),
    clientId: profile.clientId,
    audience: profile.audience,
    scope: profile.scopes.join(" "),
    redirectUri: redirectUri(),
  };
}

/** The effective sign-in profile: env values, else the discovered one
 * (confirmed or not — `beginOidcLogin` gates on the confirmation). Reads the
 * cached discovery synchronously; callers `await ensureDiscovery()` first. */
function oidcConfig(): OidcConfig | null {
  const env = envConfig();
  if (env) return env;
  const state = runtime.resource;
  return state?.kind === "ok" ? state.config : null;
}

/** A half-set configuration (or a failed discovery) is surfaced, never
 * silently ignored — but the proxy stays on the static path so a typo cannot
 * brick every request. */
function configProblem(): string {
  const issuer = process.env.MECATL_OIDC_ISSUER?.trim();
  const clientId = process.env.MECATL_OIDC_CLIENT_ID?.trim();
  if (issuer && !clientId)
    return "MECATL_OIDC_ISSUER is set but MECATL_OIDC_CLIENT_ID is missing.";
  if (clientId && !issuer)
    return "MECATL_OIDC_CLIENT_ID is set but MECATL_OIDC_ISSUER is missing.";
  const state = runtime.resource;
  if (discoveryEnabled() && state?.kind === "failed")
    return `Sign-in profile discovery against the deployment failed: ${state.error}`;
  return "";
}

type Discovery = {
  authorizationEndpoint: string;
  tokenEndpoint: string;
  /** "" when the provider does not advertise RFC 7009 revocation. */
  revocationEndpoint: string;
  issParameterSupported: boolean;
};

/** The RFC 9728 discovery against the deployment (discovery mode only). */
type ResourceDiscovery =
  | { kind: "pending"; promise: Promise<void> }
  | {
      kind: "ok";
      profile: DiscoveredProfile;
      config: OidcConfig;
      /** The confirmation token (`profileHash`) the card POSTs back. */
      hash: string;
      /** DEFAULT-DENY: false until a human confirmed THIS hash (or a
       * credential bound to it survived a restart). */
      confirmed: boolean;
    }
  | { kind: "failed"; error: string; at: number };

/** Stashed on globalThis so Next dev's module reloads don't sign the
 * operator out mid-session. */
type OidcRuntime = {
  store: OidcTokenStore;
  storeKind: "memory" | "file";
  storeProblem: string;
  attempts: LoginAttempts;
  discovery: { issuer: string; doc: Discovery } | null;
  resource: ResourceDiscovery | null;
};

const globalStash = globalThis as typeof globalThis & {
  __mecatlOidcRuntime?: OidcRuntime;
};
if (!globalStash.__mecatlOidcRuntime) {
  const selection = resolveTokenStore(process.env, process.cwd());
  globalStash.__mecatlOidcRuntime = {
    store: new OidcTokenStore(Date.now, selection.persistence),
    storeKind: selection.kind,
    storeProblem: selection.problem ?? "",
    attempts: new LoginAttempts({
      // Read per attempt so MECATL_OIDC_CALLBACK_TIMEOUT is honoured live.
      ttlMs: () =>
        callbackTimeoutSeconds(process.env.MECATL_OIDC_CALLBACK_TIMEOUT) * 1000,
    }),
    discovery: null,
    resource: null,
  };
}
const runtime: OidcRuntime = globalStash.__mecatlOidcRuntime;

/** A failed discovery is retried after this long, so a deployment that
 * comes up after Studio is found without a restart. */
const DISCOVERY_RETRY_MS = 30_000;

/** The binding a credential carries and the confirmation token of a
 * discovered profile are the SAME digest (issuer / client id / audience /
 * scopes) — one identity, two uses. */
function configHash(cfg: OidcConfig): string {
  return profileHash(cfg);
}

/**
 * Run RFC 9728 discovery once (single-flight). No-op unless
 * MECATL_OIDC_DISCOVERY=1 stands in for the env values. A credential the
 * store already holds for EXACTLY this profile is a prior human
 * confirmation — the durable enrollment carries it across a restart.
 */
async function ensureDiscovery(): Promise<void> {
  if (!discoveryEnabled()) return;
  const state = runtime.resource;
  if (state?.kind === "ok") return;
  if (state?.kind === "pending") return state.promise;
  if (state?.kind === "failed" && state.at + DISCOVERY_RETRY_MS > Date.now())
    return;
  const promise = discoverProtectedResource(process.env.MECATL_BASE_URL ?? "", {
    allowPrivateHttp: allowPrivateIssuer(),
    ...upstreamFetchInit(),
  }).then(
    (profile) => {
      const config = discoveredConfig(profile);
      const hash = configHash(config);
      runtime.resource = {
        kind: "ok",
        profile,
        config,
        hash,
        confirmed: runtime.store.binding() === hash,
      };
    },
    (error: unknown) => {
      runtime.resource = {
        kind: "failed",
        error: error instanceof Error ? error.message : "unknown error",
        at: Date.now(),
      };
    },
  );
  runtime.resource = { kind: "pending", promise };
  return promise;
}

function discoveryConfirmed(): boolean {
  return runtime.resource?.kind === "ok" && runtime.resource.confirmed;
}

/** Drop a credential minted under a DIFFERENT profile than the current one
 * — its refresh token must never reach another issuer (token-store binding
 * contract). An unbound legacy credential is left alone. */
function reconcileBinding(cfg: OidcConfig): void {
  const bound = runtime.store.binding();
  if (bound && bound !== configHash(cfg)) runtime.store.clear("signed-out");
}

/** Confirm the discovered profile the settings card showed (the TUI's
 * "Continue with browser login? [y/N]"). The hash pins the REVIEWED values:
 * a profile that changed in between is refused and must be reviewed again. */
export async function confirmDiscoveredProfile(
  hash: unknown,
): Promise<{ ok: true } | { ok: false; error: string }> {
  await ensureDiscovery();
  if (envConfig())
    return {
      ok: false,
      error:
        "The sign-in profile is set by MECATL_OIDC_ISSUER and MECATL_OIDC_CLIENT_ID — there is nothing to confirm.",
    };
  const state = runtime.resource;
  if (state?.kind !== "ok")
    return {
      ok: false,
      error:
        configProblem() || "There is no discovered sign-in profile to confirm.",
    };
  if (typeof hash !== "string" || hash !== state.hash)
    return {
      ok: false,
      error:
        "The discovered sign-in profile changed since it was reviewed — review it again.",
    };
  runtime.resource = { ...state, confirmed: true };
  return { ok: true };
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
  // MECATL_OIDC_PRIVATE_ISSUER=1 (the `--private-issuer` analogue) admits a
  // plain-HTTP issuer ONLY on a loopback / RFC 1918 host.
  const privateHttp =
    endpoint.protocol === "http:" &&
    allowPrivateIssuer() &&
    isPrivateHost(endpoint.hostname);
  if (endpoint.protocol !== "https:" && !privateHttp)
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
    ...upstreamFetchInit(),
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
 * route the UI's Sign in / Copy sign-in link actions reach — no browser
 * window ever opens without that explicit user action (H3.3). A discovered
 * profile must have been confirmed first (default-deny). */
export async function beginOidcLogin(): Promise<{
  authorizationUrl: string;
  /** Epoch ms after which the callback is refused (the callback timeout). */
  expiresAt: number;
}> {
  await ensureDiscovery();
  const cfg = oidcConfig();
  if (!cfg)
    throw new Error(
      configProblem() ||
        "OIDC is not configured — set MECATL_OIDC_ISSUER and MECATL_OIDC_CLIENT_ID, or MECATL_OIDC_DISCOVERY=1.",
    );
  if (cfg.source === "discovery" && !discoveryConfirmed())
    throw new Error(
      "Review the discovered sign-in profile in Settings and choose Continue with browser login first.",
    );
  const doc = await fetchDiscovery(cfg.issuer);
  const state = randomUrlSafe(24);
  const verifier = randomUrlSafe(48);
  const attempt = runtime.attempts.begin(state, verifier);
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
    expiresAt: attempt.expires,
  };
}

export type CallbackOutcome = { ok: true } | { ok: false; error: string };

export async function completeOidcCallback(
  search: URLSearchParams,
): Promise<CallbackOutcome> {
  await ensureDiscovery();
  const cfg = oidcConfig();
  if (!cfg) return { ok: false, error: "OIDC is not configured." };
  const params = readCallbackParams(search);
  const attempt = params.state ? runtime.attempts.take(params.state) : null;
  if (!attempt) {
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
      ...upstreamFetchInit(),
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
  // Bound to the profile it was minted under (see reconcileBinding).
  runtime.store.setTokens(
    {
      accessToken,
      refreshToken:
        typeof result.refresh_token === "string" ? result.refresh_token : "",
      expiresAt: tokenExpiryMs(result.expires_in, now),
      claims:
        (typeof result.id_token === "string" &&
          claimsFromIdToken(result.id_token)) ||
        {},
    },
    configHash(cfg),
  );
  return { ok: true };
}

async function refreshExchange(
  cfg: OidcConfig,
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
    ...upstreamFetchInit(),
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

/** The two explicit credential-ordering knobs (resolve-authorization.ts). */
function credentialKnobs(): { preferStatic: boolean; anonymous: boolean } {
  return {
    preferStatic: flag(process.env.MECATL_AUTH_PREFER_STATIC),
    anonymous: flag(process.env.MECATL_AUTH_ANONYMOUS),
  };
}

/**
 * The proxy's external-mode Authorization decision (H3.2): the current OIDC
 * access token when OIDC is configured (refreshed on demand inside the
 * 30-second refresh-ahead window), the static MECATL_AUTH_TOKEN when it is
 * not, and an honest 401/502 decision the proxy relays otherwise — unless
 * MECATL_AUTH_ANONYMOUS / MECATL_AUTH_PREFER_STATIC reorder that.
 */
export async function resolveExternalAuthorization(): Promise<ProxyAuthDecision> {
  await ensureDiscovery();
  const cfg = oidcConfig();
  const staticToken = process.env.MECATL_AUTH_TOKEN;
  const knobs = credentialKnobs();
  if (!cfg) {
    return resolveProxyAuthorization({
      oidcConfigured: false,
      oidc: null,
      staticToken,
      ...knobs,
    });
  }
  reconcileBinding(cfg);
  // When the OIDC outcome cannot decide (anonymous, or a static token that
  // outranks it) skip the token demand — no refresh round-trip is owed.
  if (
    knobs.anonymous ||
    (knobs.preferStatic && normalizeStaticToken(staticToken))
  ) {
    return resolveProxyAuthorization({
      oidcConfigured: true,
      oidc: null,
      staticToken,
      ...knobs,
    });
  }
  const outcome = await runtime.store.bearer((prior) =>
    refreshExchange(cfg, prior),
  );
  return resolveProxyAuthorization({
    oidcConfigured: true,
    oidc: outcome,
    staticToken,
    ...knobs,
  });
}

export type OidcLoginStatus = {
  /** True once a sign-in can start: env values, or a CONFIRMED discovered
   * profile. A discovered-but-unreviewed profile is not configured yet. */
  configured: boolean;
  state:
    | "not-configured"
    | "discovered"
    | "signed-out"
    | "signed-in"
    | "expired";
  /** Set when the env configuration is half-complete or discovery failed. */
  problem?: string;
  source?: "env" | "discovery";
  issuer?: string;
  clientId?: string;
  audience?: string;
  scopes?: string[];
  /** The confirmation token for POST /api/auth/oidc/confirm-discovery
   * (`discovered` state only). */
  profileHash?: string;
  subject?: string;
  email?: string;
  /** ISO timestamp of the current access token's expiry (display only). */
  expiresAt?: string;
  /** Which credential the proxy injects (configuration, never a value). */
  authMode: AuthMode;
  store: { kind: "memory" | "file"; problem?: string };
  transport: TransportPolicy;
  callbackTimeoutSeconds: number;
};

/** Status for the settings card. Never includes token material, and never
 * the deployment's address (the browser sees no daemon URL — rule 3). */
export async function oidcLoginStatus(): Promise<OidcLoginStatus> {
  await ensureDiscovery();
  const cfg = oidcConfig();
  const knobs = credentialKnobs();
  const common = {
    authMode: authMode({
      oidcConfigured: Boolean(cfg),
      staticToken: process.env.MECATL_AUTH_TOKEN,
      ...knobs,
    }),
    store: {
      kind: runtime.storeKind,
      ...(runtime.storeProblem ? { problem: runtime.storeProblem } : {}),
    },
    transport: upstreamTransport().policy,
    callbackTimeoutSeconds: callbackTimeoutSeconds(
      process.env.MECATL_OIDC_CALLBACK_TIMEOUT,
    ),
  };
  if (!cfg) {
    const problem = configProblem();
    return {
      configured: false,
      state: "not-configured",
      ...(problem ? { problem } : {}),
      ...common,
    };
  }
  reconcileBinding(cfg);
  const identity = {
    source: cfg.source,
    issuer: cfg.issuer,
    clientId: cfg.clientId,
    ...(cfg.audience ? { audience: cfg.audience } : {}),
    scopes: cfg.scope.split(/\s+/).filter(Boolean),
  };
  if (cfg.source === "discovery" && !discoveryConfirmed()) {
    return {
      configured: false,
      state: "discovered",
      ...identity,
      profileHash: configHash(cfg),
      ...common,
    };
  }
  const snapshot = runtime.store.snapshot();
  if (snapshot.state !== "signed-in") {
    return { configured: true, state: snapshot.state, ...identity, ...common };
  }
  return {
    configured: true,
    state: "signed-in",
    ...identity,
    subject: snapshot.claims.preferredUsername || snapshot.claims.sub,
    email: snapshot.claims.email,
    expiresAt: new Date(snapshot.expiresAt).toISOString(),
    ...common,
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
  await ensureDiscovery();
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
          ...upstreamFetchInit(),
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
