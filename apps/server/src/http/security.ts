// SPDX-License-Identifier: Apache-2.0

import { randomBytes } from "node:crypto";
import type { Context, MiddlewareHandler } from "hono";
import { getCookie, setCookie } from "hono/cookie";
import { requestId } from "hono/request-id";
import { secureHeaders } from "hono/secure-headers";
import type { RateLimitConfig } from "../config.js";
import type { AppEnv } from "./env.js";
import { problem } from "./problem.js";

export const csrfCookie = "studio_csrf";
export const csrfHeader = "X-Studio-CSRF";

export interface SecurityOptions {
  readonly now?: () => number;
  /** Browser-facing origin; when unset the request's own `scheme://host` is the origin. */
  readonly publicUrl?: URL;
  readonly rateLimit?: RateLimitConfig;
  readonly trustedProxyHops?: number;
}

const mutatingMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/** `requestId` + client-address resolution; first in the chain so audit records can cite both. */
export function requestContext(options: SecurityOptions): MiddlewareHandler<AppEnv>[] {
  const hops = options.trustedProxyHops ?? 0;
  const resolveAddress: MiddlewareHandler<AppEnv> = async (context, next) => {
    context.set("clientAddress", clientAddress(context, hops));
    await next();
  };
  return [requestId() as MiddlewareHandler<AppEnv>, resolveAddress];
}

/**
 * AC3.11: Hono's secure-header set plus a CSP fitted to the Vite bundle.
 * `'unsafe-inline'` styles are the one concession Tailwind needs; scripts are
 * `'self'` only and there is no `'unsafe-eval'`. No CORS middleware exists
 * anywhere in Studio, so no response ever carries `Access-Control-Allow-Origin`.
 */
export function securityHeaders(): MiddlewareHandler<AppEnv> {
  const headers = secureHeaders({
    crossOriginOpenerPolicy: "same-origin",
    contentSecurityPolicy: {
      baseUri: ["'self'"],
      connectSrc: ["'self'"],
      defaultSrc: ["'self'"],
      fontSrc: ["'self'", "data:"],
      formAction: ["'self'"],
      frameAncestors: ["'none'"],
      imgSrc: ["'self'", "data:", "blob:"],
      objectSrc: ["'none'"],
      scriptSrc: ["'self'"],
      styleSrc: ["'self'", "'unsafe-inline'"],
    },
    referrerPolicy: "same-origin",
  }) as MiddlewareHandler<AppEnv>;
  return async (context, next) => {
    await headers(context, next);
    const path = context.req.path;
    const callback = path === "/api/v1/auth/callback" || path === "/oauth/callback";
    const html = context.res.headers.get("content-type")?.startsWith("text/html") === true;
    if (
      callback ||
      path === "/api/v1/auth/callback.js" ||
      (path.startsWith("/api/v1/sessions/") &&
        path.includes("/authorizations/") &&
        path.endsWith("/presentation"))
    ) {
      context.header("Referrer-Policy", "no-referrer");
    }
    if (callback && html) {
      // The popup returns from an issuer document with COOP unsafe-none.
      // Applying allow-popups to the callback would sever its opener here.
      context.header("Cross-Origin-Opener-Policy", "unsafe-none");
    } else if (
      html &&
      !path.startsWith("/api/") &&
      !callback &&
      (path === "/index.html" || !/\.[^/]+$/u.test(path))
    ) {
      // Only the served SPA document needs to retain its popup opener across
      // the issuer hop. API responses and static assets keep same-origin.
      context.header("Cross-Origin-Opener-Policy", "same-origin-allow-popups");
    }
  };
}

/** Issues the double-submit CSRF cookie on safe requests when the browser has none yet. */
export function csrfCookieIssuer(options: SecurityOptions): MiddlewareHandler<AppEnv> {
  return async (context, next) => {
    await next();
    if (mutatingMethods.has(context.req.method)) return;
    if (getCookie(context, csrfCookie) !== undefined) return;
    setCookie(context, csrfCookie, randomBytes(32).toString("base64url"), {
      httpOnly: false,
      path: "/",
      sameSite: "Lax",
      secure: secureCookies(context, options),
    });
  };
}

/**
 * AC3.16: the DNS-rebinding guard. A request whose `Host` is not the host of
 * `STUDIO_PUBLIC_URL` (or, with no public URL, not a loopback host) is refused
 * before any route runs, so a hostile page that rebinds its own name to this
 * listener cannot read responses or pass the same-origin check below.
 * `/api/health` stays reachable because probes address a pod by its IP.
 */
export function hostAllowlist(options: SecurityOptions): MiddlewareHandler<AppEnv> {
  return async (context, next) => {
    if (context.req.path === "/api/health") return next();
    const host = context.req.header("host") ?? new URL(context.req.url).host;
    if (allowedHost(host, options.publicUrl)) return next();
    return problem(
      context,
      421,
      "host_not_allowed",
      "Host not allowed",
      options.publicUrl === undefined
        ? "Without STUDIO_PUBLIC_URL, Studio answers only on a loopback host."
        : "The request Host does not match STUDIO_PUBLIC_URL.",
    );
  };
}

/**
 * AC3.12: state-changing `/api/v1` requests must be same-origin AND carry the
 * double-submit token. An `Origin`, when present, must equal the configured
 * origin; without one, Fetch Metadata must say `same-origin`, and a present
 * `Sec-Fetch-Site` must never say anything else. SameSite=Lax on the cookies
 * is the first layer; this is the second.
 */
export function sameOriginMutations(options: SecurityOptions): MiddlewareHandler<AppEnv> {
  return async (context, next) => {
    if (!mutatingMethods.has(context.req.method)) return next();
    const expected = expectedOrigin(context, options);
    const fetchSite = context.req.header("sec-fetch-site");
    const origin = context.req.header("origin");
    const sameOrigin =
      (fetchSite === undefined || fetchSite === "same-origin") &&
      (origin === undefined ? fetchSite === "same-origin" : origin === expected);
    if (!sameOrigin) {
      return problem(
        context,
        403,
        "cross_site_request",
        "Cross-site request rejected",
        "State-changing requests must originate from the Studio origin.",
      );
    }
    const token = context.req.header(csrfHeader);
    const cookie = getCookie(context, csrfCookie);
    if (token === undefined || cookie === undefined || token === "" || token !== cookie) {
      return problem(
        context,
        403,
        "cross_site_request",
        "Cross-site request rejected",
        `The ${csrfHeader} header must equal the ${csrfCookie} cookie.`,
      );
    }
    return next();
  };
}

/**
 * The authorization presentation GET redirects to a fresh authorization
 * URL. SameSite=Lax also sends the session cookie on a cross-site top-level
 * navigation, so this browser-only route requires same-origin Fetch Metadata.
 */
export function sameOriginPresentation(options: SecurityOptions): MiddlewareHandler<AppEnv> {
  return async (context, next) => {
    if (context.req.method !== "GET") return next();
    const origin = context.req.header("origin");
    if (
      context.req.header("sec-fetch-site") !== "same-origin" ||
      (origin !== undefined && origin !== expectedOrigin(context, options))
    ) {
      context.header("Cache-Control", "no-store");
      return problem(
        context,
        403,
        "cross_site_request",
        "Cross-site request rejected",
        "Authorization pages must be opened from the Studio origin.",
      );
    }
    return next();
  };
}

/**
 * AC3.13: an in-process fixed-window limiter keyed by client address. It is
 * per replica by design; a deployment that needs a shared budget puts one at
 * the Ingress.
 */
export function rateLimiter(options: SecurityOptions): MiddlewareHandler<AppEnv> {
  const limit = options.rateLimit ?? { max: 20, windowMs: 60_000 };
  const limitRequests = fixedWindowLimiter(
    options,
    limit,
    "Too many authentication requests from this client; retry later.",
  );
  return (context, next) => {
    // The SPA polls the session on every load; limiting it would lock out
    // ordinary use long before it slowed down an attacker.
    if (context.req.method === "GET" && context.req.path === "/api/v1/auth/session") return next();
    return limitRequests(context, next);
  };
}

/** A separate budget keeps anonymous status polling from consuming login attempts. */
export function statusRateLimiter(options: SecurityOptions): MiddlewareHandler<AppEnv> {
  const limitRequests = fixedWindowLimiter(
    options,
    { max: 120, windowMs: 60_000 },
    "Too many public status requests from this client; retry later.",
    true,
  );
  return (context, next) => (context.req.method === "GET" ? limitRequests(context, next) : next());
}

function fixedWindowLimiter(
  options: SecurityOptions,
  limit: RateLimitConfig,
  detail: string,
  noStore = false,
): MiddlewareHandler<AppEnv> {
  const now = options.now ?? Date.now;
  const windows = new Map<string, { count: number; startedAt: number }>();
  return async (context, next) => {
    const address = context.get("clientAddress");
    const at = now();
    if (windows.size > 10_000) {
      for (const [key, window] of windows) {
        if (at - window.startedAt >= limit.windowMs) windows.delete(key);
      }
    }
    const window = windows.get(address);
    if (window === undefined || at - window.startedAt >= limit.windowMs) {
      windows.set(address, { count: 1, startedAt: at });
      return next();
    }
    window.count += 1;
    if (window.count > limit.max) {
      const retryAfter = Math.max(1, Math.ceil((window.startedAt + limit.windowMs - at) / 1_000));
      context.header("Retry-After", String(retryAfter));
      if (noStore) context.header("Cache-Control", "private, no-store");
      return problem(context, 429, "rate_limited", "Too many requests", detail);
    }
    return next();
  };
}

/**
 * AC3.14: with zero trusted hops the socket peer is the client and
 * `X-Forwarded-For` is ignored; with N hops the N-th right-most entry is.
 */
export function clientAddress(context: Context<AppEnv>, trustedProxyHops: number): string {
  const peer = socketAddress(context);
  if (trustedProxyHops <= 0) return peer;
  const forwarded = (context.req.header("x-forwarded-for") ?? "")
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "");
  const candidate = forwarded[forwarded.length - trustedProxyHops];
  return candidate === undefined || candidate.length > 64 ? peer : candidate;
}

export function expectedOrigin(context: Context<AppEnv>, options: SecurityOptions): string {
  if (options.publicUrl !== undefined) return options.publicUrl.origin;
  const url = new URL(context.req.url);
  const host = context.req.header("host") ?? url.host;
  return `${url.protocol}//${host}`;
}

export function secureCookies(context: Context<AppEnv>, options: SecurityOptions): boolean {
  if (options.publicUrl !== undefined) return options.publicUrl.protocol === "https:";
  return new URL(context.req.url).protocol === "https:";
}

const loopbackHostnames = new Set(["localhost", "[::1]"]);

function allowedHost(host: string, publicUrl: URL | undefined): boolean {
  if (host === "" || /[\s/\\?#@]/.test(host)) return false;
  let parsed: URL;
  try {
    parsed = new URL(`${publicUrl?.protocol ?? "http:"}//${host}`);
  } catch {
    return false;
  }
  if (publicUrl !== undefined) return parsed.host === publicUrl.host;
  return loopbackHostnames.has(parsed.hostname) || /^127(?:\.\d{1,3}){3}$/.test(parsed.hostname);
}

function socketAddress(context: Context<AppEnv>): string {
  const env = context.env as { incoming?: { socket?: { remoteAddress?: string } } } | undefined;
  return env?.incoming?.socket?.remoteAddress ?? "unknown";
}
