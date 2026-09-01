import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { createRawClient, IncompatibleServerError } from "../src/index.js";

describe("compatibility floor", () => {
  it("missing or incompatible server info fails the floor", async () => {
    let missingCreated = 0;
    const missing = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => {
          missingCreated += 1;
          return { sessionId: "must-not-exist" };
        },
      });
    });

    let incompatibleCreated = 0;
    const incompatible = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => {
          incompatibleCreated += 1;
          return { sessionId: "must-not-exist" };
        },
        getCompatibilityInfo: () => ({ apiMajor: 2 }),
      });
    });

    await expect(
      createRawClient({ transport: missing }).unary(HarnessService.method.createSession, {
        workspace: "/workspace",
      }),
    ).rejects.toBeInstanceOf(IncompatibleServerError);
    await expect(
      createRawClient({ transport: incompatible }).unary(HarnessService.method.createSession, {
        workspace: "/workspace",
      }),
    ).rejects.toBeInstanceOf(IncompatibleServerError);
    expect({ incompatibleCreated, missingCreated }).toEqual({
      incompatibleCreated: 0,
      missingCreated: 0,
    });
  });
});
