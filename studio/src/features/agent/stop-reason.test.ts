import { describe, expect, it } from "vitest";
import {
  MAX_STOP_TOKEN_RUNES,
  sanitizeLine,
  statusFromStopReason,
  stopReasonLabel,
} from "./stop-reason";

/**
 * The stop-label table mirrors cmd/mecatui/ui/footer.go `stopReasonLabel`:
 * every limit stop is a warning, cancel/plan verdicts are muted, a clean
 * end_turn and the failed-card-owned `error` say nothing, and an unknown
 * daemon token is named (sanitised) rather than dropped.
 */
describe("stopReasonLabel", () => {
  it("says nothing for a clean end, an absent stop, and the failed card's error", () => {
    expect(stopReasonLabel("end_turn")).toBeNull();
    expect(stopReasonLabel("")).toBeNull();
    expect(stopReasonLabel(undefined)).toBeNull();
    expect(stopReasonLabel(null)).toBeNull();
    expect(stopReasonLabel("error")).toBeNull();
  });

  it.each([
    ["max_turns", "stopped · turn limit"],
    ["max_tool_calls", "stopped · tool-call limit"],
    ["max_consecutive_failures", "stopped · repeated failures"],
    ["budget", "stopped · token budget"],
    ["no_progress", "stopped · no progress"],
    ["structured_output", "stopped · schema unmet"],
  ])("labels the %s limit stop as a warning", (stop, text) => {
    expect(stopReasonLabel(stop)).toEqual({ text, tone: "warn" });
  });

  it.each([
    ["cancelled", "cancelled"],
    ["plan_approved", "plan approved · executing"],
    ["plan_iterate", "plan iterate · awaiting your feedback"],
  ])("labels the %s stop as a muted cue", (stop, text) => {
    expect(stopReasonLabel(stop)).toEqual({ text, tone: "muted" });
  });

  it("names an unknown daemon token instead of dropping it", () => {
    expect(stopReasonLabel("new_daemon_stop")).toEqual({
      text: "stopped · new_daemon_stop",
      tone: "muted",
    });
  });

  it("sanitises an unknown token to one bounded line before rendering it", () => {
    // A newline, an ANSI escape (ESC) and a zero-width space all fold away.
    expect(stopReasonLabel("odd\nstop\u001b[31m token\u200b")).toEqual({
      text: "stopped · odd stop [31m token",
      tone: "muted",
    });
    const long = "x".repeat(MAX_STOP_TOKEN_RUNES + 20);
    const label = stopReasonLabel(long);
    expect(label?.text).toBe(
      `stopped · ${"x".repeat(MAX_STOP_TOKEN_RUNES - 1)}…`,
    );
    // A token that is nothing but control characters has no name to show.
    expect(stopReasonLabel("\u0000\u0007")).toBeNull();
  });
});

describe("statusFromStopReason", () => {
  it("wraps a label as a stop-kind status and clears on a clean end", () => {
    expect(statusFromStopReason("budget")).toEqual({
      text: "stopped · token budget",
      tone: "warn",
      kind: "stop",
    });
    expect(statusFromStopReason("end_turn")).toBeNull();
    expect(statusFromStopReason("error")).toBeNull();
  });
});

describe("sanitizeLine", () => {
  it("folds every line terminator and control character to one line", () => {
    // CRLF, LINE SEPARATOR, NEL, VT, FF and TAB.
    expect(sanitizeLine("a\r\nb\u2028c\u0085d\u000be\u000cf\tg", 100)).toBe(
      "a b c d e f g",
    );
  });

  it("clamps by code point, not UTF-16 unit, with an ellipsis", () => {
    expect(sanitizeLine("😀😀😀😀", 3)).toBe("😀😀…");
    expect(sanitizeLine("short", 5)).toBe("short");
  });
});
