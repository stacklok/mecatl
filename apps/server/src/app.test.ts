// SPDX-License-Identifier: Apache-2.0

import { createRoute, z } from "@hono/zod-openapi";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { createApp } from "./app.js";
import { RuntimeNotReadyError } from "./mecatl/runtime.js";
import { fakeRuntime, sampleSnapshot } from "./testing/fakes.js";
import { installedSdkPackageVersion } from "./testing/sdk-package.js";

describe("Studio BFF", () => {
  it("GET /api/health returns 200 and GET /api/v1/runtime returns the negotiated snapshot", async () => {
    const app = createApp({ runtime: fakeRuntime() });

    const health = await app.request("/api/health");
    expect(health.status).toBe(200);
    await expect(health.json()).resolves.toEqual({ service: "mecatl-studio", status: "ok" });

    const runtime = await app.request("/api/v1/runtime");
    expect(runtime.status).toBe(200);
    await expect(runtime.json()).resolves.toEqual({
      ...sampleSnapshot(),
      sdkVersion: installedSdkPackageVersion(),
    });

    // Health needs no runtime at all.
    const bare = await createApp().request("/api/health");
    expect(bare.status).toBe(200);
  });

  it("GET /api/v1/runtime returns 503 problem details before the runtime is ready", async () => {
    let readyCalls = 0;
    const app = createApp({
      runtime: fakeRuntime({
        ready: async () => {
          readyCalls += 1;
        },
        snapshot: () => {
          throw new RuntimeNotReadyError();
        },
      }),
    });
    const response = await app.request("/api/v1/runtime");
    expect(response.status).toBe(503);
    expect(response.headers.get("content-type")).toContain("application/problem+json");
    await expect(response.json()).resolves.toMatchObject({
      code: "runtime_unavailable",
      status: 503,
    });
    expect(readyCalls).toBe(1);

    const detached = await createApp().request("/api/v1/runtime");
    expect(detached.status).toBe(503);
    await expect(detached.json()).resolves.toMatchObject({ code: "runtime_unavailable" });
  });

  it("API errors are RFC 9457 problem details with a stable code", async () => {
    const app = createApp({
      runtime: fakeRuntime({
        snapshot: () => {
          // A gRPC error carries a Connect code in `status`, never an HTTP
          // status: 5 is NotFound. The mapping must come from `code`.
          throw new MecatlError("session missing", {
            code: "session_not_found",
            status: 5,
            transport: "grpc",
          });
        },
      }),
    });

    const missing = await app.request("/api/missing");
    expect(missing.status).toBe(404);
    expect(missing.headers.get("content-type")).toContain("application/problem+json");
    await expect(missing.json()).resolves.toEqual({
      code: "not_found",
      detail: "No route matches this request.",
      instance: "/api/missing",
      status: 404,
      title: "Not found",
      type: "urn:mecatl-studio:problem:not_found",
    });

    const upstream = await app.request("/api/v1/runtime");
    expect(upstream.status).toBe(404);
    await expect(upstream.json()).resolves.toMatchObject({
      code: "session_not_found",
      status: 404,
      type: "urn:mecatl-studio:problem:session_not_found",
    });
  });

  it("problem details from upstream errors are clamped and carry no bearer material", async () => {
    const noisy = `upstream https://mecak8s.internal:443/v1 at 10.0.0.7:50051 said Bearer abc.def-ghi rejected ${"x".repeat(1_000)}`;
    const app = createApp({
      runtime: fakeRuntime({
        snapshot: () => {
          throw new MecatlError(noisy, { code: "internal", status: 500, transport: "grpc" });
        },
      }),
    });
    const response = await app.request("/api/v1/runtime");
    expect(response.status).toBe(500);
    const body = (await response.json()) as { detail: string };
    expect([...body.detail].length).toBeLessThanOrEqual(400);
    expect(body.detail).not.toContain("abc.def-ghi");
    expect(body.detail).not.toContain("mecak8s.internal");
    expect(body.detail).not.toContain("10.0.0.7");
    expect(body.detail).toContain("[upstream]");
  });
  it("maps a gRPC error by its code, then by its Connect class, never as an HTTP status", async () => {
    const byCode = async (code: string, status: number) => {
      const app = createApp({
        runtime: fakeRuntime({
          snapshot: () => {
            throw new MecatlError(`upstream said ${code}`, {
              code: code as never,
              status,
              transport: "grpc",
            });
          },
        }),
      });
      const response = await app.request("/api/v1/runtime");
      return { body: (await response.json()) as { code: string }, status: response.status };
    };

    // Connect codes: 5 NotFound, 16 Unauthenticated, 3 InvalidArgument,
    // 8 ResourceExhausted, 12 Unimplemented. None is an HTTP status.
    await expect(byCode("session_not_found", 5)).resolves.toMatchObject({ status: 404 });
    await expect(byCode("unauthenticated", 16)).resolves.toMatchObject({ status: 401 });
    await expect(byCode("invalid_argument", 3)).resolves.toMatchObject({ status: 400 });
    await expect(byCode("resource_exhausted", 8)).resolves.toMatchObject({ status: 429 });
    await expect(byCode("unimplemented", 12)).resolves.toMatchObject({ status: 501 });
    await expect(byCode("transport", 14)).resolves.toMatchObject({ status: 503 });
    // A domain code Studio does not name keeps its Connect class: 9
    // FailedPrecondition and 10 Aborted are conflicts, 5 NotFound is missing.
    await expect(byCode("proposal_conflict", 9)).resolves.toMatchObject({
      body: { code: "proposal_conflict" },
      status: 409,
    });
    await expect(byCode("dream_terminal_conflict", 10)).resolves.toMatchObject({ status: 409 });
    await expect(byCode("dream_not_found", 5)).resolves.toMatchObject({ status: 404 });
    await expect(byCode("learning_unavailable", 14)).resolves.toMatchObject({ status: 503 });
    // Internal, unknown, and out-of-range Connect numbers are server errors,
    // never read as HTTP statuses.
    await expect(byCode("some_future_code", 13)).resolves.toMatchObject({ status: 500 });
    await expect(byCode("some_future_code", 404)).resolves.toMatchObject({ status: 500 });

    // Over HTTP the status IS an HTTP status and is honoured.
    const http = createApp({
      runtime: fakeRuntime({
        snapshot: () => {
          throw new MecatlError("gone", {
            code: "some_future_code" as never,
            status: 410,
            transport: "http",
          });
        },
      }),
    });
    expect((await http.request("/api/v1/runtime")).status).toBe(410);
  });

  it("request validation failures are problem details naming the invalid field", async () => {
    const app = createApp({ runtime: fakeRuntime() });
    app.openapi(
      createRoute({
        method: "get",
        path: "/api/v1/probe",
        request: { query: z.object({ limit: z.coerce.number().int().min(1) }) },
        responses: { 204: { description: "ok" } },
      }),
      (context) => context.body(null, 204),
    );
    const response = await app.request("/api/v1/probe?limit=0");
    expect(response.status).toBe(400);
    expect(response.headers.get("content-type")).toContain("application/problem+json");
    const body = (await response.json()) as { code: string; detail: string };
    expect(body.code).toBe("invalid_request");
    expect(body.detail).toContain("limit");
    expect((await app.request("/api/v1/probe?limit=2")).status).toBe(204);
  });

  it("feature routes wait for compatibility negotiation instead of answering from an empty snapshot", async () => {
    let negotiatedNow = false;
    let finish: (() => void) | undefined;
    const pending = new Promise<void>((resolve) => {
      finish = () => {
        negotiatedNow = true;
        resolve();
      };
    });
    const runtime = fakeRuntime({
      ready: () => pending,
      snapshot: () => {
        if (!negotiatedNow) throw new RuntimeNotReadyError();
        return sampleSnapshot();
      },
    });
    const app = createApp({ readinessTimeoutMs: 1_000, runtime });
    app.openapi(
      createRoute({
        method: "get",
        path: "/api/v1/feature",
        responses: { 200: { description: "ok" } },
      }),
      (context) => context.json({ supported: Object.keys(runtime.snapshot()).length > 0 }, 200),
    );

    const waiting = app.request("/api/v1/feature");
    setTimeout(() => finish?.(), 20);
    const response = await waiting;
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({ supported: true });

    // A negotiation that never completes is a retryable 503, never a false answer.
    const stuck = createApp({
      readinessTimeoutMs: 20,
      runtime: fakeRuntime({
        ready: () => new Promise(() => undefined),
        snapshot: () => {
          throw new RuntimeNotReadyError();
        },
      }),
    });
    stuck.openapi(
      createRoute({
        method: "get",
        path: "/api/v1/feature",
        responses: { 200: { description: "ok" } },
      }),
      (context) => context.json({ supported: false }, 200),
    );
    const unavailable = await stuck.request("/api/v1/feature");
    expect(unavailable.status).toBe(503);
    await expect(unavailable.json()).resolves.toMatchObject({ code: "runtime_unavailable" });
    // The auth routes and /runtime never wait.
    expect((await stuck.request("/api/v1/auth/session")).status).toBe(200);
    expect((await stuck.request("/api/v1/runtime")).status).toBe(503);
  });
});
