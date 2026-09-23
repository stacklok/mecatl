// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { createApp } from "../app";
import type { SettingsService } from "../mecatl/settings";
import { fakeRuntime } from "../testing/fakes";

const settings: SettingsService = {
  async get() {
    return {
      buildId: "dev",
      management: {
        providerConfiguration: false,
        providerConfigurationReason: "Managed by the deployment.",
        routingConfiguration: false,
        routingConfigurationReason: "Managed by the deployment.",
      },
      models: [
        {
          contextLimit: "128000",
          displayName: "Example",
          id: "example",
          image: true,
          providerId: "test",
          reasoning: true,
        },
      ],
      modelsReason: "",
      modelsSupported: true,
      providerEndpoint: "",
      providers: [
        {
          availableNotDefault: false,
          defaultModelAutoSelected: true,
          hint: "",
          id: "test",
          modelCount: 1,
          state: "available",
        },
      ],
      serverImplementation: "mecated",
    };
  },
};

describe("settings routes", () => {
  it("returns only safe runtime and model inventory", async () => {
    const response = await createApp({ settings }).request("/api/v1/settings/runtime");
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({
      management: { providerConfiguration: false },
      models: [{ id: "example", providerId: "test" }],
    });
  });
  it("answers 503 without a runtime and 401 without a session", async () => {
    const detached = await createApp().request("/api/v1/settings/runtime");
    expect(detached.status).toBe(503);
    await expect(detached.json()).resolves.toMatchObject({ code: "runtime_unavailable" });

    const gated = createApp({
      authentication: {
        clear: () => undefined,
        completeLogin: async () => {
          throw new Error("unused");
        },
        credential: async () => ({ status: "anonymous" }),
        logout: async () => undefined,
        noteLoginComplete: () => undefined,
        noteLoginFailure: () => undefined,
        save: async () => undefined,
        startLogin: async () => "https://issuer.example.com/authorize",
      },
      runtime: fakeRuntime(),
      settings,
    });
    const response = await gated.request("/api/v1/settings/runtime");
    expect(response.status).toBe(401);
    await expect(response.json()).resolves.toMatchObject({ code: "unauthenticated" });
  });
});
