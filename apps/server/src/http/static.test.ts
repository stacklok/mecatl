// SPDX-License-Identifier: Apache-2.0

import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { createApp } from "../app.js";
import { fakeRuntime } from "../testing/fakes.js";

function buildDist(): string {
  const dist = mkdtempSync(join(tmpdir(), "studio-dist-"));
  writeFileSync(join(dist, "index.html"), "<!doctype html><title>Studio</title>");
  mkdirSync(join(dist, "assets"));
  writeFileSync(join(dist, "assets", "index-abc123.js"), "console.log('studio');");
  writeFileSync(join(dist, "favicon.svg"), "<svg/>");
  return dist;
}

describe("SPA serving", () => {
  it("unknown non-API paths serve the SPA index and unknown API paths return 404 problem details", async () => {
    const app = createApp({ runtime: fakeRuntime(), webDist: buildDist() });

    for (const path of ["/", "/workspace", "/workspace/settings/identity", "/deep/link"]) {
      const response = await app.request(path);
      expect(response.status).toBe(200);
      expect(response.headers.get("content-type")).toContain("text/html");
      await expect(response.text()).resolves.toContain("<title>Studio</title>");
    }

    const api = await app.request("/api/v1/does-not-exist");
    expect(api.status).toBe(404);
    expect(api.headers.get("content-type")).toContain("application/problem+json");
    await expect(api.json()).resolves.toMatchObject({ code: "not_found" });

    const bareApi = await app.request("/api");
    expect(bareApi.status).toBe(404);
    expect(bareApi.headers.get("content-type")).toContain("application/problem+json");

    // Path traversal never escapes the dist directory.
    const traversal = await app.request("/../../etc/passwd");
    expect([200, 404]).toContain(traversal.status);
    if (traversal.status === 200) {
      await expect(traversal.text()).resolves.toContain("<title>Studio</title>");
    }
  });

  it("index.html is no-store, hashed assets are immutable, and extension paths without an asset are 404", async () => {
    const app = createApp({ runtime: fakeRuntime(), webDist: buildDist() });

    const index = await app.request("/workspace");
    expect(index.headers.get("cache-control")).toBe("no-store");
    const explicit = await app.request("/index.html");
    expect(explicit.status).toBe(200);

    const asset = await app.request("/assets/index-abc123.js");
    expect(asset.status).toBe(200);
    expect(asset.headers.get("cache-control")).toBe("public, max-age=31536000, immutable");
    expect(asset.headers.get("content-type")).toContain("javascript");

    const favicon = await app.request("/favicon.svg");
    expect(favicon.status).toBe(200);
    expect(favicon.headers.get("cache-control")).toBe("no-cache");

    const missingAsset = await app.request("/assets/index-old.js");
    expect(missingAsset.status).toBe(404);
    expect(missingAsset.headers.get("content-type")).toContain("application/problem+json");
    const missingImage = await app.request("/logo.png");
    expect(missingImage.status).toBe(404);

    const head = await app.request("/assets/index-abc123.js", { method: "HEAD" });
    expect(head.status).toBe(200);
    expect(await head.text()).toBe("");

    // Without a dist directory the BFF is API-only.
    const apiOnly = await createApp({ runtime: fakeRuntime() }).request("/workspace");
    expect(apiOnly.status).toBe(404);
  });
});
