import { readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { type Client, createClient, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { HarnessService, ScheduleService } from "../src/gen/index.js";

const generatedRoot = fileURLToPath(new URL("../src/gen", import.meta.url));

describe("protobuf-es generation", () => {
  it("no driver protocol output is generated", async () => {
    const generatedFiles = (await readdir(generatedRoot, { recursive: true }))
      .map((path) => path.replaceAll("\\", "/"))
      .sort();

    expect(generatedFiles.some((path) => path.includes("driver"))).toBe(false);
    expect(
      generatedFiles.filter((path) => path.endsWith("_pb.ts") && path.includes("mecatl/")),
    ).toEqual(["mecatl/v1/harness_pb.ts", "mecatl/v1/schedule_pb.ts"]);
  });

  it("generated descriptors construct a typed Connect client", () => {
    const transport = createRouterTransport(() => {});
    const harnessClient: Client<typeof HarnessService> = createClient(HarnessService, transport);
    const scheduleClient: Client<typeof ScheduleService> = createClient(ScheduleService, transport);

    expect(HarnessService.typeName).toBe("mecatl.v1.HarnessService");
    expect(ScheduleService.typeName).toBe("mecatl.v1.ScheduleService");
    expect(harnessClient.createSession).toBeTypeOf("function");
    expect(scheduleClient.listSchedules).toBeTypeOf("function");
  });
});
