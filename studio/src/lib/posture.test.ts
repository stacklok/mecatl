import { describe, expect, it } from "vitest";
import { POSTURE_TIERS, postureSummary, postureTone } from "./posture";

/**
 * The posture helper mirrors the TUI's `/posture` builtin
 * (`cmd/mecatui/ui/builtins.go` `postureSummary`): the SAME sentence for the
 * same tier, the same four-defense matrix (allow-all + main substitution at
 * auto and yolo, child substitution at yolo only, project trust at trusted
 * and above), and an empty/unknown token degrading to "unknown" with every
 * defense off.
 */

describe("postureSummary", () => {
  // The Go function's output, transcribed literally, one row per tier plus
  // the empty-token degrade. A drift in either implementation fails here.
  const table: [string, string][] = [
    [
      "strict",
      "posture strict — allow-all off; main $()/heredoc auto-run off; child $()/heredoc auto-run (injection-defense off) off; project-trust off (Deny & configured Ask always apply)",
    ],
    [
      "trusted",
      "posture trusted — allow-all off; main $()/heredoc auto-run off; child $()/heredoc auto-run (injection-defense off) off; project-trust on (Deny & configured Ask always apply)",
    ],
    [
      "auto",
      "posture auto — allow-all on; main $()/heredoc auto-run on; child $()/heredoc auto-run (injection-defense off) off; project-trust on (Deny & configured Ask always apply)",
    ],
    [
      "yolo",
      "posture yolo — allow-all on; main $()/heredoc auto-run on; child $()/heredoc auto-run (injection-defense off) on; project-trust on (Deny & configured Ask always apply)",
    ],
    [
      "",
      "posture unknown — allow-all off; main $()/heredoc auto-run off; child $()/heredoc auto-run (injection-defense off) off; project-trust off (Deny & configured Ask always apply)",
    ],
  ];

  it.each(table)("renders the TUI sentence for %j", (tier, expected) => {
    expect(postureSummary(tier)).toBe(expected);
  });

  it("keeps an unrecognised token as the label but switches every defense off", () => {
    expect(postureSummary("paranoid")).toBe(
      "posture paranoid — allow-all off; main $()/heredoc auto-run off; child $()/heredoc auto-run (injection-defense off) off; project-trust off (Deny & configured Ask always apply)",
    );
  });
});

describe("POSTURE_TIERS", () => {
  it("is mecated's ladder, lowest tier first", () => {
    expect(POSTURE_TIERS).toEqual(["strict", "trusted", "auto", "yolo"]);
  });
});
describe("postureTone", () => {
  it("is quiet for the asking tiers, a warning for auto, danger for yolo", () => {
    expect(postureTone("strict")).toBe("muted");
    expect(postureTone("trusted")).toBe("muted");
    expect(postureTone("")).toBe("muted");
    expect(postureTone("weird")).toBe("muted");
    expect(postureTone("auto")).toBe("warning");
    expect(postureTone("yolo")).toBe("danger");
  });
});
