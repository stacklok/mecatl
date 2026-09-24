// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from "vitest";
import { createApp } from "../app.js";
import type { AuthenticationService, CredentialResolution } from "../auth/service.js";
import { fakeClient, fakeRuntime } from "../testing/fakes.js";

function authentication(status: CredentialResolution["status"]): AuthenticationService {
  return {
    clear: () => undefined,
    completeLogin: async () => {
      throw new Error("unused");
    },
    credential: async () => {
      throw new Error("public status must not refresh or probe a session");
    },
    logout: async () => undefined,
    noteLoginComplete: () => undefined,
    noteLoginFailure: () => undefined,
    save: async () => undefined,
    signInRequired: async () => status !== "authenticated",
    startLogin: async () => "https://issuer.example.com/authorize",
  };
}

function peer(address: string) {
  return { incoming: { socket: { remoteAddress: address } } };
}

describe("public status route", () => {
  it("anonymous status exposes only coarse connection and sign-in facts", async () => {
    const publicUrl = new URL("https://studio.example.com");
    const app = createApp({
      authentication: authentication("anonymous"),
      runtime: fakeRuntime({ authMode: "oidc" }),
      security: { publicUrl },
    });

    const response = await app.request("https://studio.example.com/api/v1/status", {
      headers: { Origin: "https://another.example" },
    });
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("application/json");
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("content-security-policy")).toContain("script-src 'self'");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("access-control-allow-origin")).toBeNull();
    expect(response.headers.get("access-control-allow-credentials")).toBeNull();
    expect(await response.json()).toEqual({ connection: "reachable", signInRequired: true });

    const rejectedHost = await app.request("https://studio.example.com/api/v1/status", {
      headers: { Host: "another.example" },
    });
    expect(rejectedHost.status).toBe(421);
    await expect(rejectedHost.json()).resolves.toMatchObject({ code: "host_not_allowed" });

    // An unavailable daemon still yields a successful BFF response; a failed
    // fetch has no 200 response for the browser to confuse with this value.
    const detached = await createApp().request("/api/v1/status");
    expect(detached.status).toBe(200);
    await expect(detached.json()).resolves.toEqual({
      connection: "unavailable",
      signInRequired: false,
    });

    for (const status of ["expired", "authenticated"] as const) {
      const result = await createApp({
        authentication: authentication(status),
        runtime: fakeRuntime({ authMode: "oidc" }),
      }).request("/api/v1/status");
      expect(result.status).toBe(200);
      await expect(result.json()).resolves.toEqual({
        connection: "reachable",
        signInRequired: status === "expired",
      });
    }

    for (const mode of ["static", "none"] as const) {
      const result = await createApp({ runtime: fakeRuntime({ authMode: mode }) }).request(
        "/api/v1/status",
      );
      expect(result.status).toBe(200);
      await expect(result.json()).resolves.toEqual({
        connection: "reachable",
        signInRequired: false,
      });
    }
  });

  it("public status has its own bounded rate budget", async () => {
    let now = 1_000_000;
    const app = createApp({
      authentication: authentication("anonymous"),
      runtime: fakeRuntime({ authMode: "oidc" }),
      security: { now: () => now, rateLimit: { max: 1, windowMs: 60_000 } },
    });
    const hit = (address: string, path = "/api/v1/status") => app.request(path, {}, peer(address));

    for (let index = 0; index < 120; index += 1) {
      expect((await hit("10.0.0.1")).status).toBe(200);
    }
    const limited = await hit("10.0.0.1");
    expect(limited.status).toBe(429);
    expect(limited.headers.get("cache-control")).toBe("private, no-store");
    expect(Number(limited.headers.get("retry-after"))).toBeGreaterThan(0);
    await expect(limited.json()).resolves.toMatchObject({ code: "rate_limited", status: 429 });

    // Public polling has no effect on the separately configured login budget.
    expect((await hit("10.0.0.1", "/api/v1/auth/login")).status).toBe(302);
    expect((await hit("10.0.0.1", "/api/v1/auth/login")).status).toBe(429);
    expect((await hit("10.0.0.2")).status).toBe(200);
    now += 60_001;
    expect((await hit("10.0.0.1")).status).toBe(200);
  });

  it("status separates transport outage from authentication rejection", async () => {
    const client = fakeClient();
    const snapshot = vi.fn(() => {
      throw new Error("public status must not read the compatibility snapshot");
    });
    const ready = vi.fn(async () => {
      throw new Error("public status must not negotiate compatibility");
    });
    const verifyCredential = vi.fn(async () => {
      throw new Error("public status must not probe with a credential");
    });
    const app = createApp({
      authentication: authentication("anonymous"),
      runtime: fakeRuntime({
        authMode: "oidc",
        client,
        ready,
        runWithCredential: async () => {
          throw new Error("public status must not enter the SDK credential context");
        },
        snapshot,
        verifyCredential,
      }),
    });

    for (const [observed, connection] of [
      ["connecting", "checking"],
      ["online", "reachable"],
      ["unauthorized", "reachable"],
      ["incompatible", "reachable"],
      ["reconnecting", "unavailable"],
      ["offline", "unavailable"],
    ] as const) {
      client.emit(observed);
      const response = await app.request("/api/v1/status");
      expect(response.status).toBe(200);
      await expect(response.json()).resolves.toEqual({ connection, signInRequired: true });
    }

    expect(snapshot).not.toHaveBeenCalled();
    expect(ready).not.toHaveBeenCalled();
    expect(verifyCredential).not.toHaveBeenCalled();
    expect(client.compatibility).not.toHaveBeenCalled();

    client.emit("offline");
    const signedIn = await createApp({
      authentication: authentication("authenticated"),
      runtime: fakeRuntime({ authMode: "oidc", client }),
    }).request("/api/v1/status");
    expect(signedIn.status).toBe(200);
    await expect(signedIn.json()).resolves.toEqual({
      connection: "unavailable",
      signInRequired: false,
    });

    const detached = await createApp({ authentication: authentication("anonymous") }).request(
      "/api/v1/status",
    );
    expect(detached.status).toBe(200);
    await expect(detached.json()).resolves.toEqual({
      connection: "unavailable",
      signInRequired: true,
    });
  });
});
