// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono, z } from "@hono/zod-openapi";
import {
  problemDetailsSchema,
  providerSettingsResponseSchema,
  runtimeSettingsResponseSchema,
} from "@mecatl-studio/contracts";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import { ModelsUnsupportedError, type SettingsService } from "../mecatl/settings.js";

const route = createRoute({
  method: "get",
  operationId: "getRuntimeSettings",
  path: "/api/v1/settings/runtime",
  responses: {
    200: {
      content: { "application/json": { schema: runtimeSettingsResponseSchema } },
      description: "Safe runtime, provider, and model inventory for settings.",
    },
    500: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The settings inventory could not be read.",
    },
    503: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The Mecatl runtime is unavailable.",
    },
  },
});

const providerRoute = createRoute({
  method: "get",
  operationId: "getProviderSettings",
  path: "/api/v1/settings/provider",
  request: { query: z.object({ providerId: z.string().min(1) }) },
  responses: {
    200: {
      content: { "application/json": { schema: providerSettingsResponseSchema } },
      description: "Caller-visible provider, models, and safe diagnostic endpoint.",
    },
    400: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The provider ID is missing, empty, or repeated.",
    },
    401: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "Sign in before reading provider details in OIDC mode.",
    },
    404: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The provider is absent from the caller-visible inventory.",
    },
    501: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "Model selection is unavailable on this deployment.",
    },
    503: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The runtime or provider information is unavailable.",
    },
  },
});

export function registerSettingsRoutes(
  app: OpenAPIHono<AppEnv>,
  settings: SettingsService | undefined,
) {
  app.openapi(route, async (context) => {
    if (!settings) {
      return problem(
        context,
        503,
        "runtime_unavailable",
        "Mecatl runtime unavailable",
        "The BFF has no Mecatl runtime connection.",
      );
    }
    return context.json(await settings.get(), 200);
  });

  app.openapi(providerRoute, async (context) => {
    const providerIds = new URL(context.req.url).searchParams.getAll("providerId");
    if (providerIds.length !== 1 || !providerIds[0]) {
      return problem(context, 400, "invalid_request", "Invalid request", "Provide one providerId.");
    }
    if (!settings) {
      return problem(
        context,
        503,
        "runtime_unavailable",
        "Mecatl runtime unavailable",
        "The BFF has no Mecatl runtime connection.",
      );
    }
    try {
      const detail = await settings.getProvider(providerIds[0]);
      if (!detail) {
        return problem(
          context,
          404,
          "provider_not_found",
          "Provider not found",
          "The provider is unavailable.",
        );
      }
      return context.json(detail, 200);
    } catch (error) {
      if (error instanceof ModelsUnsupportedError) {
        return problem(
          context,
          501,
          "models_unsupported",
          "Model selection unavailable",
          "This Mecatl deployment does not support model selection.",
        );
      }
      if (
        error instanceof MecatlError &&
        (error.code === "unauthenticated" || error.code === "authentication")
      ) {
        throw error;
      }
      return problem(
        context,
        503,
        "runtime_unavailable",
        "Mecatl runtime unavailable",
        "Provider information is temporarily unavailable.",
      );
    }
  });
}
