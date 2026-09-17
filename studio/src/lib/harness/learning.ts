/**
 * Learning review queue + explicit reflection (ADR 0109), over the SDK's
 * `client.learningProposals` / `client.reflection` namespaces.
 *
 * Wire: `GET /v1/learning/proposals` (status/cursor/limit filters),
 * `GET /v1/learning/proposals/{id}`, `POST .../decision` (approve/reject with
 * `expected_version` — a stale version answers 409 `proposal_conflict`),
 * `POST .../undo`, and `POST /v1/sessions/{id}/reflect`.
 *
 * Gated by `capabilities.learning_proposals` / `capabilities.reflection` on
 * GET /v1/compatibility. The `project` field is deliberately never set:
 * Studio reads the operator-scope partition — the browser never knows the
 * workspace path (rule 2).
 */

import type {
  LearningProposal as ProtoLearningProposal,
  ReflectionReceipt as ProtoReflectionReceipt,
} from "@stacklok-oss/mecatl-sdk/gen";

import { HarnessApiError } from "./errors";
import { getHarnessClient, harness } from "./sdk";
import { timestampUnix } from "./time";

/** One human decision recorded on a proposal. */
interface LearningDecision {
  kind: string;
  actor: string;
  reason: string;
  atUnix: number;
}

/**
 * One evidence handle behind a proposal (proto LearningEvidenceRef): where
 * the daemon read the supporting content — a source session, a locator
 * (e.g. `tool`) plus ordinal, the event sequence and tool call — and whether
 * that source STILL resolves to the recorded digest. `preview` is the
 * daemon's bounded, ownership-checked re-read of the canonical content.
 */
export interface LearningEvidence {
  sessionId: string;
  locator: string;
  ordinal: number;
  /** int64 on the wire; proposals never approach 2^53 events. */
  eventSeq: number;
  toolCallId: string;
  digest: string;
  /** True only when the authorized source still resolves to `digest`. */
  available: boolean;
  /** The daemon's reason when `available` is false (may be empty). */
  availability: string;
  preview: string;
}

/** The memory write a promotion performed (proto LearningPromotionReceipt). */
interface LearningPromotionReceipt {
  memoryKey: string;
  previousExists: boolean;
  previousVersion: string;
  resultVersion: string;
}

/** The daemon's status vocabulary (engine/learning/proposal.go). */
export const PROPOSAL_STATUS_STAGED = "staged";
export const PROPOSAL_STATUS_PROMOTED = "promoted";
export const PROPOSAL_STATUS_REJECTED = "rejected";
/** A procedure the daemon could not promote when it was staged (skills were
 *  unsupported); approving it now materializes a learned-skill draft. */
export const PROPOSAL_STATUS_DEFERRED = "deferred_unsupported";

/**
 * One learning proposal, projected from the daemon's bounded digest.
 * `status` is the daemon's vocabulary: staged (pending review), promoting,
 * promoted, rejected, deferred_unsupported, conflicted, undone,
 * skill_materialized.
 */
export interface LearningProposal {
  id: string;
  /** Optimistic-concurrency token — every decision/undo must carry it. */
  version: string;
  status: string;
  kind: string;
  key: string;
  value: string;
  description: string;
  title: string;
  body: string;
  triggers: string[];
  evidence: LearningEvidence[];
  /** `evidence.length`, kept for row summaries. */
  evidenceCount: number;
  decisions: LearningDecision[];
  /** The memory write behind a promoted proposal; null until promoted. */
  promotion: LearningPromotionReceipt | null;
  createdAtUnix: number;
  updatedAtUnix: number;
  projectScoped: boolean;
  /** False when this partition has no trusted memory target — approve/undo disabled. */
  promotionAvailable: boolean;
  promotionUnavailableReason: string;
  /** Links a materialized procedure to its agent-owned learned skill. */
  learnedSkillId: string;
}

function decodeLearningProposal(
  proposal: ProtoLearningProposal | undefined,
): LearningProposal {
  const evidence = (proposal?.evidence ?? []).map((ref) => ({
    sessionId: ref.sessionId,
    locator: ref.locator,
    ordinal: ref.ordinal,
    eventSeq: Number(ref.eventSeq),
    toolCallId: ref.toolCallId,
    digest: ref.digest,
    available: ref.available,
    availability: ref.availability,
    preview: ref.preview,
  }));
  const receipt = proposal?.promotion;
  return {
    id: proposal?.id ?? "",
    version: proposal?.version ?? "",
    status: proposal?.status ?? "",
    kind: proposal?.kind ?? "",
    key: proposal?.key ?? "",
    value: proposal?.value ?? "",
    description: proposal?.description ?? "",
    title: proposal?.title ?? "",
    body: proposal?.body ?? "",
    triggers: [...(proposal?.triggers ?? [])],
    evidence,
    evidenceCount: evidence.length,
    decisions: (proposal?.decisions ?? []).map((decision) => ({
      kind: decision.kind,
      actor: decision.actor,
      reason: decision.reason,
      atUnix: timestampUnix(decision.at),
    })),
    promotion: receipt
      ? {
          memoryKey: receipt.memoryKey,
          previousExists: receipt.previousExists,
          previousVersion: receipt.previousVersion,
          resultVersion: receipt.resultVersion,
        }
      : null,
    createdAtUnix: timestampUnix(proposal?.createdAt),
    updatedAtUnix: timestampUnix(proposal?.updatedAt),
    projectScoped: proposal?.projectScoped ?? false,
    promotionAvailable: proposal?.promotionAvailable ?? false,
    promotionUnavailableReason: proposal?.promotionUnavailableReason ?? "",
    learnedSkillId: proposal?.learnedSkillId ?? "",
  };
}

/**
 * Whether Approve is offered — mecatui's `reflectionApprovable`: the
 * proposal is staged or deferred, this partition can promote, and EVERY
 * evidence handle still resolves (a proposal with no evidence is never
 * approvable — `every` alone would be vacuously true). The daemon re-checks
 * all of this on the decision; the UI only avoids offering what would be
 * refused. Use `approvalBlockedReason` for the why.
 */
export function isProposalApprovable(proposal: LearningProposal): boolean {
  return approvalBlockedReason(proposal) === undefined;
}

/**
 * The plain reason Approve is disabled, undefined when it is allowed. The
 * daemon's own `promotion_unavailable_reason` wins when it gives one.
 */
export function approvalBlockedReason(
  proposal: LearningProposal,
): string | undefined {
  if (
    proposal.status !== PROPOSAL_STATUS_STAGED &&
    proposal.status !== PROPOSAL_STATUS_DEFERRED
  ) {
    return `A ${proposal.status.replaceAll("_", " ")} proposal cannot be approved.`;
  }
  if (!proposal.promotionAvailable) {
    return (
      proposal.promotionUnavailableReason ||
      "This partition has no trusted memory target."
    );
  }
  if (proposal.evidence.length === 0) {
    return "This proposal records no evidence to verify.";
  }
  if (proposal.evidence.some((ref) => !ref.available)) {
    return "Some of this proposal's evidence is no longer available or has changed.";
  }
  return undefined;
}

export interface LearningProposalPage {
  proposals: LearningProposal[];
  nextCursor: string;
}

export async function listLearningProposals(
  options: { status?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<LearningProposalPage> {
  const response = await harness(() =>
    getHarnessClient().learningProposals.list(
      {
        $typeName: "mecatl.v1.ListLearningProposalsRequest",
        status: options.status ?? "",
        cursor: options.cursor ?? "",
        limit: options.limit ?? 0,
        project: "", // operator scope — never the workspace (rule 2)
      },
      { signal },
    ),
  );
  return {
    proposals: response.proposals.map(decodeLearningProposal),
    nextCursor: response.nextCursor,
  };
}

/**
 * Re-reads ONE proposal (`GET /v1/learning/proposals/{id}`): the detail
 * disclosure calls it so the evidence availability and version it shows are
 * current, not the list page's snapshot. Same operator-scope rule as list.
 */
export async function getLearningProposal(
  id: string,
  signal?: AbortSignal,
): Promise<LearningProposal> {
  const response = await harness(() =>
    getHarnessClient().learningProposals.get(
      { $typeName: "mecatl.v1.GetLearningProposalRequest", id, project: "" },
      { signal },
    ),
  );
  return decodeLearningProposal(response.proposal);
}

/**
 * Approves or rejects a proposal. `expectedVersion` is the version the review
 * UI showed: a proposal that changed underneath answers 409
 * `proposal_conflict` (see isProposalConflict) — refresh and re-review, never
 * blind-retry.
 */
export async function decideLearningProposal(
  id: string,
  decision: "approve" | "reject",
  expectedVersion: string,
  reason?: string,
): Promise<LearningProposal> {
  const response = await harness(() =>
    getHarnessClient().learningProposals.decide({
      $typeName: "mecatl.v1.DecideLearningProposalRequest",
      id,
      decision,
      expectedVersion,
      reason: reason ?? "",
      project: "",
    }),
  );
  return decodeLearningProposal(response.proposal);
}

/** Reverts a promoted proposal's memory write (same conflict contract). */
export async function undoLearningPromotion(
  id: string,
  expectedVersion: string,
): Promise<LearningProposal> {
  const response = await harness(() =>
    getHarnessClient().learningProposals.undoPromotion({
      $typeName: "mecatl.v1.UndoLearningPromotionRequest",
      id,
      expectedVersion,
      project: "",
    }),
  );
  return decodeLearningProposal(response.proposal);
}

/** True when a decision/undo lost the optimistic-concurrency race. */
export function isProposalConflict(error: unknown): boolean {
  return (
    error instanceof HarnessApiError &&
    (error.code === "proposal_conflict" ||
      (error.code === "" && error.status === 409))
  );
}

/** The counts an explicit reflection pass returns. */
export interface ReflectionReceipt {
  reflectionId: string;
  disposition: string;
  queued: number;
  abstained: boolean;
  staged: number;
  promoted: number;
  conflicted: number;
}

function decodeReflectionReceipt(
  receipt: ProtoReflectionReceipt | undefined,
): ReflectionReceipt {
  return {
    reflectionId: receipt?.reflectionId ?? "",
    disposition: receipt?.disposition ?? "",
    queued: receipt?.queued ?? 0,
    abstained: receipt?.abstained ?? false,
    staged: receipt?.staged ?? 0,
    promoted: receipt?.promoted ?? 0,
    conflicted: receipt?.conflicted ?? 0,
  };
}

/**
 * Runs an explicit reflection pass over one completed session
 * (`POST /v1/sessions/{id}/reflect`). Synchronous: the request lasts the
 * whole reflection run. Gated by `capabilities.reflection`.
 */
export async function reflectHarnessSession(
  sessionId: string,
  signal?: AbortSignal,
): Promise<ReflectionReceipt> {
  const response = await harness(() =>
    getHarnessClient().reflection.reflect(
      { $typeName: "mecatl.v1.ReflectSessionRequest", sessionId },
      { signal },
    ),
  );
  return decodeReflectionReceipt(response.receipt);
}
