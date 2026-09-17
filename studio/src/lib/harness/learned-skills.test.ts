import { afterEach, describe, expect, it, vi } from "vitest";
import {
  diffLearnedSkillVersions,
  fetchLearnedSkill,
  listLearnedSkillChanges,
  listLearnedSkills,
  mutateLearnedSkill,
  rollbackLearnedSkill,
} from "./learned-skills";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";

/**
 * Pins the learned-skill contract (ADR 0110) over the SDK: the routes and
 * query/body the SDK produces (snake_case, never `project`), the decode of
 * the daemon's stdlib-JSON rows (`{seconds}` timestamps), the
 * owner_agent/version/expected_revision mutation body, and the rollback
 * target_version body.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("listLearnedSkills", () => {
  it("builds the state/cursor/limit query and decodes the page", async () => {
    const stub = stubHarnessFetch(() => ({
      skills: [
        {
          id: "sk1",
          name: "triage-flakes",
          version: "v2",
          revision: "r7",
          state: "staged",
          owner_agent: "explorer",
          description: "Triage flaky tests",
          body: "# Steps",
          supersedes: "v1",
          evidence_count: 3,
          updated_at: { seconds: 1788000001, nanos: 0 },
          inspect_available: true,
        },
      ],
      next_cursor: "c2",
      generation: 12,
    }));
    const page = await listLearnedSkills({
      state: "staged",
      cursor: "c1",
      limit: 10,
    });
    expect(stub.last().url).toBe(
      "/api/mecatl/v1/skills/learned?cursor=c1&limit=10&state=staged",
    );
    expect(page.nextCursor).toBe("c2");
    expect(page.skills[0]).toEqual({
      id: "sk1",
      name: "triage-flakes",
      version: "v2",
      revision: "r7",
      state: "staged",
      ownerAgent: "explorer",
      description: "Triage flaky tests",
      body: "# Steps",
      supersedes: "v1",
      evidenceCount: 3,
      createdAtUnix: 0,
      updatedAtUnix: 1788000001,
      inspectAvailable: true,
      undoAvailable: false,
    });
  });

  it("sends no query at all for the default page", async () => {
    const stub = stubHarnessFetch(() => ({}));
    const page = await listLearnedSkills();
    expect(stub.last().url).toBe("/api/mecatl/v1/skills/learned");
    expect(page).toEqual({ skills: [], nextCursor: "" });
  });
});

describe("listLearnedSkillChanges", () => {
  it("GETs /skills/learned/changes and decodes the receipts", async () => {
    const stub = stubHarnessFetch(() => ({
      changes: [
        {
          id: "ch1",
          skill_id: "sk1",
          name: "triage-flakes",
          version: "v2",
          operation: "activate",
          from_state: "staged",
          to_state: "active",
          evidence_count: 3,
          verdict: "pass",
          at: { seconds: 1788000002, nanos: 9 },
        },
      ],
      next_cursor: "",
    }));
    const page = await listLearnedSkillChanges({ limit: 5 });
    expect(stub.last().url).toBe(
      "/api/mecatl/v1/skills/learned/changes?limit=5",
    );
    expect(page.changes).toEqual([
      {
        id: "ch1",
        skillId: "sk1",
        name: "triage-flakes",
        version: "v2",
        operation: "activate",
        fromState: "staged",
        toState: "active",
        evidenceCount: 3,
        verdict: "pass",
        atUnix: 1788000002,
      },
    ]);
  });
});

describe("fetchLearnedSkill / diffLearnedSkillVersions", () => {
  it("reads one version by owner_agent (+ version)", async () => {
    const stub = stubHarnessFetch(() => ({
      skill: { id: "sk1", version: "v1", body: "full body" },
    }));
    const skill = await fetchLearnedSkill("sk1", "explorer", "v1");
    expect(stub.last().url).toBe(
      "/api/mecatl/v1/skills/learned/sk1?owner_agent=explorer&version=v1",
    );
    expect(skill.body).toBe("full body");
  });

  it("diffs two versions via from/to", async () => {
    const stub = stubHarnessFetch(() => ({ diff: "--- a\n+++ b\n" }));
    const diff = await diffLearnedSkillVersions("sk1", "explorer", "v1", "v2");
    expect(stub.last().url).toBe(
      "/api/mecatl/v1/skills/learned/sk1/diff?owner_agent=explorer&from=v1&to=v2",
    );
    expect(diff).toBe("--- a\n+++ b\n");
  });
});

describe("mutateLearnedSkill", () => {
  it.each([
    "activate",
    "reject",
    "archive",
  ] as const)("POSTs owner_agent/version/expected_revision to /%s", async (action) => {
    const stub = stubHarnessFetch(() => ({
      skill: { id: "sk1", state: action === "activate" ? "active" : action },
      publication_status: "published",
      publication_error: "",
    }));
    const result = await mutateLearnedSkill(action, {
      id: "sk1",
      ownerAgent: "explorer",
      version: "v2",
      expectedRevision: "r7",
    });
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: `/api/mecatl/v1/skills/learned/sk1/${action}`,
      body: {
        owner_agent: "explorer",
        version: "v2",
        expected_revision: "r7",
      },
    });
    expect(result.publicationStatus).toBe("published");
    expect(result.skill.id).toBe("sk1");
  });

  it("surfaces a stale revision as the typed conflict error", async () => {
    stubHarnessFetch(() =>
      jsonResponse(409, {
        code: "conflict",
        error: "revision changed",
      }),
    );
    await expect(
      mutateLearnedSkill("activate", {
        id: "sk1",
        ownerAgent: "explorer",
        version: "v2",
        expectedRevision: "r1",
      }),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "conflict",
      status: 409,
    });
  });
});

describe("rollbackLearnedSkill", () => {
  it("POSTs target_version with the revision guard", async () => {
    const stub = stubHarnessFetch(() => ({
      skill: { id: "sk1", version: "v1", state: "active" },
    }));
    const result = await rollbackLearnedSkill({
      id: "sk1",
      ownerAgent: "explorer",
      targetVersion: "v1",
      expectedRevision: "r7",
    });
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: "/api/mecatl/v1/skills/learned/sk1/rollback",
      body: {
        owner_agent: "explorer",
        target_version: "v1",
        expected_revision: "r7",
      },
    });
    expect(result.skill.version).toBe("v1");
  });
});
