// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono } from "@hono/zod-openapi";
import { problemDetailsSchema, runtimeSettingsResponseSchema } from "@mecatl-studio/contracts";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import type { SettingsService } from "../mecatl/settings.js";

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
}
