// SPDX-License-Identifier: Apache-2.0

import { createRoute, OpenAPIHono } from "@hono/zod-openapi";
import {
  healthResponseSchema,
  problemDetailsSchema,
  runtimeResponseSchema,
} from "@mecatl-studio/contracts";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import { HTTPException } from "hono/http-exception";
import type { AuthenticationService } from "./auth/service.js";
import type { ActivityLimits } from "./config.js";
import { requestBodyLimit } from "./http/body-limit.js";
import type { AppEnv } from "./http/env.js";
import { problem, sanitizeUpstreamDetail, validationDetail } from "./http/problem.js";
import {
  csrfCookieIssuer,
  hostAllowlist,
  rateLimiter,
  requestContext,
  type SecurityOptions,
  sameOriginMutations,
  securityHeaders,
} from "./http/security.js";
import { spaHandler } from "./http/static.js";
import { type Logger, silentLogger } from "./log.js";
import { type ChatService, createMecatlChatService } from "./mecatl/chat.js";
import { type MecatlRuntime, RuntimeNotReadyError } from "./mecatl/runtime.js";
import { createMecatlScheduleService, type ScheduleService } from "./mecatl/schedules.js";
import { registerAuthRoutes } from "./routes/auth.js";
import { registerChatRoutes } from "./routes/chat.js";
import { registerScheduleRoutes } from "./routes/schedules.js";

export const openApiInfo = {
  info: { title: "Mecatl Studio API", version: "1.0.0" },
  openapi: "3.1.0",
} as const;

const problemResponse = (description: string) =>
  ({
    content: { "application/problem+json": { schema: problemDetailsSchema } },
    description,
  }) as const;

const healthRoute = createRoute({
  method: "get",
  operationId: "getHealth",
  path: "/api/health",
  responses: {
    200: {
      content: { "application/json": { schema: healthResponseSchema } },
      description: "The Studio BFF is listening.",
    },
  },
});

const runtimeRoute = createRoute({
  method: "get",
  operationId: "getRuntime",
  path: "/api/v1/runtime",
  responses: {
    200: {
      content: { "application/json": { schema: runtimeResponseSchema } },
      description: "The connected Mecatl runtime and its capabilities.",
    },
    401: problemResponse("Sign in before using this deployment."),
    503: problemResponse("The Mecatl runtime is unavailable or not yet negotiated."),
  },
});

export interface AppDependencies {
  /** Bounds on durable activity replay; defaults are the config defaults. */
  readonly activity?: ActivityLimits;
  readonly authentication?: AuthenticationService;
  readonly chat?: ChatService;
  readonly schedules?: ScheduleService;
  readonly logger?: Logger;
  readonly runtime?: MecatlRuntime;
  /** How long a feature request waits for compatibility negotiation before `503`. */
  readonly readinessTimeoutMs?: number;
  readonly security?: SecurityOptions;
  /** Directory holding the built SPA; omitted in tests and OpenAPI generation. */
  readonly webDist?: string;
}

export function createApp(dependencies: AppDependencies = {}) {
  // Every route's request validation fails as RFC 9457 problem details, never
  // the validator's raw error object; a route may still install its own hook.
  const app = new OpenAPIHono<AppEnv>({
    defaultHook: (result, context) => {
      if (result.success) return;
      return problem(
        context,
        400,
        "invalid_request",
        "Invalid request",
        validationDetail(result.error.issues),
      );
    },
  });
  const logger = dependencies.logger ?? silentLogger;
  const security = dependencies.security ?? {};
  const { authentication, runtime } = dependencies;
  const readinessTimeoutMs = dependencies.readinessTimeoutMs ?? defaultReadinessTimeoutMs;

  for (const middleware of requestContext(security)) app.use(middleware);
  app.use(securityHeaders());
  app.use(hostAllowlist(security));
  app.use("/api/*", csrfCookieIssuer(security));
  const authLimiter = rateLimiter(security);
  app.use("/api/v1/auth/*", authLimiter);
  app.use("/oauth/callback", authLimiter);
  app.use("/api/v1/*", sameOriginMutations(security));
  app.use("/api/v1/*", requestBodyLimit());

  registerAuthRoutes(app, authentication, runtime);

  // AC3.8: with interactive login active, everything under /api/v1 except the
  // auth routes themselves needs a live session; /api/health never does.
  app.use("/api/v1/*", async (context, next) => {
    if (context.req.path.startsWith("/api/v1/auth/")) return next();
    if (authentication === undefined) return next();
    const resolution = await authentication.credential(context);
    if (resolution.status === "anonymous") {
      return problem(
        context,
        401,
        "unauthenticated",
        "Authentication required",
        "Sign in before using this Mecatl deployment.",
      );
    }
    if (resolution.status === "expired") {
      return problem(
        context,
        401,
        "session_expired",
        "Session expired",
        "Your session has expired; sign in again.",
      );
    }
    if (runtime === undefined) return next();
    return runtime.runWithCredential(resolution.credential.accessToken, next);
  });

  // AC2.11: a feature route answers only once compatibility is negotiated.
  // Capabilities come from the negotiated snapshot, so answering earlier would
  // report a supported feature as `supported: false` — a successful, cacheable
  // lie. The wait runs inside the request's credential context. /runtime keeps
  // its own non-blocking 503, and the auth routes never depend on it.
  app.use("/api/v1/*", async (context, next) => {
    if (runtime === undefined) return next();
    if (context.req.path === "/api/v1/runtime" || context.req.path.startsWith("/api/v1/auth/")) {
      return next();
    }
    if (await negotiated(runtime, readinessTimeoutMs)) return next();
    return runtimeUnavailable(context, "Compatibility negotiation with Mecatl is pending.");
  });

  app.openapi(healthRoute, (context) =>
    context.json({ service: "mecatl-studio" as const, status: "ok" as const }, 200),
  );

  app.openapi(runtimeRoute, (context) => {
    if (runtime === undefined) return runtimeUnavailable(context, "The BFF has no Mecatl runtime.");
    try {
      return context.json(runtime.snapshot(), 200);
    } catch (error) {
      if (error instanceof RuntimeNotReadyError) {
        void runtime.ready().catch(() => undefined);
        return runtimeUnavailable(context, "Compatibility negotiation with Mecatl is pending.");
      }
      throw error;
    }
  });

  const chat =
    dependencies.chat ??
    (runtime === undefined ? undefined : createMecatlChatService(runtime.client));
  registerChatRoutes(app, chat, dependencies.activity);
  // Capability read LIVE from the negotiated snapshot: false until negotiation
  // completes or when the deployment has scheduling off.
  const schedulingSupported = () => {
    if (runtime === undefined) return false;
    try {
      return runtime.snapshot().capabilities.scheduling;
    } catch {
      return false;
    }
  };
  const schedules =
    dependencies.schedules ??
    (runtime === undefined
      ? undefined
      : createMecatlScheduleService(runtime.client, schedulingSupported));
  registerScheduleRoutes(app, schedules);

  if (dependencies.webDist !== undefined) app.use("*", spaHandler(dependencies.webDist));

  app.notFound((context) =>
    problem(context, 404, "not_found", "Not found", "No route matches this request."),
  );

  app.onError((error, context) => {
    if (error instanceof MecatlError) {
      if (
        authentication !== undefined &&
        (error.code === "unauthenticated" || error.code === "authentication")
      ) {
        // AC3.9: an upstream rejection of a session we believed valid ends the session.
        authentication.clear(context);
        return problem(
          context,
          401,
          "session_expired",
          "Session expired",
          "Mecatl rejected the current session; sign in again.",
        );
      }
      return problem(
        context,
        errorStatus(error),
        error.code,
        "Mecatl request failed",
        sanitizeUpstreamDetail(error.message),
      );
    }
    if (error instanceof HTTPException) {
      return problem(
        context,
        error.status,
        "http_error",
        "Request failed",
        error.message || "The request could not be completed.",
      );
    }
    logger.error("http.unhandled", {
      message: error instanceof Error ? error.message : String(error),
      path: context.req.path,
      requestId: context.get("requestId"),
    });
    return problem(
      context,
      500,
      "internal_error",
      "Internal server error",
      "The server could not complete the request.",
    );
  });

  return app;
}

function runtimeUnavailable(context: Parameters<typeof problem>[0], detail: string) {
  return problem(context, 503, "runtime_unavailable", "Mecatl runtime unavailable", detail);
}

const defaultReadinessTimeoutMs = 10_000;

/** Resolves true once the runtime has negotiated, false on failure or timeout. */
async function negotiated(runtime: MecatlRuntime, timeoutMs: number): Promise<boolean> {
  try {
    runtime.snapshot();
    return true;
  } catch (error) {
    if (!(error instanceof RuntimeNotReadyError)) throw error;
  }
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<false>((resolve) => {
    timer = setTimeout(() => resolve(false), timeoutMs);
  });
  try {
    return await Promise.race([
      runtime.ready().then(
        () => true,
        () => false,
      ),
      timeout,
    ]);
  } finally {
    clearTimeout(timer);
  }
}

/**
 * `MecatlError.status` is an HTTP status only when the transport is HTTP; over
 * gRPC it carries a Connect code, whose small integers collide with nothing
 * meaningful in HTTP space. Map the stable string code first, then fall back
 * to the transport's own status: the HTTP status for HTTP responses, the
 * Connect code's HTTP equivalent for gRPC. A domain code the daemon adds later
 * therefore keeps its gRPC class instead of degrading to 500.
 */
const statusByCode: Partial<Record<string, ProblemStatus>> = {
  authentication: 401,
  conflict: 409,
  cursor_expired: 400,
  cursor_malformed: 400,
  cursor_scope: 400,
  draining: 503,
  failed_precondition: 409,
  invalid_argument: 400,
  not_found: 404,
  request_too_large: 413,
  resource_exhausted: 429,
  session_leased_elsewhere: 409,
  session_not_found: 404,
  stale_run_control: 409,
  team_not_found: 404,
  transport: 503,
  unauthenticated: 401,
  unimplemented: 501,
};

/** Connect code → HTTP status, per the Connect protocol's own mapping. */
const statusByConnectCode: Partial<Record<number, ProblemStatus>> = {
  3: 400, // invalid_argument
  4: 503, // deadline_exceeded
  5: 404, // not_found
  6: 409, // already_exists
  8: 429, // resource_exhausted
  9: 409, // failed_precondition
  10: 409, // aborted
  11: 400, // out_of_range
  12: 501, // unimplemented
  14: 503, // unavailable
  16: 401, // unauthenticated
};

type ProblemStatus = 400 | 401 | 404 | 409 | 410 | 412 | 413 | 429 | 500 | 501 | 503;

function errorStatus(error: MecatlError): ProblemStatus {
  const byCode = statusByCode[error.code];
  if (byCode !== undefined) return byCode;
  if (error.transport === "grpc" && error.status !== undefined) {
    return statusByConnectCode[error.status] ?? 500;
  }
  if (error.transport === "http") {
    switch (error.status) {
      case 400:
      case 401:
      case 404:
      case 409:
      case 410:
      case 412:
      case 413:
      case 429:
      case 500:
      case 501:
      case 503:
        return error.status;
      default:
        break;
    }
  }
  return 500;
}
