import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { ScheduleService } from "../src/gen/mecatl/v1/schedule_pb.js";
import { HTTP_ONLY_CONTROLS, RPC_CATALOG, resolveHTTPOnlyControl } from "../src/rpc-catalog.js";

describe("RPC transport catalog", () => {
  it("HTTP-only controls do not satisfy RPC coverage", () => {
    const descriptorKeys = [
      ...Object.values(HarnessService.method).map((method) => `HarnessService.${method.name}`),
      ...Object.values(ScheduleService.method).map((method) => `ScheduleService.${method.name}`),
    ];
    expect(descriptorKeys).toHaveLength(77);
    expect(Object.keys(RPC_CATALOG).sort()).toEqual(descriptorKeys.sort());

    const omitted = "HarnessService.GetSession";
    const candidateCoverage = new Set(Object.keys(RPC_CATALOG).filter((key) => key !== omitted));
    for (const controlName of Object.keys(HTTP_ONLY_CONTROLS)) {
      candidateCoverage.add(controlName);
    }

    expect(descriptorKeys.filter((key) => !candidateCoverage.has(key))).toEqual([omitted]);
    expect(Object.values(HTTP_ONLY_CONTROLS).every((entry) => entry.kind === "http")).toBe(true);
    expect(resolveHTTPOnlyControl("cancel", { session_id: "a session" })).toMatchObject({
      method: "POST",
      path: "/v1/sessions/a%20session/cancel",
      requestBody: "optional-json",
      response: "json",
    });
    expect(() => resolveHTTPOnlyControl("cancel", {})).toThrowError(
      "Missing HTTP control path field session_id",
    );
  });
});
