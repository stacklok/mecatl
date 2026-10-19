// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { LINE_DIFF_MAX_LINES } from "../../lib/line-diff";
import { hasSkillDiffChanges, skillDiffRows } from "./skill-diff";

/** Builds a diff exactly as the daemon's DiffLearnedSkillVersions formats it. */
function daemonDiff(
  from: { body: string; description: string; version: string },
  to: { body: string; description: string; version: string },
) {
  return `--- ${from.version}\n+++ ${to.version}\n@@ description @@\n-${from.description}\n+${to.description}\n@@ body @@\n-${from.body}\n+${to.body}\n`;
}

describe("learned-skill diff", () => {
  it("classifies unified diff lines without treating file headers as changes", () => {
    const to = { body: "# Review\nnew instruction", description: "Review PRs", version: "v2" };
    const rows = skillDiffRows(
      daemonDiff(
        { body: "# Review\nold instruction", description: "Review PRs", version: "v1" },
        to,
      ),
      to,
    );
    expect(rows).toEqual([
      { kind: "metadata", text: "--- v1" },
      { kind: "metadata", text: "+++ v2" },
      { kind: "metadata", text: "@@ description @@" },
      { kind: "context", text: "Review PRs" },
      { kind: "metadata", text: "@@ body @@" },
      { kind: "context", text: "# Review" },
      { kind: "deletion", text: "old instruction" },
      { kind: "addition", text: "new instruction" },
    ]);
  });

  it("renders unchanged lines and markdown bullets in whole-body diffs by their real change", () => {
    const from = {
      body: "# Review\n\n- Read the diff\n- Run the tests\n+ legacy plus bullet\nSign off.",
      description: "Review PRs",
      version: "v1",
    };
    const to = {
      body: "# Review\n\n- Read the diff\n- Run the full suite\n+ legacy plus bullet\nSign off.",
      description: "Review pull requests",
      version: "v2",
    };
    const rows = skillDiffRows(daemonDiff(from, to), to);
    expect(rows).toEqual([
      { kind: "metadata", text: "--- v1" },
      { kind: "metadata", text: "+++ v2" },
      { kind: "metadata", text: "@@ description @@" },
      { kind: "deletion", text: "Review PRs" },
      { kind: "addition", text: "Review pull requests" },
      { kind: "metadata", text: "@@ body @@" },
      { kind: "context", text: "# Review" },
      { kind: "context", text: "" },
      { kind: "context", text: "- Read the diff" },
      { kind: "deletion", text: "- Run the tests" },
      { kind: "addition", text: "- Run the full suite" },
      { kind: "context", text: "+ legacy plus bullet" },
      { kind: "context", text: "Sign off." },
    ]);
    expect(hasSkillDiffChanges(rows)).toBe(true);

    const same = skillDiffRows(daemonDiff(from, { ...from, version: "v2" }), from);
    expect(hasSkillDiffChanges(same)).toBe(false);
  });

  it("degrades a body above the line-diff bound to a removed block then an added block", () => {
    const lines = (prefix: string) =>
      Array.from({ length: LINE_DIFF_MAX_LINES / 2 + 1 }, (_, i) => `${prefix}${i}`).join("\n");
    const from = { body: lines("old-"), description: "Review PRs", version: "v1" };
    const to = { body: lines("new-"), description: "Review PRs", version: "v2" };
    const rows = skillDiffRows(daemonDiff(from, to), to);
    const body = rows.slice(rows.findIndex((row) => row.text === "@@ body @@") + 1);
    const half = LINE_DIFF_MAX_LINES / 2 + 1;
    expect(body).toHaveLength(half * 2);
    expect(body.slice(0, half).every((row) => row.kind === "deletion")).toBe(true);
    expect(body.slice(half).every((row) => row.kind === "addition")).toBe(true);
    expect(body[0]).toEqual({ kind: "deletion", text: "old-0" });
    expect(body[half]).toEqual({ kind: "addition", text: "new-0" });
  });
});
