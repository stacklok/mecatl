import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  maxSkillUploadFiles,
  planSkillUpload,
} from "./skill-upload";

/**
 * Pins the upload planner: wrapper-folder stripping (the shape both "zip a
 * folder" and the folder picker produce), junk filtering, the root-SKILL.md
 * requirement, traversal rejection, and the caps.
 */

describe("planSkillUpload", () => {
  it("accepts root-relative paths untouched", () => {
    const plan = planSkillUpload(["SKILL.md", "scripts/run.sh"]);
    expect(plan.error).toBeNull();
    expect(plan.files).toEqual([
      { path: "SKILL.md", index: 0 },
      { path: "scripts/run.sh", index: 1 },
    ]);
  });

  it("strips one shared wrapper folder when SKILL.md lives inside it", () => {
    const plan = planSkillUpload([
      "my-skill/SKILL.md",
      "my-skill/references/notes.md",
    ]);
    expect(plan.error).toBeNull();
    expect(plan.files).toEqual([
      { path: "SKILL.md", index: 0 },
      { path: "references/notes.md", index: 1 },
    ]);
  });

  it("keeps paths as-is when SKILL.md is already at the root", () => {
    const plan = planSkillUpload(["SKILL.md", "docs/SKILL.md"]);
    expect(plan.error).toBeNull();
    expect(plan.files.map((f) => f.path)).toEqual([
      "SKILL.md",
      "docs/SKILL.md",
    ]);
  });

  it("drops directory entries, hidden files, and macOS zip noise", () => {
    const plan = planSkillUpload([
      "my-skill/",
      "my-skill/SKILL.md",
      "my-skill/.DS_Store",
      "__MACOSX/my-skill/._SKILL.md",
      "my-skill/.git/config",
    ]);
    expect(plan.error).toBeNull();
    expect(plan.files).toEqual([{ path: "SKILL.md", index: 1 }]);
  });

  it("requires a SKILL.md at the root", () => {
    const plan = planSkillUpload(["notes.md", "scripts/run.sh"]);
    expect(plan.error).toMatch(/needs a SKILL\.md/);
  });

  it("rejects traversal and absolute-ish paths", () => {
    expect(planSkillUpload(["../SKILL.md"]).error).toMatch(/invalid path/);
    expect(planSkillUpload(["a/../SKILL.md"]).error).toMatch(/invalid path/);
    expect(planSkillUpload(["/SKILL.md"]).error).toMatch(/invalid path/);
    expect(planSkillUpload(["a\\SKILL.md"]).error).toMatch(/invalid path/);
  });

  it("rejects case-colliding duplicate paths", () => {
    const plan = planSkillUpload(["SKILL.md", "Notes.md", "notes.md"]);
    expect(plan.error).toMatch(/duplicate paths/);
  });

  it("enforces the file-count cap", () => {
    const paths = ["SKILL.md"];
    for (let i = 0; i <= maxSkillUploadFiles; i += 1) {
      paths.push(`assets/file-${i}.txt`);
    }
    expect(planSkillUpload(paths).error).toMatch(/limited to/);
  });

  it("reports an empty upload", () => {
    expect(planSkillUpload(["__MACOSX/junk"]).error).toMatch(/no usable/);
  });
});

describe("bytesToBase64", () => {
  it("round-trips through atob", () => {
    const bytes = new Uint8Array([0, 1, 2, 250, 251, 252]);
    const decoded = atob(bytesToBase64(bytes));
    expect(Array.from(decoded, (c) => c.charCodeAt(0))).toEqual([
      0, 1, 2, 250, 251, 252,
    ]);
  });
});
