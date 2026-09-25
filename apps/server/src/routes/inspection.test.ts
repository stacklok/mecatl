// SPDX-License-Identifier: Apache-2.0

import { type Client, MecatlError } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createApp } from "../app";
import type { AuthenticationService } from "../auth/service";
import { fakeRuntime, memoryLogger, sampleCapabilities, sampleSnapshot } from "../testing/fakes";

const withInspection = (client: Client, capabilities = { soul: true, worktrees: true }) =>
  createApp({
    runtime: fakeRuntime({
      client,
      snapshot: () => ({
        ...sampleSnapshot(),
        capabilities: { ...sampleCapabilities(), ...capabilities },
      }),
    }),
  });

describe("inspection routes", () => {
  it("projects a connection scoped soul snapshot", async () => {
    const get = vi.fn().mockResolvedValue({
      soul: {
        content: "# Soul",
        drifted: true,
        present: true,
        provenance: 2,
        sha256: "abc123",
        sizeBytes: 9_007_199_254_740_993n,
        trusted: true,
      },
    });
    const app = withInspection({ soul: { get } } as unknown as Client);

    const response = await app.request("/api/v1/soul");

    expect(response.status).toBe(200);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(get).toHaveBeenCalledWith({ $typeName: "mecatl.v1.GetSoulRequest" });
    await expect(response.json()).resolves.toEqual({
      content: "# Soul",
      drifted: true,
      present: true,
      provenance: "project",
      sha256: "abc123",
      sizeBytes: "9007199254740993",
      trusted: true,
    });

    get.mockResolvedValueOnce({
      soul: {
        content: "",
        drifted: false,
        present: false,
        provenance: 0,
        sha256: "",
        sizeBytes: 0n,
        trusted: false,
      },
    });
    const absent = await app.request("/api/v1/soul");
    await expect(absent.json()).resolves.toMatchObject({
      present: false,
      content: "",
      sha256: "",
      provenance: "unspecified",
    });
    get.mockResolvedValueOnce({});
    expect((await app.request("/api/v1/soul")).status).toBe(500);
    expect((await app.request("/api/v1/soul?sessionId=other")).status).toBe(400);
    expect((await app.request("/api/v1/soul", { headers: { "Content-Length": "1" } })).status).toBe(
      400,
    );
  });

  it("lists only the owned session worktrees", async () => {
    const list = vi.fn().mockResolvedValue({
      worktrees: [
        {
          selector: "opaque",
          kind: "local",
          label: "Feature",
          branch: "feature",
          revision: "abc",
          bare: false,
          path: "/private/root",
        },
      ],
    });
    const app = withInspection({ worktrees: { list } } as unknown as Client);

    const response = await app.request("/api/v1/sessions/source/worktrees");

    expect(response.status).toBe(200);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(list).toHaveBeenCalledWith({
      $typeName: "mecatl.v1.ListWorktreesRequest",
      sessionId: "source",
    });
    await expect(response.json()).resolves.toEqual({
      items: [
        {
          selector: "opaque",
          kind: "local",
          label: "Feature",
          branch: "feature",
          revision: "abc",
          bare: false,
        },
      ],
    });
    list.mockResolvedValueOnce({ worktrees: [] });
    await expect((await app.request("/api/v1/sessions/no-fs/worktrees")).json()).resolves.toEqual({
      items: [],
    });
    list.mockRejectedValueOnce(
      new MecatlError("backend unavailable", {
        code: "placement_unavailable",
        status: 14,
        transport: "grpc",
      }),
    );
    expect((await app.request("/api/v1/sessions/source/worktrees")).status).toBe(503);
    expect((await app.request("/api/v1/sessions/source/worktrees?root=/private/root")).status).toBe(
      400,
    );
    expect(
      (
        await app.request("/api/v1/sessions/source/worktrees", {
          headers: { "Content-Length": "1" },
        })
      ).status,
    ).toBe(400);
  });

  it("rejects unauthenticated and foreign inspection", async () => {
    const list = vi.fn().mockRejectedValue(
      new MecatlError("/private/root", {
        code: "session_not_found",
        status: 5,
        transport: "grpc",
      }),
    );
    const get = vi.fn().mockResolvedValue({
      soul: {
        present: false,
        content: "",
        sizeBytes: 0n,
        sha256: "",
        provenance: 0,
        trusted: false,
        drifted: false,
      },
    });
    const client = { soul: { get }, worktrees: { list } } as unknown as Client;
    const anonymous = {
      credential: async () => ({ status: "anonymous" }),
    } as unknown as AuthenticationService;
    const blocked = createApp({ authentication: anonymous, runtime: fakeRuntime({ client }) });
    const anonymousSoul = await blocked.request("/api/v1/soul");
    const anonymousWorktrees = await blocked.request("/api/v1/sessions/source/worktrees");
    expect(anonymousSoul.status).toBe(401);
    expect(anonymousWorktrees.status).toBe(401);
    expect(anonymousSoul.headers.get("cache-control")).toBe("private, no-store");
    expect(anonymousWorktrees.headers.get("cache-control")).toBe("private, no-store");
    expect(get).not.toHaveBeenCalled();
    expect(list).not.toHaveBeenCalled();

    const { logger, records } = memoryLogger();
    const app = createApp({
      logger,
      runtime: fakeRuntime({
        client,
        snapshot: () => ({
          ...sampleSnapshot(),
          capabilities: { ...sampleCapabilities(), soul: true },
        }),
      }),
    });
    const unknown = await app.request("/api/v1/sessions/unknown/worktrees");
    const foreign = await app.request("/api/v1/sessions/foreign/worktrees");
    expect(unknown.status).toBe(404);
    expect(foreign.status).toBe(404);
    const unknownProblem = await unknown.json();
    const foreignProblem = await foreign.json();
    expect({ ...unknownProblem, instance: "" }).toEqual({ ...foreignProblem, instance: "" });
    expect(JSON.stringify(unknownProblem)).not.toContain("/private/root");
    expect(JSON.stringify(records)).not.toContain("/private/root");

    const unsupported = withInspection(client, { soul: false, worktrees: false });
    await expect((await unsupported.request("/api/v1/soul")).json()).resolves.toMatchObject({
      code: "soul_unsupported",
      status: 501,
    });
    await expect(
      (await unsupported.request("/api/v1/sessions/source/worktrees")).json(),
    ).resolves.toMatchObject({ code: "worktrees_unsupported", status: 501 });
    expect(get).not.toHaveBeenCalled();
    expect(list).toHaveBeenCalledTimes(2);
  });
});
