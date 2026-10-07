// SPDX-License-Identifier: Apache-2.0

import type { ObserveWriterRequest } from "@mecatl-studio/contracts";
import { describe, expect, it, vi } from "vitest";
import { createApp } from "../app.js";
import { csrfHeaders, fakeRuntime } from "../testing/fakes.js";

const input: ObserveWriterRequest = {
  document: { content: "A new thought", revision: 2 },
  checkpoint: { content: "An old thought", revision: 1 },
  observations: [{ revision: 1, status: "open", text: "What changed?" }],
  discussion: [{ role: "user", text: "I wonder" }],
};
const writer = () => ({
  observe: vi.fn(async () => ({ status: "silent" as const })),
  discuss: vi.fn(async () => ({ mode: "reply" as const, text: "Think about it." })),
});
const post = (app: ReturnType<typeof createApp>, path: string, body: unknown) =>
  app.request(path, {
    method: "POST",
    headers: { ...csrfHeaders(), "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });

describe("Writer routes", () => {
  it("defaults off and removes obsolete endpoints", async () => {
    const service = writer();
    const app = createApp({
      runtime: fakeRuntime(),
      writer: service,
      webDist: "nonexistent-static-assets",
    });
    expect(await (await app.request("/api/v1/runtime")).json()).not.toHaveProperty(
      "experimentalWriter",
    );
    for (const path of ["observe", "discuss", "propose", "start"])
      expect((await post(app, `/api/v1/writer/${path}`, { ...input, message: "Hi" })).status).toBe(
        404,
      );
    expect((await app.request("/workspace/writer")).status).toBe(404);
    expect(service.observe).not.toHaveBeenCalled();
  });

  it("validates quoted references, exact passage and CSRF before forwarding to service", async () => {
    const service = writer();
    const app = createApp({ experimentalWriter: true, writer: service, runtime: fakeRuntime() });
    expect(await (await app.request("/api/v1/runtime")).json()).toHaveProperty(
      "experimentalWriter",
      true,
    );
    const good = {
      ...input,
      message: "Consider rewriting this",
      passage: { from: 2, to: 5, text: "new" },
      references: [{ name: "notes.md", content: "Evidence" }],
    };
    expect((await post(app, "/api/v1/writer/discuss", good)).status).toBe(200);
    expect(service.discuss).toHaveBeenCalledWith(good, expect.any(AbortSignal));
    for (const bad of [
      { ...good, passage: { from: 0, to: 2, text: "wrong" } },
      { ...good, message: "" },
      { ...good, references: Array(4).fill({ name: "a.txt", content: "x" }) },
      { ...good, references: [{ name: "../secret", content: "x" }] },
      { ...good, references: [{ name: "a.txt", content: "a".repeat(8_001) }] },
      { ...good, references: [{ name: "a.txt", content: "\u0000" }] },
      { ...good, references: [{ name: "a.txt", content: "\ud800" }] },
      { ...good, references: Array(3).fill({ name: "a.txt", content: "a".repeat(8_000) }) },
      { ...good, document: { content: "x".repeat(100_001), revision: 2 } },
      { ...good, observations: Array(13).fill(input.observations[0]) },
    ])
      expect((await post(app, "/api/v1/writer/discuss", bad)).status).toBe(400);
    expect(
      (await app.request("/api/v1/writer/discuss", { method: "POST", body: JSON.stringify(good) }))
        .status,
    ).toBe(403);
    expect(service.discuss).toHaveBeenCalledOnce();
  });

  it("requires authentication and hides upstream errors", async () => {
    const service = writer();
    const app = createApp({ experimentalWriter: true, writer: service });
    service.discuss.mockRejectedValueOnce(new Error("private reference"));
    const failed = await post(app, "/api/v1/writer/discuss", { ...input, message: "Hello" });
    expect(failed.status).toBe(503);
    expect(await failed.text()).not.toContain("private reference");
    const auth = createApp({
      experimentalWriter: true,
      writer: service,
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
    expect((await post(auth, "/api/v1/writer/discuss", { ...input, message: "Hi" })).status).toBe(
      401,
    );
  });
});
