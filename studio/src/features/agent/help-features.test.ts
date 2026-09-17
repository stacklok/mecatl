import { describe, expect, it } from "vitest";
import { deriveHelpFeatures } from "./help-features";

/**
 * Pins the help page's feature derivation: every row keys off its own wire
 * capability with an explicit `=== true`, so an older daemon that omits the
 * key (or a non-boolean value) reads as "not enabled" — Studio never assumes
 * a feature the deployment did not advertise.
 */
describe("deriveHelpFeatures", () => {
  const none = new Set<string>();

  const byId = (caps: Record<string, unknown>, features = none) =>
    new Map(deriveHelpFeatures(caps, features).map((r) => [r.id, r]));

  it("has unique row ids", () => {
    const ids = deriveHelpFeatures({}, none).map((r) => r.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("disables every row against an older daemon with no capabilities", () => {
    for (const row of deriveHelpFeatures({}, none)) {
      expect(row.enabled, row.id).toBe(false);
    }
  });

  it("enables each boolean row from exactly its own wire key", () => {
    const keys = [
      "skills",
      "agents",
      "slash_commands",
      "memory",
      "manual_compaction",
      "scheduling",
      "teams",
      "image",
      "audio",
      "session_debug",
      "learned_skills",
      "worktrees",
      "mcp",
    ];
    for (const key of keys) {
      const rows = byId({ [key]: true });
      expect(rows.get(key)?.enabled, key).toBe(true);
      // No other row lights up from this key.
      for (const [id, row] of rows) {
        if (id !== key) expect(row.enabled, `${key} → ${id}`).toBe(false);
      }
    }
  });

  it("treats a non-boolean truthy value as not enabled", () => {
    expect(byId({ skills: "yes" }).get("skills")?.enabled).toBe(false);
    expect(byId({ skills: 1 }).get("skills")?.enabled).toBe(false);
  });

  it("enables steer from the live capability alone", () => {
    expect(byId({ steer: true }).get("steer")?.enabled).toBe(true);
  });

  it("enables steer from the http_steer feature alone", () => {
    expect(byId({}, new Set(["http_steer"])).get("steer")?.enabled).toBe(true);
  });

  it("leaves steer disabled with neither gate", () => {
    expect(
      byId({ steer: false }, new Set(["other"])).get("steer")?.enabled,
    ).toBe(false);
  });

  it("enables memory consolidation only for a non-empty target object", () => {
    expect(byId({}).get("manual_dream")?.enabled).toBe(false);
    expect(byId({ manual_dream: {} }).get("manual_dream")?.enabled).toBe(false);
    expect(byId({ manual_dream: true }).get("manual_dream")?.enabled).toBe(
      false,
    );
    expect(byId({ manual_dream: [] }).get("manual_dream")?.enabled).toBe(false);
    expect(
      byId({
        manual_dream: {
          project_memory: {
            generate: true,
            decide: true,
            unavailable_reason: "",
          },
        },
      }).get("manual_dream")?.enabled,
    ).toBe(true);
  });

  it("gives every row a label and a hint", () => {
    for (const row of deriveHelpFeatures({}, none)) {
      expect(row.label.length, row.id).toBeGreaterThan(0);
      expect(row.hint.length, row.id).toBeGreaterThan(0);
    }
  });
});
