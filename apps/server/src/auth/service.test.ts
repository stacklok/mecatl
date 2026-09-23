// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { createApp } from "../app.js";
import { bootstrap } from "../bootstrap.js";
import { ConfigurationError, studioConfigFromEnvironment } from "../config.js";
import type { RuntimeConfig } from "../mecatl/runtime.js";
import { fakeIssuer, resourceUrl } from "../testing/fake-issuer.js";
import {
  cookieHeader,
  csrfHeaders,
  fakeClient,
  fakeRuntime,
  memoryLogger,
} from "../testing/fakes.js";
import {
  AuthenticationError,
  type AuthenticationService,
  createAuthenticationService,
  maximumCookieValueBytes,
  validReturnTo,
} from "./service.js";

const external: RuntimeConfig = { baseUrl: resourceUrl, resourceUrl, source: "external" };
const secret = "0123456789abcdef0123456789abcdef";
const publicUrl = new URL("https://studio.example.com");

async function loggedInApp(issuerOptions: Parameters<typeof fakeIssuer>[0] = {}) {
  const issuer = fakeIssuer(issuerOptions);
  const { logger, records } = memoryLogger();
  const authentication = (await createAuthenticationService(external, {
    fetch: issuer.fetch,
    logger,
    publicUrl,
    sessionSecret: secret,
  })) as AuthenticationService;
  const app = createApp({ authentication, runtime: fakeRuntime(), security: { publicUrl } });

  const login = await app.request("https://studio.example.com/api/v1/auth/login");
  const transaction = login.headers.getSetCookie();
  const state = new URL(login.headers.get("location") ?? "").searchParams.get("state");
  const callback = await app.request(
    `https://studio.example.com/api/v1/auth/callback?code=code-1&state=${state}`,
    { headers: { Cookie: cookieHeader(transaction) } },
  );
  return { app, authentication, callback, issuer, records };
}

describe("authentication service", () => {
  it("a missing protected resource document disables interactive login", async () => {
    const issuer = fakeIssuer({ discoveryStatus: 404 });
    await expect(
      createAuthenticationService(external, { fetch: issuer.fetch, sessionSecret: secret }),
    ).resolves.toBeUndefined();
    // Spawned and static-token runtimes never even discover.
    await expect(
      createAuthenticationService({ mock: false, source: "local" }),
    ).resolves.toBeUndefined();
    await expect(
      createAuthenticationService({ ...external, authToken: "t" }),
    ).resolves.toBeUndefined();

    const app = createApp({ runtime: fakeRuntime({ authMode: "none" }) });
    const disabled = await app.request("/api/v1/auth/session");
    await expect(disabled.json()).resolves.toEqual({ mode: "none", status: "disabled" });
    const published = await createAuthenticationService(external, {
      fetch: fakeIssuer().fetch,
      sessionSecret: secret,
    });
    expect(published).toBeDefined();
  });

  it("a non-404 discovery failure is a startup error and MECATL_RESOURCE_URL overrides derivation", async () => {
    for (const discoveryStatus of [500, 503, 302, 401]) {
      await expect(
        createAuthenticationService(external, {
          fetch: fakeIssuer({ discoveryStatus }).fetch,
          sessionSecret: secret,
        }),
      ).rejects.toMatchObject({ code: "auth_discovery_failed" });
    }
    const refusing: typeof fetch = async () => {
      throw new TypeError("ECONNREFUSED");
    };
    await expect(
      createAuthenticationService(external, { fetch: refusing, sessionSecret: secret }),
    ).rejects.toMatchObject({ code: "auth_discovery_failed" });

    // The resource named by MECATL_RESOURCE_URL is what discovery fetches, byte-for-byte.
    const fetched: string[] = [];
    const recording: typeof fetch = async (input) => {
      fetched.push(new Request(input).url);
      return new Response("nope", { status: 404 });
    };
    await createAuthenticationService(
      {
        baseUrl: "http://mecated:50051",
        resourceUrl: "http://mecated:8080/tenant",
        source: "external",
      },
      { fetch: recording, sessionSecret: secret },
    );
    expect(fetched).toEqual(["http://mecated:8080/.well-known/oauth-protected-resource/tenant"]);

    // Through bootstrap, the failure is fatal before listening.
    await expect(
      bootstrap({
        environment: { MECATL_BASE_URL: resourceUrl },
        fetch: fakeIssuer({ discoveryStatus: 500 }).fetch,
        runtime: { createClient: async () => fakeClient() },
      }),
    ).rejects.toBeInstanceOf(AuthenticationError);
  });

  it("OIDC mode requires STUDIO_SESSION_SECRET of at least 32 bytes", async () => {
    expect(() => studioConfigFromEnvironment({ STUDIO_SESSION_SECRET: "short" })).toThrow(
      ConfigurationError,
    );
    expect(studioConfigFromEnvironment({ STUDIO_SESSION_SECRET: secret }).sessionSecret).toBe(
      secret,
    );
    // Raw bytes count, not characters: 32 two-byte characters pass.
    expect(
      studioConfigFromEnvironment({ STUDIO_SESSION_SECRET: "é".repeat(16) }).sessionSecret,
    ).toBe("é".repeat(16));

    await expect(
      createAuthenticationService(external, { fetch: fakeIssuer().fetch }),
    ).rejects.toMatchObject({ code: "session_secret_missing" });
    await expect(
      bootstrap({
        environment: { MECATL_BASE_URL: resourceUrl, STUDIO_PUBLIC_URL: "https://s.example" },
        fetch: fakeIssuer().fetch,
        runtime: { createClient: async () => fakeClient() },
      }),
    ).rejects.toMatchObject({ code: "session_secret_missing" });

    // Inactive login: no secret needed, one WARN says so.
    const { logger, records } = memoryLogger();
    const booted = await bootstrap({
      environment: { MECATL_BASE_URL: resourceUrl },
      fetch: fakeIssuer({ discoveryStatus: 404 }).fetch,
      logger,
      runtime: { createClient: async () => fakeClient() },
    });
    expect(records.filter((r) => r.event === "auth.session_secret_unused")).toHaveLength(1);
    await booted.runtime.close();
  });

  it("an expiring access token is refreshed once for concurrent requests and a failed refresh clears the session", async () => {
    const { app, callback, issuer } = await loggedInApp({ expiresIn: 10_000 });
    expect(callback.status).toBe(302);
    const session = cookieHeader(callback.headers.getSetCookie());
    expect(session).toContain("studio_access=");
    expect(session).toContain("studio_refresh=");

    // Five concurrent requests holding the same near-expiry session: one refresh grant.
    const responses = await Promise.all(
      Array.from({ length: 5 }, () =>
        app.request("https://studio.example.com/api/v1/runtime", { headers: { Cookie: session } }),
      ),
    );
    expect(responses.map((r) => r.status)).toEqual([200, 200, 200, 200, 200]);
    expect(issuer.refreshCalls()).toBe(1);
    const rotated = responses.flatMap((r) => r.headers.getSetCookie());
    expect(
      rotated.some((c) => c.startsWith("studio_access=") && !c.includes("studio_access=;")),
    ).toBe(true);
    expect(rotated.some((c) => c.startsWith("studio_refresh="))).toBe(true);

    // A refresh the issuer rejects ends the session: cookies cleared, 401 session_expired.
    const failing = await loggedInApp({ expiresIn: 10_000, refreshFails: true });
    const expired = await failing.app.request("https://studio.example.com/api/v1/runtime", {
      headers: { Cookie: cookieHeader(failing.callback.headers.getSetCookie()) },
    });
    expect(expired.status).toBe(401);
    await expect(expired.json()).resolves.toMatchObject({ code: "session_expired" });
    const cleared = expired.headers.getSetCookie();
    expect(cleared.some((c) => c.startsWith("studio_access=;") && c.includes("Max-Age=0"))).toBe(
      true,
    );
    expect(cleared.some((c) => c.startsWith("studio_refresh=;") && c.includes("Max-Age=0"))).toBe(
      true,
    );
  });

  it("a request still carrying a rotated refresh token reuses the new credential instead of ending the session", async () => {
    const { app, callback, issuer } = await loggedInApp({
      expiresIn: 10_000,
      rotateRefreshTokens: true,
    });
    const stale = cookieHeader(callback.headers.getSetCookie());
    const first = await app.request("https://studio.example.com/api/v1/runtime", {
      headers: { Cookie: stale },
    });
    expect(first.status).toBe(200);
    expect(issuer.refreshCalls()).toBe(1);

    // A request that left the browser before the rotated cookies arrived: the
    // issuer would refuse the spent token, so it must not be replayed.
    const late = await app.request("https://studio.example.com/api/v1/runtime", {
      headers: { Cookie: stale },
    });
    expect(late.status).toBe(200);
    expect(issuer.refreshCalls()).toBe(1);
    expect(late.headers.getSetCookie().some((c) => c.includes("Max-Age=0"))).toBe(false);
  });

  it("a sealed cookie above 3800 bytes fails login with session_too_large", async () => {
    const { callback, records } = await loggedInApp({ accessTokenLength: maximumCookieValueBytes });
    expect(callback.status).toBe(500);
    await expect(callback.json()).resolves.toMatchObject({ code: "session_too_large" });
    const set = callback.headers.getSetCookie();
    expect(set.some((c) => c.startsWith("studio_access=") && !c.includes("Max-Age=0"))).toBe(false);
    expect(set.some((c) => c.startsWith("studio_refresh=") && !c.includes("Max-Age=0"))).toBe(
      false,
    );
    expect(records.some((r) => r.audit && r.event === "auth.login.fail")).toBe(true);
  });

  it("login lifecycle emits audit records without token material", async () => {
    const { app, callback, issuer, records } = await loggedInApp();
    expect(callback.status).toBe(302);
    const session = cookieHeader(callback.headers.getSetCookie());

    // A forged callback fails, then the real session logs out.
    await app.request("https://studio.example.com/api/v1/auth/callback?code=x&state=forged");
    const logout = await app.request("https://studio.example.com/api/v1/auth/logout", {
      headers: csrfHeaders("t", { Cookie: `${session}; studio_csrf=t` }),
      method: "POST",
    });
    expect(logout.status).toBe(204);
    expect(issuer.revokedTokens().length).toBeGreaterThan(0);

    const audits = records.filter((r) => r.audit === true).map((r) => r.event);
    expect(audits).toEqual(
      expect.arrayContaining([
        "auth.login.start",
        "auth.login.complete",
        "auth.login.fail",
        "auth.logout",
      ]),
    );
    const serialised = JSON.stringify(records);
    expect(serialised).not.toMatch(/access-\d+-/u);
    expect(serialised).not.toMatch(/refresh-\d+/u);
    expect(serialised).not.toContain("code-1");
    for (const record of records.filter((r) => r.audit === true)) {
      expect(record.fields).toHaveProperty("requestId");
      expect(record.fields).toHaveProperty("clientAddress");
    }
  });

  it("accepts return_to only as a same-origin absolute path", () => {
    for (const accepted of ["/", "/workspace", "/workspace/x?tab=1#top"]) {
      expect(validReturnTo(accepted)).toBe(true);
    }
    for (const rejected of [
      undefined,
      "",
      "workspace",
      "https://evil.example/",
      "//evil.example/",
      "/\\evil.example/",
      "/\tevil",
      "/work\nspace",
      "/work\u007fspace",
      `/${"a".repeat(2_048)}`,
    ]) {
      expect(validReturnTo(rejected)).toBe(false);
    }
  });
});
