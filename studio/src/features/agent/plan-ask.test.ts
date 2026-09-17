import { describe, expect, it } from "vitest";
import {
  isHarnessProceedMessage,
  isPlanApprovalVerdict,
  isPlanAsk,
  PLAN_APPROVAL_TOOL,
  PLAN_APPROVED_PROCEED_TEXT,
  planBodyFromArgs,
  shouldAutoProceed,
} from "./plan-ask";

describe("plan-ask constants", () => {
  it("names the engine's PresentPlan tool and the harness-framed proceed prompt byte-for-byte", () => {
    // The SDK (sdk/typescript/src/plan.ts) and mecatui carry the same two
    // literals; a drift here would start execution with a different
    // recorded user turn than every other client.
    expect(PLAN_APPROVAL_TOOL).toBe("PresentPlan");
    expect(PLAN_APPROVED_PROCEED_TEXT).toBe(
      "Plan approved by operator. Proceed with execution.",
    );
  });
});

describe("isPlanAsk", () => {
  it("matches the PresentPlan tool exactly, trimming whitespace", () => {
    expect(isPlanAsk("PresentPlan")).toBe(true);
    expect(isPlanAsk(" PresentPlan ")).toBe(true);
  });

  it("rejects every other tool, an empty name, and a missing name", () => {
    expect(isPlanAsk("Bash")).toBe(false);
    expect(isPlanAsk("presentplan")).toBe(false);
    expect(isPlanAsk("")).toBe(false);
    expect(isPlanAsk(undefined)).toBe(false);
    expect(isPlanAsk(null)).toBe(false);
  });
});

describe("planBodyFromArgs", () => {
  it("returns the plan text", () => {
    expect(
      planBodyFromArgs(JSON.stringify({ plan: "# Plan\n\n1. do it\n\n" })),
    ).toBe("# Plan\n\n1. do it");
  });

  it("falls back to the note when the plan is empty or absent", () => {
    expect(planBodyFromArgs(JSON.stringify({ plan: "", note: "aside" }))).toBe(
      "aside",
    );
    expect(planBodyFromArgs(JSON.stringify({ note: "only a note" }))).toBe(
      "only a note",
    );
  });

  it("is empty for malformed JSON, non-object JSON, empty args, and non-string fields", () => {
    expect(planBodyFromArgs("{not json")).toBe("");
    expect(planBodyFromArgs('"a string"')).toBe("");
    expect(planBodyFromArgs("[1,2]")).toBe("");
    expect(planBodyFromArgs("")).toBe("");
    expect(planBodyFromArgs("   ")).toBe("");
    expect(planBodyFromArgs(undefined)).toBe("");
    expect(planBodyFromArgs(JSON.stringify({ plan: 42, note: null }))).toBe("");
  });
});

describe("isPlanApprovalVerdict", () => {
  it("counts allow_once and allow_always as approvals and deny as iterate", () => {
    expect(isPlanApprovalVerdict("allow_once")).toBe(true);
    expect(isPlanApprovalVerdict("allow_always")).toBe(true);
    expect(isPlanApprovalVerdict("deny")).toBe(false);
  });
});

describe("shouldAutoProceed", () => {
  it("fires only for an armed plan_approved terminal with no ask still waiting", () => {
    expect(
      shouldAutoProceed({ stop: "plan_approved", armed: true, queueLength: 0 }),
    ).toBe(true);
  });

  it("never fires on plan_iterate, any other stop, or an absent stop", () => {
    for (const stop of ["plan_iterate", "end_turn", "error", "cancelled"]) {
      expect(shouldAutoProceed({ stop, armed: true, queueLength: 0 })).toBe(
        false,
      );
    }
    expect(
      shouldAutoProceed({ stop: undefined, armed: true, queueLength: 0 }),
    ).toBe(false);
  });

  it("never fires for a client that did not approve the plan (a watcher, or a second tab)", () => {
    expect(
      shouldAutoProceed({
        stop: "plan_approved",
        armed: false,
        queueLength: 0,
      }),
    ).toBe(false);
  });

  it("holds while an ask is still queued", () => {
    expect(
      shouldAutoProceed({ stop: "plan_approved", armed: true, queueLength: 1 }),
    ).toBe(false);
  });
});

describe("isHarnessProceedMessage", () => {
  it("matches the synthetic proceed prompt this tab sent", () => {
    expect(
      isHarnessProceedMessage({
        role: "user",
        content: PLAN_APPROVED_PROCEED_TEXT,
        synthetic: true,
      }),
    ).toBe(true);
  });

  it("matches the same recorded text replayed from the transcript without a flag", () => {
    expect(
      isHarnessProceedMessage({
        role: "user",
        content: `${PLAN_APPROVED_PROCEED_TEXT}\n`,
      }),
    ).toBe(true);
  });

  it("never matches an assistant turn, an ordinary prompt, or a delivery note", () => {
    expect(
      isHarnessProceedMessage({
        role: "assistant",
        content: PLAN_APPROVED_PROCEED_TEXT,
      }),
    ).toBe(false);
    expect(
      isHarnessProceedMessage({ role: "user", content: "please proceed" }),
    ).toBe(false);
    expect(
      isHarnessProceedMessage({
        role: "user",
        content: PLAN_APPROVED_PROCEED_TEXT,
        delivery: {
          scheduleId: "sched-1",
          fireId: "f1",
          phase: "start",
        } as never,
      }),
    ).toBe(false);
  });
});
