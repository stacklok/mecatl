// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";
import { effortLevelSchema } from "./chat.ts";

export const modelInventoryItemSchema = z.object({
  contextLimit: z.string(),
  displayName: z.string(),
  id: z.string(),
  image: z.boolean(),
  /** The provider's display name, as on its `providerInventoryItemSchema` entry. */
  providerDisplayName: z.string(),
  providerId: z.string(),
  reasoning: z.boolean(),
  /**
   * The reasoning-effort levels requests for this model are decided against.
   * Absent when the server doesn't report per-model effort levels. With
   * `known` false the levels are unknown: a requested level is sent and the
   * provider may refuse it. With `known` true and no levels, the model gets no
   * effort; otherwise a requested level the list lacks is sent as the highest
   * listed level below it, or not at all.
   */
  effortLevels: z
    .object({
      known: z.boolean(),
      levels: z.array(effortLevelSchema),
    })
    .optional(),
});

export const providerInventoryItemSchema = z.object({
  availableNotDefault: z.boolean(),
  defaultModelAutoSelected: z.boolean(),
  /** The BFF's name for the provider: a known registry ID's name, else the ID title-cased. */
  displayName: z.string(),
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
