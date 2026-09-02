/** Connection info for the mecatl daemon this bot talks to. Exactly one of the two is set. */
export type MecatlTarget =
  | { baseUrl: string; socketPath?: never }
  | { socketPath: string; baseUrl?: never };

export interface RateLimitConfig {
  max: number;
  windowMs: number;
}

export interface BotConfig {
  slackBotToken: string;
  slackAppToken: string;
  mecatlTarget: MecatlTarget;
  mecatlWorkspace: string;
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
  const mecatlWorkspace = required(env, "MECATL_WORKSPACE");

  const grpcAddress = env.MECATL_GRPC_ADDRESS;
  const socketPath = env.MECATL_SOCKET_PATH;
  if (grpcAddress === undefined && socketPath === undefined) {
    throw new Error("Set exactly one of MECATL_GRPC_ADDRESS or MECATL_SOCKET_PATH");
  }
  if (grpcAddress !== undefined && socketPath !== undefined) {
    throw new Error("Set only one of MECATL_GRPC_ADDRESS or MECATL_SOCKET_PATH, not both");
  }

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
    mecatlTarget: socketPath !== undefined ? { socketPath } : { baseUrl: `http://${grpcAddress}` },
    mecatlWorkspace,
    rateLimit: {
      max: positiveIntOr(env.SLACK_RATE_LIMIT_MAX, DEFAULT_RATE_LIMIT_MAX),
      windowMs: positiveIntOr(env.SLACK_RATE_LIMIT_WINDOW_MS, DEFAULT_RATE_LIMIT_WINDOW_MS),
    },
    slackAppToken,
    slackBotToken,
  };
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
