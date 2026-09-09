import { describe, expect, it, vi } from "vitest";

import {
  EmailAllowlistResolver,
  type MinimalLogger,
  type UserLookupClient,
} from "../src/access.js";

type FakeUser = NonNullable<Awaited<ReturnType<UserLookupClient["users"]["info"]>>["user"]>;

function fakeClient(usersInfo: (userId: string) => Promise<{ user?: FakeUser }>): UserLookupClient {
  return { users: { info: ({ user }) => usersInfo(user) } };
}

function fakeLogger(): MinimalLogger & { warn: ReturnType<typeof vi.fn> } {
  return { warn: vi.fn<MinimalLogger["warn"]>() };
}

const VERIFIED_USER: FakeUser = { profile: { email: "person@example.com" } };

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
      fakeClient(async () => ({ user: { profile: { email: "Person@Example.com" } } })),
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
      fakeClient(async () => ({ user: { profile: { email: "anyone@Example.com" } } })),
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

  it("fails closed when users.info returns no email (missing scope)", async () => {
    const logger = fakeLogger();
    const resolver = new EmailAllowlistResolver(
      fakeClient(async () => ({ user: { profile: {} } })),
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
