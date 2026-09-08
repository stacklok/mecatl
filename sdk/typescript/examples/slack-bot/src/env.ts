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
  /** Slack user IDs allowed to trigger a prompt. `undefined` = unrestricted (see README's
   * security note — inviting the bot to a channel then extends this to everyone in it). */
  allowedUserIds: Set<string> | undefined;
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

  const allowList = env.SLACK_ALLOWED_USER_IDS;
  const allowedUserIds =
    allowList === undefined || allowList.trim() === ""
      ? undefined
      : new Set(
          allowList
            .split(",")
            .map((id) => id.trim())
            .filter((id) => id.length > 0),
        );

  return {
    allowedUserIds,
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
