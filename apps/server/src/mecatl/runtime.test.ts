// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { bootstrap } from "../bootstrap.js";
import { ConfigurationError } from "../config.js";
import { fakeIssuer, resourceUrl } from "../testing/fake-issuer.js";
import { fakeClient, memoryLogger } from "../testing/fakes.js";
import { runtimeConfigFromEnvironment, startMecatlRuntime } from "./runtime.js";

describe("mecatl runtime", () => {
  it("MECATL_BASE_URL selects external mode and rejects MECATL_DEV_MOCK", () => {
    expect(runtimeConfigFromEnvironment({})).toEqual({ mock: false, source: "local" });
    expect(runtimeConfigFromEnvironment({ MECATL_DEV_MOCK: "1" })).toEqual({
      mock: true,
      source: "local",
    });
    expect(runtimeConfigFromEnvironment({ MECATED_BIN: "/opt/mecated" })).toEqual({
      binaryPath: "/opt/mecated",
      mock: false,
      source: "local",
    });
    expect(
      runtimeConfigFromEnvironment({
        MECATL_AUTH_TOKEN: "secret",
        MECATL_BASE_URL: "https://mecatl.example.com/",
      }),
    ).toEqual({
      authToken: "secret",
      baseUrl: "https://mecatl.example.com",
      resourceUrl: "https://mecatl.example.com",
      source: "external",
    });
    expect(
      runtimeConfigFromEnvironment({
        MECATL_BASE_URL: "http://mecated:50051",
        MECATL_RESOURCE_URL: "http://mecated:8080/",
      }),
    ).toEqual({
      baseUrl: "http://mecated:50051",
      resourceUrl: "http://mecated:8080",
      source: "external",
    });

    const rejects = (environment: NodeJS.ProcessEnv, variable: string) => {
      try {
        runtimeConfigFromEnvironment(environment);
      } catch (error) {
        expect(error).toBeInstanceOf(ConfigurationError);
        expect((error as ConfigurationError).variable).toBe(variable);
        expect((error as Error).message).toContain(variable);
        return;
      }
      throw new Error(`expected ${variable} to be rejected`);
    };
    rejects({ MECATL_BASE_URL: "https://x.example", MECATL_DEV_MOCK: "1" }, "MECATL_DEV_MOCK");
    rejects({ MECATL_AUTH_TOKEN: "secret" }, "MECATL_AUTH_TOKEN");
    rejects({ MECATL_RESOURCE_URL: "https://x.example" }, "MECATL_RESOURCE_URL");
    rejects({ MECATL_BASE_URL: "ftp://x.example" }, "MECATL_BASE_URL");
    rejects({ MECATL_BASE_URL: "not a url" }, "MECATL_BASE_URL");
    // The contract calls this the gRPC listener's authority: anything more is
    // misconfiguration that must fail before the server listens.
    rejects({ MECATL_BASE_URL: "https://user:pass@mecatl.example.com" }, "MECATL_BASE_URL");
    rejects({ MECATL_BASE_URL: "https://mecatl.example.com/?x=1" }, "MECATL_BASE_URL");
    rejects({ MECATL_BASE_URL: "https://mecatl.example.com/#frag" }, "MECATL_BASE_URL");
    rejects({ MECATL_BASE_URL: "https://mecatl.example.com/v1" }, "MECATL_BASE_URL");
    // A resource may be namespaced by a path, but nothing else.
    rejects(
      {
        MECATL_BASE_URL: "https://mecatl.example.com",
        MECATL_RESOURCE_URL: "https://r.example/?x=1",
      },
      "MECATL_RESOURCE_URL",
    );
    rejects({ MECATL_DEV_MOCK: "yes" }, "MECATL_DEV_MOCK");
  });

  it("STUDIO_IMAGE=1 refuses local spawn and mock modes", () => {
    for (const environment of [{}, { MECATL_DEV_MOCK: "1" }, { MECATED_BIN: "/opt/mecated" }]) {
      let caught: unknown;
      try {
        runtimeConfigFromEnvironment({ ...environment, STUDIO_IMAGE: "1" });
      } catch (error) {
        caught = error;
      }
      expect(caught).toBeInstanceOf(ConfigurationError);
      expect((caught as ConfigurationError).variable).toBe("MECATL_BASE_URL");
      expect((caught as Error).message).toContain("MECATL_BASE_URL");
    }
    expect(
      runtimeConfigFromEnvironment({
        MECATL_BASE_URL: "https://mecatl.example.com",
        STUDIO_IMAGE: "1",
      }),
    ).toMatchObject({ source: "external" });
  });

  it("STUDIO_IMAGE=1 with a static token or no-auth runtime requires STUDIO_ALLOW_UNAUTHENTICATED", async () => {
    const base = {
      MECATL_BASE_URL: resourceUrl,
      STUDIO_IMAGE: "1",
      STUDIO_PUBLIC_URL: "https://studio.example.com",
    };
    const boot = (environment: NodeJS.ProcessEnv, discoveryStatus = 404) => {
      const { logger, records } = memoryLogger();
      return {
        records,
        result: bootstrap({
          environment,
          fetch: fakeIssuer({ discoveryStatus }).fetch,
          logger,
          runtime: { createClient: async () => fakeClient() },
        }),
      };
    };

    // No-auth runtime (discovery 404) inside the image: refused.
    let refused = boot(base);
    await expect(refused.result).rejects.toMatchObject({
      name: "ConfigurationError",
      variable: "STUDIO_ALLOW_UNAUTHENTICATED",
    });
    // Static token inside the image: refused.
    refused = boot({ ...base, MECATL_AUTH_TOKEN: "service-token" });
    await expect(refused.result).rejects.toMatchObject({
      variable: "STUDIO_ALLOW_UNAUTHENTICATED",
    });

    // Opt-in: allowed, and both modes still WARN naming the mode.
    for (const [environment, mode] of [
      [{ ...base, STUDIO_ALLOW_UNAUTHENTICATED: "1" }, "none"],
      [
        { ...base, MECATL_AUTH_TOKEN: "service-token", STUDIO_ALLOW_UNAUTHENTICATED: "1" },
        "static",
      ],
    ] as const) {
      const allowed = boot(environment);
      const booted = await allowed.result;
      expect(booted.runtime.authMode).toBe(mode);
      const warnings = allowed.records.filter(
        (record) => record.level === "warn" && record.event === "auth.unauthenticated_runtime",
      );
      expect(warnings).toHaveLength(1);
      expect(warnings[0]?.fields.mode).toBe(mode);
      await booted.runtime.close();
    }

    // Outside the image the same runtimes start and only WARN.
    const dev = boot({ MECATL_BASE_URL: resourceUrl });
    const booted = await dev.result;
    expect(booted.runtime.authMode).toBe("none");
    expect(dev.records.some((record) => record.event === "auth.unauthenticated_runtime")).toBe(
      true,
    );
    await booted.runtime.close();
  });

  it("losing the upstream after negotiation reports reconnecting and re-negotiates", async () => {
    const client = fakeClient();
    const runtime = await startMecatlRuntime(
      { baseUrl: resourceUrl, resourceUrl, source: "external" },
      {
        createClient: async () => client,
        reconnectBackoffMs: [0],
        setTimeoutFn: ((callback: () => void) => setTimeout(callback, 0)) as typeof setTimeout,
      },
    );
    await runtime.ready();
    expect(client.compatibility).toHaveBeenCalledTimes(1);
    expect(runtime.snapshot().connection).toBe("online");

    // Upstream drops: the snapshot keeps the last capabilities with the live status.
    client.compatibility.mockRejectedValueOnce(new Error("upstream gone"));
    client.emit("reconnecting");
    expect(runtime.snapshot().connection).toBe("reconnecting");
    expect(runtime.snapshot().capabilities.agents).toBe(true);

    // Bounded backoff retries until negotiation succeeds again.
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(client.compatibility.mock.calls.length).toBeGreaterThanOrEqual(3);
    client.emit("online");
    expect(runtime.snapshot().connection).toBe("online");
    await runtime.close();
  });

  it("a request credential never leaks into a concurrent request", async () => {
    let provider: (() => HeadersInit) | undefined;
    const runtime = await startMecatlRuntime(
      { baseUrl: resourceUrl, resourceUrl, source: "external" },
      {
        createClient: async (_config, credentialProvider) => {
          provider = credentialProvider;
          return fakeClient();
        },
      },
    );
    const authorization = () => new Headers(provider?.()).get("authorization");

    const seen: Array<string | null> = [];
    await Promise.all(
      ["alpha", "beta", "gamma"].map((token, index) =>
        runtime.runWithCredential(token, async () => {
          await new Promise((resolve) => setTimeout(resolve, 5 * (3 - index)));
          seen.push(authorization());
          await new Promise((resolve) => setTimeout(resolve, 5 * index));
          seen.push(authorization());
        }),
      ),
    );
    expect(seen.filter((value) => value === "Bearer alpha")).toHaveLength(2);
    expect(seen.filter((value) => value === "Bearer beta")).toHaveLength(2);
    expect(seen.filter((value) => value === "Bearer gamma")).toHaveLength(2);
    // Outside any request there is no credential at all.
    expect(authorization()).toBeNull();
    await runtime.close();
  });
});
