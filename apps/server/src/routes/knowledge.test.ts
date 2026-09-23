// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { createApp } from "../app";
import { KnowledgeNotFoundError, type KnowledgeService } from "../mecatl/knowledge";
import { csrfHeaders } from "../testing/fakes";

const knowledge: KnowledgeService = {
  capabilities: {
    learnedSkills: true,
    learningProposals: true,
    memoryConsolidation: { decide: true, generate: true, unavailableReason: "" },
    reflection: true,
    skills: true,
    userModel: true,
  },
  async actOnLearnedSkill() {
    return {
      actions: { activate: false, archive: true, reject: false, rollback: false },
      body: "# Review pull requests",
      description: "Review pull requests consistently",
      evidenceCount: 2,
      id: "review",
      name: "pr-review",
      ownerAgent: "mecatl",
      revision: "2",
      state: "active",
      supersedes: "",
      updatedAt: null,
      version: "v1",
    };
  },
  async diffLearnedSkillVersions(_id, _ownerAgent, fromVersion, toVersion) {
    // The daemon's format: whole old/new description and body, not a line diff.
    return {
      diff: `--- ${fromVersion}\n+++ ${toVersion}\n@@ description @@\n-old\n+new\n@@ body @@\n-# Old\n+# New\n`,
      fromVersion,
      toVersion,
    };
  },
  async decideLearningProposal(id, request) {
    return proposal(
      id,
      request.expectedVersion,
      request.decision === "approve" ? "promoted" : "rejected",
    );
  },
  async decideMemoryConsolidationPlan(planId, request) {
    return {
      applied: request.decision === "apply" ? 2 : 0,
      conflicted: 0,
      disposition: request.decision,
      failed: 0,
      id: planId,
      planned: 2,
      skipped: 0,
      target: "user_model",
    };
  },
  async generateMemoryConsolidationPlan() {
    return {
      expiresAt: null,
      id: "plan-1",
      operations: [
        {
          exactDuplicateEligible: false,
          kind: "merge",
          reason: "overlapping preferences",
          replacement: { description: "Writing preference", value: "Keep answers concise." },
          sources: [{ description: "Short answers", key: "brevity", value: "Be brief." }],
          survivor: {
            description: "Communication preference",
            key: "communication",
            value: "Prefer concise answers.",
          },
        },
      ],
      plannedOperationCount: 1,
      plannedSourceCount: 2,
      target: "user_model",
    };
  },
  async getLearnedSkill(id, ownerAgent, version) {
    return {
      actions: { activate: false, archive: true, reject: false, rollback: true },
      body: "# Review pull requests",
      description: "Review pull requests consistently",
      evidenceCount: 2,
      id,
      name: "pr-review",
      ownerAgent,
      revision: "2",
      state: "active",
      supersedes: "v1",
      updatedAt: null,
      version,
    };
  },
  async getMemory(key) {
    return {
      current: {
        description: "Prefers concise answers",
        key,
        origin: "conversation",
        sourceSessionId: "session-1",
        status: "active",
        updatedAt: null,
        value: "Keep explanations short.",
        version: "1",
        writer: "mecatl",
      },
      history: [],
      historyAvailable: true,
    };
  },
  async listConfiguredSkills() {
    return {
      items: [
        {
          activeVersion: "",
          agentOwned: false,
          description: "Commit conventions",
          name: "commit-style",
          ownerAgent: "",
        },
      ],
      reason: "",
      supported: true,
    };
  },
  async listLearnedSkills() {
    return { complete: true, items: [], reason: "", supported: true };
  },
  async listLearnedSkillChanges() {
    return {
      complete: true,
      items: [
        {
          at: null,
          evidenceCount: 2,
          fromState: "staged",
          id: "change-1",
          name: "pr-review",
          operation: "activate",
          skillId: "review",
          toState: "active",
          verdict: "approved",
          version: "v2",
        },
      ],
    };
  },
  async listLearningProposals() {
    return {
      complete: true,
      items: [proposal("proposal-1", "v1", "staged")],
      reason: "",
      supported: true,
    };
  },
  async listMemory() {
    return {
      items: [{ description: "Prefers concise answers", key: "communication" }],
      reason: "",
      sha256: "abc",
      sizeBytes: "42",
      supported: true,
    };
  },
  async reflectSession() {
    return {
      abstained: false,
      conflicted: 0,
      disposition: "completed",
      message: "",
      promoted: 0,
      queued: 0,
      reason: "",
      reflectionId: "reflection-1",
      staged: 1,
    };
  },
  async undoLearningPromotion(id, request) {
    return proposal(id, request.expectedVersion, "undone");
  },
};

function proposal(id: string, version: string, status: string) {
  return {
    body: "",
    createdAt: null,
    decisions: [],
    description: "Prefer concise answers",
    evidenceCount: 2,
    id,
    key: "communication",
    kind: "user_model",
    learnedSkillId: "",
    projectScoped: false,
    promotionAvailable: true,
    promotionUnavailableReason: "",
    status,
    title: "Communication preference",
    triggers: ["explicit"],
    updatedAt: null,
    value: "Keep answers concise.",
    version,
  };
}

/** A test app whose `request` sends the bootstrap's CSRF pair on every mutation. */
function csrfApp(...args: Parameters<typeof createApp>) {
  const app = createApp(...args);
  const original = app.request.bind(app);
  app.request = ((input: string, init?: RequestInit, ...rest: unknown[]) => {
    const method = init?.method?.toUpperCase() ?? "GET";
    const headers = new Headers(init?.headers);
    if (["POST", "PUT", "PATCH", "DELETE"].includes(method)) {
      for (const [name, value] of Object.entries(csrfHeaders())) headers.set(name, value);
    }
    return (original as (...a: unknown[]) => ReturnType<typeof original>)(
      input,
      { ...init, headers },
      ...rest,
    );
  }) as typeof app.request;
  return app;
}

const app = csrfApp({ knowledge });

describe("knowledge routes", () => {
  it("lists configured skills", async () => {
    const response = await app.request("/api/v1/skills");
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({ items: [{ name: "commit-style" }] });
  });

  it("returns exact memory detail", async () => {
    const response = await app.request("/api/v1/user-memory/communication");
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({ current: { key: "communication" } });

    const missing = csrfApp({
      knowledge: {
        ...knowledge,
        getMemory: async (key) => {
          throw new KnowledgeNotFoundError(`No memory entry named ${key}`);
        },
      },
    });
    const notFound = await missing.request("/api/v1/user-memory/nope");
    expect(notFound.status).toBe(404);
    await expect(notFound.json()).resolves.toMatchObject({ code: "not_found" });
  });

  it("returns learned-skill detail, diff, and lifecycle history", async () => {
    const detail = await app.request("/api/v1/learned-skills/review?ownerAgent=mecatl&version=v2");
    expect(detail.status).toBe(200);
    expect(await detail.json()).toMatchObject({ id: "review", version: "v2" });

    const diff = await app.request(
      "/api/v1/learned-skills/review/diff?ownerAgent=mecatl&fromVersion=v1&toVersion=v2",
    );
    expect(diff.status).toBe(200);
    expect(await diff.json()).toMatchObject({
      diff: "--- v1\n+++ v2\n@@ description @@\n-old\n+new\n@@ body @@\n-# Old\n+# New\n",
      fromVersion: "v1",
      toVersion: "v2",
    });

    const changes = await app.request("/api/v1/learned-skills/changes");
    expect(changes.status).toBe(200);
    expect(await changes.json()).toMatchObject({ items: [{ operation: "activate" }] });
  });

  it("capability-disables learned-skill mutations", async () => {
    const unsupported = csrfApp({
      knowledge: {
        ...knowledge,
        capabilities: { ...knowledge.capabilities, learnedSkills: false },
      },
    });
    const response = await unsupported.request("/api/v1/learned-skills/review/actions", {
      body: JSON.stringify({
        action: "activate",
        expectedRevision: "1",
        ownerAgent: "mecatl",
        version: "v1",
      }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    expect(response.status).toBe(501);
    expect(await response.json()).toMatchObject({ code: "learned_skills_unsupported" });
  });

  it("reviews proposals and reflects completed sessions", async () => {
    const list = await app.request("/api/v1/learning-proposals?status=staged");
    expect(list.status).toBe(200);
    expect(await list.json()).toMatchObject({ items: [{ id: "proposal-1" }] });

    const decision = await app.request("/api/v1/learning-proposals/proposal-1/decisions", {
      body: JSON.stringify({ decision: "approve", expectedVersion: "v1", reason: "" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    expect(decision.status).toBe(200);
    expect(await decision.json()).toMatchObject({ status: "promoted" });

    const undo = await app.request("/api/v1/learning-proposals/proposal-1/undo", {
      body: JSON.stringify({ expectedVersion: "v2" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    expect(undo.status).toBe(200);
    expect(await undo.json()).toMatchObject({ status: "undone" });

    const reflection = await app.request("/api/v1/sessions/session-1/reflection", {
      method: "POST",
    });
    expect(reflection.status).toBe(200);
    expect(await reflection.json()).toMatchObject({ reflectionId: "reflection-1", staged: 1 });
  });

  it("generates and decides daemon-curated memory consolidation plans", async () => {
    const generated = await app.request("/api/v1/user-memory/consolidation/plans", {
      method: "POST",
    });
    expect(generated.status).toBe(201);
    expect(await generated.json()).toMatchObject({
      id: "plan-1",
      operations: [{ survivor: { key: "communication" } }],
    });

    const decided = await app.request("/api/v1/user-memory/consolidation/plans/plan-1/decisions", {
      body: JSON.stringify({ decision: "apply" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    expect(decided.status).toBe(200);
    expect(await decided.json()).toMatchObject({ applied: 2, disposition: "apply" });
  });
  it("capability-disables each knowledge surface with its own 501 code", async () => {
    const off = csrfApp({
      knowledge: {
        ...knowledge,
        capabilities: {
          learnedSkills: false,
          learningProposals: false,
          memoryConsolidation: {
            decide: false,
            generate: false,
            unavailableReason: "Dreaming is off",
          },
          reflection: false,
          skills: false,
          userModel: false,
        },
      },
    });
    const json = { "Content-Type": "application/json" };
    const cases: Array<[string, RequestInit | undefined, string]> = [
      [
        "/api/v1/learned-skills/review?ownerAgent=mecatl&version=v2",
        undefined,
        "learned_skills_unsupported",
      ],
      [
        "/api/v1/learned-skills/review/diff?ownerAgent=mecatl&fromVersion=v1&toVersion=v2",
        undefined,
        "learned_skills_unsupported",
      ],
      ["/api/v1/learned-skills/changes", undefined, "learned_skills_unsupported"],
      [
        "/api/v1/learned-skills/review/actions",
        {
          body: JSON.stringify({
            action: "activate",
            expectedRevision: "r1",
            ownerAgent: "mecatl",
            version: "v2",
          }),
          headers: json,
          method: "POST",
        },
        "learned_skills_unsupported",
      ],
      [
        "/api/v1/learning-proposals/proposal-1/decisions",
        {
          body: JSON.stringify({ decision: "approve", expectedVersion: "1" }),
          headers: json,
          method: "POST",
        },
        "learning_proposals_unsupported",
      ],
      [
        "/api/v1/learning-proposals/proposal-1/undo",
        { body: JSON.stringify({ expectedVersion: "1" }), headers: json, method: "POST" },
        "learning_proposals_unsupported",
      ],
      ["/api/v1/sessions/session-1/reflection", { method: "POST" }, "reflection_unsupported"],
      [
        "/api/v1/user-memory/consolidation/plans",
        { method: "POST" },
        "memory_consolidation_unsupported",
      ],
      [
        "/api/v1/user-memory/consolidation/plans/plan-1/decisions",
        { body: JSON.stringify({ decision: "apply" }), headers: json, method: "POST" },
        "memory_consolidation_decision_unsupported",
      ],
      ["/api/v1/user-memory/communication", undefined, "user_memory_unsupported"],
    ];
    for (const [path, init, code] of cases) {
      const response = await off.request(path, init);
      expect(response.status, path).toBe(501);
      await expect(response.json()).resolves.toMatchObject({ code });
    }
    const consolidation = await off.request("/api/v1/user-memory/consolidation/plans", {
      method: "POST",
    });
    await expect(consolidation.json()).resolves.toMatchObject({ detail: "Dreaming is off" });
    // The list routes degrade inside the service rather than at the route; the route stays 200.
    expect((await off.request("/api/v1/skills")).status).toBe(200);
    expect((await off.request("/api/v1/learned-skills")).status).toBe(200);
  });

  it("answers 503 runtime_unavailable on every knowledge route without a runtime", async () => {
    const detached = csrfApp();
    const json = { "Content-Type": "application/json" };
    for (const [path, init] of [
      ["/api/v1/skills", undefined],
      ["/api/v1/learned-skills", undefined],
      ["/api/v1/learned-skills/changes", undefined],
      ["/api/v1/learned-skills/review?ownerAgent=mecatl&version=v2", undefined],
      [
        "/api/v1/learned-skills/review/diff?ownerAgent=mecatl&fromVersion=v1&toVersion=v2",
        undefined,
      ],
      [
        "/api/v1/learned-skills/review/actions",
        {
          body: JSON.stringify({
            action: "activate",
            expectedRevision: "r1",
            ownerAgent: "mecatl",
            version: "v2",
          }),
          headers: json,
          method: "POST",
        },
      ],
      ["/api/v1/learning-proposals?status=staged", undefined],
      [
        "/api/v1/learning-proposals/proposal-1/decisions",
        {
          body: JSON.stringify({ decision: "approve", expectedVersion: "1" }),
          headers: json,
          method: "POST",
        },
      ],
      [
        "/api/v1/learning-proposals/proposal-1/undo",
        { body: JSON.stringify({ expectedVersion: "1" }), headers: json, method: "POST" },
      ],
      ["/api/v1/sessions/session-1/reflection", { method: "POST" }],
      ["/api/v1/user-memory", undefined],
      ["/api/v1/user-memory/communication", undefined],
      ["/api/v1/user-memory/consolidation/plans", { method: "POST" }],
      [
        "/api/v1/user-memory/consolidation/plans/plan-1/decisions",
        { body: JSON.stringify({ decision: "apply" }), headers: json, method: "POST" },
      ],
    ] as const) {
      const response = await detached.request(path, init);
      expect(response.status, path).toBe(503);
      await expect(response.json()).resolves.toMatchObject({ code: "runtime_unavailable" });
    }
  });

  it("knowledge mutations require the CSRF pair and a session when interactive login is active", async () => {
    const bare = createApp({ knowledge });
    const json = { "Content-Type": "application/json" };
    for (const [path, init] of [
      ["/api/v1/learned-skills/review/actions", { body: "{}", headers: json, method: "POST" }],
      [
        "/api/v1/learning-proposals/proposal-1/decisions",
        { body: "{}", headers: json, method: "POST" },
      ],
      ["/api/v1/learning-proposals/proposal-1/undo", { body: "{}", headers: json, method: "POST" }],
      ["/api/v1/sessions/session-1/reflection", { method: "POST" }],
      ["/api/v1/user-memory/consolidation/plans", { method: "POST" }],
      [
        "/api/v1/user-memory/consolidation/plans/plan-1/decisions",
        { body: "{}", headers: json, method: "POST" },
      ],
    ] as const) {
      const response = await bare.request(path, init);
      expect(response.status, path).toBe(403);
      await expect(response.json()).resolves.toMatchObject({ code: "cross_site_request" });
    }
    const gated = csrfApp({
      authentication: {
        clear: () => undefined,
        completeLogin: async () => {
          throw new Error("unused");
        },
        credential: async () => ({ status: "anonymous" }),
        logout: async () => undefined,
        noteLoginComplete: () => undefined,
        noteLoginFailure: () => undefined,
        save: async () => undefined,
        startLogin: async () => "https://issuer.example.com/authorize",
      },
      knowledge,
    });
    expect((await gated.request("/api/v1/skills")).status).toBe(401);
    const reflect = await gated.request("/api/v1/sessions/session-1/reflection", {
      method: "POST",
    });
    expect(reflect.status).toBe(401);
    await expect(reflect.json()).resolves.toMatchObject({ code: "unauthenticated" });
  });

  it("refuses a rollback without a target version before any SDK call", async () => {
    let called = false;
    const guarded = csrfApp({
      knowledge: {
        ...knowledge,
        async actOnLearnedSkill(...args: Parameters<typeof knowledge.actOnLearnedSkill>) {
          called = true;
          return knowledge.actOnLearnedSkill(...args);
        },
      },
    });
    const response = await guarded.request("/api/v1/learned-skills/review/actions", {
      body: JSON.stringify({
        action: "rollback",
        expectedRevision: "1",
        ownerAgent: "mecatl",
        version: "v2",
      }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    expect(response.status).toBe(400);
    expect(called).toBe(false);
  });
});
