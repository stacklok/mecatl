// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono, z } from "@hono/zod-openapi";
import {
  createScheduleRequestSchema,
  listScheduleFiresResponseSchema,
  listSchedulesResponseSchema,
  problemDetailsSchema,
  scheduleActionRequestSchema,
  scheduleSchema,
  updateScheduleRequestSchema,
} from "@mecatl-studio/contracts";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import { type ScheduleService, ScheduleTriggerImmutableError } from "../mecatl/schedules.js";

const scheduleParameters = z.object({
  scheduleName: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "scheduleName" } }),
});

const errorResponse = {
  content: { "application/problem+json": { schema: problemDetailsSchema } },
  description: "The request could not be completed.",
} as const;

const invalidResponse = {
  content: { "application/problem+json": { schema: problemDetailsSchema } },
  description: "The schedule is invalid, for example an unknown time zone.",
} as const;

const unavailableResponse = {
  content: { "application/problem+json": { schema: problemDetailsSchema } },
  description: "The Mecatl runtime or scheduler is unavailable.",
} as const;

const listRoute = createRoute({
  method: "get",
  operationId: "listSchedules",
  path: "/api/v1/schedules",
  responses: {
    200: {
      content: { "application/json": { schema: listSchedulesResponseSchema } },
      description: "The visible scheduled work.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const createRouteDefinition = createRoute({
  method: "post",
  operationId: "createSchedule",
  path: "/api/v1/schedules",
  request: {
    body: {
      content: { "application/json": { schema: createScheduleRequestSchema } },
      required: true,
    },
  },
  responses: {
    201: {
      content: { "application/json": { schema: scheduleSchema } },
      description: "The created schedule.",
    },
    400: invalidResponse,
    500: errorResponse,
    501: unavailableResponse,
    503: unavailableResponse,
  },
});

const updateRoute = createRoute({
  method: "put",
  operationId: "updateSchedule",
  path: "/api/v1/schedules/{scheduleName}",
  request: {
    body: {
      content: { "application/json": { schema: updateScheduleRequestSchema } },
      required: true,
    },
    params: scheduleParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: scheduleSchema } },
      description: "The updated schedule.",
    },
    400: invalidResponse,
    409: {
      content: { "application/problem+json": { schema: problemDetailsSchema } },
      description:
        "The update changes the schedule's trigger, which cannot be edited; create a new schedule instead.",
    },
    500: errorResponse,
    501: unavailableResponse,
    503: unavailableResponse,
  },
});

const actionRoute = createRoute({
  method: "post",
  operationId: "actOnSchedule",
  path: "/api/v1/schedules/{scheduleName}/actions",
  request: {
    body: {
      content: { "application/json": { schema: scheduleActionRequestSchema } },
      required: true,
    },
    params: scheduleParameters,
  },
  responses: {
    204: { description: "The schedule action was accepted." },
    500: errorResponse,
    501: unavailableResponse,
    503: unavailableResponse,
  },
});

const deleteRoute = createRoute({
  method: "delete",
  operationId: "deleteSchedule",
  path: "/api/v1/schedules/{scheduleName}",
  request: { params: scheduleParameters },
  responses: {
    204: { description: "The schedule was deleted." },
    500: errorResponse,
    501: unavailableResponse,
    503: unavailableResponse,
  },
});

const firesRoute = createRoute({
  method: "get",
  operationId: "listScheduleFires",
  path: "/api/v1/schedules/{scheduleName}/fires",
  request: { params: scheduleParameters },
  responses: {
    200: {
      content: { "application/json": { schema: listScheduleFiresResponseSchema } },
      description: "The schedule run history.",
    },
    500: errorResponse,
    501: unavailableResponse,
    503: unavailableResponse,
  },
});

export function registerScheduleRoutes(
  app: OpenAPIHono<AppEnv>,
  schedules: ScheduleService | undefined,
) {
  app.openapi(listRoute, async (context) => {
    if (!schedules) return unavailable(context);
    return context.json(await schedules.list(), 200);
  });

  app.openapi(
    createRouteDefinition,
    async (context) => {
      if (!schedules) return unavailable(context);
      if (!schedules.supported) return unsupported(context);
      return context.json(await schedules.create(context.req.valid("json")), 201);
    },
    (result, context) => (result.success ? undefined : invalidSchedule(context, result.error)),
  );

  app.openapi(
    updateRoute,
    async (context) => {
      if (!schedules) return unavailable(context);
      if (!schedules.supported) return unsupported(context);
      const { scheduleName } = context.req.valid("param");
      try {
        return context.json(await schedules.update(scheduleName, context.req.valid("json")), 200);
      } catch (error) {
        if (!(error instanceof ScheduleTriggerImmutableError)) throw error;
        return problem(
          context,
          409,
          "schedule_trigger_immutable",
          "Schedule trigger cannot change",
          error.message,
        );
      }
    },
    (result, context) => (result.success ? undefined : invalidSchedule(context, result.error)),
  );

  app.openapi(actionRoute, async (context) => {
    if (!schedules) return unavailable(context);
    if (!schedules.supported) return unsupported(context);
    const { scheduleName } = context.req.valid("param");
    const { action } = context.req.valid("json");
    await schedules[action](scheduleName);
    return context.body(null, 204);
  });

  app.openapi(deleteRoute, async (context) => {
    if (!schedules) return unavailable(context);
    if (!schedules.supported) return unsupported(context);
    await schedules.delete(context.req.valid("param").scheduleName);
    return context.body(null, 204);
  });

  app.openapi(firesRoute, async (context) => {
    if (!schedules) return unavailable(context);
    if (!schedules.supported) return unsupported(context);
    return context.json(await schedules.listFires(context.req.valid("param").scheduleName), 200);
  });
}

function unavailable(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    503,
    "runtime_unavailable",
    "Mecatl runtime unavailable",
    "The BFF has no Mecatl runtime connection.",
  );
}

/**
 * A schedule body the contract rejects, answered as a problem whose detail
 * names the first offending field, so the form can show why it was refused.
 */
function invalidSchedule(context: Parameters<typeof problem>[0], error: z.ZodError) {
  const issue = error.issues[0];
  const field = issue?.path.join(".");
  const detail = issue
    ? `${field ? `${field}: ` : ""}${issue.message}`
    : "The schedule is not valid.";
  return problem(context, 400, "invalid_schedule", "Invalid schedule", detail);
}

function unsupported(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    501,
    "schedule_unsupported",
    "Scheduling unavailable",
    "Scheduling is not enabled on this Mecatl deployment.",
  );
}
