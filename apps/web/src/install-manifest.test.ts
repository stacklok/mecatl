// SPDX-License-Identifier: Apache-2.0

import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";
import { consumeChatSeed } from "./features/chat/chat-seed";

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
    // Shared text opens a chat draft through the chat seed (see below).
    expect(manifest.share_target).toEqual({
      action: "/workspace/chat",
      method: "GET",
      params: { text: "prompt" },
    });

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

  it("hands shared text to the chat seed, sanitized and consumed", async () => {
    const manifest = JSON.parse(await readFile(publicAsset("manifest.webmanifest"), "utf8"));
    const target = manifest.share_target as {
      action: string;
      method: string;
      params: Record<string, string>;
    };
    // GET share targets need no enctype, and the action stays inside the app's scope.
    expect(target.method).toBe("GET");
    expect(target.action.startsWith(manifest.scope)).toBe(true);
    // Only the seed's prompt is requested: a share never asks to send, and no
    // parameter is left for the chat route to ignore in the address bar.
    expect(Object.values(target.params)).toEqual(["prompt"]);

    const shared: Record<string, string> = {
      text: ` Summarize\u0000 this ${"x".repeat(40_000)}😀`,
      title: "Ignored title",
      url: "https://example.com/ignored",
    };
    // The browser builds the action URL from the shared fields the manifest names.
    const arrival = new URL(target.action, "https://studio.example");
    for (const [field, param] of Object.entries(target.params)) {
      const value = shared[field];
      if (value !== undefined) arrival.searchParams.append(param, value);
    }
    const consumed = consumeChatSeed(arrival);
    expect(consumed.seed?.requiresConfirmation).toBe(false);
    expect(consumed.seed?.text.startsWith("Summarize this ")).toBe(true);
    expect(consumed.seed?.text.length).toBeLessThanOrEqual(32 * 1024);
    expect(consumed.url.pathname).toBe("/workspace/chat");
    expect(consumed.url.search).toBe("");
  });
});
