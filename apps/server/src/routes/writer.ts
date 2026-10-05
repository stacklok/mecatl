// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono } from "@hono/zod-openapi";
import {
  discussWriterRequestSchema,
  discussWriterResponseSchema,
  observeWriterRequestSchema,
  observeWriterResponseSchema,
  problemDetailsSchema,
} from "@mecatl-studio/contracts";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import type { WriterService } from "../mecatl/writer.js";

const responses = (
  schema: typeof observeWriterResponseSchema | typeof discussWriterResponseSchema,
) => ({
  200: { content: { "application/json": { schema } }, description: "Completed Writer response." },
  400: {
    content: { "application/problem+json": { schema: problemDetailsSchema } },
    description: "Invalid input.",
  },
  503: {
    content: { "application/problem+json": { schema: problemDetailsSchema } },
    description: "Writer unavailable.",
  },
});

const observeRoute = createRoute({
  method: "post",
  operationId: "observeWriter",
  path: "/api/v1/writer/observe",
  request: { body: { content: { "application/json": { schema: observeWriterRequestSchema } } } },
  responses: responses(observeWriterResponseSchema),
});
const discussRoute = createRoute({
  method: "post",
  operationId: "discussWriter",
  path: "/api/v1/writer/discuss",
  request: { body: { content: { "application/json": { schema: discussWriterRequestSchema } } } },
  responses: responses(discussWriterResponseSchema),
});

export function registerWriterRoutes(app: OpenAPIHono<AppEnv>, writer: WriterService | undefined) {
  const unavailable = (context: Parameters<typeof problem>[0]) =>
    problem(
      context,
      503,
      "writer_unavailable",
      "Writer unavailable",
      "The Writer could not complete this request.",
    );
  const execute = async <T>(
    context: Parameters<typeof problem>[0],
    operation: () => Promise<T>,
  ) => {
    try {
      return await operation();
    } catch (error) {
      if (
        error instanceof MecatlError &&
        (error.code === "authentication" || error.code === "unauthenticated")
      )
        throw error;
      // Model output and upstream errors can include document content; neither is logged or returned.
      return unavailable(context);
    }
  };
  app.openapi(observeRoute, async (context) => {
    if (!writer) return unavailable(context);
    const result = await execute(context, () =>
      writer.observe(context.req.valid("json"), context.req.raw.signal),
    );
    return result instanceof Response ? result : context.json(result, 200);
  });
  app.openapi(discussRoute, async (context) => {
    if (!writer) return unavailable(context);
    const result = await execute(context, () =>
      writer.discuss(context.req.valid("json"), context.req.raw.signal),
    );
    return result instanceof Response ? result : context.json(result, 200);
  });
}
