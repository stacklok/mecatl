// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { type ConfiguredSkill, filterSkills, sortSkills } from "./skill-inventory";

const skill = (name: string, over: Partial<ConfiguredSkill> = {}): ConfiguredSkill => ({
  activeVersion: "",
  agentOwned: false,
  description: "",
  name,
  ownerAgent: "",
  ...over,
});

describe("skill inventory", () => {
  it("sorts case-insensitively without mutating the input", () => {
    const input = [skill("b-skill", { description: "z" }), skill("A", { description: "y" })];
    expect(sortSkills(input, "name", "asc").map((s) => s.name)).toEqual(["A", "b-skill"]);
    expect(sortSkills(input, "name", "desc").map((s) => s.name)).toEqual(["b-skill", "A"]);
    expect(sortSkills(input, "description", "asc").map((s) => s.name)).toEqual(["A", "b-skill"]);
    expect(input.map((s) => s.name)).toEqual(["b-skill", "A"]);
  });

  it("breaks ties by name whatever the direction", () => {
    const input = [skill("b", { description: "same" }), skill("a", { description: "same" })];
    expect(sortSkills(input, "description", "desc").map((s) => s.name)).toEqual(["a", "b"]);
  });

  it("filters across name, description and owner", () => {
    const input = [
      skill("deploy"),
      skill("lint", { description: "Fix DEPLOY warnings" }),
      skill("other", { ownerAgent: "deployer" }),
      skill("unrelated"),
    ];
    expect(filterSkills(input, " Deploy ").map((s) => s.name)).toEqual(["deploy", "lint", "other"]);
    expect(filterSkills(input, "")).toHaveLength(4);
  });
});
