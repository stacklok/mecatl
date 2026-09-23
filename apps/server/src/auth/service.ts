// SPDX-License-Identifier: Apache-2.0

import type { Context } from "hono";
import { deleteCookie, getCookie, setCookie } from "hono/cookie";
import {
  authorizationCodeGrant,
  buildAuthorizationUrl,
  type Configuration,
  calculatePKCECodeChallenge,
  customFetch,
  discovery,
  None,
  randomPKCECodeVerifier,
  randomState,
  refreshTokenGrant,
  tokenRevocation,
} from "openid-client";
import type { AppEnv } from "../http/env.js";
import { csrfCookie } from "../http/security.js";
import { type Logger, silentLogger } from "../log.js";
import type { RuntimeConfig } from "../mecatl/runtime.js";

export const accessCookie = "studio_access";
export const refreshCookie = "studio_refresh";
/** How long a completed refresh keeps answering requests that still carry the old token. */
export const rotationGraceMilliseconds = 60_000;

export const transactionCookie = "studio_login";

const cookieLifetimeSeconds = 30 * 24 * 60 * 60;
const transactionLifetimeSeconds = 10 * 60;
const refreshAheadMilliseconds = 30_000;
const maximumMetadataBytes = 1 << 20;
export const maximumCookieValueBytes = 3_800;
const defaultScopes = ["openid", "profile", "offline_access"];
/** mecatui's loopback callback; shared so one public-client registration serves both clients. */
export const mecatuiCallbackUrl = "http://127.0.0.1:18473/oauth/callback";
export const hostedCallbackPath = "/api/v1/auth/callback";
export const defaultReturnTo = "/workspace";

interface AccessCredential {
  readonly accessToken: string;
  readonly email?: string;
  readonly expiresAt: number;
  readonly subject: string;
  readonly tokenType: "Bearer";
}

interface LoginTransaction {
  readonly codeVerifier: string;
  /** Epoch ms after which this transaction is refused, independent of cookie Max-Age. */
  readonly expiresAt: number;
  readonly redirectUri: string;
  readonly returnTo: string;
  readonly state: string;
  readonly flow?: "popup";
}

interface RefreshCredential {
  readonly refreshToken: string;
}

export interface AuthenticationCredential extends AccessCredential {
  readonly refreshToken?: string;
}

export interface AuthenticationResult {
  readonly credential: AuthenticationCredential;
  readonly returnTo: string;
  readonly flow?: "popup";
}

export type CredentialResolution =
  | { readonly status: "anonymous" }
  | { readonly status: "expired" }
  | { readonly credential: AuthenticationCredential; readonly status: "authenticated" };

export interface MecatlAuthenticationProfile {
  readonly audience: string;
  readonly clientId: string;
  readonly issuer: string;
  readonly resource: string;
  readonly scopes: readonly string[];
}

export interface AuthenticationService {
  clear(context: Context<AppEnv>): void;
  /** Records a completed login; call only after the credential is verified and stored. */
  noteLoginComplete(context: Context<AppEnv>): void;
  /** Records a login that failed after the exchange, such as a rejected credential. */
  noteLoginFailure(context: Context<AppEnv>, reason: string): void;
  completeLogin(context: Context<AppEnv>): Promise<AuthenticationResult>;
  credential(context: Context<AppEnv>): Promise<CredentialResolution>;
  logout(context: Context<AppEnv>): Promise<void>;
  save(context: Context<AppEnv>, credential: AuthenticationCredential): Promise<void>;
  signInRequired(context: Context<AppEnv>): Promise<boolean>;
  startLogin(
    context: Context<AppEnv>,
    returnTo: string | undefined,
    flow?: "popup",
  ): Promise<string>;
}

export class AuthenticationError extends Error {
  readonly code: string;
  readonly popup: boolean;

  constructor(code: string, message: string, popup = false) {
    super(message);
    this.name = "AuthenticationError";
    this.code = code;
    this.popup = popup;
  }
}

export interface AuthenticationOptions {
  readonly fetch?: typeof fetch;
  readonly logger?: Logger;
  /** Browser-facing origin; the callback URL and cookie `Secure` flag derive from it. */
  readonly publicUrl?: URL;
  /** Raw secret bytes (>= 32) sealing the session cookies. */
  readonly sessionSecret?: string;
  readonly verifyCredential?: (accessToken: string) => Promise<void>;
}

/**
 * Discovers the deployment's RFC 9728 protected-resource document and, when it
 * is published, returns the interactive login service. A `404` means the
 * runtime advertises no interactive authentication and yields `undefined`;
 * every other discovery outcome throws so the process fails closed at start.
 */
export async function createAuthenticationService(
  runtimeConfig: RuntimeConfig,
  options: AuthenticationOptions = {},
): Promise<AuthenticationService | undefined> {
  if (runtimeConfig.source !== "external" || runtimeConfig.authToken !== undefined) {
    return undefined;
  }

  const fetcher = options.fetch ?? globalThis.fetch;
  const profile = await discoverMecatlAuthentication(runtimeConfig.resourceUrl, fetcher);
  if (profile === undefined) return undefined;

  const secret = options.sessionSecret;
  if (secret === undefined || new TextEncoder().encode(secret).byteLength < 32) {
    throw new AuthenticationError(
      "session_secret_missing",
      "STUDIO_SESSION_SECRET (at least 32 bytes) is required when interactive login is active",
    );
  }

  return new OidcAuthenticationService(
    profile,
    new SealedCookieCodec(secret),
    fetcher,
    options.publicUrl,
    options.logger ?? silentLogger,
    options.verifyCredential,
  );
}

export async function discoverMecatlAuthentication(
  resourceUrl: string,
  fetcher: typeof fetch = globalThis.fetch,
): Promise<MecatlAuthenticationProfile | undefined> {
  const resource = canonicalResource(resourceUrl);
  const metadataUrl = protectedResourceMetadataUrl(resource);
  let response: Response;
  try {
    response = await fetcher(metadataUrl, {
      headers: { Accept: "application/json" },
      redirect: "manual",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    throw new AuthenticationError(
      "auth_discovery_failed",
      `The Mecatl authentication profile could not be fetched from ${metadataUrl}`,
    );
  }

  if (response.status === 404) return undefined;
  if (!response.ok || response.type === "opaqueredirect") {
    throw new AuthenticationError(
      "auth_discovery_failed",
      `The Mecatl authentication profile request to ${metadataUrl} returned ${response.status}`,
    );
  }

  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > maximumMetadataBytes) {
    throw invalidDiscovery();
  }

  let metadata: unknown;
  try {
    metadata = JSON.parse(body);
  } catch {
    throw invalidDiscovery();
  }
  if (!isRecord(metadata)) throw invalidDiscovery();

  const discoveredResource = readString(metadata.resource);
  const authorizationServers = readStringArray(metadata.authorization_servers);
  const bearerMethods = readStringArray(metadata.bearer_methods_supported);
  const audience = readString(metadata["com.stacklok.mecatl.audience"]);
  const clientId = readString(metadata["com.stacklok.mecatl.client_id"]);
  const discoveredScopes =
    metadata.scopes_supported === undefined
      ? defaultScopes
      : readStringArray(metadata.scopes_supported);

  if (
    discoveredResource !== resource ||
    authorizationServers?.length !== 1 ||
    bearerMethods === undefined ||
    !bearerMethods.includes("header") ||
    audience === undefined ||
    clientId === undefined ||
    discoveredScopes === undefined ||
    discoveredScopes.length === 0 ||
    new Set(discoveredScopes).size !== discoveredScopes.length
  ) {
    throw invalidDiscovery();
  }

  const issuer = authorizationServers[0];
  if (issuer === undefined || !validHttpsUrl(issuer)) throw invalidDiscovery();

  return {
    audience,
    clientId,
    issuer,
    resource,
    scopes: [...discoveredScopes].sort(),
  };
}

export function protectedResourceMetadataUrl(resourceUrl: string): string {
  const resource = new URL(canonicalResource(resourceUrl));
  const resourcePath = resource.pathname === "/" ? "" : resource.pathname.replace(/\/$/u, "");
  resource.pathname = `/.well-known/oauth-protected-resource${resourcePath}`;
  resource.search = "";
  resource.hash = "";
  return resource.toString();
}

class OidcAuthenticationService implements AuthenticationService {
  readonly #codec: SealedCookieCodec;
  #configurationPromise: Promise<Configuration> | undefined;
  readonly #fetch: typeof fetch;
  readonly #logger: Logger;
  readonly #profile: MecatlAuthenticationProfile;
  readonly #publicUrl: URL | undefined;
  readonly #refreshes = new Map<string, Promise<AuthenticationCredential>>();
  readonly #verifyCredential: ((accessToken: string) => Promise<void>) | undefined;

  constructor(
    profile: MecatlAuthenticationProfile,
    codec: SealedCookieCodec,
    fetcher: typeof fetch,
    publicUrl: URL | undefined,
    logger: Logger,
    verifyCredential: ((accessToken: string) => Promise<void>) | undefined,
  ) {
    this.#profile = profile;
    this.#codec = codec;
    this.#fetch = fetcher;
    this.#publicUrl = publicUrl;
    this.#logger = logger;
    this.#verifyCredential = verifyCredential;
  }

  clear(context: Context<AppEnv>): void {
    const options = this.#deletionOptions(context);
    deleteCookie(context, accessCookie, options);
    deleteCookie(context, refreshCookie, options);
    deleteCookie(context, transactionCookie, options);
    // The double-submit token belongs to the session that is ending; leaving it
    // behind would outlive every credential it was paired with.
    deleteCookie(context, csrfCookie, { ...options, httpOnly: false });
  }

  async completeLogin(context: Context<AppEnv>): Promise<AuthenticationResult> {
    const encoded = getCookie(context, transactionCookie);
    deleteCookie(context, transactionCookie, this.#deletionOptions(context));
    const transaction = await this.#codec.open<LoginTransaction>("login", encoded);
    const callbackUrl = new URL(context.req.url);
    const state = callbackUrl.searchParams.get("state");
    if (!validLoginTransaction(transaction) || state === null || state !== transaction.state) {
      this.#audit("login.fail", context, { reason: "login_state_mismatch" });
      throw new AuthenticationError(
        "login_state_mismatch",
        "The login transaction is missing, expired, or does not match the callback state",
      );
    }

    try {
      const configuration = await this.#configuration();
      const authorizationResponseUrl = new URL(transaction.redirectUri);
      authorizationResponseUrl.search = callbackUrl.search;
      const tokens = await authorizationCodeGrant(configuration, authorizationResponseUrl, {
        expectedState: transaction.state,
        pkceCodeVerifier: transaction.codeVerifier,
      });
      // Audited by noteLoginComplete once the caller has verified the
      // credential and sealed the session: a token exchange that Mecatl then
      // rejects, or a session too large to store, is not a completed login.
      const claims = tokens.claims();
      const subject = verifiedSubject(claims?.sub);
      if (subject === undefined) throw missingSubject();
      const credential = credentialFromTokens(tokens, {
        subject,
        email: verifiedEmail(claims?.email),
      });
      return {
        credential,
        returnTo: transaction.returnTo,
        ...(transaction.flow === "popup" ? { flow: "popup" as const } : {}),
      };
    } catch (error) {
      if (error instanceof AuthenticationError) {
        this.#audit("login.fail", context, { reason: error.code });
        throw new AuthenticationError(error.code, error.message, transaction.flow === "popup");
      }
      this.#audit("login.fail", context, { reason: "token_exchange_failed" });
      throw new AuthenticationError(
        "token_exchange_failed",
        "The identity provider rejected the authorization code exchange",
        transaction.flow === "popup",
      );
    }
  }

  async credential(context: Context<AppEnv>): Promise<CredentialResolution> {
    const encodedAccess = getCookie(context, accessCookie);
    const encodedRefresh = getCookie(context, refreshCookie);
    if (encodedAccess === undefined && encodedRefresh === undefined) {
      return { status: "anonymous" };
    }
    const access = await this.#codec.open<AccessCredential>("access", encodedAccess);
    const refresh = await this.#codec.open<RefreshCredential>("refresh", encodedRefresh);
    if (!validAccessCredential(access)) {
      this.clear(context);
      return { status: "expired" };
    }

    const credential: AuthenticationCredential = {
      ...access,
      ...(validRefreshCredential(refresh) ? { refreshToken: refresh.refreshToken } : {}),
    };
    if (access.expiresAt - Date.now() > refreshAheadMilliseconds) {
      return { credential, status: "authenticated" };
    }
    if (!validRefreshCredential(refresh)) {
      this.clear(context);
      return { status: "expired" };
    }

    try {
      const refreshed = await this.#refresh(refresh.refreshToken, access);
      await this.save(context, refreshed);
      return { credential: refreshed, status: "authenticated" };
    } catch {
      this.clear(context);
      this.#audit("session.expired", context, {});
      return { status: "expired" };
    }
  }

  noteLoginComplete(context: Context<AppEnv>): void {
    this.#audit("login.complete", context, {});
  }

  noteLoginFailure(context: Context<AppEnv>, reason: string): void {
    this.#audit("login.fail", context, { reason });
  }

  async logout(context: Context<AppEnv>): Promise<void> {
    const access = await this.#codec.open<AccessCredential>(
      "access",
      getCookie(context, accessCookie),
    );
    const refresh = await this.#codec.open<RefreshCredential>(
      "refresh",
      getCookie(context, refreshCookie),
    );
    this.clear(context);
    this.#audit("logout", context, {});

    const tokens = [
      ...(validRefreshCredential(refresh) ? [refresh.refreshToken] : []),
      ...(validAccessCredential(access) ? [access.accessToken] : []),
    ];
    if (tokens.length === 0) return;

    try {
      const configuration = await this.#configuration();
      await Promise.allSettled(tokens.map((token) => tokenRevocation(configuration, token)));
    } catch {
      // Local logout succeeds even when discovery or best-effort revocation fails.
    }
  }

  async save(context: Context<AppEnv>, credential: AuthenticationCredential): Promise<void> {
    const options = this.#sessionOptions(context);
    let access: string;
    let refresh: string | undefined;
    try {
      [access, refresh] = await this.#sealSession(credential);
    } catch (error) {
      if (error instanceof AuthenticationError) {
        this.#audit("login.fail", context, { reason: error.code });
      }
      throw error;
    }
    // Both sealed BEFORE either is set, so an oversized refresh never leaves a
    // half-written session behind.
    setCookie(context, accessCookie, access, options);
    if (refresh === undefined) {
      deleteCookie(context, refreshCookie, this.#deletionOptions(context));
    } else {
      setCookie(context, refreshCookie, refresh, options);
    }
  }

  async #sealSession(
    credential: AuthenticationCredential,
  ): Promise<[access: string, refresh: string | undefined]> {
    if (verifiedSubject(credential.subject) === undefined) throw missingSubject();
    const access = await this.#codec.seal("access", {
      accessToken: credential.accessToken,
      ...(credential.email === undefined ? {} : { email: credential.email }),
      expiresAt: credential.expiresAt,
      subject: credential.subject,
      tokenType: credential.tokenType,
    } satisfies AccessCredential);
    const refresh =
      credential.refreshToken === undefined
        ? undefined
        : await this.#codec.seal("refresh", {
            refreshToken: credential.refreshToken,
          } satisfies RefreshCredential);
    return [access, refresh];
  }

  async signInRequired(context: Context<AppEnv>): Promise<boolean> {
    const access = await this.#codec.open<AccessCredential>(
      "access",
      getCookie(context, accessCookie),
    );
    if (!validAccessCredential(access)) return true;
    if (access.expiresAt > Date.now()) return false;
    const refresh = await this.#codec.open<RefreshCredential>(
      "refresh",
      getCookie(context, refreshCookie),
    );
    return !validRefreshCredential(refresh);
  }

  async startLogin(
    context: Context<AppEnv>,
    returnTo: string | undefined,
    flow?: "popup",
  ): Promise<string> {
    const safeReturnTo = validReturnTo(returnTo) ? returnTo : defaultReturnTo;
    const redirectUri = this.#redirectUri(context);
    const codeVerifier = randomPKCECodeVerifier();
    const state = randomState();
    const codeChallenge = await calculatePKCECodeChallenge(codeVerifier);
    const transaction = await this.#codec.seal("login", {
      codeVerifier,
      expiresAt: Date.now() + transactionLifetimeSeconds * 1_000,
      redirectUri,
      returnTo: safeReturnTo,
      state,
      ...(flow === "popup" ? { flow } : {}),
    } satisfies LoginTransaction);
    setCookie(context, transactionCookie, transaction, this.#transactionOptions(context));

    try {
      const configuration = await this.#configuration();
      this.#audit("login.start", context, {});
      return buildAuthorizationUrl(configuration, {
        // The deployment's protected-resource document names the audience its
        // issuer must mint the token for; omitting it yields a token Mecatl
        // then rejects. The Go client sends the same parameter.
        ...(this.#profile.audience === "" ? {} : { audience: this.#profile.audience }),
        code_challenge: codeChallenge,
        code_challenge_method: "S256",
        redirect_uri: redirectUri,
        scope: this.#profile.scopes.join(" "),
        state,
      }).toString();
    } catch (error) {
      deleteCookie(context, transactionCookie, this.#deletionOptions(context));
      if (error instanceof AuthenticationError) throw error;
      throw new AuthenticationError(
        "issuer_discovery_failed",
        "The identity provider configuration could not be discovered",
      );
    }
  }

  #audit(event: string, context: Context<AppEnv>, fields: { reason?: string }) {
    this.#logger.audit(`auth.${event}`, {
      clientAddress: context.get("clientAddress"),
      reason: fields.reason,
      requestId: context.get("requestId"),
    });
  }

  #configuration(): Promise<Configuration> {
    if (this.#configurationPromise === undefined) {
      const operation = discovery(
        new URL(this.#profile.issuer),
        this.#profile.clientId,
        { token_endpoint_auth_method: "none" },
        None(),
        {
          [customFetch]: (url, options) => this.#fetch(url, options as unknown as RequestInit),
          timeout: 10,
        },
      ).then((configuration) => {
        const metadata = configuration.serverMetadata();
        if (!metadata.code_challenge_methods_supported?.includes("S256")) {
          throw new AuthenticationError(
            "pkce_unsupported",
            "The identity provider does not advertise PKCE S256 support",
          );
        }
        for (const endpoint of [
          metadata.authorization_endpoint,
          metadata.token_endpoint,
          metadata.jwks_uri,
        ]) {
          if (!sameHttpsOrigin(endpoint, this.#profile.issuer)) {
            throw new AuthenticationError(
              "issuer_discovery_invalid",
              "The identity provider returned an invalid endpoint",
            );
          }
        }
        return configuration;
      });
      this.#configurationPromise = operation.catch((error: unknown) => {
        this.#configurationPromise = undefined;
        throw error;
      });
    }
    return this.#configurationPromise;
  }

  #deletionOptions(context: Context<AppEnv>) {
    return { path: "/", secure: this.#secure(context) } as const;
  }

  /**
   * The single-flight refresh: concurrent holders of one refresh token await
   * one grant. A successful grant stays shared for `rotationGraceMilliseconds`
   * after it completes, because an issuer that rotates refresh tokens rejects
   * the old one: a request that left the browser before the rotated cookies
   * arrived must reuse the new credential, not replay the spent token and
   * clear the session it just refreshed. A failed grant is forgotten at once.
   * The window is per replica; replicas behind a load balancer without
   * session affinity can still race each other.
   */
  #refresh(refreshToken: string, access: AccessCredential): Promise<AuthenticationCredential> {
    const active = this.#refreshes.get(refreshToken);
    if (active !== undefined) return active;

    const operation = this.#configuration()
      .then(async (configuration) => {
        const tokens = await refreshTokenGrant(configuration, refreshToken);
        const claims = tokens.claims();
        const subject = claims === undefined ? access.subject : verifiedSubject(claims.sub);
        if (subject === undefined || subject !== access.subject) {
          throw new AuthenticationError(
            "refresh_subject_mismatch",
            "The identity provider changed the session subject during refresh",
          );
        }
        const credential = credentialFromTokens(tokens, {
          subject,
          email: claims === undefined ? access.email : verifiedEmail(claims.email),
        });
        const refreshed = {
          ...credential,
          refreshToken: credential.refreshToken ?? refreshToken,
        };
        await this.#verifyCredential?.(refreshed.accessToken);
        return refreshed;
      })
      .then(
        (refreshed) => {
          const forget = setTimeout(() => {
            if (this.#refreshes.get(refreshToken) === operation)
              this.#refreshes.delete(refreshToken);
          }, rotationGraceMilliseconds);
          forget.unref?.();
          return refreshed;
        },
        (error: unknown) => {
          this.#refreshes.delete(refreshToken);
          throw error;
        },
      );
    this.#refreshes.set(refreshToken, operation);
    return operation;
  }

  /**
   * AC3.4: the callback is `STUDIO_PUBLIC_URL/api/v1/auth/callback`. The only
   * exception is local development through the Vite server on 127.0.0.1:18473,
   * which reuses mecatui's registered loopback callback.
   */
  #redirectUri(context: Context<AppEnv>): string {
    if (this.#publicUrl !== undefined) {
      return new URL(hostedCallbackPath, this.#publicUrl).toString();
    }
    const host = context.req.header("host");
    if (host === new URL(mecatuiCallbackUrl).host) return mecatuiCallbackUrl;
    throw new AuthenticationError(
      "login_unconfigured",
      "STUDIO_PUBLIC_URL must be set before browser login can start on this origin",
    );
  }

  #secure(context: Context<AppEnv>): boolean {
    if (this.#publicUrl !== undefined) return this.#publicUrl.protocol === "https:";
    return new URL(context.req.url).protocol === "https:";
  }

  #sessionOptions(context: Context<AppEnv>) {
    return {
      httpOnly: true,
      maxAge: cookieLifetimeSeconds,
      path: "/",
      sameSite: "Lax",
      secure: this.#secure(context),
    } as const;
  }

  #transactionOptions(context: Context<AppEnv>) {
    return { ...this.#sessionOptions(context), maxAge: transactionLifetimeSeconds } as const;
  }
}

/**
 * AES-256-GCM over a key that is the SHA-256 digest of the raw secret bytes;
 * a random 96-bit IV and the cookie kind as additional authenticated data.
 * Every replica must hold the same secret.
 */
export class SealedCookieCodec {
  readonly #key: Promise<CryptoKey>;

  constructor(secret: string) {
    this.#key = crypto.subtle
      .digest("SHA-256", new TextEncoder().encode(secret))
      .then((digest) =>
        crypto.subtle.importKey("raw", digest, { name: "AES-GCM" }, false, ["decrypt", "encrypt"]),
      );
  }

  async open<T>(kind: string, encoded: string | undefined): Promise<T | undefined> {
    if (encoded === undefined || encoded.length > maximumCookieValueBytes) return undefined;
    try {
      const value = Buffer.from(encoded, "base64url");
      if (value.byteLength <= 12) return undefined;
      const plaintext = await crypto.subtle.decrypt(
        {
          additionalData: new TextEncoder().encode(kind),
          iv: value.subarray(0, 12),
          name: "AES-GCM",
        },
        await this.#key,
        value.subarray(12),
      );
      return JSON.parse(new TextDecoder().decode(plaintext)) as T;
    } catch {
      return undefined;
    }
  }

  async seal(kind: string, value: unknown): Promise<string> {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt(
      {
        additionalData: new TextEncoder().encode(kind),
        iv,
        name: "AES-GCM",
      },
      await this.#key,
      new TextEncoder().encode(JSON.stringify(value)),
    );
    const encoded = Buffer.concat([Buffer.from(iv), Buffer.from(ciphertext)]).toString("base64url");
    if (encoded.length > maximumCookieValueBytes) {
      throw new AuthenticationError(
        "session_too_large",
        "The identity provider returned a credential too large for a browser session cookie",
      );
    }
    return encoded;
  }
}

function canonicalResource(url: string): string {
  const resource = new URL(url);
  resource.search = "";
  resource.hash = "";
  return resource.toString().replace(/\/$/u, "");
}

function credentialFromTokens(
  tokens: {
    readonly access_token?: string;
    readonly expires_in?: number;
    readonly refresh_token?: string;
    readonly token_type?: string;
  },
  identity: { readonly subject: string; readonly email?: string },
): AuthenticationCredential {
  if (
    tokens.access_token === undefined ||
    tokens.access_token === "" ||
    (tokens.token_type !== undefined && tokens.token_type.toLowerCase() !== "bearer")
  ) {
    throw new AuthenticationError(
      "token_response_invalid",
      "The identity provider returned an invalid access token",
    );
  }
  const expiresIn =
    typeof tokens.expires_in === "number" && Number.isFinite(tokens.expires_in)
      ? Math.max(0, tokens.expires_in)
      : 300;
  return {
    accessToken: tokens.access_token,
    expiresAt: Date.now() + expiresIn * 1_000,
    ...(tokens.refresh_token === undefined || tokens.refresh_token === ""
      ? {}
      : { refreshToken: tokens.refresh_token }),
    ...(identity.email === undefined ? {} : { email: identity.email }),
    subject: identity.subject,
    tokenType: "Bearer",
  };
}

function verifiedSubject(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function missingSubject(): AuthenticationError {
  return new AuthenticationError(
    "id_token_subject_missing",
    "The identity provider did not return a usable verified subject",
  );
}

function verifiedEmail(value: unknown): string | undefined {
  return typeof value === "string" &&
    value !== "" &&
    new TextEncoder().encode(value).byteLength <= 254 &&
    [...value].every((character) => {
      const code = character.codePointAt(0) ?? 0;
      return code >= 0x20 && code !== 0x7f;
    })
    ? value
    : undefined;
}

function invalidDiscovery(): AuthenticationError {
  return new AuthenticationError(
    "auth_discovery_invalid",
    "The Mecatl authentication profile is invalid",
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readString(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" && value.length <= 2_048 ? value : undefined;
}

function readStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.length > 100) return undefined;
  const strings = value.map(readString);
  return strings.every((entry): entry is string => entry !== undefined) ? strings : undefined;
}

function sameHttpsOrigin(endpoint: string | undefined, issuer: string): boolean {
  if (endpoint === undefined) return false;
  try {
    const endpointUrl = new URL(endpoint);
    const issuerUrl = new URL(issuer);
    return endpointUrl.protocol === "https:" && endpointUrl.origin === issuerUrl.origin;
  } catch {
    return false;
  }
}

function validAccessCredential(value: unknown): value is AccessCredential {
  return (
    isRecord(value) &&
    typeof value.accessToken === "string" &&
    value.accessToken !== "" &&
    value.accessToken.length <= 16_384 &&
    typeof value.expiresAt === "number" &&
    Number.isFinite(value.expiresAt) &&
    value.tokenType === "Bearer" &&
    verifiedSubject(value.subject) !== undefined &&
    (value.email === undefined || verifiedEmail(value.email) !== undefined)
  );
}

function validHttpsUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.username === "" && url.password === "";
  } catch {
    return false;
  }
}

function validLoginTransaction(value: unknown): value is LoginTransaction {
  return (
    isRecord(value) &&
    typeof value.expiresAt === "number" &&
    Number.isFinite(value.expiresAt) &&
    // Max-Age only governs a well-behaved browser; a copied sealed cookie must
    // expire on its own terms.
    value.expiresAt > Date.now() &&
    typeof value.codeVerifier === "string" &&
    value.codeVerifier !== "" &&
    typeof value.redirectUri === "string" &&
    value.redirectUri !== "" &&
    typeof value.returnTo === "string" &&
    typeof value.state === "string" &&
    value.state !== "" &&
    (value.flow === undefined || value.flow === "popup")
  );
}

function validRefreshCredential(value: unknown): value is RefreshCredential {
  return (
    isRecord(value) &&
    typeof value.refreshToken === "string" &&
    value.refreshToken !== "" &&
    value.refreshToken.length <= 16_384
  );
}

/** `return_to` is accepted only as an absolute same-origin path. */
export function validReturnTo(value: string | undefined): value is string {
  if (value === undefined) return false;
  return (
    value.startsWith("/") &&
    !value.startsWith("//") &&
    !value.includes("\\") &&
    [...value].every((character) => {
      const code = character.codePointAt(0) ?? 0;
      return code >= 0x20 && code !== 0x7f;
    }) &&
    value.length <= 2_048
  );
}
