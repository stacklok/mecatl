// SPDX-License-Identifier: Apache-2.0

import { fileURLToPath } from "node:url";
import { Hono } from "hono";
import { describe, expect, it } from "vitest";
import type { AppEnv } from "./env.js";
import { spaHandler } from "./static.js";

describe("Studio installation assets", () => {
  it("serves the manifest and icons without an SPA fallback for missing assets", async () => {
    const publicDir = fileURLToPath(new URL("../../../web/public/", import.meta.url));
    const app = new Hono<AppEnv>();
    app.use("*", spaHandler(publicDir));

    const manifest = await app.request("/manifest.webmanifest");
    expect(manifest.status).toBe(200);
    expect(manifest.headers.get("content-type")).toContain("application/manifest+json");
    expect((await manifest.json()).start_url).toBe("/workspace/chat");

    for (const icon of [
      "icon-192.png",
      "icon-512.png",
      "icon-maskable-512.png",
      "apple-touch-icon.png",
      "stacklok-favicon.png",
    ]) {
      const response = await app.request(`/${icon}`);
      expect(response.status).toBe(200);
      expect(response.headers.get("content-type")).toContain("image/png");
      expect((await response.arrayBuffer()).byteLength).toBeGreaterThan(0);
    }
    expect((await app.request("/missing.png")).status).toBe(404);
  });
});
