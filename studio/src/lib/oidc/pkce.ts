/**
 * Pure Authorization Code + PKCE helpers for Studio's remote OIDC login
 * (requirement H3, modeled on the MCP-gateway OAuth machinery in
 * `scripts/local-controller.mjs` and mecatui's `internal/adapter/clientauth`
 * per ADR 0277). No I/O and no process state — the server-tier session module
 * (`src/lib/oidc-session.ts`) owns fetches and storage.
 */
import { createHash, randomBytes } from "node:crypto";

/** URL-safe random value for `state` and the PKCE `code_verifier`
 * (RFC 7636 section 4.1's unreserved charset via base64url, no padding). */
export function randomUrlSafe(bytes = 32): string {
  return randomBytes(bytes).toString("base64url");
}

/** RFC 7636 S256: BASE64URL(SHA256(ASCII(code_verifier))). */
export function pkceChallengeS256(verifier: string): string {
  return createHash("sha256").update(verifier).digest("base64url");
}

export function buildAuthorizationUrl(options: {
  authorizationEndpoint: string;
  clientId: string;
  redirectUri: string;
  scope: string;
  state: string;
  codeChallenge: string;
  /** Sent as the `audience` parameter when set — some providers (e.g. Auth0)
   * need it to mint a JWT access token bearing the daemon's `--oidc-audience`
   * claim; providers that don't know the parameter ignore it. */
  audience?: string;
}): string {
  const url = new URL(options.authorizationEndpoint);
  url.searchParams.set("response_type", "code");
  url.searchParams.set("client_id", options.clientId);
  url.searchParams.set("redirect_uri", options.redirectUri);
  url.searchParams.set("scope", options.scope);
  url.searchParams.set("state", options.state);
  url.searchParams.set("code_challenge", options.codeChallenge);
  url.searchParams.set("code_challenge_method", "S256");
  if (options.audience) url.searchParams.set("audience", options.audience);
  return url.toString();
}

/**
 * Bound a provider-controlled OAuth callback `error`/`error_description`
 * before it may reach the operator — ADR 0277's narrow exception: each value
 * is independently restricted to the RFC 6749 printable subset
 * (%x20-21 / %x23-5B / %x5D-7E — printable ASCII minus `"` and `\`) and
 * length-clamped. Everything else a provider says stays out of errors.
 */
export function boundProviderError(value: string, max = 200): string {
  let out = "";
  for (const ch of value) {
    const code = ch.charCodeAt(0);
    if (code >= 0x20 && code <= 0x7e && code !== 0x22 && code !== 0x5c) {
      out += ch;
    }
    if (out.length >= max) break;
  }
  return out;
}

export type CallbackParams = {
  state: string;
  code: string;
  error: string;
  errorDescription: string;
  iss: string;
};

export function readCallbackParams(search: URLSearchParams): CallbackParams {
  return {
    state: search.get("state") || "",
    code: search.get("code") || "",
    error: search.get("error") || "",
    errorDescription: search.get("error_description") || "",
    iss: search.get("iss") || "",
  };
}

export type CallbackVerdict =
  | { ok: true; code: string }
  | { ok: false; reason: string };

const normalizeIssuer = (issuer: string) => issuer.replace(/\/+$/, "");

/**
 * Validate an authorization-response callback AFTER its `state` has matched a
 * pending attempt (the state→attempt lookup is the caller's job — an unknown
 * state never reaches this function). Mirrors mecatui's `clientauth.Login`:
 * provider errors are bounded, a missing code fails, and RFC 9207 `iss` is
 * checked when present and REQUIRED when discovery advertised support.
 */
export function evaluateCallback(
  params: Pick<CallbackParams, "code" | "error" | "errorDescription" | "iss">,
  check: { expectedIssuer: string; issRequired: boolean },
): CallbackVerdict {
  if (params.error) {
    const detail = boundProviderError(params.errorDescription);
    return {
      ok: false,
      reason: `The identity provider reported: ${boundProviderError(params.error)}${detail ? ` — ${detail}` : ""}`,
    };
  }
  if (!params.code) {
    return {
      ok: false,
      reason: "The callback did not include an authorization code.",
    };
  }
  if (!params.iss) {
    if (check.issRequired) {
      return {
        ok: false,
        reason:
          "The authorization server advertises RFC 9207 but the callback carried no iss parameter.",
      };
    }
  } else if (
    normalizeIssuer(params.iss) !== normalizeIssuer(check.expectedIssuer)
  ) {
    return {
      ok: false,
      reason: "The callback iss did not match the configured issuer.",
    };
  }
  return { ok: true, code: params.code };
}
