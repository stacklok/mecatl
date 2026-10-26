// SPDX-License-Identifier: Apache-2.0

import { generateKeyPairSync, sign } from "node:crypto";

/**
 * A fake mecatl protected resource + OIDC issuer behind a `fetch` function, so
 * discovery, PKCE login, refresh, and revocation exercise the real
 * `openid-client` code paths with no network.
 */
export const issuerUrl = "https://issuer.example.com";
export const resourceUrl = "https://mecatl.example.com";

const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
const publicJwk = {
  ...publicKey.export({ format: "jwk" }),
  alg: "RS256",
  kid: "fixture-key",
  use: "sig",
};

function signedJwt(typ: string, claims: Record<string, unknown>): string {
  const now = Math.floor(Date.now() / 1_000);
  const header = Buffer.from(JSON.stringify({ alg: "RS256", kid: "fixture-key", typ })).toString(
    "base64url",
  );
  const payload = Buffer.from(
    JSON.stringify({ aud: "studio", exp: now + 3_600, iat: now, iss: issuerUrl, ...claims }),
  ).toString("base64url");
  const signed = `${header}.${payload}`;
  return `${signed}.${sign("RSA-SHA256", Buffer.from(signed), privateKey).toString("base64url")}`;
}

/** Decodes the claims of a fixture JWT without verifying it. */
export function jwtClaims(token: string): Record<string, unknown> {
  return JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8"));
}

export interface FakeIssuerOptions {
  /** Access-token size in characters for the code exchange; large values trip the cookie bound. */
  readonly accessTokenLength?: number;
  readonly discoveryStatus?: number;
  readonly refreshFails?: boolean;
  /** Reject a refresh token that was already exchanged, as rotating issuers do. */
  readonly rotateRefreshTokens?: boolean;
  /** Milliseconds a fresh access token lives; short values force refresh. */
  readonly expiresIn?: number;
  /** Verified claims in the authorization-code ID token; defaults to a usable subject. */
  readonly idTokenClaims?: Record<string, unknown>;
  /** When present, include a new ID token in the refresh response. */
  readonly refreshIdTokenClaims?: Record<string, unknown>;
  /**
   * Behave as the identity broker's AS-1: advertise
   * `com.stacklok.mecatl.identity_broker_sign_in` and issue `at+jwt` access
   * tokens naming the requesting `client_id`, the user's `sub_id`, and the
   * `resource` the request asked for as `aud`.
   */
  readonly identityBrokerSignIn?: boolean;
}

/** One token endpoint request as the fake received it. */
export interface TokenRequest {
  readonly clientId: string | null;
  readonly grantType: string | null;
  readonly refreshToken: string | null;
  readonly resource: string | null;
}

export interface FakeIssuer {
  readonly fetch: typeof fetch;
  readonly refreshCalls: () => number;
  readonly revokedTokens: () => readonly string[];
  readonly tokenCalls: () => number;
  /** Every token endpoint request, in order. */
  readonly tokenRequests: () => readonly TokenRequest[];
}

export function fakeIssuer(options: FakeIssuerOptions = {}): FakeIssuer {
  let refreshCalls = 0;
  let tokenCalls = 0;
  const revoked: string[] = [];
  const tokenRequests: TokenRequest[] = [];
  let accessCounter = 0;

  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
      status,
    });

  const spentRefreshTokens = new Set<string>();
  const fetcher: typeof fetch = async (input, init) => {
    const request = new Request(input, init);
    const url = new URL(request.url);
    const path = `${url.origin}${url.pathname}`;

    if (path === `${resourceUrl}/.well-known/oauth-protected-resource`) {
      const status = options.discoveryStatus ?? 200;
      if (status !== 200) return new Response("nope", { status });
      return json({
        authorization_servers: [issuerUrl],
        bearer_methods_supported: ["header"],
        "com.stacklok.mecatl.audience": "mecatl",
        "com.stacklok.mecatl.client_id": "studio",
        ...(options.identityBrokerSignIn === true
          ? { "com.stacklok.mecatl.identity_broker_sign_in": true }
          : {}),
        resource: resourceUrl,
        scopes_supported: ["openid", "offline_access"],
      });
    }
    if (path === `${issuerUrl}/.well-known/openid-configuration`) {
      return json({
        authorization_endpoint: `${issuerUrl}/authorize`,
        code_challenge_methods_supported: ["S256"],
        grant_types_supported: ["authorization_code", "refresh_token"],
        issuer: issuerUrl,
        jwks_uri: `${issuerUrl}/jwks`,
        response_types_supported: ["code"],
        revocation_endpoint: `${issuerUrl}/revoke`,
        token_endpoint: `${issuerUrl}/token`,
        token_endpoint_auth_methods_supported: ["none"],
      });
    }
    if (path === `${issuerUrl}/jwks`) return json({ keys: [publicJwk] });
    if (path === `${issuerUrl}/token` && request.method === "POST") {
      const form = new URLSearchParams(await request.text());
      const refreshing = form.get("grant_type") === "refresh_token";
      const clientId = form.get("client_id");
      tokenRequests.push({
        clientId,
        grantType: form.get("grant_type"),
        refreshToken: form.get("refresh_token"),
        resource: form.get("resource"),
      });
      tokenCalls += 1;
      if (refreshing) {
        refreshCalls += 1;
        if (options.refreshFails === true) {
          return json({ error: "invalid_grant" }, 400);
        }
        const presented = form.get("refresh_token") ?? "";
        if (options.rotateRefreshTokens === true && spentRefreshTokens.has(presented)) {
          return json({ error: "invalid_grant" }, 400);
        }
        spentRefreshTokens.add(presented);
        // Slow enough that concurrent callers overlap, proving single-flight.
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
      accessCounter += 1;
      const length = options.accessTokenLength ?? 24;
      const subject = String((options.idTokenClaims ?? { sub: "subject-1" }).sub ?? "");
      return json({
        access_token:
          options.identityBrokerSignIn === true
            ? signedJwt("at+jwt", {
                aud: form.get("resource") ?? "",
                client_id: clientId,
                jti: `access-${accessCounter}`,
                sub: `broker-${subject}`,
                sub_id: { format: "iss_sub", iss: issuerUrl, sub: subject },
              })
            : `access-${accessCounter}-`.padEnd(length, "x"),
        expires_in: Math.max(1, Math.floor((options.expiresIn ?? 3_600_000) / 1_000)),
        refresh_token: `refresh-${accessCounter}`,
        token_type: "Bearer",
        ...(refreshing
          ? options.refreshIdTokenClaims === undefined
            ? {}
            : { id_token: signedJwt("JWT", { aud: clientId, ...options.refreshIdTokenClaims }) }
          : {
              id_token: signedJwt("JWT", {
                aud: clientId,
                ...(options.idTokenClaims ?? { sub: "subject-1" }),
              }),
            }),
      });
    }
    if (path === `${issuerUrl}/revoke` && request.method === "POST") {
      const form = new URLSearchParams(await request.text());
      revoked.push(form.get("token") ?? "");
      return new Response(null, { status: 200 });
    }
    return new Response("not found", { status: 404 });
  };

  return {
    fetch: fetcher,
    refreshCalls: () => refreshCalls,
    revokedTokens: () => revoked,
    tokenCalls: () => tokenCalls,
    tokenRequests: () => tokenRequests,
  };
}
