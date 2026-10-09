// SPDX-License-Identifier: Apache-2.0

import { expect, it } from "vitest";
import { humanizeSkillName } from "./humanize-skill-name";

it("title-cases slugs split on hyphens and underscores", () => {
  expect(humanizeSkillName("pr-feedback")).toBe("Pr Feedback");
  expect(humanizeSkillName("deploy_service--safely")).toBe("Deploy Service Safely");
  expect(humanizeSkillName("")).toBe("");
});
