/**
 * The OIDC token store for Studio's server tier (requirement H3): process
 * memory only — tokens never reach the browser (CLAUDE.md rule 3) and are
 * never written to disk, so a Next server restart deliberately requires a
 * fresh sign-in (v1 defers durable/keyring storage; ADR 0277's encrypted
 * credential store is the eventual model).
 *
 * Refresh discipline mirrors mecatui's `internal/adapter/clientauth`
 * (ADR 0270/0277): refresh only on token DEMAND inside the 30-second
 * refresh-ahead window (demand-driven refresh IS the activity gate — there is
 * no background timer to keep an idle provider SSO session alive),
 * single-flight so a rotated one-time refresh token is never raced, and
 * `invalid_grant` classified STRUCTURALLY (the exact RFC 6749 section 5.2
 * `error` code off the token endpoint's JSON body — provider prose and nested
 * descriptions never classify) before the credential is dropped.
 */

export const REFRESH_AHEAD_MS = 30_000;

/** Display-only identity claims parsed (unverified) from the id_token. The
 * daemon does the real validation; Studio never authorizes on these. */
export type OidcClaims = {
  sub?: string;
  email?: string;
  preferredUsername?: string;
};

export type StoredTokens = {
  accessToken: string;
  /** Empty when the provider issued none (refresh then impossible). */
  refreshToken: string;
  /** Epoch milliseconds. */
  expiresAt: number;
  claims: OidcClaims;
};

export type SignedOutReason = "signed-out" | "expired";

export type BearerOutcome =
  | { kind: "bearer"; token: string }
  | { kind: "login-required"; reason: SignedOutReason }
  /** A transient refresh failure with no still-valid access token to fall
   * back on. Tokens are retained so a later demand can retry. */
  | { kind: "refresh-failed" };

export type RefreshResult =
  | { kind: "refreshed"; tokens: StoredTokens }
  | { kind: "invalid-grant" }
  | { kind: "transient" };

/** The provider exchange, injected so the store stays pure and offline-testable. */
export type RefreshExchange = (prior: StoredTokens) => Promise<RefreshResult>;

export function tokenDecision(
  tokens: StoredTokens | null,
  now: number,
): "use" | "refresh" | "login-required" {
  if (!tokens?.accessToken) return "login-required";
  if (tokens.expiresAt - now > REFRESH_AHEAD_MS) return "use";
  return tokens.refreshToken ? "refresh" : "login-required";
}

/**
 * RFC 6749 section 5.2 structured classification, mirroring clientauth's
 * `isInvalidGrant` (an `oauth2.RetrieveError` with the EXACT ErrorCode): a
 * failed token response whose JSON body carries `error: "invalid_grant"`
 * byte-exactly. `error_description` prose never classifies.
 */
export function isInvalidGrantResponse(status: number, body: unknown): boolean {
  return (
    (status < 200 || status >= 300) &&
    typeof body === "object" &&
    body !== null &&
    (body as { error?: unknown }).error === "invalid_grant"
  );
}

/** Expiry from a token response's `expires_in`, floored/defaulted like the
 * controller's gateway refresh (default 300s, floor 60s). */
export function tokenExpiryMs(expiresIn: unknown, now: number): number {
  return now + Math.max(60, Number(expiresIn) || 300) * 1000;
}

/** Unverified, display-only parse of a JWT payload (see OidcClaims). */
export function claimsFromIdToken(idToken: string): OidcClaims | null {
  const payload = idToken.split(".")[1];
  if (!payload) return null;
  try {
    const decoded = JSON.parse(
      Buffer.from(payload, "base64url").toString("utf8"),
    ) as Record<string, unknown>;
    const readString = (key: string) =>
      typeof decoded[key] === "string" ? (decoded[key] as string) : undefined;
    return {
      sub: readString("sub"),
      email: readString("email"),
      preferredUsername: readString("preferred_username"),
    };
  } catch {
    return null;
  }
}

/**
 * Build the replacement credential from a SUCCESSFUL refresh response:
 * adopt a rotated refresh token when the provider sent one, otherwise keep
 * the prior one (RFC 6749 section 6 — rotation is optional), and keep the
 * prior claims unless a fresh id_token parses.
 */
export function tokensFromRefreshResponse(
  body: Record<string, unknown>,
  prior: StoredTokens,
  now: number,
): StoredTokens | null {
  const accessToken =
    typeof body.access_token === "string" ? body.access_token : "";
  if (!accessToken) return null;
  const rotated =
    typeof body.refresh_token === "string" && body.refresh_token
      ? body.refresh_token
      : prior.refreshToken;
  const claims =
    (typeof body.id_token === "string" && claimsFromIdToken(body.id_token)) ||
    prior.claims;
  return {
    accessToken,
    refreshToken: rotated,
    expiresAt: tokenExpiryMs(body.expires_in, now),
    claims,
  };
}

export type StoreSnapshot =
  | { state: "signed-out" | "expired" }
  | { state: "signed-in"; claims: OidcClaims; expiresAt: number };

export class OidcTokenStore {
  private tokens: StoredTokens | null = null;
  private reason: SignedOutReason = "signed-out";
  private inflight: Promise<BearerOutcome> | null = null;

  constructor(private readonly now: () => number = Date.now) {}

  setTokens(tokens: StoredTokens): void {
    this.tokens = tokens;
    this.reason = "signed-out";
  }

  /** Drop the credential. `reason` records WHY for the next 401's copy. */
  clear(reason: SignedOutReason = "signed-out"): void {
    this.tokens = null;
    this.reason = reason;
  }

  current(): StoredTokens | null {
    return this.tokens;
  }

  snapshot(): StoreSnapshot {
    if (this.tokens) {
      return {
        state: "signed-in",
        claims: this.tokens.claims,
        expiresAt: this.tokens.expiresAt,
      };
    }
    return { state: this.reason };
  }

  /**
   * Obtain a bearer for one proxied request: reuse a token outside the
   * refresh-ahead window, refresh (single-flight) inside it, and report
   * login-required with an honest reason otherwise.
   */
  async bearer(exchange: RefreshExchange): Promise<BearerOutcome> {
    const decision = tokenDecision(this.tokens, this.now());
    if (decision === "use" && this.tokens) {
      return { kind: "bearer", token: this.tokens.accessToken };
    }
    if (decision === "login-required") {
      if (this.tokens) this.clear("expired"); // expired with no refresh token
      return { kind: "login-required", reason: this.reason };
    }
    if (!this.inflight) {
      this.inflight = this.refresh(exchange).finally(() => {
        this.inflight = null;
      });
    }
    return this.inflight;
  }

  private async refresh(exchange: RefreshExchange): Promise<BearerOutcome> {
    const prior = this.tokens;
    if (!prior) return { kind: "login-required", reason: this.reason };
    let result: RefreshResult;
    try {
      result = await exchange(prior);
    } catch {
      result = { kind: "transient" };
    }
    if (result.kind === "refreshed") {
      this.setTokens(result.tokens);
      return { kind: "bearer", token: result.tokens.accessToken };
    }
    if (result.kind === "invalid-grant") {
      // The provider rejected the refresh token itself — the session is over.
      // Drop the credential (clientauth parity) so the UI can say so.
      this.clear("expired");
      return { kind: "login-required", reason: "expired" };
    }
    // Transient (network/5xx/malformed body): keep the credential. A token
    // that has not actually expired yet still serves this request.
    if (prior.expiresAt > this.now()) {
      return { kind: "bearer", token: prior.accessToken };
    }
    return { kind: "refresh-failed" };
  }
}
