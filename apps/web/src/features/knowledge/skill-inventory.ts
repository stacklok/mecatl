// SPDX-License-Identifier: Apache-2.0

import type { ListConfiguredSkillsResponse } from "@mecatl-studio/contracts/generated";
import type { SortDirection } from "../../components/ui/sortable-head";

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
 * SPEC: sorting is stable and case-insensitive, and never mutates its input.
 * SPEC: filtering matches name, description and owner agent, case-insensitively.
 */
export type ConfiguredSkill = ListConfiguredSkillsResponse["items"][number];

export type SkillSortKey = "name" | "owner" | "version";

const sortValue: Record<SkillSortKey, (skill: ConfiguredSkill) => string> = {
  name: (skill) => skill.name,
  owner: (skill) => skill.ownerAgent,
  version: (skill) => skill.activeVersion,
};

export function sortSkills(
  skills: readonly ConfiguredSkill[],
  key: SkillSortKey,
  direction: SortDirection,
): ConfiguredSkill[] {
  const read = sortValue[key];
  const sign = direction === "asc" ? 1 : -1;
  return [...skills].sort(
    (a, b) =>
      sign * read(a).localeCompare(read(b), undefined, { numeric: true, sensitivity: "base" }),
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
