/**
 * OAuth2 client_credentials token minting for the staging mecak8s M2M Okta
 * app (infra `okta/main.tf`'s `mecak8s_slack_bot_m2m`). Used only when the
 * bot talks to a remote, OIDC-gated `mecated`-compatible backend
 * (`MECATL_GRPC_TLS=true`) — the local/Compose dev path never touches this.
 *
 * Caches the access token in memory and refetches shortly before it expires,
 * exposed as a `CredentialProvider` (see `@stacklok/mecatl-sdk`'s
 * `credentials.ts`) so the SDK's gRPC transport attaches it to every call.
 */

import { Buffer } from "node:buffer";

export interface M2MTokenConfig {
  tokenUrl: string;
  clientId: string;
  clientSecret: string;
  scope: string;
}

// Refetch this many seconds before the token's reported expiry, so an
// in-flight run never races a token that expires mid-request.
const EXPIRY_SAFETY_MARGIN_SECONDS = 60;

interface TokenResponse {
  access_token: string;
  expires_in: number;
}

export class M2MTokenProvider {
  readonly #config: M2MTokenConfig;
  #cached: { token: string; expiresAtMs: number } | undefined;

  constructor(config: M2MTokenConfig) {
    this.#config = config;
  }

  /** `CredentialProvider`-shaped: usable directly as `NodeTransportOptions.credentialProvider`. */
  headers = async (): Promise<HeadersInit> => {
    return { authorization: `Bearer ${await this.#token()}` };
  };

  async #token(): Promise<string> {
    const cached = this.#cached;
    if (cached !== undefined && cached.expiresAtMs > Date.now()) return cached.token;

    const body = new URLSearchParams({
      grant_type: "client_credentials",
      scope: this.#config.scope,
    });
    const basicAuth = Buffer.from(`${this.#config.clientId}:${this.#config.clientSecret}`).toString(
      "base64",
    );
    const response = await fetch(this.#config.tokenUrl, {
      body,
      headers: {
        authorization: `Basic ${basicAuth}`,
        "content-type": "application/x-www-form-urlencoded",
      },
      method: "POST",
    });
    if (!response.ok) {
      const detail = await response.text().catch(() => "");
      throw new Error(
        `M2M token request to ${this.#config.tokenUrl} failed: ${response.status} ${detail}`,
      );
    }
    const parsed = (await response.json()) as TokenResponse;
    this.#cached = {
      expiresAtMs: Date.now() + (parsed.expires_in - EXPIRY_SAFETY_MARGIN_SECONDS) * 1000,
      token: parsed.access_token,
    };
    return parsed.access_token;
  }
}
