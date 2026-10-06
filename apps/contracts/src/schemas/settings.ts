// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const modelInventoryItemSchema = z.object({
  contextLimit: z.string(),
  displayName: z.string(),
  id: z.string(),
  image: z.boolean(),
  providerId: z.string(),
  reasoning: z.boolean(),
});

export const providerInventoryItemSchema = z.object({
  availableNotDefault: z.boolean(),
  defaultModelAutoSelected: z.boolean(),
  hint: z.string(),
  id: z.string(),
  modelCount: z.number().int().nonnegative(),
  state: z.string(),
});

export const providerSettingsResponseSchema = z.object({
  // OpenAPI 3.1 type unions keep null in the generated client; nullable:true does not.
  displayEndpoint: z
    .custom<string | null>((value) => typeof value === "string" || value === null)
    .meta({ type: ["string", "null"] }),
  models: z.array(modelInventoryItemSchema),
  provider: providerInventoryItemSchema,
});

export const runtimeSettingsResponseSchema = z.object({
  buildId: z.string(),
  management: z.object({
    providerConfiguration: z.boolean(),
    providerConfigurationReason: z.string(),
    routingConfiguration: z.boolean(),
    routingConfigurationReason: z.string(),
  }),
  models: z.array(modelInventoryItemSchema),
  modelsReason: z.string(),
  modelsSupported: z.boolean(),
  providerEndpoint: z.string(),
  providers: z.array(providerInventoryItemSchema),
  serverImplementation: z.string(),
});

export type RuntimeSettingsResponse = z.infer<typeof runtimeSettingsResponseSchema>;
export type ProviderSettingsResponse = z.infer<typeof providerSettingsResponseSchema>;
