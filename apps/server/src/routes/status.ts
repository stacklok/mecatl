// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono } from "@hono/zod-openapi";
import { type PublicStatusResponse, publicStatusResponseSchema } from "@mecatl-studio/contracts";
import type { ConnectionStatus } from "@stacklok-oss/mecatl-sdk";
import type { AuthenticationService } from "../auth/service.js";
import type { AppEnv } from "../http/env.js";
import type { MecatlRuntime } from "../mecatl/runtime.js";

const statusRoute = createRoute({
  method: "get",
  operationId: "getPublicStatus",
  path: "/api/v1/status",
  responses: {
    200: {
      content: { "application/json": { schema: publicStatusResponseSchema } },
      description: "Coarse Mecatl connection and browser sign-in facts.",
    },
  },
});

export function registerStatusRoutes(
  app: OpenAPIHono<AppEnv>,
  authentication: AuthenticationService | undefined,
  runtime: MecatlRuntime | undefined,
) {
  app.openapi(statusRoute, async (context) => {
    context.header("Cache-Control", "private, no-store");
    const signInRequired =
      authentication !== undefined && (await authentication.signInRequired(context));
    const connection =
      runtime === undefined ? "unavailable" : publicConnection(runtime.client.status.getSnapshot());
    return context.json({ connection, signInRequired }, 200);
  });
}

function publicConnection(status: ConnectionStatus): PublicStatusResponse["connection"] {
  switch (status) {
    case "connecting":
      return "checking";
    case "online":
    case "unauthorized":
    case "incompatible":
      return "reachable";
    case "reconnecting":
    case "offline":
      return "unavailable";
  }
}
