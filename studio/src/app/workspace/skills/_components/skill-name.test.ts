import { describe, expect, it } from "vitest";
import { deriveSkillName, sanitizeSkillName } from "./skill-name";

/**
 * The upload path's name suggestion: frontmatter `name:` first, filename slug
 * second, both coerced through the daemon's activation-name grammar. Every
 * output must satisfy the shared validSkillName gate — the POST would be
 * refused otherwise.
 */
describe("sanitizeSkillName", () => {
  it("lowercases and hyphenates whitespace", () => {
    expect(sanitizeSkillName("My Skill Notes")).toBe("my-skill-notes");
  });

  it("strips characters outside the grammar", () => {
    expect(sanitizeSkillName("PR (feedback) v2!")).toBe("pr-feedback-v2");
  });

  it("strips a leading hyphen/underscore run so the name starts alphanumeric", () => {
    expect(sanitizeSkillName("_private-notes")).toBe("private-notes");
    expect(sanitizeSkillName("--draft")).toBe("draft");
  });

  it("clamps to the 64-character bound", () => {
    expect(sanitizeSkillName("x".repeat(80))).toBe("x".repeat(64));
  });

  it("returns empty when nothing grammar-valid survives", () => {
    expect(sanitizeSkillName("###")).toBe("");
    expect(sanitizeSkillName("")).toBe("");
    expect(sanitizeSkillName("---")).toBe("");
  });
});

describe("deriveSkillName", () => {
  it("prefers the frontmatter name over the filename", () => {
    const content = "---\nname: pr-helper\ndescription: Handles PRs\n---\nbody";
    expect(deriveSkillName("Whatever Else.md", content)).toBe("pr-helper");
  });

  it("sanitizes a quoted, cased frontmatter name", () => {
    const content = '---\nname: "My Helper"\n---\nbody';
    expect(deriveSkillName("file.md", content)).toBe("my-helper");
  });

  it("falls back to the filename slug without frontmatter", () => {
    expect(deriveSkillName("My Skill Notes.md", "# just markdown")).toBe(
      "my-skill-notes",
    );
  });

  it("falls back to the filename when the frontmatter name sanitizes to nothing", () => {
    const content = "---\nname: 日本語\n---\nbody";
    expect(deriveSkillName("fallback-name.md", content)).toBe("fallback-name");
  });

  it("ignores a name line outside the frontmatter block", () => {
    const content = "# heading\nname: not-frontmatter";
    expect(deriveSkillName("real-name.md", content)).toBe("real-name");
  });

  it("drops only the final extension", () => {
    expect(deriveSkillName("skill.notes.md", "no frontmatter")).toBe(
      "skillnotes",
    );
  });

  it("returns empty when neither source yields a valid name", () => {
    expect(deriveSkillName("###.md", "plain text")).toBe("");
  });
});
