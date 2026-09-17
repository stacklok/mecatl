import { describe, expect, it } from "vitest";
import {
  CLEAR_ACTIVE_ELSEWHERE,
  CLEAR_ELIGIBILITY_UNKNOWN,
  clearConversationGate,
} from "./clear-conversation";

/**
 * Pins the Clear conversation gate to the daemon's row verdict: the `fork`
 * capability enables it; the two states ClearSession settles itself
 * (a parked approval, a run THIS tab drives) stay enabled; a run this tab is
 * not driving disables with the plain reason; a non-chat kind hides it; a
 * missing verdict disables rather than guesses.
 */

describe("clearConversationGate", () => {
  it("hides the item with no row and for a non-chat kind", () => {
    expect(clearConversationGate(undefined, false)).toEqual({ kind: "hidden" });
    expect(
      clearConversationGate(
        { canFork: false, forkReason: "inspect_only_kind" },
        false,
      ),
    ).toEqual({ kind: "hidden" });
  });

  it("enables it when the daemon offers a successor", () => {
    expect(
      clearConversationGate({ canFork: true, forkReason: "" }, false),
    ).toEqual({ kind: "enabled" });
  });

  it("enables it on a parked approval — clear cancels the ask itself", () => {
    expect(
      clearConversationGate(
        { canFork: false, forkReason: "awaiting_approval" },
        false,
      ),
    ).toEqual({ kind: "enabled" });
  });

  it("enables it for the run this tab is driving, disables it for one it is not", () => {
    const row = { canFork: false, forkReason: "active_elsewhere" };
    expect(clearConversationGate(row, true)).toEqual({ kind: "enabled" });
    expect(clearConversationGate(row, false)).toEqual({
      kind: "disabled",
      reason: CLEAR_ACTIVE_ELSEWHERE,
    });
  });

  it("disables with a reason when the row carries no verdict", () => {
    expect(clearConversationGate({}, false)).toEqual({
      kind: "disabled",
      reason: CLEAR_ELIGIBILITY_UNKNOWN,
    });
    expect(
      clearConversationGate({ canFork: false, forkReason: "" }, true),
    ).toEqual({ kind: "disabled", reason: CLEAR_ELIGIBILITY_UNKNOWN });
  });

  it("shows an unknown daemon reason verbatim instead of hiding or guessing", () => {
    expect(
      clearConversationGate(
        { canFork: false, forkReason: "quota_exhausted" },
        false,
      ),
    ).toEqual({ kind: "disabled", reason: "quota_exhausted" });
  });
});
