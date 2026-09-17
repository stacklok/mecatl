import { describe, expect, it, vi } from "vitest";
import {
  type BearerOutcome,
  claimsFromIdToken,
  isInvalidGrantResponse,
  OidcTokenStore,
  type PersistedTokens,
  REFRESH_AHEAD_MS,
  type RefreshResult,
  type StoredTokens,
  type TokenPersistence,
  tokenDecision,
  tokenExpiryMs,
  tokensFromRefreshResponse,
} from "./token-store";

const NOW = 1_700_000_000_000;

function tokens(overrides: Partial<StoredTokens> = {}): StoredTokens {
  return {
    accessToken: "access-1",
    refreshToken: "refresh-1",
    expiresAt: NOW + 3_600_000,
    claims: { sub: "user-1", email: "op@example.com" },
    ...overrides,
  };
}

describe("tokenDecision", () => {
  it("uses a token outside the refresh-ahead window", () => {
    expect(tokenDecision(tokens(), NOW)).toBe("use");
  });

  it("refreshes inside the 30-second refresh-ahead window", () => {
    expect(
      tokenDecision(tokens({ expiresAt: NOW + REFRESH_AHEAD_MS - 1 }), NOW),
    ).toBe("refresh");
    expect(tokenDecision(tokens({ expiresAt: NOW - 1 }), NOW)).toBe("refresh");
  });

  it("requires login when there is nothing to refresh with", () => {
    expect(tokenDecision(null, NOW)).toBe("login-required");
    expect(
      tokenDecision(tokens({ expiresAt: NOW - 1, refreshToken: "" }), NOW),
    ).toBe("login-required");
  });
});

describe("isInvalidGrantResponse", () => {
  it("classifies only the exact structured error code (ADR 0277)", () => {
    expect(isInvalidGrantResponse(400, { error: "invalid_grant" })).toBe(true);
    expect(
      isInvalidGrantResponse(400, {
        error: "server_error",
        // Prose never classifies — the clientauth discipline.
        error_description: "the grant was invalid_grant maybe",
      }),
    ).toBe(false);
    expect(isInvalidGrantResponse(400, { error: "invalid_grant " })).toBe(
      false,
    );
    expect(isInvalidGrantResponse(200, { error: "invalid_grant" })).toBe(false);
    expect(isInvalidGrantResponse(400, "invalid_grant")).toBe(false);
    expect(isInvalidGrantResponse(400, null)).toBe(false);
  });
});

describe("tokenExpiryMs", () => {
  it("defaults to 300s and floors at 60s", () => {
    expect(tokenExpiryMs(undefined, NOW)).toBe(NOW + 300_000);
    expect(tokenExpiryMs("garbage", NOW)).toBe(NOW + 300_000);
    expect(tokenExpiryMs(5, NOW)).toBe(NOW + 60_000);
    expect(tokenExpiryMs(900, NOW)).toBe(NOW + 900_000);
  });
});

describe("claimsFromIdToken", () => {
  const payload = (claims: object) =>
    `h.${Buffer.from(JSON.stringify(claims)).toString("base64url")}.s`;

  it("parses display claims", () => {
    expect(
      claimsFromIdToken(
        payload({ sub: "u1", email: "a@b.c", preferred_username: "ada" }),
      ),
    ).toEqual({ sub: "u1", email: "a@b.c", preferredUsername: "ada" });
  });

  it("returns null on garbage", () => {
    expect(claimsFromIdToken("not-a-jwt")).toBeNull();
    expect(claimsFromIdToken("a.%%%.c")).toBeNull();
  });
});

describe("tokensFromRefreshResponse", () => {
  it("adopts a rotated refresh token", () => {
    const next = tokensFromRefreshResponse(
      { access_token: "access-2", refresh_token: "refresh-2", expires_in: 120 },
      tokens(),
      NOW,
    );
    expect(next).toMatchObject({
      accessToken: "access-2",
      refreshToken: "refresh-2",
      expiresAt: NOW + 120_000,
    });
  });

  it("keeps the prior refresh token and claims when the provider omits them", () => {
    const next = tokensFromRefreshResponse(
      { access_token: "access-2" },
      tokens(),
      NOW,
    );
    expect(next?.refreshToken).toBe("refresh-1");
    expect(next?.claims).toEqual(tokens().claims);
  });

  it("refuses a response without an access token", () => {
    expect(tokensFromRefreshResponse({}, tokens(), NOW)).toBeNull();
  });
});

describe("OidcTokenStore.bearer", () => {
  const store = (initial: StoredTokens | null, now = () => NOW) => {
    const s = new OidcTokenStore(now);
    if (initial) s.setTokens(initial);
    return s;
  };

  it("serves a valid token without touching the provider", async () => {
    let exchanges = 0;
    const s = store(tokens());
    const outcome = await s.bearer(async () => {
      exchanges++;
      return { kind: "transient" };
    });
    expect(outcome).toEqual({ kind: "bearer", token: "access-1" });
    expect(exchanges).toBe(0);
  });

  it("refreshes inside the window and stores the rotation", async () => {
    const s = store(tokens({ expiresAt: NOW + 10_000 }));
    const outcome = await s.bearer(async (prior) => ({
      kind: "refreshed",
      tokens: { ...prior, accessToken: "access-2", refreshToken: "refresh-2" },
    }));
    expect(outcome).toEqual({ kind: "bearer", token: "access-2" });
    expect(s.current()?.refreshToken).toBe("refresh-2");
  });

  it("drops the credential on invalid_grant and reports the expiry honestly", async () => {
    const s = store(tokens({ expiresAt: NOW + 10_000 }));
    const outcome = await s.bearer(async () => ({ kind: "invalid-grant" }));
    expect(outcome).toEqual({ kind: "login-required", reason: "expired" });
    expect(s.current()).toBeNull();
    // The reason is sticky for the NEXT request too — the 401 copy says
    // "session expired", not "not signed in".
    const next = await s.bearer(async () => ({ kind: "transient" }));
    expect(next).toEqual({ kind: "login-required", reason: "expired" });
    expect(s.snapshot()).toEqual({ state: "expired" });
  });

  it("keeps a still-valid token through a transient refresh failure", async () => {
    const s = store(tokens({ expiresAt: NOW + 10_000 }));
    const outcome = await s.bearer(async () => ({ kind: "transient" }));
    expect(outcome).toEqual({ kind: "bearer", token: "access-1" });
    expect(s.current()).not.toBeNull();
  });

  it("reports refresh-failed (retaining tokens) when transient and already expired", async () => {
    const s = store(tokens({ expiresAt: NOW - 1_000 }));
    const outcome = await s.bearer(async () => ({ kind: "transient" }));
    expect(outcome).toEqual({ kind: "refresh-failed" });
    expect(s.current()).not.toBeNull(); // a later demand retries
  });

  it("treats a thrown exchange as transient", async () => {
    const s = store(tokens({ expiresAt: NOW + 10_000 }));
    const outcome = await s.bearer(async () => {
      throw new Error("network down");
    });
    expect(outcome).toEqual({ kind: "bearer", token: "access-1" });
  });

  it("single-flights concurrent refreshes so a one-time refresh token is never raced", async () => {
    const s = store(tokens({ expiresAt: NOW + 10_000 }));
    let exchanges = 0;
    let release: (result: RefreshResult) => void = () => {};
    const gate = new Promise<RefreshResult>((resolve) => {
      release = resolve;
    });
    const exchange = async (prior: StoredTokens) => {
      exchanges++;
      const result = await gate;
      return result.kind === "refreshed"
        ? {
            kind: "refreshed" as const,
            tokens: { ...prior, accessToken: "access-2" },
          }
        : result;
    };
    const first = s.bearer(exchange);
    const second = s.bearer(exchange);
    release({ kind: "refreshed", tokens: tokens() });
    const outcomes: BearerOutcome[] = await Promise.all([first, second]);
    expect(exchanges).toBe(1);
    for (const outcome of outcomes) {
      expect(outcome).toEqual({ kind: "bearer", token: "access-2" });
    }
  });

  it("requires login when signed out, and expires an unrefreshable token", async () => {
    const fresh = store(null);
    expect(await fresh.bearer(async () => ({ kind: "transient" }))).toEqual({
      kind: "login-required",
      reason: "signed-out",
    });
    const unrefreshable = store(
      tokens({ expiresAt: NOW - 1, refreshToken: "" }),
    );
    expect(
      await unrefreshable.bearer(async () => ({ kind: "transient" })),
    ).toEqual({ kind: "login-required", reason: "expired" });
    expect(unrefreshable.snapshot()).toEqual({ state: "expired" });
  });
});

describe("OidcTokenStore persistence + binding", () => {
  const memoryPersistence = (initial: PersistedTokens | null = null) => {
    let saved = initial;
    const persistence: TokenPersistence = {
      load: vi.fn(() => saved),
      save: vi.fn((next: PersistedTokens) => {
        saved = next;
      }),
      clear: vi.fn(() => {
        saved = null;
      }),
    };
    return { persistence, current: () => saved };
  };

  it("adopts the mirrored credential and its binding at construction", () => {
    const { persistence } = memoryPersistence({
      tokens: tokens(),
      binding: "p1",
    });
    const s = new OidcTokenStore(() => NOW, persistence);
    expect(s.current()).toEqual(tokens());
    expect(s.binding()).toBe("p1");
    expect(persistence.load).toHaveBeenCalledTimes(1);
  });

  it("mirrors every set (keeping the binding on a refresh) and clear", async () => {
    const { persistence, current } = memoryPersistence();
    const s = new OidcTokenStore(() => NOW, persistence);
    s.setTokens(tokens({ expiresAt: NOW + 10_000 }), "p1");
    expect(current()).toEqual({
      tokens: tokens({ expiresAt: NOW + 10_000 }),
      binding: "p1",
    });

    // A refresh re-sets the tokens WITHOUT naming the binding: it survives.
    await s.bearer(async (prior) => ({
      kind: "refreshed",
      tokens: { ...prior, accessToken: "access-2" },
    }));
    expect(current()?.binding).toBe("p1");
    expect(current()?.tokens.accessToken).toBe("access-2");

    s.clear("expired");
    expect(current()).toBeNull();
    expect(s.binding()).toBe("");
    expect(persistence.clear).toHaveBeenCalledTimes(1);
  });

  it("is unbound ('') without a binding and never reports one while signed out", () => {
    const s = new OidcTokenStore(() => NOW);
    expect(s.binding()).toBe("");
    s.setTokens(tokens());
    expect(s.binding()).toBe("");
    s.setTokens(tokens(), "p2");
    expect(s.binding()).toBe("p2");
    s.clear();
    expect(s.binding()).toBe("");
  });

  it("survives a throwing persistence layer", () => {
    const broken: TokenPersistence = {
      load: () => {
        throw new Error("disk");
      },
      save: () => {
        throw new Error("disk");
      },
      clear: () => {
        throw new Error("disk");
      },
    };
    const s = new OidcTokenStore(() => NOW, broken);
    expect(() => s.setTokens(tokens(), "p")).not.toThrow();
    expect(s.current()).toEqual(tokens());
    expect(() => s.clear()).not.toThrow();
    expect(s.current()).toBeNull();
  });
});
