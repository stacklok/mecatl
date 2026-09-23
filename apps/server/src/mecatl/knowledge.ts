// SPDX-License-Identifier: Apache-2.0

import type {
  ConfiguredSkillsResponse,
  DecideLearningProposalRequest,
  DecideMemoryConsolidationPlanRequest,
  LearnedSkillActionRequest,
  LearnedSkillChangesResponse,
  LearnedSkillDiffResponse,
  LearnedSkillResponse,
  LearnedSkillsResponse,
  LearningProposalResponse,
  LearningProposalsResponse,
  MemoryConsolidationPlanResponse,
  MemoryConsolidationReceiptResponse,
  MemoryDetailResponse,
  ReflectionReceiptResponse,
  UndoLearningPromotionRequest,
  UserMemoryResponse,
} from "@mecatl-studio/contracts";
import type { Client } from "@stacklok-oss/mecatl-sdk";
import type {
  DreamParticipant,
  DreamReviewPlan,
  LearnedSkillVersion,
  LearningProposal,
  UserModelRevision,
} from "@stacklok-oss/mecatl-sdk/gen";

export interface KnowledgeCapabilities {
  learnedSkills: boolean;
  learningProposals: boolean;
  memoryConsolidation: {
    decide: boolean;
    generate: boolean;
    unavailableReason: string;
  };
  reflection: boolean;
  skills: boolean;
  userModel: boolean;
}

/** The daemon answered, but the addressed entry does not exist; routes map it to 404. */
export class KnowledgeNotFoundError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "KnowledgeNotFoundError";
  }
}

export interface KnowledgeService {
  readonly capabilities: KnowledgeCapabilities;
  actOnLearnedSkill(id: string, request: LearnedSkillActionRequest): Promise<LearnedSkillResponse>;
  decideLearningProposal(
    id: string,
    request: DecideLearningProposalRequest,
  ): Promise<LearningProposalResponse>;
  decideMemoryConsolidationPlan(
    planId: string,
    request: DecideMemoryConsolidationPlanRequest,
  ): Promise<MemoryConsolidationReceiptResponse>;
  diffLearnedSkillVersions(
    id: string,
    ownerAgent: string,
    fromVersion: string,
    toVersion: string,
  ): Promise<LearnedSkillDiffResponse>;
  getLearnedSkill(id: string, ownerAgent: string, version: string): Promise<LearnedSkillResponse>;
  getMemory(key: string): Promise<MemoryDetailResponse>;
  generateMemoryConsolidationPlan(): Promise<MemoryConsolidationPlanResponse>;
  listConfiguredSkills(): Promise<ConfiguredSkillsResponse>;
  listLearnedSkills(): Promise<LearnedSkillsResponse>;
  listLearnedSkillChanges(): Promise<LearnedSkillChangesResponse>;
  listLearningProposals(status: string): Promise<LearningProposalsResponse>;
  listMemory(): Promise<UserMemoryResponse>;
  reflectSession(sessionId: string): Promise<ReflectionReceiptResponse>;
  undoLearningPromotion(
    id: string,
    request: UndoLearningPromotionRequest,
  ): Promise<LearningProposalResponse>;
}

export function createMecatlKnowledgeService(
  client: Client,
  capabilities: KnowledgeCapabilities | (() => KnowledgeCapabilities),
): KnowledgeService {
  const getCapabilities = typeof capabilities === "function" ? capabilities : () => capabilities;

  return {
    get capabilities() {
      return getCapabilities();
    },

    async actOnLearnedSkill(id, request) {
      const target = {
        expectedRevision: request.expectedRevision,
        id,
        ownerAgent: request.ownerAgent,
        project: "",
      };
      const response =
        request.action === "rollback"
          ? await client.learnedSkills.rollback({
              $typeName: "mecatl.v1.RollbackLearnedSkillRequest",
              ...target,
              targetVersion: rollbackTarget(request.targetVersion),
            })
          : await client.learnedSkills[request.action]({
              $typeName: "mecatl.v1.MutateLearnedSkillRequest",
              ...target,
              version: request.version,
            });
      return learnedSkillFromSdk(response.skill);
    },

    async decideLearningProposal(id, request) {
      const response = await client.learningProposals.decide({
        $typeName: "mecatl.v1.DecideLearningProposalRequest",
        decision: request.decision,
        expectedVersion: request.expectedVersion,
        id,
        project: "",
        reason: request.reason,
      });
      return learningProposalFromSdk(response.proposal);
    },

    async decideMemoryConsolidationPlan(planId, request) {
      const response = await client.dreamPlans.decide({
        $typeName: "mecatl.v1.DecideDreamPlanRequest",
        decision: request.decision,
        planId,
      });
      const receipt = response.receipt;
      if (!receipt) throw new Error("Mecatl returned no memory consolidation receipt");
      if (receipt.target !== "user_model")
        throw new Error("Mecatl returned a memory consolidation receipt for another target");
      return {
        applied: receipt.appliedSourceCount,
        conflicted: receipt.conflictedSourceCount,
        disposition: receipt.disposition,
        failed: receipt.failedSourceCount,
        id: receipt.id,
        planned: receipt.plannedSourceCount,
        skipped: receipt.skippedSourceCount,
        target: "user_model",
      };
    },

    async diffLearnedSkillVersions(id, ownerAgent, fromVersion, toVersion) {
      const response = await client.learnedSkills.diffVersions({
        $typeName: "mecatl.v1.DiffLearnedSkillVersionsRequest",
        fromVersion,
        id,
        ownerAgent,
        project: "",
        toVersion,
      });
      return { diff: response.diff, fromVersion, toVersion };
    },

    async getLearnedSkill(id, ownerAgent, version) {
      const response = await client.learnedSkills.get({
        $typeName: "mecatl.v1.GetLearnedSkillRequest",
        id,
        ownerAgent,
        project: "",
        version,
      });
      return learnedSkillFromSdk(response.skill);
    },

    async getMemory(key) {
      const response = await client.userModel.get({
        $typeName: "mecatl.v1.GetUserModelRequest",
        key,
      });
      const detail = response.detail;
      if (!detail?.current) throw new KnowledgeNotFoundError(`No memory entry named ${key}`);
      return {
        current: memoryRevisionFromSdk(detail.current),
        history: detail.history.map(memoryRevisionFromSdk),
        historyAvailable: detail.historyAvailable,
      };
    },

    async generateMemoryConsolidationPlan() {
      const response = await client.dreamPlans.generate({
        $typeName: "mecatl.v1.GenerateDreamPlanRequest",
        target: "user_model",
      });
      return memoryConsolidationPlanFromSdk(response.plan);
    },

    async listConfiguredSkills() {
      if (!getCapabilities().skills) {
        return {
          items: [],
          reason: "Skills are not enabled on this Mecatl deployment.",
          supported: false,
        };
      }
      const response = await client.skills.list({ $typeName: "mecatl.v1.ListSkillsRequest" });
      return {
        items: response.skills.map((skill) => ({
          activeVersion: skill.activeVersion,
          agentOwned: skill.agentOwned,
          description: skill.description,
          name: skill.name,
          ownerAgent: skill.ownerAgent,
        })),
        reason: "",
        supported: true,
      };
    },

    async listLearnedSkills() {
      if (!getCapabilities().learnedSkills) {
        return {
          complete: true,
          items: [],
          reason: "Learned skills are not enabled on this Mecatl deployment.",
          supported: false,
        };
      }
      const response = await client.learnedSkills.list({
        $typeName: "mecatl.v1.ListLearnedSkillsRequest",
        cursor: "",
        limit: 100,
        ownerAgent: "",
        project: "",
        state: "",
      });
      return {
        complete: !response.nextCursor,
        items: response.skills.map(learnedSkillFromSdk),
        reason: "",
        supported: true,
      };
    },

    async listLearnedSkillChanges() {
      const response = await client.learnedSkills.listChanges({
        $typeName: "mecatl.v1.ListSkillChangesRequest",
        cursor: "",
        limit: 100,
        project: "",
      });
      return {
        complete: !response.nextCursor,
        items: response.changes.map((change) => ({
          at: timestampToIso(change.at),
          evidenceCount: change.evidenceCount,
          fromState: change.fromState,
          id: change.id,
          name: change.name,
          operation: change.operation,
          skillId: change.skillId,
          toState: change.toState,
          verdict: change.verdict,
          version: change.version,
        })),
      };
    },

    async listLearningProposals(status) {
      if (!getCapabilities().learningProposals) {
        return {
          complete: true,
          items: [],
          reason: "Learning proposals are not enabled on this Mecatl deployment.",
          supported: false,
        };
      }
      const response = await client.learningProposals.list({
        $typeName: "mecatl.v1.ListLearningProposalsRequest",
        cursor: "",
        limit: 100,
        project: "",
        status,
      });
      return {
        complete: !response.nextCursor,
        items: response.proposals.map(learningProposalFromSdk),
        reason: "",
        supported: true,
      };
    },

    async listMemory() {
      if (!getCapabilities().userModel) {
        return {
          items: [],
          reason: "User memory is not enabled on this Mecatl deployment.",
          sha256: "",
          sizeBytes: "0",
          supported: false,
        };
      }
      const response = await client.userModel.get({
        $typeName: "mecatl.v1.GetUserModelRequest",
        key: "",
      });
      return {
        items: response.entries.map((entry) => ({
          description: entry.description,
          key: entry.key,
        })),
        reason: "",
        sha256: response.sha256,
        sizeBytes: response.sizeBytes.toString(),
        supported: true,
      };
    },

    async reflectSession(sessionId) {
      const response = await client.reflection.reflect({
        $typeName: "mecatl.v1.ReflectSessionRequest",
        sessionId,
      });
      const receipt = response.receipt;
      if (!receipt) throw new Error("Mecatl returned no reflection receipt");
      return {
        abstained: receipt.abstained,
        conflicted: receipt.conflicted,
        disposition: receipt.disposition,
        message: receipt.message,
        promoted: receipt.promoted,
        queued: receipt.queued,
        reason: receipt.reason,
        reflectionId: receipt.reflectionId,
        staged: receipt.staged,
      };
    },

    async undoLearningPromotion(id, request) {
      const response = await client.learningProposals.undoPromotion({
        $typeName: "mecatl.v1.UndoLearningPromotionRequest",
        expectedVersion: request.expectedVersion,
        id,
        project: "",
      });
      return learningProposalFromSdk(response.proposal);
    },
  };
}

function learningProposalFromSdk(proposal: LearningProposal | undefined): LearningProposalResponse {
  if (!proposal?.id) throw new Error("Mecatl returned an incomplete learning proposal");
  return {
    body: proposal.body,
    createdAt: timestampToIso(proposal.createdAt),
    decisions: proposal.decisions.map((decision) => ({
      actor: decision.actor,
      at: timestampToIso(decision.at),
      kind: decision.kind,
      reason: decision.reason,
    })),
    description: proposal.description,
    evidenceCount: proposal.evidence.length,
    id: proposal.id,
    key: proposal.key,
    kind: proposal.kind,
    learnedSkillId: proposal.learnedSkillId,
    projectScoped: proposal.projectScoped,
    promotionAvailable: proposal.promotionAvailable,
    promotionUnavailableReason: proposal.promotionUnavailableReason,
    status: proposal.status,
    title: proposal.title,
    triggers: [...proposal.triggers],
    updatedAt: timestampToIso(proposal.updatedAt),
    value: proposal.value,
    version: proposal.version,
  };
}

function memoryConsolidationPlanFromSdk(
  plan: DreamReviewPlan | undefined,
): MemoryConsolidationPlanResponse {
  if (!plan?.id) throw new Error("Mecatl returned no memory consolidation plan");
  if (plan.target !== "user_model")
    throw new Error("Mecatl returned a memory consolidation plan for another target");
  return {
    expiresAt: timestampToIso(plan.expiresAt),
    id: plan.id,
    operations: plan.operations.map((operation) => ({
      exactDuplicateEligible: operation.exactDuplicateEligible,
      kind: operation.kind,
      reason: operation.reason,
      replacement: {
        description: operation.replacement?.description ?? "",
        value: operation.replacement?.value ?? "",
      },
      sources: operation.sources.map(memoryParticipantFromSdk),
      survivor: memoryParticipantFromSdk(operation.survivor),
    })),
    plannedOperationCount: plan.plannedOperationCount,
    plannedSourceCount: plan.plannedSourceCount,
    target: "user_model",
  };
}

function memoryParticipantFromSdk(participant: DreamParticipant | undefined) {
  return {
    description: participant?.description ?? "",
    key: participant?.key ?? "",
    value: participant?.value ?? "",
  };
}

function learnedSkillFromSdk(skill: LearnedSkillVersion | undefined): LearnedSkillResponse {
  if (!skill?.id) throw new Error("Mecatl returned an incomplete learned skill");
  return {
    actions: learnedSkillActions(skill),
    body: skill.body,
    description: skill.description,
    evidenceCount: skill.evidenceCount,
    id: skill.id,
    name: skill.name,
    ownerAgent: skill.ownerAgent,
    revision: skill.revision,
    state: skill.state,
    supersedes: skill.supersedes,
    updatedAt: timestampToIso(skill.updatedAt),
    version: skill.version,
  };
}

/**
 * Offers each lifecycle action exactly where the daemon's learned-skill state
 * machine accepts it, so the UI never presents a button the daemon is certain
 * to refuse with `proposal_conflict`:
 *
 * - activate: a `staged` version whose latest evaluation verdict is `pass`
 *   (ActivateLearnedSkill never takes the validated-abstain path);
 * - reject: a `draft`, `evaluated`, or `staged` version;
 * - archive: the `active` version;
 * - rollback: the `active` version (the daemon checks the expected revision
 *   against it) when it names a prior version to return to. Whether that prior
 *   version is itself eligible (archived after having been active) is decided
 *   by the daemon; it is not visible from a single version.
 */
function learnedSkillActions(skill: LearnedSkillVersion): LearnedSkillResponse["actions"] {
  const latestVerdict = skill.evaluations?.at(-1)?.verdict;
  return {
    activate: skill.state === "staged" && latestVerdict === "pass",
    archive: skill.state === "active",
    reject: skill.state === "draft" || skill.state === "evaluated" || skill.state === "staged",
    rollback: skill.state === "active" && Boolean(skill.supersedes),
  };
}

function memoryRevisionFromSdk(revision: UserModelRevision) {
  return {
    description: revision.description,
    key: revision.key,
    origin: revision.origin,
    sourceSessionId: revision.sourceSessionId,
    status: revision.status,
    updatedAt: timestampToIso(revision.updatedAt),
    value: revision.value,
    version: revision.version,
    writer: revision.writer,
  };
}

function timestampToIso(value: { nanos: number; seconds: bigint } | undefined): string | null {
  if (!value || value.seconds <= 0n) return null;
  return new Date(
    Number(value.seconds) * 1_000 + Math.floor(value.nanos / 1_000_000),
  ).toISOString();
}

/** The contract refuses a rollback without a target; this guards direct callers. */
function rollbackTarget(targetVersion: string | undefined): string {
  if (targetVersion === undefined || targetVersion === "") {
    throw new TypeError("rollback requires targetVersion");
  }
  return targetVersion;
}
