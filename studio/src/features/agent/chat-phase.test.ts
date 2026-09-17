import { describe, expect, it } from "vitest";
import { deriveChatPhase } from "./chat-phase";

/**
 * The phase fold behind the tab title (and the away notice): the chat hook's
 * own status OR the inventory row's daemon state, awaiting beating running.
 */
describe("deriveChatPhase", () => {
  it.each([
    ["streaming", "running"],
    ["waiting_approval", "awaiting"],
    ["waiting_authorization", "awaiting"],
    ["waiting_clarification", "awaiting"],
    ["idle", "idle"],
    ["error", "idle"],
    [undefined, "idle"],
    ["some-future-status", "idle"],
  ] as const)("status %j alone → %s", (status, phase) => {
    expect(deriveChatPhase(status, undefined)).toBe(phase);
  });

  it.each([
    ["running", "running"],
    ["awaiting", "awaiting"],
    ["idle", "idle"],
    ["completed", "idle"],
    ["failed", "idle"],
    ["cancelled", "idle"],
    ["", "idle"],
    [undefined, "idle"],
  ] as const)("daemon state %j with an idle tab → %s", (state, phase) => {
    expect(deriveChatPhase("idle", state)).toBe(phase);
  });

  it("a run driven elsewhere reaches the phase before this tab's watch attaches", () => {
    // status is still "idle" locally, the inventory row already says running.
    expect(deriveChatPhase("idle", "running")).toBe("running");
    expect(deriveChatPhase(undefined, "awaiting")).toBe("awaiting");
  });

  it("awaiting wins over running from either source", () => {
    expect(deriveChatPhase("streaming", "awaiting")).toBe("awaiting");
    expect(deriveChatPhase("waiting_approval", "running")).toBe("awaiting");
  });

  it("a failed turn stays idle even when the row still reads failed", () => {
    expect(deriveChatPhase("error", "failed")).toBe("idle");
  });
});
