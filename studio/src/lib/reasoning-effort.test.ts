import { describe, expect, it } from "vitest";
import {
  AUTO_EFFORT,
  EFFORT_TIERS,
  effortLabel,
  effortTier,
  effortWire,
  isKnownEffort,
} from "./reasoning-effort";

/**
 * The reasoning-effort vocabulary mirrors the daemon's tiers exactly (the
 * proto CreateSessionRequest.reasoning_effort doc / mecatui's effort.go):
 * `auto` is Studio's name for the unset sentinel (wire ""), and the five
 * real tiers ride verbatim.
 */
describe("reasoning-effort vocabulary", () => {
  it("lists auto first, then the daemon's tiers in rank order", () => {
    expect([...EFFORT_TIERS]).toEqual([
      "auto",
      "low",
      "medium",
      "high",
      "xhigh",
      "max",
    ]);
    expect(AUTO_EFFORT).toBe("auto");
  });

  it("maps auto to the empty wire value and tiers to themselves", () => {
    expect(effortWire("auto")).toBe("");
    expect(effortWire("")).toBe("");
    expect(effortWire("medium")).toBe("medium");
    expect(effortWire(" XHIGH ")).toBe("xhigh");
  });

  it("reads the empty wire value back as auto", () => {
    expect(effortTier("")).toBe("auto");
    expect(effortTier("auto")).toBe("auto");
    expect(effortTier("max")).toBe("max");
  });

  it("labels every tier, the sentinel as Auto, and an unknown tier verbatim", () => {
    expect(effortLabel("")).toBe("Auto");
    expect(effortLabel("auto")).toBe("Auto");
    expect(effortLabel("low")).toBe("Low");
    expect(effortLabel("xhigh")).toBe("Extra high");
    expect(effortLabel("max")).toBe("Max");
    // A newer daemon's tier is shown, never dropped.
    expect(effortLabel("ultra")).toBe("ultra");
  });

  it("knows the sentinel and the five tiers, nothing else", () => {
    expect(isKnownEffort("")).toBe(true);
    expect(isKnownEffort("auto")).toBe(true);
    for (const tier of ["low", "medium", "high", "xhigh", "max"]) {
      expect(isKnownEffort(tier)).toBe(true);
    }
    expect(isKnownEffort("light")).toBe(false);
    expect(isKnownEffort("extra-high")).toBe(false);
  });
});
