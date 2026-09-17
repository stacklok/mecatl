import { describe, expect, it } from "vitest";
import {
  AWAY_NOTICE_DURATION_MS,
  AWAY_NOTICE_TOAST_ID,
  type AwayNoticeInput,
  composeAwayNotice,
  MIN_AWAY_MS,
} from "./away-notice";
import type { ChatPhase } from "./chat-phase";

/**
 * The resume notice's wording table: every phase-at-leaving × phase-on-return
 * pair, the absence cutoff, the offline precedence, and the title clamp. The
 * daemon state is authoritative, so these are the ONLY inputs.
 */

function compose(overrides: Partial<AwayNoticeInput> = {}): string | null {
  return composeAwayNotice({
    leftPhase: "running",
    returnedPhase: "idle",
    chatTitle: "Fix the flaky test",
    awayMs: 60_000,
    connected: true,
    ...overrides,
  });
}

describe("composeAwayNotice — the phase table", () => {
  const phases: ChatPhase[] = ["idle", "running", "awaiting"];

  it.each<[ChatPhase, ChatPhase, string | null]>([
    ["idle", "idle", null],
    [
      "idle",
      "running",
      "“Fix the flaky test” started working while you were away.",
    ],
    ["idle", "awaiting", "“Fix the flaky test” is waiting for your approval."],
    [
      "running",
      "idle",
      "While you were away, “Fix the flaky test” kept running and finished.",
    ],
    [
      "running",
      "running",
      "“Fix the flaky test” is still working — it kept running while you were away.",
    ],
    [
      "running",
      "awaiting",
      "While you were away, “Fix the flaky test” kept running and is now waiting for your approval.",
    ],
    [
      "awaiting",
      "idle",
      "The approval on “Fix the flaky test” was resolved while you were away.",
    ],
    [
      "awaiting",
      "running",
      "The approval on “Fix the flaky test” was resolved while you were away; it is working again.",
    ],
    [
      "awaiting",
      "awaiting",
      "“Fix the flaky test” is still waiting for your approval.",
    ],
  ])("%s → %s", (leftPhase, returnedPhase, expected) => {
    expect(compose({ leftPhase, returnedPhase })).toBe(expected);
  });

  it("covers every pair (idle→idle is the one silent cell)", () => {
    const silent: string[] = [];
    for (const left of phases) {
      for (const returned of phases) {
        if (compose({ leftPhase: left, returnedPhase: returned }) === null) {
          silent.push(`${left}→${returned}`);
        }
      }
    }
    expect(silent).toEqual(["idle→idle"]);
  });

  it("names the session in every message", () => {
    for (const left of phases) {
      for (const returned of phases) {
        const message = compose({ leftPhase: left, returnedPhase: returned });
        if (message !== null) expect(message).toContain("“Fix the flaky test”");
      }
    }
  });
});

describe("composeAwayNotice — the absence cutoff", () => {
  it("is silent for an absence just under MIN_AWAY_MS", () => {
    expect(compose({ awayMs: MIN_AWAY_MS - 1 })).toBeNull();
  });

  it("speaks at exactly MIN_AWAY_MS", () => {
    expect(compose({ awayMs: MIN_AWAY_MS })).toBe(
      "While you were away, “Fix the flaky test” kept running and finished.",
    );
  });

  it("honours a caller-supplied minAwayMs", () => {
    expect(compose({ awayMs: 5_000, minAwayMs: 1_000 })).not.toBeNull();
    expect(compose({ awayMs: 5_000, minAwayMs: 10_000 })).toBeNull();
  });

  it("pins the defaults the hook uses", () => {
    expect(MIN_AWAY_MS).toBe(20_000);
    expect(AWAY_NOTICE_DURATION_MS).toBe(8_000);
    expect(AWAY_NOTICE_TOAST_ID).toBe("away-notice");
  });
});

describe("composeAwayNotice — offline precedence", () => {
  it("reports going offline ahead of any phase pair", () => {
    for (const left of ["idle", "running", "awaiting"] as ChatPhase[]) {
      expect(
        compose({
          leftPhase: left,
          returnedPhase: "running",
          connected: false,
        }),
      ).toBe("Mecatl went offline while you were away.");
    }
  });

  it("is silent when the daemon was already offline at leaving (the banner has been up)", () => {
    expect(compose({ connected: false, leftConnected: false })).toBeNull();
  });

  it("still applies the absence cutoff to the offline line", () => {
    expect(compose({ connected: false, awayMs: 1_000 })).toBeNull();
  });
});

describe("composeAwayNotice — the title", () => {
  it("clamps a long title to the tab title's 40 characters with an ellipsis", () => {
    const long = "a".repeat(60);
    const message = compose({ chatTitle: long });
    expect(message).toContain(`“${"a".repeat(39)}…”`);
    expect(message).not.toContain("a".repeat(40));
  });

  it("collapses whitespace and strips control characters like the tab title", () => {
    expect(compose({ chatTitle: "Fix\n\tthe test" })).toBe(
      "While you were away, “Fix the test” kept running and finished.",
    );
  });

  it("falls back to 'this chat' for a blank title, capitalised at a sentence head", () => {
    expect(compose({ chatTitle: "   " })).toBe(
      "While you were away, this chat kept running and finished.",
    );
    expect(
      compose({ chatTitle: "", leftPhase: "idle", returnedPhase: "awaiting" }),
    ).toBe("This chat is waiting for your approval.");
  });
});
