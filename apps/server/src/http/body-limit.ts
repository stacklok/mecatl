// SPDX-License-Identifier: Apache-2.0

import type { MiddlewareHandler } from "hono";
import { bodyLimit } from "hono/body-limit";
import type { AppEnv } from "./env.js";
import { problem } from "./problem.js";

const mebibyte = 1024 * 1024;

/**
 * The largest body any ordinary `/api/v1` request may send: a prompt or
 * schedule text of 1,000,000 characters is at most 4 MB of UTF-8.
 */
export const defaultBodyLimitBytes = 8 * mebibyte;

/**
 * A run carries up to 20 MiB of base64 images plus its prompt, so the run
 * routes get a larger bound. Nothing is buffered past either bound, so an
 * oversized body is refused before JSON parsing or schema validation reads it.
 */
export const runBodyLimitBytes = 32 * mebibyte;

const runRoute = /^\/api\/v1\/sessions\/[^/]+\/runs$/;

export function requestBodyLimit(): MiddlewareHandler<AppEnv> {
  const tooLarge = (bytes: number): MiddlewareHandler<AppEnv> =>
    bodyLimit({
      maxSize: bytes,
      onError: (context) =>
        problem(
          context,
          413,
          "request_too_large",
          "Request too large",
          `The request body exceeds ${Math.round(bytes / mebibyte)} MiB.`,
        ),
    }) as MiddlewareHandler<AppEnv>;
  const ordinary = tooLarge(defaultBodyLimitBytes);
  const run = tooLarge(runBodyLimitBytes);
  return (context, next) => (runRoute.test(context.req.path) ? run : ordinary)(context, next);
}
