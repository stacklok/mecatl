/** The minimal shape this resolver needs from `app.client` — deliberately not the real
 * `@slack/web-api` `WebClient` type (that package is only a transitive dep via `@slack/bolt`,
 * not declared in this package's own `package.json`). Bolt's real client satisfies this
 * structurally, and tests can fake it without pulling in the SDK's types either. */
export interface UserLookupClient {
  /** `auth.test` needs no scope beyond the bot token itself; used once, cached, to learn our
   * own installed workspace's team id (see `EmailAllowlistResolver`'s Slack Connect check). */
  auth: {
    test(): Promise<{ team_id?: string }>;
  };
  users: {
    info(args: { user: string }): Promise<{
      user?: {
        deleted?: boolean;
        is_email_confirmed?: boolean;
        is_restricted?: boolean;
        is_stranger?: boolean;
        is_ultra_restricted?: boolean;
        profile?: { email?: string };
        team_id?: string;
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
 *    any member of a different company's workspace entirely — whether or not they're in a
 *    shared channel with us (see the Slack Connect note below) — or an explicitly unconfirmed
 *    email (`is_email_confirmed === false` — checked only when the field is actually present;
 *    not every token populates it, and its absence must never itself be treated as a
 *    rejection). These aren't configurable policy — they're the exact structural gaps
 *    `SLACK_ALLOWED_USER_IDS` couldn't express (see issue #1241).
 * 2. **The configured allowlist**, once identity is verified: allowed if the email is in
 *    `allowedEmails`, or its domain is in `allowedEmailDomains`. If BOTH are unset, every
 *    identity-verified (non-guest, non-external) user is allowed — the same "unrestricted by
 *    default" posture `SLACK_ALLOWED_USER_IDS` had, minus the guest/external gap.
 *
 * **Slack Connect note:** `is_stranger` is true only when the looked-up user belongs to a
 * different workspace AND shares no channel visible to us — it is false (or absent) for an
 * external Slack Connect member who *does* share a channel with us, even though that person
 * is still not part of our company. Slack's API has no single "is external" field for this
 * (confirmed against the `users.info` docs — the documented way to tell is comparing
 * `team_id`), so this resolver additionally fetches its own installed workspace's team id
 * once via `auth.test` (cached for the resolver's lifetime) and rejects any user whose
 * `team_id` doesn't match, `is_stranger` notwithstanding.
 *
 * Fails closed: any `users.info`/`auth.test` error (network, missing scope, unknown user) is
 * logged and treated as not allowed — never falls back to trusting an unresolved identity.
 */
export class EmailAllowlistResolver implements AccessResolver {
  readonly #client: UserLookupClient;
  readonly #config: EmailAllowlistConfig;
  readonly #logger: MinimalLogger;
  #ownTeamId: Promise<string | undefined> | undefined;

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
    const ownTeamId = await this.#resolveOwnTeamId();
    if (ownTeamId === undefined) {
      this.#logger.warn(
        "own team id could not be resolved — cannot verify workspace membership, treating as not authorized",
      );
      return { allowed: false };
    }
    if (user.team_id === undefined) {
      this.#logger.warn(`users.info returned no team_id for ${ctx.slackUserId}`);
      return { allowed: false };
    }
    if (user.team_id !== ownTeamId) {
      // A Slack Connect member from another company, sharing a channel with us — is_stranger
      // is false for exactly this case, so it takes this separate check to reject them.
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

  /** Resolved via `auth.test` and cached ONLY on success — the bot's own installed workspace
   * never changes mid-process, so a real team id is cached for the resolver's lifetime. A
   * failure (network blip, transient Slack API error) or a response with no `team_id` clears
   * the cache back to `undefined` so the NEXT call retries; the CURRENT call still fails
   * closed (the caller treats an `undefined` return as "can't verify, deny"). Caching a
   * failure would conflate a transient hiccup with a permanent misconfiguration (like a
   * missing scope) and deny every user for the rest of the process's life over one bad
   * moment at startup. */
  #resolveOwnTeamId(): Promise<string | undefined> {
    if (this.#ownTeamId === undefined) {
      this.#ownTeamId = this.#client.auth.test().then(
        (response) => {
          if (response.team_id === undefined) {
            this.#logger.warn("auth.test returned no team_id");
            this.#ownTeamId = undefined;
            return undefined;
          }
          return response.team_id;
        },
        (error: unknown) => {
          this.#logger.warn("auth.test failed — cannot resolve own team id", error);
          this.#ownTeamId = undefined;
          return undefined;
        },
      );
    }
    return this.#ownTeamId;
  }
}
