import { describe, expect, it } from "vitest";
import {
  DEBUG_ASK_ID_PREFIX,
  DEBUG_ASK_PAYLOADS,
  DEBUG_ASK_REASON,
  debugAskRequest,
  debugAskResolvedNotice,
  withoutSyntheticAsks,
} from "./debug-ask";
import type { ApprovalRequest } from "./types";

const real = (approvalId: string): ApprovalRequest => ({
  approvalId,
  sessionId: "s1",
  toolName: "Edit",
  description: "Edit needs your approval.",
  details: "",
});

describe("debugAskRequest", () => {
  it("mints a synthetic Shell ask that names itself as not from the model", () => {
    const ask = debugAskRequest("s1");
    expect(ask.synthetic).toBe(true);
    expect(ask.toolName).toBe("Shell");
    expect(ask.sessionId).toBe("s1");
    expect(ask.description).toBe("Shell needs your approval.");
    expect(ask.reason).toBe(DEBUG_ASK_REASON);
    expect(ask.details.startsWith(`${DEBUG_ASK_REASON}\n`)).toBe(true);
    expect(ask.child).toBe(false);
  });

  it("carries a long canned command as decodable {command} args and in details", () => {
    const ask = debugAskRequest("s1", 0);
    const decoded = JSON.parse(ask.args ?? "{}") as { command: string };
    expect(decoded.command).toBe(DEBUG_ASK_PAYLOADS[0]);
    expect(decoded.command.length).toBeGreaterThan(200);
    expect(ask.details.endsWith(decoded.command)).toBe(true);
  });

  it("rotates through the canned payloads, including the multi-line heredoc", () => {
    const commands = DEBUG_ASK_PAYLOADS.map(
      (_, index) =>
        (
          JSON.parse(debugAskRequest("s1", index).args ?? "{}") as {
            command: string;
          }
        ).command,
    );
    expect(commands).toEqual([...DEBUG_ASK_PAYLOADS]);
    expect(commands.some((command) => command.includes("\n"))).toBe(true);
    // Wraps around, and a negative cycle is tolerated.
    expect(debugAskRequest("s1", DEBUG_ASK_PAYLOADS.length).args).toBe(
      debugAskRequest("s1", 0).args,
    );
    expect(debugAskRequest("s1", -1).synthetic).toBe(true);
  });

  it("salts the id with the cycle so a re-injection is never a re-surfaced known ask, and keeps it colon-free (a MAIN ask)", () => {
    const first = debugAskRequest("s1", 0);
    const second = debugAskRequest("s1", 1);
    expect(first.approvalId).not.toBe(second.approvalId);
    expect(first.approvalId.startsWith(DEBUG_ASK_ID_PREFIX)).toBe(true);
    expect(first.approvalId).not.toContain(":");
  });
});

describe("withoutSyntheticAsks", () => {
  it("drops every fake ask and keeps the genuine ones in order", () => {
    const fake = debugAskRequest("s1");
    const queue = [fake, real("s1:1:c1:r1"), real("s1:2:c2:r1")];
    expect(withoutSyntheticAsks(queue).map((ask) => ask.approvalId)).toEqual([
      "s1:1:c1:r1",
      "s1:2:c2:r1",
    ]);
  });

  it("returns the same array when nothing is fake (no render churn)", () => {
    const queue = [real("s1:1:c1:r1")];
    expect(withoutSyntheticAsks(queue)).toBe(queue);
    const empty: ApprovalRequest[] = [];
    expect(withoutSyntheticAsks(empty)).toBe(empty);
  });
});

describe("debugAskResolvedNotice", () => {
  it("records the choice and that the daemon saw nothing", () => {
    expect(debugAskResolvedNotice("once")).toBe(
      "debug ask resolved: once — nothing was sent to the daemon",
    );
    expect(debugAskResolvedNotice("deny")).toContain("deny");
  });
});
