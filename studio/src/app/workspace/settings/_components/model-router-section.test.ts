import { describe, expect, it } from "vitest";
import { categoryProblem } from "./model-router-section";

/**
 * Pins the category dialog's validation gate: the rules mirror the
 * controller's own (name grammar, description bound, model required,
 * no duplicate names), so a bad category can never be saved into the
 * routing draft in the first place.
 */

const valid = {
  name: "routine",
  description: "Mechanical edits, quick lookups, formatting.",
  model: "openai/gpt-test",
};
const noOthers = new Set<string>();

describe("categoryProblem", () => {
  it("accepts a complete category", () => {
    expect(categoryProblem(valid, noOthers)).toBeNull();
  });

  it("requires a name", () => {
    expect(categoryProblem({ ...valid, name: "   " }, noOthers)).toMatch(
      /name/i,
    );
  });

  it("enforces the name grammar", () => {
    for (const bad of ["9routine", "has space", "dot.name", "-lead"]) {
      expect(categoryProblem({ ...valid, name: bad }, noOthers)).toMatch(
        /start with a letter/i,
      );
    }
  });

  it("compares names case-insensitively (uppercase input is normalized, not rejected)", () => {
    expect(categoryProblem({ ...valid, name: "Routine" }, noOthers)).toBeNull();
  });

  it("caps the name at 40 characters", () => {
    expect(
      categoryProblem({ ...valid, name: `a${"b".repeat(39)}` }, noOthers),
    ).toBeNull();
    expect(
      categoryProblem({ ...valid, name: `a${"b".repeat(40)}` }, noOthers),
    ).toMatch(/start with a letter/i);
  });

  it("rejects a name another category already holds, whatever the casing", () => {
    const taken = new Set(["routine"]);
    expect(categoryProblem({ ...valid, name: " Routine " }, taken)).toMatch(
      /already named/i,
    );
    // The caller excludes the edited category's own name, so an unchanged
    // edit stays valid — pinned by the empty-set case.
    expect(categoryProblem(valid, noOthers)).toBeNull();
  });

  it("requires a description and bounds it at 300 characters", () => {
    expect(categoryProblem({ ...valid, description: "  " }, noOthers)).toMatch(
      /describe what belongs/i,
    );
    expect(
      categoryProblem({ ...valid, description: "x".repeat(300) }, noOthers),
    ).toBeNull();
    expect(
      categoryProblem({ ...valid, description: "x".repeat(301) }, noOthers),
    ).toMatch(/at most 300/i);
  });

  it("requires a model", () => {
    expect(categoryProblem({ ...valid, model: " " }, noOthers)).toMatch(
      /choose a model/i,
    );
  });
});
