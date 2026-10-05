// SPDX-License-Identifier: Apache-2.0

import type { ObserveWriterRequest } from "@mecatl-studio/contracts";
import { describe, expect, it, vi } from "vitest";
import { createApp } from "../app.js";
import { csrfHeaders, fakeRuntime } from "../testing/fakes.js";

const input: ObserveWriterRequest = {
  document: { content: "A new thought", revision: 2 },
  checkpoint: { content: "An old thought", revision: 1 },
  observations: [{ revision: 1, status: "active", text: "What changed?" }],
  discussion: [{ role: "user", text: "I wonder" }],
};

const post = (app: ReturnType<typeof createApp>, path: string, body: unknown) =>
  app.request(path, {
    method: "POST",
    headers: { ...csrfHeaders(), "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

describe("Writer routes", () => {
  it("defaults off: no runtime feature, API 404 before service, direct SPA path 404", async () => {
    const writer = { observe: vi.fn(), discuss: vi.fn() };
    const app = createApp({ runtime: fakeRuntime(), writer, webDist: "nonexistent-static-assets" });
    const runtime = await app.request("/api/v1/runtime");
    expect(await runtime.json()).not.toHaveProperty("experimentalWriter");
    expect((await post(app, "/api/v1/writer/observe", input)).status).toBe(404);
    expect((await post(app, "/api/v1/writer/discuss", { ...input, message: "Hi" })).status).toBe(
      404,
    );
    expect((await app.request("/workspace/writer")).status).toBe(404);
    expect(writer.observe).not.toHaveBeenCalled();
  });

  it("validates revisions, history bounds, content bounds, and request CSRF before invoking service", async () => {
    const writer = {
      observe: vi.fn(async () => ({ status: "silent" as const })),
      discuss: vi.fn(async () => ({ text: "Yes" })),
    };
    const app = createApp({ experimentalWriter: true, writer, runtime: fakeRuntime() });
    const runtime = await app.request("/api/v1/runtime");
    expect(await runtime.json()).toHaveProperty("experimentalWriter", true);
    for (const bad of [
      { ...input, document: { ...input.document, content: "a".repeat(100_001) } },
      { ...input, checkpoint: { content: "not the same", revision: 2 } },
      { ...input, checkpoint: { content: "next", revision: 3 } },
      { ...input, observations: Array(13).fill(input.observations[0]) },
      { ...input, discussion: Array(13).fill(input.discussion[0]) },
      { ...input, model: { id: "", providerId: "provider" } },
      { ...input, model: { id: "model", providerId: "" } },
    ]) {
      expect((await post(app, "/api/v1/writer/observe", bad)).status).toBe(400);
    }
    expect((await post(app, "/api/v1/writer/discuss", { ...input, message: "" })).status).toBe(400);
    expect(
      (await app.request("/api/v1/writer/observe", { method: "POST", body: JSON.stringify(input) }))
        .status,
    ).toBe(403);
    expect(writer.observe).not.toHaveBeenCalled();
    expect(writer.discuss).not.toHaveBeenCalled();
  });

  it("requires an authenticated principal before evaluating", async () => {
    const writer = { observe: vi.fn(), discuss: vi.fn() };
    const app = createApp({
      experimentalWriter: true,
      writer,
      authentication: {
        clear: () => undefined,
        completeLogin: async () => {
          throw new Error("unused");
        },
        credential: async () => ({ status: "anonymous" }),
        logout: async () => undefined,
        noteLoginComplete: () => undefined,
        noteLoginFailure: () => undefined,
        save: async () => undefined,
        signInRequired: async () => true,
        startLogin: async () => "https://issuer.example.com/authorize",
      },
    });
    expect((await post(app, "/api/v1/writer/observe", input)).status).toBe(401);
    expect(writer.observe).not.toHaveBeenCalled();
  });

  it("forwards validated input to both operations and gives safe failures", async () => {
    const writer = {
      observe: vi.fn(async () => ({ status: "observe" as const, text: "Why now?" })),
      discuss: vi.fn(async () => ({ text: "Try another approach." })),
    };
    const app = createApp({ experimentalWriter: true, writer });
    const observation = await post(app, "/api/v1/writer/observe", {
      ...input,
      model: { id: "model", providerId: "provider" },
    });
    expect(await observation.json()).toEqual({ status: "observe", text: "Why now?" });
    expect(writer.observe).toHaveBeenCalledWith(
      { ...input, model: { id: "model", providerId: "provider" } },
      expect.any(AbortSignal),
    );
    const discussion = {
      ...input,
      model: { id: "model", providerId: "provider" },
      message: "What next?",
    };
    expect(await (await post(app, "/api/v1/writer/discuss", discussion)).json()).toEqual({
      text: "Try another approach.",
    });
    expect(writer.discuss).toHaveBeenCalledWith(discussion, expect.any(AbortSignal));
    writer.observe.mockRejectedValueOnce(new Error("private document leaked upstream"));
    const failed = await post(app, "/api/v1/writer/observe", input);
    expect(failed.status).toBe(503);
    expect(await failed.text()).not.toContain("private document");
  });
});
