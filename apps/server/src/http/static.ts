// SPDX-License-Identifier: Apache-2.0

import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { extname, normalize, resolve, sep } from "node:path";
import { Readable } from "node:stream";
import type { MiddlewareHandler } from "hono";
import { getMimeType } from "hono/utils/mime";
import type { AppEnv } from "./env.js";
import { problem } from "./problem.js";

/**
 * AC4.1/AC4.2: serves the built SPA from one origin. Assets under `/assets/`
 * are content-hashed by Vite and immutable; `index.html` is never cached; a
 * path with a file extension that matches nothing is a real `404` so a broken
 * bundle cannot hide behind the SPA fallback. `/api` never reaches here.
 */
export function spaHandler(distDir: string): MiddlewareHandler<AppEnv> {
  const root = resolve(distDir);
  const indexPath = resolve(root, "index.html");
  return async (context, next) => {
    if (context.req.path === "/api" || context.req.path.startsWith("/api/")) return next();
    if (context.req.method !== "GET" && context.req.method !== "HEAD") return next();

    const requested = decodeURIComponent(context.req.path);
    const candidate = resolve(root, `.${normalize(requested)}`);
    if (candidate !== root && !candidate.startsWith(`${root}${sep}`)) {
      return problem(context, 404, "not_found", "Not found", "No such asset.");
    }

    const file = await fileInfo(candidate);
    if (file !== undefined) {
      const immutable = requested.startsWith("/assets/");
      return respondWithFile(
        context,
        candidate,
        file.size,
        immutable ? "public, max-age=31536000, immutable" : "no-cache",
      );
    }
    if (extname(requested) !== "") {
      return problem(context, 404, "not_found", "Not found", "No such asset.");
    }
    const index = await fileInfo(indexPath);
    if (index === undefined) return next();
    return respondWithFile(context, indexPath, index.size, "no-store");
  };
}

async function fileInfo(path: string): Promise<{ size: number } | undefined> {
  try {
    const info = await stat(path);
    return info.isFile() ? { size: info.size } : undefined;
  } catch {
    return undefined;
  }
}

function respondWithFile(
  context: Parameters<MiddlewareHandler<AppEnv>>[0],
  path: string,
  size: number,
  cacheControl: string,
): Response {
  const headers = new Headers({
    "Cache-Control": cacheControl,
    "Content-Length": String(size),
    "Content-Type": getMimeType(path) ?? "application/octet-stream",
  });
  if (context.req.method === "HEAD") return new Response(null, { headers });
  const body = Readable.toWeb(createReadStream(path)) as ReadableStream<Uint8Array>;
  return new Response(body, { headers });
}
