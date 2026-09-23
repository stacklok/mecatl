// SPDX-License-Identifier: Apache-2.0

import type { ProviderSettingsResponse, RuntimeSettingsResponse } from "@mecatl-studio/contracts";
import type { Client } from "@stacklok-oss/mecatl-sdk";

export interface SettingsService {
  get(): Promise<RuntimeSettingsResponse>;
  getProvider(providerId: string): Promise<ProviderSettingsResponse | null>;
}

export class ModelsUnsupportedError extends Error {
  constructor() {
    super("Model selection is unavailable");
    this.name = "ModelsUnsupportedError";
  }
}

export function createMecatlSettingsService(
  client: Client,
  options:
    | { modelSelection: boolean; serverInfo: boolean }
    | (() => { modelSelection: boolean; serverInfo: boolean }),
): SettingsService {
  const getOptions = typeof options === "function" ? options : () => options;

  return {
    async getProvider(providerId) {
      const options = getOptions();
      if (!options.modelSelection) throw new ModelsUnsupportedError();

      const inventory = await client.models.list({ $typeName: "mecatl.v1.ListModelsRequest" });
      const projected = projectInventory(inventory);
      const provider = projected.providers.find((candidate) => candidate.id === providerId);
      if (!provider) return null;

      const info = options.serverInfo ? await client.server.info({ providerId }) : undefined;
      return {
        displayEndpoint: info?.llmProviderDisplayEndpoint || null,
        models: projected.models.filter((model) => model.providerId === providerId),
        provider,
      };
    },
    async get() {
      const options = getOptions();
      const info = options.serverInfo ? await client.server.info() : undefined;
      if (!options.modelSelection) {
        return {
          buildId: info?.buildId ?? "",
          management: readOnlyManagement,
          models: [],
          modelsReason: "Model selection is not enabled on this Mecatl deployment.",
          modelsSupported: false,
          providerEndpoint: info?.llmProviderDisplayEndpoint ?? "",
          providers: [],
          serverImplementation: info?.serverImplementation ?? "unknown",
        };
      }

      const inventory = projectInventory(
        await client.models.list({ $typeName: "mecatl.v1.ListModelsRequest" }),
      );

      return {
        buildId: info?.buildId ?? "",
        management: readOnlyManagement,
        models: inventory.models,
        modelsReason: "",
        modelsSupported: true,
        providerEndpoint: info?.llmProviderDisplayEndpoint ?? "",
        providers: inventory.providers,
        serverImplementation: info?.serverImplementation ?? "unknown",
      };
    },
  };
}

function projectInventory(
  inventory: Awaited<ReturnType<Client["models"]["list"]>>,
): Pick<RuntimeSettingsResponse, "models" | "providers"> {
  const providers = new Map(
    inventory.providerStatus.map((provider) => [
      provider.providerId,
      {
        availableNotDefault: provider.availableNotDefault,
        defaultModelAutoSelected: provider.defaultModelAutoSelected,
        hint: provider.hint,
        id: provider.providerId,
        modelCount: provider.modelCount,
        state: provider.state,
      },
    ]),
  );

  for (const model of inventory.models) {
    if (!providers.has(model.providerId)) {
      providers.set(model.providerId, {
        availableNotDefault: false,
        defaultModelAutoSelected: false,
        hint: "",
        id: model.providerId,
        modelCount: inventory.models.filter(
          (candidate) => candidate.providerId === model.providerId,
        ).length,
        state: "available",
      });
    }
  }

  return {
    models: inventory.models.map((model) => ({
      contextLimit: model.contextLimit.toString(),
      displayName: model.displayName || model.id,
      id: model.id,
      image: model.image,
      providerId: model.providerId,
      reasoning: model.reasoning,
    })),
    providers: [...providers.values()].sort((left, right) => left.id.localeCompare(right.id)),
  };
}

const readOnlyManagement = {
  providerConfiguration: false,
  providerConfigurationReason:
    "Provider credentials and configuration are managed by the Mecatl deployment.",
  routingConfiguration: false,
  routingConfigurationReason: "Model routing is managed by the Mecatl deployment settings.",
} as const;
