// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono } from "@hono/zod-openapi";
import { problemDetailsSchema, storageHealthResponseSchema } from "@mecatl-studio/contracts";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import type { StorageService } from "../mecatl/storage.js";

const healthRoute = createRoute({
  method: "get",
  operationId: "getStorageHealth",
  path: "/api/v1/storage/health",
  responses: {
    200: {
      content: { "application/json": { schema: storageHealthResponseSchema } },
      description: "A plain-words summary of the connected agent's saved chats and runs.",
    },
    500: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The storage health could not be read.",
    },
    503: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description: "The Mecatl runtime is unavailable.",
    },
  },
});

export function registerStorageRoutes(
  app: OpenAPIHono<AppEnv>,
  storage: StorageService | undefined,
) {
  app.openapi(healthRoute, async (context) => {
    if (!storage) {
      return problem(
        context,
        503,
        "runtime_unavailable",
        "Mecatl runtime unavailable",
        "The BFF has no Mecatl runtime connection.",
      );
    }
    return context.json(await storage.getHealth(), 200);
  });
}
