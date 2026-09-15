import { afterEach, describe, expect, it, vi } from "vitest";
import {
  decideDreamPlan,
  dreamTargetCapability,
  generateDreamPlan,
  isStaleDreamPlan,
} from "./dream";
import { HarnessApiError } from "./errors";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";

/**
 * Pins the manual-dream contract (ADR 0227) over the SDK: the routes and
 * bodies the SDK produces, the decode of the daemon's stdlib-JSON plan and
 * receipt (`{seconds}` expiry), the process-local plan-id staleness
 * classification (regenerate, never retry), and the capability-object
 * reader over the wire-keyed `manual_dream` projection.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("generateDreamPlan", () => {
  it("POSTs {target} to /dream/plans and decodes the plan", async () => {
    const stub = stubHarnessFetch(() => ({
      plan: {
        id: "plan1",
        target: "user_model",
        expires_at: { seconds: 1788287318, nanos: 927881000 },
        planned_operation_count: 1,
        planned_source_count: 2,
        operations: [
          {
            kind: "merge",
            survivor: { key: "k1", value: "v1", description: "d1" },
            sources: [{ key: "k2", value: "v2" }],
            replacement: { value: "merged", description: "both" },
            reason: "near-duplicates",
            exact_duplicate_eligible: true,
          },
        ],
      },
    }));
    const plan = await generateDreamPlan("user_model");
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: "/api/mecatl/v1/dream/plans",
      body: { target: "user_model" },
    });
    expect(plan).toEqual({
      id: "plan1",
      target: "user_model",
      expiresAtUnix: 1788287318,
      plannedOperationCount: 1,
      plannedSourceCount: 2,
      operations: [
        {
          kind: "merge",
          survivor: { key: "k1", value: "v1", description: "d1" },
          sources: [{ key: "k2", value: "v2", description: "" }],
          replacement: { value: "merged", description: "both" },
          reason: "near-duplicates",
          exactDuplicateEligible: true,
        },
      ],
    });
  });

  it("degrades an operation with no survivor/replacement to empty values", async () => {
    stubHarnessFetch(() => ({
      plan: { id: "plan2", operations: [{ kind: "delete" }] },
    }));
    const plan = await generateDreamPlan("project_memory");
    expect(plan.operations[0]).toEqual({
      kind: "delete",
      survivor: { key: "", value: "", description: "" },
      sources: [],
      replacement: { value: "", description: "" },
      reason: "",
      exactDuplicateEligible: false,
    });
  });
});

describe("decideDreamPlan", () => {
  it("POSTs {decision} to /dream/plans/{id}/decision and decodes the receipt", async () => {
    const stub = stubHarnessFetch(() => ({
      receipt: {
        id: "plan1",
        target: "user_model",
        disposition: "applied",
        planned_source_count: 2,
        applied_source_count: 2,
        conflicted_source_count: 0,
        skipped_source_count: 0,
        failed_source_count: 0,
      },
    }));
    const receipt = await decideDreamPlan("plan1", "apply");
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: "/api/mecatl/v1/dream/plans/plan1/decision",
      body: { decision: "apply" },
    });
    expect(receipt).toEqual({
      id: "plan1",
      target: "user_model",
      disposition: "applied",
      planned: 2,
      applied: 2,
      conflicted: 0,
      skipped: 0,
      failed: 0,
    });
  });

  it("classifies a vanished plan as stale (regenerate, never retry)", async () => {
    stubHarnessFetch(() =>
      jsonResponse(404, { code: "dream_not_found", error: "unknown plan" }),
    );
    const error = await decideDreamPlan("gone", "apply").catch(
      (caught) => caught,
    );
    expect(error).toBeInstanceOf(HarnessApiError);
    expect(isStaleDreamPlan(error)).toBe(true);
  });
});

describe("isStaleDreamPlan", () => {
  it("keys on the stable codes, with a bare 404/410 fallback for code-less daemons", () => {
    expect(
      isStaleDreamPlan(new HarnessApiError(404, "dream_not_found", "")),
    ).toBe(true);
    expect(
      isStaleDreamPlan(new HarnessApiError(409, "dream_terminal_conflict", "")),
    ).toBe(true);
    expect(isStaleDreamPlan(new HarnessApiError(404, "", ""))).toBe(true);
    expect(isStaleDreamPlan(new HarnessApiError(410, "", ""))).toBe(true);
    expect(
      isStaleDreamPlan(new HarnessApiError(409, "dream_in_progress", "")),
    ).toBe(false);
    expect(isStaleDreamPlan(new Error("x"))).toBe(false);
  });
});

describe("dreamTargetCapability", () => {
  it("reads one target off the wire-keyed manual_dream object", () => {
    const manualDream = {
      project_memory: {
        generate: true,
        decide: false,
        unavailable_reason: "no trusted memory target",
      },
    };
    expect(dreamTargetCapability(manualDream, "project_memory")).toEqual({
      generate: true,
      decide: false,
      unavailableReason: "no trusted memory target",
    });
    expect(dreamTargetCapability(manualDream, "user_model")).toEqual({
      generate: false,
      decide: false,
      unavailableReason: "",
    });
  });

  it("is all-false for an absent or foreign capability object", () => {
    expect(dreamTargetCapability(undefined, "user_model").generate).toBe(false);
    expect(dreamTargetCapability("nope", "user_model").decide).toBe(false);
  });
});
