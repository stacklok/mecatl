/** The minimal shape this resolver needs from `app.client` — deliberately not the real
 * `@slack/web-api` `WebClient` type (that package is only a transitive dep via `@slack/bolt`,
 * not declared in this package's own `package.json`). Bolt's real client satisfies this
 * structurally, and tests can fake it without pulling in the SDK's types either. */
export interface UserLookupClient {
  users: {
    info(args: { user: string }): Promise<{
      user?: {
        deleted?: boolean;
        is_email_confirmed?: boolean;
        is_restricted?: boolean;
        is_stranger?: boolean;
        is_ultra_restricted?: boolean;
        profile?: { email?: string };
      };
    }>;
  };
}

/**
 * The bot's own Slack-native view of "who sent this" — never email, Okta, or any directory
 * concept, so that {@link AccessResolver} stays generic and org-specific identity backends
 * (Okta rosters, directory-service group lookups) can plug in as a separate adapter without
 * this repo, or the bot's own wiring, ever needing to know they exist. See issue #1241.
 */
export interface SlackMessageContext {
  slackUserId: string;
  channelId?: string;
}

export type AccessDecision = { allowed: false } | { allowed: true; principal: string };

/** The pluggable access-control seam. `EmailAllowlistResolver` below is this repo's default,
 * generic, Slack-only implementation; an org-specific one (Okta-roster-backed, directory
 * service/channel-group-backed) is a separate adapter implementing the same interface,
 * injected at the consuming deployment's level — never merged into this reference bot. */
export interface AccessResolver {
  resolve(ctx: SlackMessageContext): Promise<AccessDecision>;
}

export interface MinimalLogger {
  warn(msg: string, ...meta: unknown[]): void;
}

export interface EmailAllowlistConfig {
  /** Exact verified emails to allow, lowercased. `undefined` = no explicit per-email allow. */
  allowedEmails: Set<string> | undefined;
  /** Verified-email domains (e.g. "example.com") whose members are all allowed, lowercased.
   * `undefined` = no domain-wide allow. */
  allowedEmailDomains: Set<string> | undefined;
}

/**
 * The default, generic `AccessResolver`: resolves a Slack user's verified email via
 * `users.info` (needs the `users:read` + `users:read.email` bot scopes) and applies two kinds
 * of check —
 *
 * 1. **Identity-integrity rejects, always enforced, regardless of allowlist config:** a
 *    deleted/deactivated account, a workspace guest (`is_restricted`/`is_ultra_restricted`),
 *    a Slack Connect member of a different company entirely (`is_stranger`), or an explicitly
 *    unconfirmed email (`is_email_confirmed === false` — checked only when the field is
 *    actually present; not every token populates it, and its absence must never itself be
 *    treated as a rejection). These aren't configurable policy — they're the exact structural
 *    gaps `SLACK_ALLOWED_USER_IDS` couldn't express (see issue #1241).
 * 2. **The configured allowlist**, once identity is verified: allowed if the email is in
 *    `allowedEmails`, or its domain is in `allowedEmailDomains`. If BOTH are unset, every
 *    identity-verified (non-guest, non-stranger) user is allowed — the same "unrestricted by
 *    default" posture `SLACK_ALLOWED_USER_IDS` had, minus the guest/stranger gap.
 *
 * Fails closed: any `users.info` error (network, missing scope, unknown user) is logged and
 * treated as not allowed — never falls back to trusting an unresolved identity.
 */
export class EmailAllowlistResolver implements AccessResolver {
  readonly #client: UserLookupClient;
  readonly #config: EmailAllowlistConfig;
  readonly #logger: MinimalLogger;

  constructor(client: UserLookupClient, config: EmailAllowlistConfig, logger: MinimalLogger) {
    this.#client = client;
    this.#config = config;
    this.#logger = logger;
  }

  async resolve(ctx: SlackMessageContext): Promise<AccessDecision> {
    let user: Awaited<ReturnType<UserLookupClient["users"]["info"]>>["user"];
    try {
      const response = await this.#client.users.info({ user: ctx.slackUserId });
      user = response.user;
    } catch (error) {
      this.#logger.warn("users.info failed — treating as not authorized", error);
      return { allowed: false };
    }

    if (user === undefined) {
      this.#logger.warn(`users.info returned no user for ${ctx.slackUserId}`);
      return { allowed: false };
    }
    if (user.deleted === true || user.is_restricted === true || user.is_ultra_restricted === true) {
      return { allowed: false };
    }
    if (user.is_stranger === true) {
      return { allowed: false };
    }
    if (user.is_email_confirmed === false) {
      return { allowed: false };
    }

    const email = user.profile?.email;
    if (email === undefined || email.length === 0) {
      this.#logger.warn(
        `users.info returned no email for ${ctx.slackUserId} (missing users:read.email scope?)`,
      );
      return { allowed: false };
    }
    const normalizedEmail = email.toLowerCase();

    const { allowedEmails, allowedEmailDomains } = this.#config;
    if (allowedEmails === undefined && allowedEmailDomains === undefined) {
      return { allowed: true, principal: normalizedEmail };
    }

    if (allowedEmails?.has(normalizedEmail) === true) {
      return { allowed: true, principal: normalizedEmail };
    }
    const domain = normalizedEmail.split("@")[1];
    if (domain !== undefined && allowedEmailDomains?.has(domain) === true) {
      return { allowed: true, principal: normalizedEmail };
    }
    return { allowed: false };
  }
}
