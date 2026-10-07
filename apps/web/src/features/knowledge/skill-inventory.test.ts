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
  it("sorts case-insensitively and numerically without mutating the input", () => {
    const input = [skill("b", { activeVersion: "v10" }), skill("A", { activeVersion: "v2" })];
    expect(sortSkills(input, "name", "asc").map((s) => s.name)).toEqual(["A", "b"]);
    expect(sortSkills(input, "version", "asc").map((s) => s.activeVersion)).toEqual(["v2", "v10"]);
    expect(sortSkills(input, "name", "desc").map((s) => s.name)).toEqual(["b", "A"]);
    expect(input.map((s) => s.name)).toEqual(["b", "A"]);
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
