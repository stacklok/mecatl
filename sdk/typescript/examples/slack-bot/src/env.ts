import type { CredentialProvider } from "@stacklok/mecatl-sdk/node";
import { M2MTokenProvider } from "./m2mToken.js";

/** Connection info for the mecatl daemon this bot talks to. Exactly one of the two is set.
 * `credentialProvider` only ever accompanies `baseUrl` — a remote, OIDC-gated deployment
 * (`MECATL_GRPC_TLS=true`); the local/Compose `socketPath` path has no bearer to attach. */
export type MecatlTarget =
  | { baseUrl: string; socketPath?: never; credentialProvider?: CredentialProvider }
  | { socketPath: string; baseUrl?: never; credentialProvider?: never };

export interface RateLimitConfig {
  max: number;
  windowMs: number;
}

export interface BotConfig {
  slackBotToken: string;
  slackAppToken: string;
  mecatlTarget: MecatlTarget;
  /** Verified emails allowed to trigger a prompt (lowercased). `undefined` = no per-email
   * allow configured — see `allowedEmailDomains` and README's security note. */
  allowedEmails: Set<string> | undefined;
  /** Verified-email domains (e.g. "example.com", lowercased, no leading "@") whose members
   * are all allowed. `undefined` alongside `allowedEmails` also unset = unrestricted, except
   * guests/deactivated/Slack-Connect-strangers, who are never allowed regardless (see
   * `EmailAllowlistResolver`). */
  allowedEmailDomains: Set<string> | undefined;
  /** Slack channel IDs (e.g. `C0123ABCDEF`) allowed to trigger a prompt via `@mention`.
   * `undefined` = no channel restriction — every channel the bot is invited to is usable,
   * subject to the identity checks above. This is a hard, channel-level gate: it never
   * applies to DMs (each DM channel ID is per-user, so restricting by ID there would mean
   * hand-maintaining a list of DM channel IDs instead of just using
   * `allowedEmails`/`allowedEmailDomains`, which already gate DM access by identity). Kept
   * case-sensitive/unmodified — unlike emails, Slack channel IDs aren't meaningfully
   * lowercased. */
  allowedChannelIds: Set<string> | undefined;
  rateLimit: RateLimitConfig;
}

const DEFAULT_RATE_LIMIT_MAX = 20;
const DEFAULT_RATE_LIMIT_WINDOW_MS = 10 * 60 * 1000;

/** Reads and validates the bot's configuration from the environment, failing fast on gaps. */
export function loadConfig(env: NodeJS.ProcessEnv = process.env): BotConfig {
  const slackBotToken = required(env, "SLACK_BOT_TOKEN");
  const slackAppToken = required(env, "SLACK_APP_TOKEN");
  const grpcAddress = env.MECATL_GRPC_ADDRESS;
  const socketPath = env.MECATL_SOCKET_PATH;
  if (grpcAddress === undefined && socketPath === undefined) {
    throw new Error("Set exactly one of MECATL_GRPC_ADDRESS or MECATL_SOCKET_PATH");
  }
  if (grpcAddress !== undefined && socketPath !== undefined) {
    throw new Error("Set only one of MECATL_GRPC_ADDRESS or MECATL_SOCKET_PATH, not both");
  }
  const mecatlTarget = mecatlTargetFor(env, grpcAddress, socketPath);

  const allowedEmails = parseCommaListLower(env.SLACK_ALLOWED_EMAILS);
  const allowedEmailDomains = parseCommaListLower(env.SLACK_ALLOWED_EMAIL_DOMAINS);
  const allowedChannelIds = parseCommaList(env.SLACK_ALLOWED_CHANNEL_IDS);

  return {
    allowedChannelIds,
    allowedEmailDomains,
    allowedEmails,
    mecatlTarget,
    rateLimit: {
      max: positiveIntOr(env.SLACK_RATE_LIMIT_MAX, DEFAULT_RATE_LIMIT_MAX),
      windowMs: positiveIntOr(env.SLACK_RATE_LIMIT_WINDOW_MS, DEFAULT_RATE_LIMIT_WINDOW_MS),
    },
    slackAppToken,
    slackBotToken,
  };
}

/**
 * Builds the mecatl connection target. `MECATL_GRPC_TLS=true` selects a
 * remote, OIDC-gated deployment (e.g. the shared staging `mecak8s`): the
 * scheme becomes `https://` and every call carries a bearer minted via
 * OAuth2 client_credentials (`MECAK8S_OIDC_TOKEN_URL`/`_CLIENT_ID`/
 * `_CLIENT_SECRET`/`_SCOPE`). Unset (the local/Compose default), the target
 * is a plain, unauthenticated `http://` address, matching the existing dev
 * flow exactly.
 */
function mecatlTargetFor(
  env: NodeJS.ProcessEnv,
  grpcAddress: string | undefined,
  socketPath: string | undefined,
): MecatlTarget {
  if (socketPath !== undefined) return { socketPath };

  const useTls = env.MECATL_GRPC_TLS === "true";
  if (!useTls) return { baseUrl: `http://${grpcAddress}` };

  const tokenProvider = new M2MTokenProvider({
    clientId: required(env, "MECAK8S_OIDC_CLIENT_ID"),
    clientSecret: required(env, "MECAK8S_OIDC_CLIENT_SECRET"),
    scope: env.MECAK8S_OIDC_SCOPE === undefined ? "agent.invoke" : env.MECAK8S_OIDC_SCOPE,
    tokenUrl: required(env, "MECAK8S_OIDC_TOKEN_URL"),
  });
  return { baseUrl: `https://${grpcAddress}`, credentialProvider: tokenProvider.headers };
}

function parseCommaListLower(value: string | undefined): Set<string> | undefined {
  if (value === undefined || value.trim() === "") return undefined;
  return new Set(
    value
      .split(",")
      .map((entry) => entry.trim().toLowerCase())
      .filter((entry) => entry.length > 0),
  );
}

function parseCommaList(value: string | undefined): Set<string> | undefined {
  if (value === undefined || value.trim() === "") return undefined;
  return new Set(
    value
      .split(",")
      .map((entry) => entry.trim())
      .filter((entry) => entry.length > 0),
  );
}

function required(env: NodeJS.ProcessEnv, name: string): string {
  const value = env[name];
  if (value === undefined || value === "") {
    throw new Error(`Missing required environment variable: ${name}`);
  }
  return value;
}

function positiveIntOr(value: string | undefined, fallback: number): number {
  if (value === undefined || value.trim() === "") return fallback;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}
