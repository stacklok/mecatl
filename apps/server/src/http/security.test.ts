// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { createApp } from "../app.js";
import type { AuthenticationService } from "../auth/service.js";
import { csrfHeaders, fakeRuntime } from "../testing/fakes.js";

function anonymousAuthentication(): AuthenticationService {
  return {
    clear: () => undefined,
    completeLogin: async () => {
      throw new Error("unused");
    },
    credential: async () => ({ status: "anonymous" }),
    logout: async () => undefined,
    noteLoginComplete: () => undefined,
    noteLoginFailure: () => undefined,
    save: async () => undefined,
    signInRequired: async () => true,
    startLogin: async () => "https://issuer.example.com/authorize",
  };
}

function peer(address: string) {
  return { incoming: { socket: { remoteAddress: address } } };
}

describe("security middleware", () => {
  it("responses carry security headers and a content security policy and no CORS headers", async () => {
    const app = createApp({ runtime: fakeRuntime() });
    for (const path of ["/api/health", "/api/v1/runtime", "/api/nope"]) {
      const response = await app.request(path, { headers: { Origin: "https://evil.example" } });
      const csp = response.headers.get("content-security-policy") ?? "";
      expect(csp).toContain("default-src 'self'");
      expect(csp).toContain("script-src 'self'");
      expect(csp).toContain("frame-ancestors 'none'");
      expect(csp).not.toContain("unsafe-eval");
      expect(response.headers.get("x-content-type-options")).toBe("nosniff");
      expect(response.headers.get("x-frame-options")).toBe("SAMEORIGIN");
      expect(response.headers.get("referrer-policy")).toBe("same-origin");
      expect(response.headers.get("access-control-allow-origin")).toBeNull();
      expect(response.headers.get("access-control-allow-credentials")).toBeNull();
    }
    // Safe API requests issue the double-submit cookie once.
    const first = await app.request("/api/v1/auth/session");
    const issued = first.headers.getSetCookie().find((c) => c.startsWith("studio_csrf="));
    expect(issued).toBeDefined();
    expect(issued).not.toContain("HttpOnly");
    const token = issued?.split(";")[0]?.split("=")[1] ?? "";
    const second = await app.request("/api/v1/auth/session", {
      headers: { Cookie: `studio_csrf=${token}` },
    });
    expect(second.headers.getSetCookie().some((c) => c.startsWith("studio_csrf="))).toBe(false);
  });

  it("cross-site or token-less mutations under /api/v1 are rejected with 403", async () => {
    const publicUrl = new URL("https://studio.example.com");
    const app = createApp({ runtime: fakeRuntime(), security: { publicUrl } });
    const post = (headers: Record<string, string>) =>
      app.request("https://studio.example.com/api/v1/auth/logout", { headers, method: "POST" });

    const crossSite = await post({ ...csrfHeaders(), "Sec-Fetch-Site": "cross-site" });
    expect(crossSite.status).toBe(403);
    await expect(crossSite.json()).resolves.toMatchObject({ code: "cross_site_request" });

    const foreignOrigin = await post({
      Cookie: "studio_csrf=t",
      Origin: "https://evil.example",
      "X-Studio-CSRF": "t",
    });
    expect(foreignOrigin.status).toBe(403);

    const noHeaders = await post({});
    expect(noHeaders.status).toBe(403);

    // Fetch Metadata never overrides a foreign Origin, and a same-origin Origin
    // never overrides a cross-site Fetch Metadata value.
    const rebound = await post({ ...csrfHeaders(), Origin: "http://attacker.example:3100" });
    expect(rebound.status).toBe(403);
    const mixed = await post({
      ...csrfHeaders(),
      Origin: "https://studio.example.com",
      "Sec-Fetch-Site": "same-site",
    });
    expect(mixed.status).toBe(403);

    const missingToken = await post({ Cookie: "studio_csrf=t", "Sec-Fetch-Site": "same-origin" });
    expect(missingToken.status).toBe(403);

    const mismatch = await post({
      Cookie: "studio_csrf=t",
      "Sec-Fetch-Site": "same-origin",
      "X-Studio-CSRF": "other",
    });
    expect(mismatch.status).toBe(403);

    // Same-origin by Fetch Metadata, or by Origin matching STUDIO_PUBLIC_URL, with the token: allowed.
    expect((await post(csrfHeaders())).status).toBe(204);
    expect(
      (
        await post({
          Cookie: "studio_csrf=t",
          Origin: "https://studio.example.com",
          "X-Studio-CSRF": "t",
        })
      ).status,
    ).toBe(204);
    // Safe methods are untouched.
    expect((await app.request("https://studio.example.com/api/v1/runtime")).status).toBe(200);
  });

  it("auth routes are rate limited per client with 429 and Retry-After", async () => {
    let now = 1_000_000;
    const app = createApp({
      runtime: fakeRuntime(),
      security: { now: () => now, rateLimit: { max: 3, windowMs: 60_000 } },
    });
    const hit = (address: string, path = "/api/v1/auth/login") =>
      app.request(path, {}, peer(address));

    for (let index = 0; index < 3; index += 1) expect((await hit("10.0.0.1")).status).not.toBe(429);
    const limited = await hit("10.0.0.1");
    expect(limited.status).toBe(429);
    expect(Number(limited.headers.get("retry-after"))).toBeGreaterThan(0);
    await expect(limited.json()).resolves.toMatchObject({ code: "rate_limited" });
    // The dev callback alias shares the auth budget.
    expect((await hit("10.0.0.1", "/oauth/callback")).status).toBe(429);

    // Another client has its own budget; the window resets with time.
    expect((await hit("10.0.0.2")).status).not.toBe(429);
    now += 60_001;
    expect((await hit("10.0.0.1")).status).not.toBe(429);

    // Health and the session poll the SPA makes on every load are never limited.
    for (let index = 0; index < 10; index += 1) {
      expect((await app.request("/api/health", {}, peer("10.0.0.3"))).status).toBe(200);
      expect((await hit("10.0.0.3", "/api/v1/auth/session")).status).toBe(200);
    }
  });

  it("requests whose Host is not the public or a loopback host are refused with 421", async () => {
    const local = createApp({ runtime: fakeRuntime() });
    for (const host of ["localhost:3100", "127.0.0.1:18473", "[::1]:3100", "127.0.0.2"]) {
      const response = await local.request("/api/v1/runtime", { headers: { Host: host } });
      expect(response.status).toBe(200);
    }
    for (const host of [
      "attacker.example:3100",
      "10.0.0.5:3100",
      "localhost.attacker.example",
      "",
    ]) {
      const response = await local.request("/api/v1/runtime", { headers: { Host: host } });
      expect(response.status).toBe(421);
      await expect(response.json()).resolves.toMatchObject({ code: "host_not_allowed" });
    }
    // A rebound mutation is refused before the CSRF check could compare origins.
    const rebound = await local.request("/api/v1/auth/logout", {
      headers: {
        ...csrfHeaders(),
        Host: "attacker.example:3100",
        Origin: "http://attacker.example:3100",
      },
      method: "POST",
    });
    expect(rebound.status).toBe(421);

    const publicUrl = new URL("https://studio.example.com");
    const hosted = createApp({ runtime: fakeRuntime(), security: { publicUrl } });
    for (const host of ["studio.example.com", "STUDIO.example.com", "studio.example.com:443"]) {
      const response = await hosted.request("/api/v1/runtime", { headers: { Host: host } });
      expect(response.status).toBe(200);
    }
    for (const host of [
      "localhost:3100",
      "studio.example.com:8443",
      "studio.example.com.attacker.example",
    ]) {
      const response = await hosted.request("/api/v1/runtime", { headers: { Host: host } });
      expect(response.status).toBe(421);
    }
    // Probes address the pod by IP, so health answers on any Host.
    expect(
      (await hosted.request("/api/health", { headers: { Host: "10.0.0.5:3100" } })).status,
    ).toBe(200);
  });

  it("API requests without a valid session are rejected with 401 when interactive login is active", async () => {
    const app = createApp({ authentication: anonymousAuthentication(), runtime: fakeRuntime() });
    const runtime = await app.request("/api/v1/runtime");
    expect(runtime.status).toBe(401);
    await expect(runtime.json()).resolves.toMatchObject({ code: "unauthenticated", status: 401 });

    // Auth routes and health stay reachable.
    expect((await app.request("/api/v1/auth/session")).status).toBe(200);
    expect((await app.request("/api/health")).status).toBe(200);

    const expiring: AuthenticationService = {
      ...anonymousAuthentication(),
      credential: async () => ({ status: "expired" }),
    };
    const expired = await createApp({ authentication: expiring, runtime: fakeRuntime() }).request(
      "/api/v1/runtime",
    );
    expect(expired.status).toBe(401);
    await expect(expired.json()).resolves.toMatchObject({ code: "session_expired" });

    // Without interactive login nothing is gated.
    expect((await createApp({ runtime: fakeRuntime() }).request("/api/v1/runtime")).status).toBe(
      200,
    );
  });

  it("the client address honours STUDIO_TRUSTED_PROXY_HOPS", async () => {
    const limited = (hops: number) =>
      createApp({
        runtime: fakeRuntime(),
        security: { rateLimit: { max: 1, windowMs: 60_000 }, trustedProxyHops: hops },
      });
    const forwarded = { "X-Forwarded-For": "203.0.113.9, 198.51.100.4" };

    // Zero hops: the socket peer is the client; the header is ignored, so two
    // forwarded clients behind one proxy share the proxy's budget.
    const zero = limited(0);
    expect(
      (await zero.request("/api/v1/auth/login", { headers: forwarded }, peer("10.0.0.1"))).status,
    ).not.toBe(429);
    expect(
      (
        await zero.request(
          "/api/v1/auth/login",
          { headers: { "X-Forwarded-For": "203.0.113.10" } },
          peer("10.0.0.1"),
        )
      ).status,
    ).toBe(429);

    // One hop: the right-most forwarded entry is the client.
    const one = limited(1);
    expect(
      (await one.request("/api/v1/auth/login", { headers: forwarded }, peer("10.0.0.1"))).status,
    ).not.toBe(429);
    expect(
      (
        await one.request(
          "/api/v1/auth/login",
          { headers: { "X-Forwarded-For": "203.0.113.9, 198.51.100.5" } },
          peer("10.0.0.1"),
        )
      ).status,
    ).not.toBe(429);
    expect(
      (await one.request("/api/v1/auth/login", { headers: forwarded }, peer("10.0.0.1"))).status,
    ).toBe(429);

    // Two hops: the second right-most entry; a header shorter than the hop count falls back to the peer.
    const two = limited(2);
    expect(
      (await two.request("/api/v1/auth/login", { headers: forwarded }, peer("10.0.0.1"))).status,
    ).not.toBe(429);
    expect(
      (await two.request("/api/v1/auth/login", { headers: forwarded }, peer("10.0.0.2"))).status,
    ).toBe(429);
    expect(
      (
        await two.request(
          "/api/v1/auth/login",
          { headers: { "X-Forwarded-For": "198.51.100.4" } },
          peer("10.0.0.3"),
        )
      ).status,
    ).not.toBe(429);
  });
});
