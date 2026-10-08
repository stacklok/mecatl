// SPDX-License-Identifier: Apache-2.0

import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";

const publicAsset = (name: string) => new URL(`../public/${name}`, import.meta.url);

async function pngSize(name: string): Promise<[number, number]> {
  const bytes = await readFile(publicAsset(name));
  expect(bytes.subarray(0, 8)).toEqual(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]));
  expect(bytes.toString("ascii", 12, 16)).toBe("IHDR");
  return [bytes.readUInt32BE(16), bytes.readUInt32BE(20)];
}

describe("Studio installation", () => {
  it("declares install metadata and serves each icon", async () => {
    const manifest = JSON.parse(await readFile(publicAsset("manifest.webmanifest"), "utf8"));
    expect(manifest).toMatchObject({
      name: "Mecatl Studio",
      short_name: "Studio",
      description: "The web workspace for the Mecatl agent harness",
      id: "/workspace/",
      start_url: "/workspace/chat",
      scope: "/",
      display: "standalone",
      background_color: "#03433e",
      theme_color: "#03433e",
    });
    expect(manifest.icons).toEqual([
      { src: "/icon-192.png", sizes: "192x192", type: "image/png" },
      { src: "/icon-512.png", sizes: "512x512", type: "image/png" },
      { src: "/icon-maskable-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
    ]);
    expect(manifest).not.toHaveProperty("share_target");

    for (const [name, size] of [
      ["icon-192.png", 192],
      ["icon-512.png", 512],
      ["icon-maskable-512.png", 512],
      ["apple-touch-icon.png", 180],
    ] as const) {
      expect(await pngSize(name)).toEqual([size, size]);
    }
    expect((await readFile(publicAsset("stacklok-favicon.png"))).length).toBeGreaterThan(0);

    const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
    expect(html).toContain('rel="manifest" href="/manifest.webmanifest"');
    expect(html).toContain('rel="apple-touch-icon" href="/apple-touch-icon.png"');
    expect(html).toContain('rel="icon" href="/stacklok-favicon.png"');
    expect(html).toContain('media="(prefers-color-scheme: light)" content="#03433e"');
    expect(html).toContain('media="(prefers-color-scheme: dark)" content="#02141b"');
    expect(html).toContain("width=device-width, initial-scale=1.0");
    expect(html).not.toContain("user-scalable=no");
  });
});
