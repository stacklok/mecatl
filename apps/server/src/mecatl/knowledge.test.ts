// SPDX-License-Identifier: Apache-2.0

import { type Client, MecatlError } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it, vi } from "vitest";
import { createApp, knowledgeCapabilities } from "../app.js";
import { csrfHeaders, fakeRuntime, sampleSnapshot } from "../testing/fakes.js";
import { createMecatlKnowledgeService } from "./knowledge.js";
import { RuntimeNotReadyError } from "./runtime.js";

function sdkSkill(over: Record<string, unknown> = {}) {
  return {
    skill: {
      body: "# Review",
      description: "Review PRs",
      evidenceCount: 2,
      id: "review",
      name: "review",
      ownerAgent: "mecatl",
      revision: "r2",
      state: "active",
      supersedes: "v1",
      updatedAt: { nanos: 0, seconds: 1_700_000_000n },
      version: "v2",
      ...over,
    },
  };
}

describe("Mecatl knowledge adapter", () => {
  it("derives the knowledge capability set from the live runtime snapshot", async () => {
    const snapshot = sampleSnapshot();
    snapshot.capabilities = {
      ...snapshot.capabilities,
      learnedSkills: true,
      learningProposals: false,
      manualDream: {
        userModel: { decide: true, generate: false, unavailableReason: "needs a model" },
      },
      reflection: true,
      skills: false,
      userModel: true,
    };
    const live = knowledgeCapabilities(fakeRuntime({ snapshot: () => snapshot }));
    expect(live).toEqual({
      learnedSkills: true,
      learningProposals: false,
      memoryConsolidation: { decide: true, generate: false, unavailableReason: "needs a model" },
      reflection: true,
      skills: false,
      userModel: true,
    });

    const pending = knowledgeCapabilities(
      fakeRuntime({
        snapshot: () => {
          throw new RuntimeNotReadyError();
        },
      }),
    );
    expect(pending).toMatchObject({
      learnedSkills: false,
      learningProposals: false,
      memoryConsolidation: { decide: false, generate: false },
      reflection: false,
      skills: false,
      userModel: false,
    });

    // The two inventory lists degrade inside the service when their capability is off.
    const list = vi.fn();
    const service = createMecatlKnowledgeService(
      {
        learnedSkills: { list },
        learningProposals: { list },
        skills: { list },
      } as unknown as Client,
      () => pending,
    );
    await expect(service.listConfiguredSkills()).resolves.toMatchObject({
      items: [],
      supported: false,
    });
    await expect(service.listLearnedSkills()).resolves.toMatchObject({
      items: [],
      supported: false,
    });
    await expect(service.listLearningProposals("staged")).resolves.toMatchObject({
      items: [],
      supported: false,
    });
    expect(list).not.toHaveBeenCalled();
  });

  it("maps learned-skill actions onto the SDK operations with the expected revision and rollback target", async () => {
    const learnedSkills = {
      activate: vi.fn().mockResolvedValue(sdkSkill({ state: "active" })),
      archive: vi.fn().mockResolvedValue(sdkSkill({ state: "archived" })),
      reject: vi.fn().mockResolvedValue(sdkSkill({ state: "rejected" })),
      rollback: vi.fn().mockResolvedValue(sdkSkill({ version: "v1" })),
    };
    const service = createMecatlKnowledgeService({ learnedSkills } as unknown as Client, {
      learnedSkills: true,
      learningProposals: true,
      memoryConsolidation: { decide: true, generate: true, unavailableReason: "" },
      reflection: true,
      skills: true,
      userModel: true,
    });
    const base = { expectedRevision: "r2", ownerAgent: "mecatl", version: "v2" };

    for (const action of ["activate", "archive", "reject"] as const) {
      const result = await service.actOnLearnedSkill("review", { ...base, action });
      expect(learnedSkills[action]).toHaveBeenCalledWith({
        $typeName: "mecatl.v1.MutateLearnedSkillRequest",
        expectedRevision: "r2",
        id: "review",
        ownerAgent: "mecatl",
        project: "",
        version: "v2",
      });
      expect(result.id).toBe("review");
    }
    // Rollback reactivates an archived version; defaulting to the active one
    // would ask the daemon for something it always refuses.
    await expect(
      service.actOnLearnedSkill("review", { ...base, action: "rollback" }),
    ).rejects.toThrow("targetVersion");
    expect(learnedSkills.rollback).not.toHaveBeenCalled();
    await service.actOnLearnedSkill("review", { ...base, action: "rollback", targetVersion: "v1" });
    expect(learnedSkills.rollback).toHaveBeenLastCalledWith(
      expect.objectContaining({ expectedRevision: "r2", id: "review", targetVersion: "v1" }),
    );

    const active = await service.getLearnedSkill("review", "mecatl", "v2").catch(() => undefined);
    expect(active).toBeUndefined(); // no `get` on this fake: the adapter never falls back to another call

    // A missing concurrency token is refused at the route before any SDK call.
    const app = createApp({ knowledge: service });
    const missing = await app.request("/api/v1/learned-skills/review/actions", {
      body: JSON.stringify({
        action: "activate",
        expectedRevision: "",
        ownerAgent: "mecatl",
        version: "v2",
      }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
    });
    expect(missing.status).toBe(400);
    expect(learnedSkills.activate).toHaveBeenCalledTimes(1);
  });

  it("offers each learned-skill action only in the lifecycle states the daemon accepts", async () => {
    const pass = { verdict: "pass" };
    const abstain = { verdict: "abstain" };
    const cases = [
      {
        over: { evaluations: [], state: "draft", supersedes: "" },
        want: { activate: false, archive: false, reject: true, rollback: false },
      },
      {
        over: { evaluations: [pass], state: "evaluated" },
        want: { activate: false, archive: false, reject: true, rollback: false },
      },
      {
        over: { evaluations: [pass], state: "staged" },
        want: { activate: true, archive: false, reject: true, rollback: false },
      },
      {
        // The daemon activates only a staged version whose LATEST verdict is pass.
        over: { evaluations: [pass, abstain], state: "staged" },
        want: { activate: false, archive: false, reject: true, rollback: false },
      },
      {
        over: { evaluations: [pass], state: "active" },
        want: { activate: false, archive: true, reject: false, rollback: true },
      },
      {
        over: { evaluations: [pass], state: "active", supersedes: "" },
        want: { activate: false, archive: true, reject: false, rollback: false },
      },
      {
        over: { evaluations: [pass], state: "archived" },
        want: { activate: false, archive: false, reject: false, rollback: false },
      },
      {
        over: { evaluations: [{ verdict: "fail" }], state: "rejected" },
        want: { activate: false, archive: false, reject: false, rollback: false },
      },
    ];
    for (const { over, want } of cases) {
      const get = vi.fn().mockResolvedValue(sdkSkill(over));
      const service = createMecatlKnowledgeService(
        { learnedSkills: { get } } as unknown as Client,
        {
          learnedSkills: true,
          learningProposals: true,
          memoryConsolidation: { decide: true, generate: true, unavailableReason: "" },
          reflection: true,
          skills: true,
          userModel: true,
        },
      );
      const skill = await service.getLearnedSkill("review", "mecatl", "v2");
      expect({ state: skill.state, ...skill.actions }).toEqual({ state: over.state, ...want });
    }
  });

  it("relays a stale-revision conflict from the daemon without retrying", async () => {
    // The daemon maps learning.ErrSkillConflict to the stable `proposal_conflict`
    // code, carried over gRPC as Aborted (Connect code 10) with an ErrorInfo detail.
    const activate = vi.fn().mockRejectedValue(
      new MecatlError("server: proposal conflict: learning: skill revision conflict", {
        code: "proposal_conflict",
        status: 10,
        transport: "grpc",
      }),
    );
    const service = createMecatlKnowledgeService(
      { learnedSkills: { activate } } as unknown as Client,
      {
        learnedSkills: true,
        learningProposals: true,
        memoryConsolidation: { decide: true, generate: true, unavailableReason: "" },
        reflection: true,
        skills: true,
        userModel: true,
      },
    );
    const app = createApp({ knowledge: service });
    const response = await app.request("/api/v1/learned-skills/review/actions", {
      body: JSON.stringify({
        action: "activate",
        expectedRevision: "r1",
        ownerAgent: "mecatl",
        version: "v2",
      }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
    });
    expect(response.status).toBe(409);
    await expect(response.json()).resolves.toMatchObject({
      code: "proposal_conflict",
      status: 409,
    });
    expect(activate).toHaveBeenCalledTimes(1);
  });
});
