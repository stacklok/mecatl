// SPDX-License-Identifier: Apache-2.0

/** "pr-feedback" → "Pr Feedback"; the raw slug stays the id and route param. */
export function humanizeSkillName(name: string): string {
  return name
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}
