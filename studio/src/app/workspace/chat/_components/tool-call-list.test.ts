import { describe, expect, it } from "vitest";
import { activitySummary } from "./tool-call-list";

const call = (status: "running" | "completed" | "failed") => ({ status });

/**
 * The collapsed "Activity" line's text: the count fragment always, the
 * failed fragment ONLY when something actually failed — "0 failed" noise
 * would train users to ignore it.
 */
describe("activitySummary", () => {
  it("composes 'N tools · M failed' when any call failed", () => {
    const summary = activitySummary([
      call("completed"),
      call("failed"),
      call("completed"),
    ]);
    expect(`${summary.tools} · ${summary.failed}`).toBe("3 tools · 1 failed");
  });

  it("omits the failed fragment when every call succeeded", () => {
    expect(activitySummary([call("completed"), call("completed")])).toEqual({
      tools: "2 tools",
      failed: null,
    });
  });

  it("does not count a still-running call as failed", () => {
    expect(activitySummary([call("running")])).toEqual({
      tools: "1 tool",
      failed: null,
    });
  });

  it("pluralizes only the tool count", () => {
    const summary = activitySummary([call("failed")]);
    expect(summary.tools).toBe("1 tool");
    expect(summary.failed).toBe("1 failed");
  });
});
