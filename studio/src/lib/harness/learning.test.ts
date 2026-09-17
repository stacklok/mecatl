import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import {
  approvalBlockedReason,
  decideLearningProposal,
  getLearningProposal,
  isProposalApprovable,
  isProposalConflict,
  type LearningProposal,
  listLearningProposals,
  PROPOSAL_STATUS_DEFERRED,
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
          evidence: [
            {
              session_id: "s1",
              locator: "tool",
              ordinal: 3,
              // int64 rides as a JSON string in protojson; the SDK yields
              // a bigint, which the digest narrows to a number.
              event_seq: "9007199254740",
              tool_call_id: "call-9",
              digest: "sha256:abcdef0123456789",
              available: true,
              availability: "",
              preview: "$ task test\nok",
            },
            {
              session_id: "s2",
              available: false,
              availability: "source session deleted",
            },
          ],
          promotion: {
            memory_key: "deploy/steps",
            previous_exists: true,
            previous_version: "7",
            result_version: "8",
          },
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
    // Every evidence field crosses, and an omitted field decodes to its zero
    // value rather than undefined — the row never has to null-check.
    expect(page.proposals[0]?.evidence).toEqual([
      {
        sessionId: "s1",
        locator: "tool",
        ordinal: 3,
        eventSeq: 9007199254740,
        toolCallId: "call-9",
        digest: "sha256:abcdef0123456789",
        available: true,
        availability: "",
        preview: "$ task test\nok",
      },
      {
        sessionId: "s2",
        locator: "",
        ordinal: 0,
        eventSeq: 0,
        toolCallId: "",
        digest: "",
        available: false,
        availability: "source session deleted",
        preview: "",
      },
    ]);
    expect(page.proposals[0]?.promotion).toEqual({
      memoryKey: "deploy/steps",
      previousExists: true,
      previousVersion: "7",
      resultVersion: "8",
    });
  });

  it("decodes a proposal without a receipt as promotion: null", async () => {
    stubHarnessFetch(() => ({
      proposals: [{ id: "p1", status: "staged" }],
    }));
    const page = await listLearningProposals();
    expect(page.proposals[0]?.promotion).toBeNull();
    expect(page.proposals[0]?.evidence).toEqual([]);
    expect(page.proposals[0]?.evidenceCount).toBe(0);
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

describe("getLearningProposal", () => {
  it("GETs /learning/proposals/{id} with no `project` and decodes the proposal", async () => {
    const stub = stubHarnessFetch(() => ({
      proposal: {
        id: "prop-1",
        version: "v2",
        status: "deferred_unsupported",
        kind: "procedure",
        evidence: [{ session_id: "s1", available: true }],
      },
    }));
    const proposal = await getLearningProposal("prop-1");
    const request = stub.last();
    expect(request.method).toBe("GET");
    // The empty operator-scope `project` is omitted from the query, so the
    // browser never spells a workspace path (rule 2) — pinned like list.
    expect(request.url).toBe("/api/mecatl/v1/learning/proposals/prop-1");
    expect(request.url).not.toContain("project");
    expect(proposal).toMatchObject({
      id: "prop-1",
      version: "v2",
      status: PROPOSAL_STATUS_DEFERRED,
      kind: "procedure",
      evidenceCount: 1,
    });
    expect(proposal.evidence[0]?.available).toBe(true);
  });

  it("throws the typed error when the proposal is gone", async () => {
    stubHarnessFetch(() =>
      jsonResponse(404, { code: "not_found", error: "proposal missing" }),
    );
    await expect(getLearningProposal("prop-9")).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "not_found",
      status: 404,
    });
  });
});

describe("isProposalApprovable / approvalBlockedReason", () => {
  const evidence = (available: boolean) => ({
    sessionId: "s1",
    locator: "tool",
    ordinal: 1,
    eventSeq: 4,
    toolCallId: "",
    digest: "d",
    available,
    availability: available ? "" : "digest mismatch",
    preview: "",
  });
  const proposal = (
    overrides: Partial<LearningProposal> = {},
  ): LearningProposal => ({
    id: "p1",
    version: "1",
    status: "staged",
    kind: "fact",
    key: "k",
    value: "v",
    description: "",
    title: "",
    body: "",
    triggers: [],
    evidence: [evidence(true)],
    evidenceCount: 1,
    decisions: [],
    promotion: null,
    createdAtUnix: 0,
    updatedAtUnix: 0,
    projectScoped: false,
    promotionAvailable: true,
    promotionUnavailableReason: "",
    learnedSkillId: "",
    ...overrides,
  });

  it("allows a staged proposal whose evidence all resolves", () => {
    expect(isProposalApprovable(proposal())).toBe(true);
    expect(approvalBlockedReason(proposal())).toBeUndefined();
  });

  it("allows a deferred proposal too — approving materializes the learned-skill draft", () => {
    const deferred = proposal({
      status: PROPOSAL_STATUS_DEFERRED,
      kind: "procedure",
    });
    expect(isProposalApprovable(deferred)).toBe(true);
  });

  it("uses the exact deferred token, not a prefix", () => {
    expect(PROPOSAL_STATUS_DEFERRED).toBe("deferred_unsupported");
    expect(isProposalApprovable(proposal({ status: "deferred" }))).toBe(false);
  });

  it("refuses when ANY evidence handle is unavailable, naming that", () => {
    const stale = proposal({ evidence: [evidence(true), evidence(false)] });
    expect(isProposalApprovable(stale)).toBe(false);
    expect(approvalBlockedReason(stale)).toMatch(/no longer available/);
  });

  it("refuses a proposal with no evidence at all (never vacuously approvable)", () => {
    const bare = proposal({ evidence: [], evidenceCount: 0 });
    expect(isProposalApprovable(bare)).toBe(false);
    expect(approvalBlockedReason(bare)).toMatch(/no evidence/);
  });

  it("refuses when the partition cannot promote, preferring the daemon's reason", () => {
    const blocked = proposal({
      promotionAvailable: false,
      promotionUnavailableReason: "memory target is read-only",
    });
    expect(isProposalApprovable(blocked)).toBe(false);
    expect(approvalBlockedReason(blocked)).toBe("memory target is read-only");
    expect(approvalBlockedReason(proposal({ promotionAvailable: false }))).toBe(
      "This partition has no trusted memory target.",
    );
  });

  it("refuses every other status", () => {
    for (const status of ["promoted", "rejected", "conflicted", "undone"]) {
      expect(isProposalApprovable(proposal({ status }))).toBe(false);
    }
    expect(approvalBlockedReason(proposal({ status: "promoted" }))).toBe(
      "A promoted proposal cannot be approved.",
    );
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
