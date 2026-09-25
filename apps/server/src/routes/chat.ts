// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono, z } from "@hono/zod-openapi";
import {
  clearSessionResponseSchema,
  compactSessionResponseSchema,
  createSessionRequestSchema,
  createSessionResponseSchema,
  forkSessionRequestSchema,
  forkSessionResponseSchema,
  listSessionsResponseSchema,
  problemDetailsSchema,
  renameSessionRequestSchema,
  renameSessionResponseSchema,
  resolvePermissionRequestSchema,
  runStreamEventSchema,
  sessionDetailResponseSchema,
  sessionTranscriptResponseSchema,
  setSessionModeRequestSchema,
  setSessionModeResponseSchema,
  startRunRequestSchema,
  steerRunRequestSchema,
} from "@mecatl-studio/contracts";
import { ProtocolError } from "@stacklok-oss/mecatl-sdk";
import { streamSSE } from "hono/streaming";
import type { ActivityLimits } from "../config.js";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import {
  type ActivityDelivery,
  type ChatService,
  isUnusableCursor,
  runError,
} from "../mecatl/chat.js";

const sessionParameters = z.object({
  sessionId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "sessionId" } }),
});

const runParameters = sessionParameters.extend({
  runId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "runId" } }),
});

const permissionParameters = runParameters.extend({
  askId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "askId" } }),
});

const authorizationParameters = sessionParameters.extend({
  authorizationId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "authorizationId" } }),
});

const unavailableResponse = {
  content: {
    "application/problem+json": {
      schema: problemDetailsSchema,
    },
  },
  description: "The Mecatl runtime is unavailable.",
} as const;

const errorResponse = {
  content: {
    "application/problem+json": {
      schema: problemDetailsSchema,
    },
  },
  description: "The request could not be completed.",
} as const;

const listSessionsRoute = createRoute({
  method: "get",
  operationId: "listSessions",
  path: "/api/v1/sessions",
  responses: {
    200: {
      content: { "application/json": { schema: listSessionsResponseSchema } },
      description: "The visible Mecatl chat sessions.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const createSessionRoute = createRoute({
  method: "post",
  operationId: "createSession",
  path: "/api/v1/sessions",
  request: {
    body: {
      content: { "application/json": { schema: createSessionRequestSchema } },
      required: true,
    },
  },
  responses: {
    201: {
      content: { "application/json": { schema: createSessionResponseSchema } },
      description: "The newly created Mecatl session.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const getTranscriptRoute = createRoute({
  method: "get",
  operationId: "getSessionTranscript",
  path: "/api/v1/sessions/{sessionId}/transcript",
  request: { params: sessionParameters },
  responses: {
    200: {
      content: { "application/json": { schema: sessionTranscriptResponseSchema } },
      description: "The authoritative session transcript.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const getSessionDetailRoute = createRoute({
  method: "get",
  operationId: "getSessionDetail",
  path: "/api/v1/sessions/{sessionId}",
  request: { params: sessionParameters },
  responses: {
    200: {
      content: { "application/json": { schema: sessionDetailResponseSchema } },
      description: "The authoritative session configuration and usage snapshot.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const renameSessionRoute = createRoute({
  method: "patch",
  operationId: "renameSession",
  path: "/api/v1/sessions/{sessionId}",
  request: {
    body: {
      content: { "application/json": { schema: renameSessionRequestSchema } },
      required: true,
    },
    params: sessionParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: renameSessionResponseSchema } },
      description: "The authoritative session title.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const deleteSessionRoute = createRoute({
  method: "delete",
  operationId: "deleteSession",
  path: "/api/v1/sessions/{sessionId}",
  request: { params: sessionParameters },
  responses: {
    204: { description: "The session was deleted." },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const startRunRoute = createRoute({
  method: "post",
  operationId: "startRun",
  path: "/api/v1/sessions/{sessionId}/runs",
  request: {
    body: {
      content: { "application/json": { schema: startRunRequestSchema } },
      required: true,
    },
    params: sessionParameters,
  },
  responses: {
    200: {
      content: { "text/event-stream": { schema: runStreamEventSchema } },
      description: "The Mecatl run event stream.",
    },
    500: errorResponse,
    503: unavailableResponse,
  },
});

const watchSessionActivityRoute = createRoute({
  method: "get",
  operationId: "watchSessionActivity",
  path: "/api/v1/sessions/{sessionId}/activity",
  request: {
    params: sessionParameters,
    query: z.object({
      /**
       * Resume exactly after this durable cursor. The `Last-Event-ID` request
       * header carries the same value and wins when both are present; this
       * parameter exists for clients that cannot set that header. It is
       * deliberately not named `cursor`: the client generator reads that name
       * as a pagination marker and emits an infinite-query wrapper, which
       * cannot type a Server-Sent Events response.
       */
      resumeFrom: z.string().max(1_024).optional(),
    }),
  },
  responses: {
    200: {
      content: { "text/event-stream": { schema: runStreamEventSchema } },
      description:
        "Bounded durable replay followed by live activity. Each event frame carries its cursor in the SSE id field.",
    },
    400: errorResponse,
    429: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const setSessionModeRoute = createRoute({
  method: "put",
  operationId: "setSessionMode",
  path: "/api/v1/sessions/{sessionId}/mode",
  request: {
    body: {
      content: { "application/json": { schema: setSessionModeRequestSchema } },
      required: true,
    },
    params: sessionParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: setSessionModeResponseSchema } },
      description: "The authoritative session permission mode.",
    },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const compactSessionRoute = createRoute({
  method: "post",
  operationId: "compactSession",
  path: "/api/v1/sessions/{sessionId}/compaction",
  request: { params: sessionParameters },
  responses: {
    200: {
      content: { "application/json": { schema: compactSessionResponseSchema } },
      description: "Whether manual compaction reduced the conversation.",
    },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const forkSessionRoute = createRoute({
  method: "post",
  operationId: "forkSession",
  path: "/api/v1/sessions/{sessionId}/fork",
  request: {
    body: {
      content: { "application/json": { schema: forkSessionRequestSchema } },
      required: true,
    },
    params: sessionParameters,
  },
  responses: {
    201: {
      content: { "application/json": { schema: forkSessionResponseSchema } },
      description: "A successor session forked onto the selected model and effort.",
    },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const clearSessionRoute = createRoute({
  method: "post",
  operationId: "clearSession",
  path: "/api/v1/sessions/{sessionId}/clear",
  request: { params: sessionParameters },
  responses: {
    201: {
      content: { "application/json": { schema: clearSessionResponseSchema } },
      description: "An empty-history successor session, replacing this one's conversation.",
    },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const retrySessionRoute = createRoute({
  method: "post",
  operationId: "retrySession",
  path: "/api/v1/sessions/{sessionId}/retry",
  request: { params: sessionParameters },
  responses: {
    200: {
      content: { "text/event-stream": { schema: runStreamEventSchema } },
      description: "The retry run event stream.",
    },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const cancelRunRoute = createRoute({
  method: "post",
  operationId: "cancelRun",
  path: "/api/v1/sessions/{sessionId}/runs/{runId}/cancel",
  request: { params: runParameters },
  responses: {
    204: { description: "Cancellation was sent to the active run." },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const steerRunRoute = createRoute({
  method: "post",
  operationId: "steerRun",
  path: "/api/v1/sessions/{sessionId}/runs/{runId}/steer",
  request: {
    body: {
      content: { "application/json": { schema: steerRunRequestSchema } },
      required: true,
    },
    params: runParameters,
  },
  responses: {
    204: { description: "The steering instruction was sent to the active run." },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const resolvePermissionRoute = createRoute({
  method: "post",
  operationId: "resolveRunPermission",
  path: "/api/v1/sessions/{sessionId}/runs/{runId}/permissions/{askId}",
  request: {
    body: {
      content: { "application/json": { schema: resolvePermissionRequestSchema } },
      required: true,
    },
    params: permissionParameters,
  },
  responses: {
    204: { description: "The permission decision was sent to the active run." },
    409: errorResponse,
    500: errorResponse,
    503: unavailableResponse,
  },
});

const authorizationPresentationRoute = createRoute({
  method: "get",
  operationId: "getAuthorizationPresentation",
  path: "/api/v1/sessions/{sessionId}/authorizations/{authorizationId}/presentation",
  request: { params: authorizationParameters },
  responses: {
    302: { description: "Redirect to a fresh external authorization page." },
    502: errorResponse,
    503: unavailableResponse,
  },
});

const recheckAuthorizationRoute = createRoute({
  method: "post",
  operationId: "recheckAuthorization",
  path: "/api/v1/sessions/{sessionId}/authorizations/{authorizationId}/recheck",
  request: { params: authorizationParameters },
  responses: {
    200: {
      content: { "text/event-stream": { schema: runStreamEventSchema } },
      description: "Authorization recheck and any continuation, in the run event stream.",
    },
    503: unavailableResponse,
  },
});

const cancelAuthorizationRoute = createRoute({
  method: "post",
  operationId: "cancelAuthorization",
  path: "/api/v1/sessions/{sessionId}/authorizations/{authorizationId}/cancel",
  request: { params: authorizationParameters },
  responses: {
    200: {
      content: { "text/event-stream": { schema: runStreamEventSchema } },
      description: "Authorization cancellation and any continuation, in the run event stream.",
    },
    503: unavailableResponse,
  },
});

/**
 * Bounds concurrent activity streams per session, per replica. Replay is the
 * expensive half of the activity route, so a browser that reattaches in a loop
 * is refused rather than allowed to pile durable reads on the daemon.
 */
function activityStreamGate(maxStreams: number) {
  const open = new Map<string, number>();
  return {
    acquire(sessionId: string) {
      const current = open.get(sessionId) ?? 0;
      if (current >= maxStreams) return false;
      open.set(sessionId, current + 1);
      return true;
    },
    release(sessionId: string) {
      const current = open.get(sessionId) ?? 0;
      if (current <= 1) open.delete(sessionId);
      else open.set(sessionId, current - 1);
    },
  };
}

export function registerChatRoutes(
  app: OpenAPIHono<AppEnv>,
  chat: ChatService | undefined,
  limits: ActivityLimits = { maxStreams: 4, replayMax: 2_000 },
) {
  const { maxStreams, replayMax } = limits;
  const streams = activityStreamGate(maxStreams);
  app.openapi(listSessionsRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    return context.json(await chat.listSessions(), 200);
  });

  app.openapi(createSessionRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    return context.json(await chat.createSession(context.req.valid("json")), 201);
  });

  app.openapi(getTranscriptRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { sessionId } = context.req.valid("param");
    return context.json(await chat.transcript(sessionId, context.req.raw.signal), 200);
  });

  app.openapi(getSessionDetailRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    return context.json(await chat.detail(sessionId, context.req.raw.signal), 200);
  });

  app.openapi(renameSessionRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { sessionId } = context.req.valid("param");
    const { title } = context.req.valid("json");
    return context.json(await chat.renameSession(sessionId, title), 200);
  });

  app.openapi(deleteSessionRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { sessionId } = context.req.valid("param");
    await chat.deleteSession(sessionId);
    return context.body(null, 204);
  });

  app.openapi(startRunRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { sessionId } = context.req.valid("param");
    const request = context.req.valid("json");

    return streamSSE(context, async (stream) => {
      const { controller, abort, unlink } = linkedAbort(context.req.raw.signal);
      stream.onAbort(abort);

      try {
        for await (const event of chat.run(sessionId, request, controller.signal)) {
          await stream.writeSSE({
            data: JSON.stringify(event),
            event: event.type,
          });
        }
      } catch (error) {
        const event = runError(error);
        await stream.writeSSE({ data: JSON.stringify(event), event: event.type });
      } finally {
        unlink();
      }
    });
  });

  app.openapi(watchSessionActivityRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    // `Last-Event-ID` is the standard SSE resume header and wins; the query
    // parameter serves clients that cannot set it.
    const cursor = context.req.header("last-event-id") || context.req.valid("query").resumeFrom;
    // A cursor is an opaque durable position. The SDK's `start` and `now`
    // sentinels would turn a resume into an unbounded or history-skipping
    // attach, and an oversized header is never a cursor Studio issued.
    if (cursor !== undefined && (cursor === "start" || cursor === "now" || cursor.length > 1_024)) {
      return invalidCursor(context);
    }

    if (!streams.acquire(sessionId)) {
      context.header("Retry-After", "5");
      return problem(
        context,
        429,
        "too_many_streams",
        "Too many activity streams",
        "This session already has the maximum number of concurrent activity streams.",
      );
    }

    const { controller, abort, unlink } = linkedAbort(context.req.raw.signal);
    const release = () => {
      unlink();
      streams.release(sessionId);
    };

    // Pull the first delivery before any SSE header is committed, so an
    // unusable cursor is an ordinary 400 instead of an error frame inside a
    // 200 stream.
    let iterator: AsyncIterator<ActivityDelivery> | undefined;
    let first: IteratorResult<ActivityDelivery>;
    try {
      // The call is inside the try as well: an async generator surfaces its
      // failure at `next()`, but a hand-written service may throw on call.
      iterator = chat
        .activity(sessionId, {
          ...(cursor === undefined ? {} : { cursor }),
          replayMax,
          signal: controller.signal,
        })
        [Symbol.asyncIterator]();
      first = await iterator.next();
    } catch (error) {
      release();
      if (isUnusableCursor(error)) return invalidCursor(context);
      // The client left while the first delivery was pending; nobody reads a reply.
      if (controller.signal.aborted) return context.body(null, 204);
      throw error;
    }

    return streamSSE(context, async (stream) => {
      stream.onAbort(abort);
      // Proxies drop an SSE response that stays silent; an idle session can
      // wait a long time for its next live event.
      const heartbeat = setInterval(() => {
        void stream.write(": keep-alive\n\n").catch(() => undefined);
      }, activityHeartbeatMs);
      const write = async ({ cursor: at, delivery }: ActivityDelivery) => {
        if (delivery === undefined) return;
        await stream.writeSSE({
          data: JSON.stringify(delivery),
          event: delivery.type,
          ...(at === undefined ? {} : { id: at }),
        });
      };
      try {
        for (let next = first; next.done !== true; next = await iterator.next()) {
          await write(next.value);
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          const delivery = runError(error);
          await stream.writeSSE({ data: JSON.stringify(delivery), event: delivery.type });
        }
      } finally {
        clearInterval(heartbeat);
        await iterator.return?.().catch(() => undefined);
        release();
      }
    });
  });

  app.openapi(setSessionModeRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    const { mode } = context.req.valid("json");
    return context.json(await chat.setMode(sessionId, mode), 200);
  });

  app.openapi(compactSessionRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    return context.json(await chat.compactSession(sessionId), 200);
  });

  app.openapi(forkSessionRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    return context.json(await chat.forkSession(sessionId, context.req.valid("json")), 201);
  });

  app.openapi(clearSessionRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    return context.json(await chat.clearSession(sessionId), 201);
  });

  app.openapi(retrySessionRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId } = context.req.valid("param");
    return streamSSE(context, async (stream) => {
      const { controller, abort, unlink } = linkedAbort(context.req.raw.signal);
      stream.onAbort(abort);

      try {
        for await (const delivery of chat.retry(sessionId, controller.signal)) {
          await stream.writeSSE({ data: JSON.stringify(delivery), event: delivery.type });
        }
      } catch (error) {
        const delivery = runError(error);
        await stream.writeSSE({ data: JSON.stringify(delivery), event: delivery.type });
      } finally {
        unlink();
      }
    });
  });

  app.openapi(cancelRunRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { runId, sessionId } = context.req.valid("param");
    if (!(await chat.cancelRun(sessionId, runId))) {
      return staleRun(context);
    }
    return context.body(null, 204);
  });

  app.openapi(steerRunRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { runId, sessionId } = context.req.valid("param");
    const { text } = context.req.valid("json");
    if (!(await chat.steer(sessionId, runId, text))) {
      return staleRun(context);
    }
    return context.body(null, 204);
  });

  app.openapi(resolvePermissionRoute, async (context) => {
    if (chat === undefined) {
      return unavailable(context);
    }
    const { askId, runId, sessionId } = context.req.valid("param");
    const { verdict } = context.req.valid("json");
    if (!(await chat.resolvePermission(sessionId, runId, askId, verdict))) {
      return staleRun(context);
    }
    return context.body(null, 204);
  });

  app.openapi(authorizationPresentationRoute, async (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId, authorizationId } = context.req.valid("param");
    let url: string;
    try {
      url = await chat.authorizationPresentation(
        sessionId,
        authorizationId,
        context.req.raw.signal,
      );
    } catch (error) {
      // The SDK reports a malformed daemon URL as a protocol error. It is an
      // upstream presentation failure, never a browser redirect or a 500.
      if (error instanceof ProtocolError) return invalidPresentation(context);
      throw error;
    }
    // The SDK rejects invalid presentation URLs. Keep the redirect boundary
    // strict even if a future ChatService implementation changes that promise.
    let destination: URL;
    try {
      destination = new URL(url);
    } catch {
      return invalidPresentation(context);
    }
    if (
      (destination.protocol !== "http:" && destination.protocol !== "https:") ||
      !destination.hostname
    ) {
      return invalidPresentation(context);
    }
    context.header("Cache-Control", "no-store");
    context.header("Referrer-Policy", "no-referrer");
    return context.redirect(destination.href, 302);
  });

  const streamAuthorization = (
    context: Parameters<typeof problem>[0],
    sessionId: string,
    authorizationId: string,
    operation: "recheck" | "cancel",
  ) =>
    streamSSE(context, async (stream) => {
      if (chat === undefined) return;
      const { controller, abort, unlink } = linkedAbort(context.req.raw.signal);
      stream.onAbort(abort);
      try {
        for await (const delivery of chat.authorizationFlow(
          sessionId,
          authorizationId,
          operation,
          controller.signal,
        )) {
          await stream.writeSSE({ data: JSON.stringify(delivery), event: delivery.type });
        }
      } catch (error) {
        const delivery = runError(error);
        await stream.writeSSE({ data: JSON.stringify(delivery), event: delivery.type });
      } finally {
        unlink();
      }
    });

  app.openapi(recheckAuthorizationRoute, (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId, authorizationId } = context.req.valid("param");
    return streamAuthorization(context, sessionId, authorizationId, "recheck");
  });

  app.openapi(cancelAuthorizationRoute, (context) => {
    if (chat === undefined) return unavailable(context);
    const { sessionId, authorizationId } = context.req.valid("param");
    return streamAuthorization(context, sessionId, authorizationId, "cancel");
  });
}

const activityHeartbeatMs = 15_000;

/**
 * An AbortController that follows the request signal, including one that
 * aborted before the listener was attached — `addEventListener` alone never
 * fires for an already-aborted signal.
 */
function linkedAbort(signal: AbortSignal) {
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (signal.aborted) controller.abort();
  else signal.addEventListener("abort", abort, { once: true });
  return { abort, controller, unlink: () => signal.removeEventListener("abort", abort) };
}

function invalidCursor(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    400,
    "invalid_cursor",
    "Unusable resume cursor",
    "This activity cursor is malformed, expired, or scoped to another stream; reattach without one.",
  );
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

function staleRun(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    409,
    "stale_run_control",
    "Run is no longer active",
    "This control belongs to a run that has already ended or is no longer connected.",
  );
}

function invalidPresentation(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    502,
    "invalid_authorization_presentation",
    "Authorization page unavailable",
    "The authorization page address was not a valid HTTP(S) URL.",
  );
}
