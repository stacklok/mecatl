// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { createRoute, type OpenAPIHono, z } from "@hono/zod-openapi";
import { authSessionResponseSchema, problemDetailsSchema } from "@mecatl-studio/contracts";
import type { Context } from "hono";
import { AuthenticationError, type AuthenticationService } from "../auth/service.js";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import type { MecatlRuntime } from "../mecatl/runtime.js";

const problemResponse = {
  content: { "application/problem+json": { schema: problemDetailsSchema } },
  description: "The authentication operation could not be completed.",
} as const;

const redirectResponse = {
  description: "Continue the browser authentication flow.",
  headers: z.object({ Location: z.string() }),
} as const;

const sessionRoute = createRoute({
  method: "get",
  operationId: "getAuthSession",
  path: "/api/v1/auth/session",
  responses: {
    200: {
      content: { "application/json": { schema: authSessionResponseSchema } },
      description: "How this deployment identifies callers, and this browser's own state.",
    },
  },
});

const loginRoute = createRoute({
  method: "get",
  operationId: "startAuthLogin",
  path: "/api/v1/auth/login",
  request: {
    query: z.object({
      // The contract's public name; see docs/acceptance/studio-bootstrap.md.
      flow: z.enum(["popup"]).optional(),
      return_to: z.string().optional(),
    }),
  },
  responses: {
    302: redirectResponse,
    400: problemResponse,
    409: problemResponse,
    503: problemResponse,
  },
});

const callbackRoute = createRoute({
  method: "get",
  operationId: "completeAuthLogin",
  path: "/api/v1/auth/callback",
  responses: {
    200: {
      content: { "text/html": { schema: z.string() } },
      description: "A popup completion page that reports only the authentication result.",
    },
    302: redirectResponse,
    400: problemResponse,
    401: problemResponse,
    409: problemResponse,
    500: problemResponse,
    503: problemResponse,
  },
});

const logoutRoute = createRoute({
  method: "post",
  operationId: "logoutAuthSession",
  path: "/api/v1/auth/logout",
  responses: {
    204: { description: "The browser session was cleared." },
    403: problemResponse,
  },
});

export function registerAuthRoutes(
  app: OpenAPIHono<AppEnv>,
  authentication: AuthenticationService | undefined,
  runtime: MecatlRuntime | undefined,
) {
  app.openapi(sessionRoute, async (context) => {
    context.header("Cache-Control", "private, no-store");
    if (authentication === undefined) {
      return context.json(
        {
          mode: runtime?.authMode === "static" ? ("static" as const) : ("none" as const),
          status: "disabled" as const,
        },
        200,
      );
    }
    const resolution = await authentication.credential(context);
    if (resolution.status !== "authenticated") {
      return context.json({ mode: "oidc" as const, status: "anonymous" as const }, 200);
    }
    const { subject, email } = resolution.credential;
    if (subject === undefined || subject === "") {
      authentication.clear(context);
      return context.json({ mode: "oidc" as const, status: "anonymous" as const }, 200);
    }
    return context.json(
      {
        account: accountKey(subject),
        ...(email === undefined ? {} : { email }),
        mode: "oidc" as const,
        status: "authenticated" as const,
      },
      200,
    );
  });

  app.openapi(loginRoute, async (context) => {
    if (authentication === undefined) return authenticationDisabled(context);
    try {
      const location = await authentication.startLogin(
        context,
        context.req.valid("query").return_to,
        context.req.valid("query").flow,
      );
      return context.redirect(location, 302);
    } catch (error) {
      return authenticationFailure(context, error, 503);
    }
  });

  const completeLogin = async (context: Context<AppEnv>) => {
    if (authentication === undefined || runtime === undefined) {
      return authenticationDisabled(context);
    }
    let popup = false;
    try {
      const result = await authentication.completeLogin(context);
      popup = result.flow === "popup";
      await runtime.verifyCredential(result.credential.accessToken);
      await authentication.save(context, result.credential);
      // Only now is the login complete: verified upstream and stored locally.
      authentication.noteLoginComplete(context);
      return popup ? popupResult(context, "success") : context.redirect(result.returnTo, 302);
    } catch (error) {
      if (error instanceof AuthenticationError && error.popup) popup = true;
      // A callback with no matching transaction is exactly what a forged
      // cross-site link produces; it must not sign the user out of a session
      // they already hold. completeLogin has dropped the transaction cookie.
      const forgeable =
        error instanceof AuthenticationError && error.code === "login_state_mismatch";
      if (!forgeable) authentication.clear(context);
      if (error instanceof AuthenticationError) {
        // completeLogin audits the failures it detects itself; anything raised
        // after the exchange (verification, sealing) is recorded here.
        if (error.code !== "login_state_mismatch") {
          authentication.noteLoginFailure(context, error.code);
        }
        return popup ? popupResult(context, "failure") : authenticationFailure(context, error, 400);
      }
      authentication.noteLoginFailure(context, "mecatl_credential_rejected");
      return popup
        ? popupResult(context, "failure")
        : problem(
            context,
            401,
            "mecatl_credential_rejected",
            "Login rejected",
            "Mecatl rejected the credential returned by the identity provider.",
          );
    }
  };

  app.openapi(callbackRoute, completeLogin);
  // The mecatui loopback callback alias used by local development (AC3.4).
  app.get("/oauth/callback", completeLogin);
  app.get("/api/v1/auth/callback.js", (context) => {
    context.header("Cache-Control", "private, no-store");
    context.header("Referrer-Policy", "no-referrer");
    return context.body(popupCallbackScript, 200, {
      "Content-Type": "text/javascript; charset=utf-8",
    });
  });

  app.openapi(logoutRoute, async (context) => {
    await authentication?.logout(context);
    return context.body(null, 204);
  });
}

const popupCallbackScript = `(() => {
  const result = document.documentElement.dataset.result;
  if (result === "success" || result === "failure") {
    window.opener?.postMessage({ type: "studio.auth.result", result }, window.location.origin);
  }
  window.close();
})();`;

function popupResult(context: Context<AppEnv>, result: "success" | "failure"): Response {
  context.header("Cache-Control", "private, no-store");
  context.header("Referrer-Policy", "no-referrer");
  return context.html(
    `<!doctype html><html lang="en" data-result="${result}"><head><meta charset="utf-8"><title>Studio sign-in</title><script src="/api/v1/auth/callback.js" defer></script></head><body><p>You can return to Studio. If this window stays open, close it manually.</p></body></html>`,
    200,
  );
}

function authenticationDisabled(context: Context<AppEnv>) {
  return problem(
    context,
    409,
    "authentication_disabled",
    "Authentication disabled",
    "This Mecatl connection does not advertise interactive authentication.",
  );
}

function authenticationFailure(
  context: Context<AppEnv>,
  error: unknown,
  fallbackStatus: 400 | 503,
) {
  if (error instanceof AuthenticationError) {
    const status =
      error.code === "session_too_large"
        ? 500
        : error.code.includes("discovery") ||
            error.code === "pkce_unsupported" ||
            error.code === "login_unconfigured"
          ? 503
          : 400;
    return problem(context, status, error.code, "Authentication failed", error.message);
  }
  return problem(
    context,
    fallbackStatus,
    "authentication_failed",
    "Authentication failed",
    "The browser login could not be completed.",
  );
}

/**
 * An opaque key for the signed-in identity: stable for one subject, and not
 * the subject itself, so the browser can tell accounts apart without holding
 * an issuer identifier.
 */
export function accountKey(subject: string): string {
  return createHash("sha256")
    .update(`mecatl-studio-account\0${subject}`)
    .digest("base64url")
    .slice(0, 22);
}
