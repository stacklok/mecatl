// SPDX-License-Identifier: Apache-2.0

import type { Client } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createMecatlSettingsService } from "./settings";

function sdkModel(id: string, providerId: string) {
  return {
    contextLimit: 200_000n,
    displayName: "",
    id,
    image: false,
    providerId,
    reasoning: true,
  };
}

describe("Mecatl runtime settings", () => {
  it("synthesises provider rows for models whose provider reports no status", async () => {
    const list = vi.fn().mockResolvedValue({
      models: [sdkModel("b-1", "beta"), sdkModel("a-1", "alpha"), sdkModel("a-2", "alpha")],
      providerStatus: [
        {
          availableNotDefault: true,
          defaultModelAutoSelected: false,
          hint: "set a default",
          modelCount: 1,
          providerId: "beta",
          state: "available",
        },
      ],
    });
    const info = vi.fn().mockResolvedValue({
      buildId: "v1.2.3",
      llmProviderDisplayEndpoint: "https://gateway.example.com",
      serverImplementation: "mecak8s",
    });
    const service = createMecatlSettingsService(
      { models: { list }, server: { info } } as unknown as Client,
      { modelSelection: true, serverInfo: true },
    );

    const settings = await service.get();

    expect(settings).toMatchObject({
      buildId: "v1.2.3",
      modelsSupported: true,
      providerEndpoint: "https://gateway.example.com",
      serverImplementation: "mecak8s",
    });
    expect(settings.providers).toEqual([
      {
        availableNotDefault: false,
        defaultModelAutoSelected: false,
        hint: "",
        id: "alpha",
        modelCount: 2,
        state: "available",
      },
      {
        availableNotDefault: true,
        defaultModelAutoSelected: false,
        hint: "set a default",
        id: "beta",
        modelCount: 1,
        state: "available",
      },
    ]);
    expect(settings.models[0]).toEqual({
      contextLimit: "200000",
      displayName: "b-1",
      id: "b-1",
      image: false,
      providerId: "beta",
      reasoning: true,
    });
    // The projection is an allowlist: nothing beyond the contract's keys leaves the BFF.
    expect(Object.keys(settings).sort()).toEqual([
      "buildId",
      "management",
      "models",
      "modelsReason",
      "modelsSupported",
      "providerEndpoint",
      "providers",
      "serverImplementation",
    ]);
    expect(settings.management).toEqual({
      providerConfiguration: false,
      providerConfigurationReason:
        "Provider credentials and configuration are managed by the Mecatl deployment.",
      routingConfiguration: false,
      routingConfigurationReason: "Model routing is managed by the Mecatl deployment settings.",
    });
  });

  it("skips the models call and server info when the capabilities are off", async () => {
    const list = vi.fn();
    const info = vi.fn();
    let options = { modelSelection: false, serverInfo: false };
    const service = createMecatlSettingsService(
      { models: { list }, server: { info } } as unknown as Client,
      () => options,
    );

    await expect(service.get()).resolves.toEqual({
      buildId: "",
      management: expect.objectContaining({ providerConfiguration: false }),
      models: [],
      modelsReason: "Model selection is not enabled on this Mecatl deployment.",
      modelsSupported: false,
      providerEndpoint: "",
      providers: [],
      serverImplementation: "unknown",
    });
    expect(list).not.toHaveBeenCalled();
    expect(info).not.toHaveBeenCalled();

    // Capabilities are read live on every call.
    options = { modelSelection: true, serverInfo: false };
    list.mockResolvedValue({ models: [], providerStatus: [] });
    await expect(service.get()).resolves.toMatchObject({
      modelsSupported: true,
      serverImplementation: "unknown",
    });
    expect(list).toHaveBeenCalledTimes(1);
    expect(info).not.toHaveBeenCalled();
  });
});
