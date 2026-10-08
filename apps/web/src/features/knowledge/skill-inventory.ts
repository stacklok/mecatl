// SPDX-License-Identifier: Apache-2.0

import type { ListConfiguredSkillsResponse } from "@mecatl-studio/contracts/generated";
import type { SortDirection } from "../../components/ui/sortable-head";
import { humanizeSkillName } from "./humanize-skill-name";

/**
 * TERM: configured skill — a skill in the daemon's resolved ListSkills inventory,
 * managed by the deployment and read-only in Studio. Avoid: "installed skill",
 * "live skill" (a learned skill becomes live once activated, but stays "learned").
 *
 * TERM: learned skill — an agent-drafted version with a review lifecycle
 * (staged → active/rejected/archived). Avoid: "proposed skill", "draft skill".
 *
 * DECISION: the inventory is sorted and filtered client-side over the single page
 * the daemon returns. Reason: ListSkills has no paging or query parameters, and
 * the learned inventory is capped at 100 with `complete: false` shown in the UI.
 * Rejected: a server-side sort contract — it needs a daemon change nobody asked for.
 *
 * SPEC: sorting is case-insensitive, never mutates its input, and keeps name order as the
 * direction-independent tiebreak so equal rows do not move.
 * SPEC: filtering matches name, description and owner agent, case-insensitively.
 */
export type ConfiguredSkill = ListConfiguredSkillsResponse["items"][number];

export type SkillSortKey = "name" | "description";

const sortValue: Record<SkillSortKey, (skill: ConfiguredSkill) => string> = {
  description: (skill) => skill.description,
  name: (skill) => humanizeSkillName(skill.name),
};

export function sortSkills(
  skills: readonly ConfiguredSkill[],
  key: SkillSortKey,
  direction: SortDirection,
): ConfiguredSkill[] {
  const read = sortValue[key];
  const sign = direction === "asc" ? 1 : -1;
  const compare = (a: string, b: string) =>
    a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" });
  return [...skills].sort(
    (a, b) =>
      sign * compare(read(a), read(b)) ||
      compare(humanizeSkillName(a.name), humanizeSkillName(b.name)),
  );
}

export function filterSkills(skills: readonly ConfiguredSkill[], query: string): ConfiguredSkill[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...skills];
  return skills.filter((skill) =>
    [skill.name, skill.description, skill.ownerAgent].some((field) =>
      field.toLowerCase().includes(needle),
    ),
  );
}
