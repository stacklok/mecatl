// SPDX-License-Identifier: Apache-2.0

import type { ServerCapabilitiesResponse } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import { deriveHelpFeatures } from "./help-features";

const allOff: ServerCapabilitiesResponse = {
  agents: false,
  audio: false,
  bash: false,
  debugMcp: false,
  image: false,
  learnedSkills: false,
  learningProposals: false,
  manualCompaction: false,
  mcp: false,
  mcpConnectorStatus: false,
  memory: false,
  modelSelection: false,
  posture: "strict",
  reflection: false,
  scheduling: false,
  sessionDebug: false,
  skills: false,
  slashCommands: false,
  soul: false,
  steer: false,
  storageCleanup: false,
  storageHealth: false,
  storageMigration: false,
  teams: false,
  userModel: false,
  workspaceEnrollment: false,
  worktrees: false,
};

describe("deriveHelpFeatures", () => {
  it("reads every row off as disabled when the daemon reports nothing on", () => {
    const rows = deriveHelpFeatures(allOff);
    expect(rows.every((row) => !row.enabled)).toBe(true);
    expect(rows.map((row) => row.id)).toEqual([
      "skills",
      "learned_skills",
      "memory",
      "manual_dream",
      "reflection",
      "steer",
      "manual_compaction",
      "scheduling",
      "model_selection",
    ]);
  });

  it("flips a flag row on when the capability is true", () => {
    const rows = deriveHelpFeatures({ ...allOff, skills: true, steer: true });
    expect(rows.find((row) => row.id === "skills")?.enabled).toBe(true);
    expect(rows.find((row) => row.id === "steer")?.enabled).toBe(true);
    expect(rows.find((row) => row.id === "memory")?.enabled).toBe(false);
  });

  it("gates the Memory row on the user-model capability, not generic memory", () => {
    const memoryOnly = deriveHelpFeatures({ ...allOff, memory: true });
    expect(memoryOnly.find((row) => row.id === "memory")?.enabled).toBe(false);

    const userModel = deriveHelpFeatures({ ...allOff, userModel: true });
    expect(userModel.find((row) => row.id === "memory")?.enabled).toBe(true);
  });

  it("reads memory consolidation off the exact gate the Consolidate button uses", () => {
    const off = deriveHelpFeatures(allOff);
    expect(off.find((row) => row.id === "manual_dream")?.enabled).toBe(false);

    const decideOnly = deriveHelpFeatures({
      ...allOff,
      manualDream: { userModel: { decide: true, generate: false } },
    });
    expect(decideOnly.find((row) => row.id === "manual_dream")?.enabled).toBe(false);

    const generateReady = deriveHelpFeatures({
      ...allOff,
      manualDream: { userModel: { decide: true, generate: true } },
    });
    expect(generateReady.find((row) => row.id === "manual_dream")?.enabled).toBe(true);
  });
});
