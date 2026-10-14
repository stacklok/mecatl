// SPDX-License-Identifier: Apache-2.0

import {
  modelInventoryItemSchema,
  providerInventoryItemSchema,
  providerSettingsResponseSchema,
  runtimeSettingsResponseSchema,
} from "@mecatl-studio/contracts";
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
  it("serves a safe detail for a known provider", async () => {
    const list = vi.fn().mockResolvedValue({
      models: [
        { ...sdkModel("other", "other"), credential: "secret" },
        { ...sdkModel("slash", "team/provider"), apiKey: "secret" },
      ],
      providerStatus: [
        {
          availableNotDefault: true,
          defaultModelAutoSelected: false,
          hint: "Available",
          modelCount: 1,
          providerId: "team/provider",
          state: "available",
          credentials: "secret",
        },
      ],
    });
    const info = vi.fn().mockResolvedValue({
      buildId: "daemon-v1",
      llmProviderDisplayEndpoint: "https://gateway.example.com/a%2Fb",
      rawEndpoint: "https://user:secret@gateway.example.com/?token=secret",
      serverImplementation: "mecated",
    });
    const service = createMecatlSettingsService(
      { models: { list }, server: { info } } as unknown as Client,
      { modelSelection: true, serverInfo: true },
    );

    await expect(service.getProvider("team/provider")).resolves.toEqual({
      displayEndpoint: "https://gateway.example.com/a%2Fb",
      models: [
        {
          contextLimit: "200000",
          displayName: "slash",
          id: "slash",
          image: false,
          providerDisplayName: "Team/provider",
          providerId: "team/provider",
          reasoning: true,
        },
      ],
      provider: {
        availableNotDefault: true,
        defaultModelAutoSelected: false,
        displayName: "Team/provider",
        hint: "Available",
        id: "team/provider",
        modelCount: 1,
        state: "available",
      },
    });
    expect(info).toHaveBeenCalledExactlyOnceWith({ providerId: "team/provider" });

    await expect(service.getProvider("other")).resolves.toMatchObject({
      displayEndpoint: "https://gateway.example.com/a%2Fb",
    });
    await expect(service.getProvider("missing")).resolves.toBeNull();
    expect(info).toHaveBeenCalledTimes(2);
  });

  it("returns null endpoint when server info is unsupported or empty", async () => {
    const list = vi.fn().mockResolvedValue({
      models: [sdkModel("known", "known")],
      providerStatus: [],
    });
    const info = vi.fn().mockResolvedValue({ llmProviderDisplayEndpoint: "" });
    let serverInfo = false;
    const service = createMecatlSettingsService(
      { models: { list }, server: { info } } as unknown as Client,
      () => ({ modelSelection: true, serverInfo }),
    );
    await expect(service.getProvider("known")).resolves.toMatchObject({ displayEndpoint: null });
    expect(info).not.toHaveBeenCalled();
    serverInfo = true;
    await expect(service.getProvider("known")).resolves.toMatchObject({ displayEndpoint: null });
    expect(info).toHaveBeenCalledExactlyOnceWith({ providerId: "known" });
  });

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
        displayName: "Alpha",
        hint: "",
        id: "alpha",
        modelCount: 2,
        state: "available",
      },
      {
        availableNotDefault: true,
        defaultModelAutoSelected: false,
        displayName: "Beta",
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
      providerDisplayName: "Beta",
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

  it("keeps every inventory field and value for a provider with no display name", async () => {
    // Pins the inventory served before provider display names (#2260): an
    // operator-defined provider keeps each field it had, in both responses.
    const list = vi.fn().mockResolvedValue({
      models: [{ ...sdkModel("gateway-model", "team-gateway"), displayName: "Gateway Model" }],
      providerStatus: [
        {
          availableNotDefault: false,
          defaultModelAutoSelected: true,
          hint: "ready",
          modelCount: 1,
          providerId: "team-gateway",
          state: "ok",
        },
      ],
    });
    const info = vi.fn().mockResolvedValue({
      llmProviderDisplayEndpoint: "https://gateway.example.com/v1",
    });
    const service = createMecatlSettingsService(
      { models: { list }, server: { info } } as unknown as Client,
      { modelSelection: true, serverInfo: true },
    );
    const provider = {
      availableNotDefault: false,
      defaultModelAutoSelected: true,
      hint: "ready",
      id: "team-gateway",
      modelCount: 1,
      state: "ok",
    };
    const model = {
      contextLimit: "200000",
      displayName: "Gateway Model",
      id: "gateway-model",
      image: false,
      providerId: "team-gateway",
      reasoning: true,
    };

    const settings = await service.get();
    expect(settings.providers).toHaveLength(1);
    expect(settings.providers[0]).toMatchObject(provider);
    expect(settings.models).toHaveLength(1);
    expect(settings.models[0]).toMatchObject(model);

    const detail = await service.getProvider("team-gateway");
    expect(detail).toMatchObject({ displayEndpoint: "https://gateway.example.com/v1", provider });
    expect(detail?.models).toHaveLength(1);
    expect(detail?.models[0]).toMatchObject(model);
  });

  it("names each provider, and each model's provider, for the browser", async () => {
    const status = (providerId: string) => ({
      availableNotDefault: false,
      defaultModelAutoSelected: false,
      hint: "",
      modelCount: 1,
      providerId,
      state: "ok",
    });
    const list = vi.fn().mockResolvedValue({
      models: [
        sdkModel("gpt-5", "openai"),
        sdkModel("gateway-model", "team-gateway"),
        sdkModel("claude", "anthropic"),
      ],
      // Anthropic reports no status, so its row is synthesised from its model.
      providerStatus: [status("openai"), status("team-gateway")],
    });
    const service = createMecatlSettingsService(
      { models: { list }, server: { info: vi.fn() } } as unknown as Client,
      { modelSelection: true, serverInfo: false },
    );

    const settings = await service.get();
    expect(settings.providers.map(({ displayName, id }) => [id, displayName])).toEqual([
      ["anthropic", "Anthropic"],
      ["openai", "OpenAI"],
      ["team-gateway", "Team Gateway"],
    ]);
    expect(settings.models.map(({ id, providerDisplayName }) => [id, providerDisplayName])).toEqual(
      [
        ["gpt-5", "OpenAI"],
        ["gateway-model", "Team Gateway"],
        ["claude", "Anthropic"],
      ],
    );
    // Parsing strips nothing and finds every required field.
    expect(runtimeSettingsResponseSchema.parse(settings)).toEqual(settings);

    const detail = await service.getProvider("openai");
    expect(detail?.provider.displayName).toBe("OpenAI");
    expect(detail?.models.map((model) => model.providerDisplayName)).toEqual(["OpenAI"]);
    expect(providerSettingsResponseSchema.parse(detail)).toEqual(detail);
  });

  it("projects exactly the contract's provider and model fields", async () => {
    const list = vi.fn().mockResolvedValue({
      models: [{ ...sdkModel("gpt-5", "openai"), credential: "secret" }],
      providerStatus: [
        {
          availableNotDefault: false,
          defaultModelAutoSelected: true,
          hint: "",
          modelCount: 1,
          providerId: "openai",
          rawEndpoint: "https://user:secret@private.example",
          state: "ok",
        },
      ],
    });
    const service = createMecatlSettingsService(
      { models: { list }, server: { info: vi.fn() } } as unknown as Client,
      { modelSelection: true, serverInfo: false },
    );
    const settings = await service.get();
    const providerFields = Object.keys(providerInventoryItemSchema.shape).sort();
    const modelFields = Object.keys(modelInventoryItemSchema.shape).sort();
    expect(providerFields).toContain("displayName");
    expect(modelFields).toContain("providerDisplayName");
    expect(Object.keys(settings.providers[0] ?? {}).sort()).toEqual(providerFields);
    expect(Object.keys(settings.models[0] ?? {}).sort()).toEqual(modelFields);

    const { displayName: _, ...unnamedProvider } = settings.providers[0] ?? {};
    expect(providerInventoryItemSchema.safeParse(unnamedProvider).success).toBe(false);
    const { providerDisplayName: __, ...unnamedModel } = settings.models[0] ?? {};
    expect(modelInventoryItemSchema.safeParse(unnamedModel).success).toBe(false);
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
