// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono, z } from "@hono/zod-openapi";
import {
  problemDetailsSchema,
  sessionWorktreesResponseSchema,
  soulInspectionResponseSchema,
} from "@mecatl-studio/contracts";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import type { InspectionService } from "../mecatl/inspection.js";

const problemResponse = {
  content: { "application/problem+json": { schema: problemDetailsSchema } },
  description: "The inspection could not be completed.",
} as const;

const soulRoute = createRoute({
  method: "get",
  operationId: "getSoulInspection",
  path: "/api/v1/soul",
  responses: {
    200: {
      content: { "application/json": { schema: soulInspectionResponseSchema } },
      description: "The connection's resolved soul snapshot.",
    },
    400: problemResponse,
    401: problemResponse,
    500: problemResponse,
    501: problemResponse,
    503: problemResponse,
  },
});

const worktreesRoute = createRoute({
  method: "get",
  operationId: "getSessionWorktrees",
  path: "/api/v1/sessions/{sessionId}/worktrees",
  request: {
    params: z.object({
      sessionId: z
        .string()
        .min(1)
        .openapi({ param: { in: "path", name: "sessionId" } }),
    }),
  },
  responses: {
    200: {
      content: { "application/json": { schema: sessionWorktreesResponseSchema } },
      description: "Eligible choices for the owned source session.",
    },
    400: problemResponse,
    401: problemResponse,
    404: problemResponse,
    500: problemResponse,
    501: problemResponse,
    503: problemResponse,
  },
});

export interface InspectionAvailability {
  readonly soul: boolean;
  readonly worktrees: boolean;
  readonly connection: string;
}

export function registerInspectionRoutes(
  app: OpenAPIHono<AppEnv>,
  inspection: InspectionService | undefined,
  availability: () => InspectionAvailability | undefined,
) {
  app.openapi(soulRoute, async (context) => {
    context.header("Cache-Control", "private, no-store");
    if (context.req.raw.url.includes("?") || hasBody(context.req.raw))
      return invalidRequest(context);
    const state = availability();
    if (inspection === undefined || state === undefined || state.connection !== "online")
      return unavailable(context);
    if (!state.soul) return unsupported(context, "soul_unsupported");
    try {
      return context.json(await inspection.soul(), 200);
    } catch (error) {
      return inspectionFailure(context, error);
    }
  });

  app.openapi(worktreesRoute, async (context) => {
    context.header("Cache-Control", "private, no-store");
    if (context.req.raw.url.includes("?") || hasBody(context.req.raw))
      return invalidRequest(context);
    const state = availability();
    if (inspection === undefined || state === undefined || state.connection !== "online")
      return unavailable(context);
    if (!state.worktrees) return unsupported(context, "worktrees_unsupported");
    try {
      const { sessionId } = context.req.valid("param");
      return context.json(await inspection.worktrees(sessionId), 200);
    } catch (error) {
      if (
        error instanceof MecatlError &&
        (error.code === "session_not_found" || error.code === "not_found" || error.status === 5)
      ) {
        return problem(
          context,
          404,
          "session_not_found",
          "Session not found",
          "The session is unavailable.",
        );
      }
      return inspectionFailure(context, error);
    }
  });
}

function hasBody(request: Request): boolean {
  return (
    request.body !== null ||
    (request.headers.has("content-length") && request.headers.get("content-length") !== "0") ||
    request.headers.has("transfer-encoding")
  );
}

function invalidRequest(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    400,
    "invalid_request",
    "Invalid request",
    "This inspection accepts no query or body.",
  );
}

function unavailable(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    503,
    "runtime_unavailable",
    "Mecatl runtime unavailable",
    "Inspection is unavailable right now.",
  );
}

function unsupported(
  context: Parameters<typeof problem>[0],
  code: "soul_unsupported" | "worktrees_unsupported",
) {
  return problem(
    context,
    501,
    code,
    "Inspection unsupported",
    "This deployment does not support this inspection.",
  );
}

/** Inspection failures have fixed details; daemon messages may contain private placement data. */
function inspectionFailure(context: Parameters<typeof problem>[0], error: unknown) {
  if (error instanceof MecatlError) {
    if (error.code === "unimplemented" || error.status === 12)
      return unsupported(
        context,
        context.req.path === "/api/v1/soul" ? "soul_unsupported" : "worktrees_unsupported",
      );
    if (
      error.code === "transport" ||
      error.code === "draining" ||
      error.code === "placement_unavailable" ||
      error.status === 14 ||
      (error.transport === "http" && error.status === 503)
    )
      return unavailable(context);
  }
  return problem(
    context,
    500,
    "inspection_failed",
    "Inspection failed",
    "The inspection could not be completed.",
  );
}
