import { describe, expect, it } from "vitest";
import { validSkillName } from "./controller-security.mjs";

/**
 * The shared skill-name gate — the ONE grammar (mirroring the daemon's
 * skillfs.ValidSkillName) that both the controller's filesystem endpoints and
 * the browser client validate through. Everything the filesystem side relies
 * on (no separators, no dots, no traversal) must hold here, because a valid
 * name is joined directly onto the pinned skills directory.
 */
describe("validSkillName", () => {
  it("accepts the daemon's activation-name grammar", () => {
    for (const name of [
      "a",
      "pr-feedback",
      "skill_2",
      "0day-notes",
      "x".repeat(64),
    ]) {
      expect(validSkillName(name), name).toBe(true);
    }
  });

  it("rejects path traversal and separators", () => {
    for (const name of [
      "..",
      ".",
      "../evil",
      "a/../b",
      "a/b",
      "a\\b",
      ".disabled",
      ".hidden",
      "name.md",
    ]) {
      expect(validSkillName(name), name).toBe(false);
    }
  });

  it("rejects whitespace, case, emptiness, and oversized names", () => {
    for (const name of [
      "",
      " ",
      "two words",
      "Upper",
      "tab\tname",
      "new\nline",
      "-leading-dash",
      "_leading-underscore",
      "x".repeat(65),
    ]) {
      expect(validSkillName(name), JSON.stringify(name)).toBe(false);
    }
  });
});
