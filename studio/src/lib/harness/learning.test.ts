import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import {
  decideLearningProposal,
  isProposalConflict,
  listLearningProposals,
  reflectHarnessSession,
  undoLearningPromotion,
} from "./learning";
import { resetHarnessClient } from "./sdk";
import { jsonResponse, stubHarnessFetch } from "./sdk-test-stub";

/**
 * Pins the learning-review contract (ADR 0109) over the SDK: the routes and
 * query/body the SDK produces (snake_case, no `project`), the decode of the
 * daemon's stdlib-JSON digest (`{seconds}` timestamps), the expected_version
 * concurrency token, and the 409 proposal_conflict classification.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("listLearningProposals", () => {
  it("builds the status/cursor/limit query — never `project` — and decodes the page", async () => {
    const stub = stubHarnessFetch(() => ({
      proposals: [
        {
          id: "p1",
          version: "v3",
          status: "staged",
          kind: "fact",
          key: "deploy/steps",
          title: "Deploy steps",
          description: "How this repo deploys",
          evidence: [{ session_id: "s1" }, { session_id: "s2" }],
          triggers: ["deploy"],
          decisions: [
            {
              kind: "approve",
              actor: "operator",
              at: { seconds: 1788000000, nanos: 0 },
            },
          ],
          created_at: { seconds: 1787000000, nanos: 5 },
          promotion_available: true,
          learned_skill_id: "sk1",
        },
      ],
      next_cursor: "c2",
    }));
    const page = await listLearningProposals({
      status: "staged",
      cursor: "c1",
      limit: 25,
    });
    const request = stub.last();
    expect(request.method).toBe("GET");
    expect(request.url).toBe(
      "/api/mecatl/v1/learning/proposals?status=staged&cursor=c1&limit=25",
    );
    expect(request.url).not.toContain("project");
    expect(page.nextCursor).toBe("c2");
    expect(page.proposals[0]).toMatchObject({
      id: "p1",
      version: "v3",
      status: "staged",
      key: "deploy/steps",
      evidenceCount: 2,
      triggers: ["deploy"],
      createdAtUnix: 1787000000,
      updatedAtUnix: 0,
      promotionAvailable: true,
      promotionUnavailableReason: "",
      learnedSkillId: "sk1",
    });
    expect(page.proposals[0]?.decisions).toEqual([
      { kind: "approve", actor: "operator", reason: "", atUnix: 1788000000 },
    ]);
  });

  it("omits empty filters and treats the daemon's `{}` answer as an empty queue", async () => {
    const stub = stubHarnessFetch(() => ({}));
    const page = await listLearningProposals();
    expect(stub.last().url).toBe("/api/mecatl/v1/learning/proposals");
    expect(page).toEqual({ proposals: [], nextCursor: "" });
  });

  it("throws the typed error on a problem response", async () => {
    stubHarnessFetch(() =>
      jsonResponse(501, {
        code: "learning_unavailable",
        error: "learning proposals are not configured",
      }),
    );
    await expect(listLearningProposals()).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "learning_unavailable",
      status: 501,
    });
  });
});

describe("decideLearningProposal", () => {
  it("POSTs {decision, expected_version} to /decision, id only in the path", async () => {
    const stub = stubHarnessFetch(() => ({
      proposal: { id: "p1", status: "promoted" },
    }));
    const updated = await decideLearningProposal("p1", "approve", "v3");
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: "/api/mecatl/v1/learning/proposals/p1/decision",
      body: { decision: "approve", expected_version: "v3" },
    });
    expect(updated.status).toBe("promoted");
  });

  it("carries a reason only when one is given", async () => {
    const stub = stubHarnessFetch(() => ({ proposal: { id: "p1" } }));
    await decideLearningProposal("p1", "reject", "v3", "stale");
    expect(stub.last().body).toEqual({
      decision: "reject",
      expected_version: "v3",
      reason: "stale",
    });
  });

  it("surfaces a stale-version 409 as a proposal conflict", async () => {
    stubHarnessFetch(() =>
      jsonResponse(409, {
        code: "proposal_conflict",
        error: "proposal changed",
      }),
    );
    const error = await decideLearningProposal("p1", "reject", "v1").catch(
      (caught) => caught,
    );
    expect(error).toBeInstanceOf(HarnessApiError);
    expect(isProposalConflict(error)).toBe(true);
    // A different code is NOT a conflict — flow control keys on the code.
    expect(
      isProposalConflict(new HarnessApiError(409, "dream_in_progress", "x")),
    ).toBe(false);
    // A code-less legacy daemon still classifies on the bare 409.
    expect(isProposalConflict(new HarnessApiError(409, "", "x"))).toBe(true);
  });
});

describe("undoLearningPromotion", () => {
  it("POSTs {expected_version} to /undo", async () => {
    const stub = stubHarnessFetch(() => ({
      proposal: { id: "p1", status: "undone" },
    }));
    const updated = await undoLearningPromotion("p1", "v4");
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: "/api/mecatl/v1/learning/proposals/p1/undo",
      body: { expected_version: "v4" },
    });
    expect(updated.status).toBe("undone");
  });
});

describe("reflectHarnessSession", () => {
  it("POSTs /sessions/{id}/reflect and decodes the receipt counts", async () => {
    const stub = stubHarnessFetch(() => ({
      receipt: {
        reflection_id: "r1",
        disposition: "completed",
        staged: 2,
        promoted: 1,
        conflicted: 0,
        abstained: false,
      },
    }));
    const receipt = await reflectHarnessSession("sess-1");
    expect(stub.last()).toMatchObject({
      method: "POST",
      url: "/api/mecatl/v1/sessions/sess-1/reflect",
    });
    expect(receipt).toEqual({
      reflectionId: "r1",
      disposition: "completed",
      queued: 0,
      abstained: false,
      staged: 2,
      promoted: 1,
      conflicted: 0,
    });
  });

  it("propagates a reflection failure as the typed error", async () => {
    stubHarnessFetch(() =>
      jsonResponse(500, { code: "internal", error: "reflection failed" }),
    );
    await expect(reflectHarnessSession("sess-1")).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "internal",
      status: 500,
    });
  });
});
