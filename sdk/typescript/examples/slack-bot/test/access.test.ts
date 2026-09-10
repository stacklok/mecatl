import { describe, expect, it, vi } from "vitest";

import {
  EmailAllowlistResolver,
  type MinimalLogger,
  type UserLookupClient,
} from "../src/access.js";

type FakeUser = NonNullable<Awaited<ReturnType<UserLookupClient["users"]["info"]>>["user"]>;

const OWN_TEAM_ID = "T_OWN";

function fakeClient(
  usersInfo: (userId: string) => Promise<{ user?: FakeUser }>,
  authTest: () => Promise<{ team_id?: string }> = async () => ({ team_id: OWN_TEAM_ID }),
): UserLookupClient {
  return { auth: { test: authTest }, users: { info: ({ user }) => usersInfo(user) } };
}

function fakeLogger(): MinimalLogger & { warn: ReturnType<typeof vi.fn> } {
  return { warn: vi.fn<MinimalLogger["warn"]>() };
}

const VERIFIED_USER: FakeUser = { profile: { email: "person@example.com" }, team_id: OWN_TEAM_ID };

describe("EmailAllowlistResolver", () => {
  it("allows a verified, non-guest user when no allowlist is configured", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: VERIFIED_USER })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: true,
      principal: "person@example.com",
    });
  });

  it("rejects a multi-channel guest even with no allowlist configured", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { ...VERIFIED_USER, is_restricted: true } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("rejects a single-channel guest even with no allowlist configured", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { ...VERIFIED_USER, is_ultra_restricted: true } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("rejects a deactivated account even with no allowlist configured", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { ...VERIFIED_USER, deleted: true } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("rejects a Slack Connect stranger even with no allowlist configured", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { ...VERIFIED_USER, is_stranger: true } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("rejects a Slack Connect external member who shares a channel — is_stranger is false for them", async () => {
    // The exact gap is_stranger alone misses: a member of a different company's workspace,
    // visible to us because they're in a shared channel, so Slack does NOT set is_stranger.
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({
        user: { ...VERIFIED_USER, is_stranger: false, team_id: "T_OTHER_COMPANY" },
      })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("rejects an explicitly unconfirmed email even with no allowlist configured", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { ...VERIFIED_USER, is_email_confirmed: false } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("does not reject when is_email_confirmed is simply absent", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: VERIFIED_USER })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: true,
      principal: "person@example.com",
    });
  });

  it("allows an exact email match against the configured allowlist", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({
        user: { profile: { email: "Person@Example.com" }, team_id: OWN_TEAM_ID },
      })),
      { allowedEmailDomains: undefined, allowedEmails: new Set(["person@example.com"]) },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: true,
      principal: "person@example.com",
    });
  });

  it("rejects an email that matches neither the allowlist nor a domain", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: VERIFIED_USER })),
      {
        allowedEmailDomains: new Set(["other.com"]),
        allowedEmails: new Set(["nobody@example.com"]),
      },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("allows any email whose domain matches an allowed domain", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({
        user: { profile: { email: "anyone@Example.com" }, team_id: OWN_TEAM_ID },
      })),
      { allowedEmailDomains: new Set(["example.com"]), allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: true,
      principal: "anyone@example.com",
    });
  });

  it("still rejects a guest from an allowed domain — identity-integrity checks run first", async () => {
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { ...VERIFIED_USER, is_restricted: true } })),
      { allowedEmailDomains: new Set(["example.com"]), allowedEmails: undefined },
      fakeLogger(),
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
  });

  it("fails closed when users.info returns no user", async () => {
    const logger = fakeLogger();
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({})),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      logger,
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
    expect(logger.warn).toHaveBeenCalled();
  });

  it("fails closed when users.info returns no team_id", async () => {
    const logger = fakeLogger();
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { profile: { email: "person@example.com" } } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      logger,
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
    expect(logger.warn).toHaveBeenCalled();
  });

  it("fails closed when auth.test fails, even for an otherwise-verified user", async () => {
    const logger = fakeLogger();
    const resolver = new EmailAllowlistResolver(
      fakeClient(
        async () => ({ user: VERIFIED_USER }),
        async () => {
          throw new Error("auth.test failed");
        },
      ),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      logger,
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
    expect(logger.warn).toHaveBeenCalled();
  });

  it("fails closed when users.info returns no email (missing scope)", async () => {
    const logger = fakeLogger();
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { profile: {}, team_id: OWN_TEAM_ID } })),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      logger,
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
    expect(logger.warn).toHaveBeenCalled();
  });

  it("fails closed when users.info throws", async () => {
    const logger = fakeLogger();
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => {
        throw new Error("network error");
      }),
      { allowedEmailDomains: undefined, allowedEmails: undefined },
      logger,
    );

    await expect(resolver.resolve({ slackUserId: "U1" })).resolves.toStrictEqual({
      allowed: false,
    });
    expect(logger.warn).toHaveBeenCalled();
  });
});
