import { ServerPosture } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { POSTURE_OPTIONS } from "@/app/workspace/settings/_components/permissions-section";
import {
  ALLOW_ALL_POSTURES,
  POSTURES,
  postureImpliesTrust,
  postureRank,
} from "./controller-permissions.mjs";
import { POSTURE_TIERS } from "./posture";

/**
 * The operator posture ladder has ONE vocabulary — the SDK's `ServerPosture`
 * (what the daemon's compatibility document can report) — and Studio
 * restates it in three places for three audiences: `POSTURES` in
 * controller-permissions.mjs (the node controller turns the saved tier into
 * `--posture <tier>` and cannot import the browser SDK), `POSTURE_TIERS` in
 * posture.ts (the read-only defenses table off the daemon-reported tier),
 * and `POSTURE_OPTIONS` on the Permissions page (the picker a person
 * chooses the launch flag from). A tier added or renamed on the daemon
 * side lands in the SDK first; this test is what makes the three copies
 * fail loudly instead of drifting apart one at a time.
 *
 * Order matters too: every copy is the LADDER, lowest tier first, because
 * `postureRank` compares positions (a higher-ranked tier implies project
 * trust on an interactive root).
 */

const ladder = Object.values(ServerPosture);

describe("the posture vocabulary", () => {
  it("is the SDK's ServerPosture ladder, in order, in every Studio copy", () => {
    expect(ladder).toEqual(["strict", "trusted", "auto", "yolo"]);
    expect([...POSTURES]).toEqual(ladder);
    expect([...POSTURE_TIERS]).toEqual(ladder);
    expect(POSTURE_OPTIONS.map((option) => option.value)).toEqual(ladder);
  });

  it("ranks the ladder by position and reads any other token as below strict", () => {
    for (const [index, tier] of ladder.entries()) {
      expect(postureRank(tier)).toBe(index);
    }
    expect(postureRank("")).toBe(-1);
    expect(postureRank("paranoid")).toBe(-1);
  });

  it("keeps the allow-all tiers a subset of the ladder, at the top of it", () => {
    for (const tier of ALLOW_ALL_POSTURES) {
      expect(ladder).toContain(tier);
      // Allow-all waives the mutate-ask floor, which mecated only does at
      // tiers that already imply project trust.
      expect(postureImpliesTrust(tier)).toBe(true);
    }
    expect([...ALLOW_ALL_POSTURES]).toEqual([
      ServerPosture.Auto,
      ServerPosture.Yolo,
    ]);
  });

  it("implies project trust from the SDK's Trusted tier upward, never at Strict", () => {
    expect(postureImpliesTrust(ServerPosture.Strict)).toBe(false);
    expect(postureImpliesTrust(ServerPosture.Trusted)).toBe(true);
    expect(postureImpliesTrust(ServerPosture.Auto)).toBe(true);
    expect(postureImpliesTrust(ServerPosture.Yolo)).toBe(true);
    // An unknown token never implies trust (fail-safe in the untrusted direction).
    expect(postureImpliesTrust("")).toBe(false);
  });
});
