import { describe, expect, it } from "vitest";
import {
  APPROVAL_VERDICT_LABELS,
  approvalQueuePosition,
  DEBUG_MCP_ASK_NOTE,
  enqueueAsk,
  formatVerdictNotice,
  isChildAsk,
  isDebugMcpMutationAsk,
  resolveAsk,
  retractAsk,
  retractedAskNotice,
} from "./approval-queue";
import type { ApprovalRequest } from "./types";

const ask = (approvalId: string, toolName = "Bash"): ApprovalRequest => ({
  approvalId,
  sessionId: "s1",
  toolName,
  description: `${toolName} needs your approval.`,
  details: "",
});

describe("isChildAsk", () => {
  it("classifies an ask prefixed with the live session id as the main agent's", () => {
    expect(isChildAsk("s1:1:c1:r1", "s1")).toBe(false);
  });

  it("classifies an ask prefixed with another (child) session id as a child's", () => {
    expect(isChildAsk("subagent-x:1:c1:r1", "s1")).toBe(true);
  });

  it("does not let a session id that is a PREFIX of the child id pass as main", () => {
    // "s1" vs "s10:…": the separator is part of the match.
    expect(isChildAsk("s10:1:c1:r1", "s1")).toBe(true);
  });

  it("treats a colon-free fixture id as the main agent (fail-safe: offers Always)", () => {
    expect(isChildAsk("ask-1", "s1")).toBe(false);
  });

  it("treats every ask as a child's when the session id is empty (fail-safe: withholds Always)", () => {
    expect(isChildAsk("s1:1:c1:r1", "")).toBe(true);
  });
});

describe("enqueueAsk", () => {
  it("appends in FIFO order", () => {
    const queue = enqueueAsk(enqueueAsk([], ask("a1")), ask("a2"));
    expect(queue.map((entry) => entry.approvalId)).toEqual(["a1", "a2"]);
  });

  it("dedupes a known askId and returns the same array", () => {
    const queue = [ask("a1"), ask("a2")];
    expect(enqueueAsk(queue, ask("a1", "Edit"))).toBe(queue);
  });
});

describe("retractAsk", () => {
  it("removes a middle entry and reports it (not the head)", () => {
    const queue = [ask("a1"), ask("a2"), ask("a3")];
    const result = retractAsk(queue, "a2");
    expect(result.queue.map((entry) => entry.approvalId)).toEqual(["a1", "a3"]);
    expect(result.removed?.approvalId).toBe("a2");
    expect(result.wasHead).toBe(false);
  });

  it("reports the head when the visible ask is withdrawn", () => {
    const result = retractAsk([ask("a1"), ask("a2")], "a1");
    expect(result.queue.map((entry) => entry.approvalId)).toEqual(["a2"]);
    expect(result.wasHead).toBe(true);
  });

  it("removes nothing for an unknown id and returns the same array", () => {
    const queue = [ask("a1")];
    expect(retractAsk(queue, "zzz")).toEqual({
      queue,
      removed: null,
      wasHead: false,
    });
    expect(retractAsk(queue, "zzz").queue).toBe(queue);
  });
});

describe("resolveAsk", () => {
  it("advances the head: the next queued ask becomes position 1", () => {
    const queue = [ask("a1"), ask("a2")];
    const next = resolveAsk(queue, "a1");
    expect(next[0]?.approvalId).toBe("a2");
    expect(next).toHaveLength(1);
  });

  it("leaves the queue as the same array when the id is not queued", () => {
    const queue = [ask("a1")];
    expect(resolveAsk(queue, "a9")).toBe(queue);
  });
});

describe("formatVerdictNotice", () => {
  it("renders each daemon verdict in plain words", () => {
    expect(formatVerdictNotice("Bash", "allow_once")).toBe(
      "Permission: Bash allowed once",
    );
    expect(formatVerdictNotice("Edit", "allow_always")).toBe(
      "Permission: Edit always allowed",
    );
    expect(formatVerdictNotice("Write", "deny")).toBe(
      "Permission: Write denied",
    );
    expect(Object.keys(APPROVAL_VERDICT_LABELS).sort()).toEqual([
      "allow_always",
      "allow_once",
      "deny",
    ]);
  });

  it("passes an unknown verdict through and names an unnamed tool 'tool'", () => {
    expect(formatVerdictNotice("", "escalated")).toBe(
      "Permission: tool escalated",
    );
    expect(formatVerdictNotice(undefined, "")).toBe(
      "Permission: tool resolved",
    );
  });
});

describe("retractedAskNotice", () => {
  it("distinguishes the visible ask from a queued one", () => {
    expect(retractedAskNotice(true)).toBe(
      "Permission request withdrawn — the subagent that asked was cancelled",
    );
    expect(retractedAskNotice(false)).toBe(
      "Queued permission request withdrawn — the subagent that asked was cancelled",
    );
  });
});

describe("approvalQueuePosition", () => {
  it("is undefined for an empty queue and 1-of-N otherwise", () => {
    expect(approvalQueuePosition(0)).toBeUndefined();
    expect(approvalQueuePosition(1)).toEqual({ index: 1, total: 1 });
    expect(approvalQueuePosition(3)).toEqual({ index: 1, total: 3 });
  });
});

describe("isDebugMcpMutationAsk", () => {
  // The daemon's own wording (internal/adapter/sessiondebug/permission.go).
  const DEBUG_REASON =
    "debug MCP call requires fresh current operator approval";
  const debugAsk: ApprovalRequest = {
    ...ask("dbg-1:1:c1:r1", "mcp__github__create_issue"),
    reason: DEBUG_REASON,
    details: `${DEBUG_REASON}\n\ntitle: Flaky scheduler test`,
  };

  it("is true for a direct MCP tool with the daemon's debug reason in a debug session", () => {
    expect(isDebugMcpMutationAsk(debugAsk, true)).toBe(true);
  });

  it("is false outside a debug session, whatever the ask says", () => {
    expect(isDebugMcpMutationAsk(debugAsk, false)).toBe(false);
  });

  it("is false for a non-MCP tool even with the debug reason", () => {
    expect(
      isDebugMcpMutationAsk({ ...debugAsk, toolName: "InspectSession" }, true),
    ).toBe(false);
    expect(isDebugMcpMutationAsk({ ...debugAsk, toolName: "" }, true)).toBe(
      false,
    );
  });

  it("is false for an MCP tool asked for an ordinary reason", () => {
    expect(
      isDebugMcpMutationAsk(
        {
          ...debugAsk,
          reason: "runs an MCP tool",
          details: "runs an MCP tool",
        },
        true,
      ),
    ).toBe(false);
  });

  it("falls back to the joined details on a legacy ask with no reason tier", () => {
    expect(
      isDebugMcpMutationAsk({ ...debugAsk, reason: undefined }, true),
    ).toBe(true);
  });

  it("names the one-call-at-a-time rule and that Always allow is not learned", () => {
    expect(DEBUG_MCP_ASK_NOTE).toMatch(/one call at a time/);
    expect(DEBUG_MCP_ASK_NOTE).toMatch(/Always allow is not learned/);
  });
});
