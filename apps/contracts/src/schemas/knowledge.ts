// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const configuredSkillSchema = z.object({
  activeVersion: z.string(),
  agentOwned: z.boolean(),
  description: z.string(),
  name: z.string(),
  ownerAgent: z.string(),
});

export const configuredSkillsResponseSchema = z.object({
  items: z.array(configuredSkillSchema),
  reason: z.string(),
  supported: z.boolean(),
});

export const learnedSkillActionsSchema = z.object({
  activate: z.boolean(),
  archive: z.boolean(),
  reject: z.boolean(),
  rollback: z.boolean(),
});

export const learnedSkillSchema = z.object({
  actions: learnedSkillActionsSchema,
  body: z.string(),
  description: z.string(),
  evidenceCount: z.number().int().nonnegative(),
  id: z.string(),
  name: z.string(),
  ownerAgent: z.string(),
  revision: z.string(),
  state: z.string(),
  supersedes: z.string(),
  updatedAt: z.string().nullable(),
  version: z.string(),
});

export const learnedSkillsResponseSchema = z.object({
  complete: z.boolean(),
  items: z.array(learnedSkillSchema),
  reason: z.string(),
  supported: z.boolean(),
});

export const learnedSkillDiffResponseSchema = z.object({
  diff: z.string(),
  fromVersion: z.string(),
  toVersion: z.string(),
});

export const learnedSkillChangeSchema = z.object({
  at: z.string().nullable(),
  evidenceCount: z.number().int().nonnegative(),
  fromState: z.string(),
  id: z.string(),
  name: z.string(),
  operation: z.string(),
  skillId: z.string(),
  toState: z.string(),
  verdict: z.string(),
  version: z.string(),
});

export const learnedSkillChangesResponseSchema = z.object({
  complete: z.boolean(),
  items: z.array(learnedSkillChangeSchema),
});

export const learnedSkillActionRequestSchema = z
  .object({
    action: z.enum(["activate", "archive", "reject", "rollback"]),
    expectedRevision: z.string().min(1),
    ownerAgent: z.string(),
    // Rollback reactivates an archived version, never the active one, so the
    // target is required for it and meaningless for every other action.
    targetVersion: z.string().optional(),
    version: z.string().min(1),
  })
  .refine((request) => request.action !== "rollback" || (request.targetVersion ?? "") !== "", {
    message: "rollback requires targetVersion",
    path: ["targetVersion"],
  });

export const learningDecisionSchema = z.object({
  actor: z.string(),
  at: z.string().nullable(),
  kind: z.string(),
  reason: z.string(),
});

export const learningProposalSchema = z.object({
  body: z.string(),
  createdAt: z.string().nullable(),
  decisions: z.array(learningDecisionSchema),
  description: z.string(),
  evidenceCount: z.number().int().nonnegative(),
  id: z.string(),
  key: z.string(),
  kind: z.string(),
  learnedSkillId: z.string(),
  projectScoped: z.boolean(),
  promotionAvailable: z.boolean(),
  promotionUnavailableReason: z.string(),
  status: z.string(),
  title: z.string(),
  triggers: z.array(z.string()),
  updatedAt: z.string().nullable(),
  value: z.string(),
  version: z.string(),
});

export const learningProposalsResponseSchema = z.object({
  complete: z.boolean(),
  items: z.array(learningProposalSchema),
  reason: z.string(),
  supported: z.boolean(),
});

export const decideLearningProposalRequestSchema = z.object({
  decision: z.enum(["approve", "reject"]),
  expectedVersion: z.string().min(1),
  reason: z.string().max(2_000).default(""),
});

export const undoLearningPromotionRequestSchema = z.object({
  expectedVersion: z.string().min(1),
});

export const reflectionReceiptSchema = z.object({
  abstained: z.boolean(),
  conflicted: z.number().int().nonnegative(),
  disposition: z.string(),
  message: z.string(),
  promoted: z.number().int().nonnegative(),
  queued: z.number().int().nonnegative(),
  reason: z.string(),
  reflectionId: z.string(),
  staged: z.number().int().nonnegative(),
});

export const memoryConsolidationParticipantSchema = z.object({
  description: z.string(),
  key: z.string(),
  value: z.string(),
});

export const memoryConsolidationOperationSchema = z.object({
  exactDuplicateEligible: z.boolean(),
  kind: z.string(),
  reason: z.string(),
  replacement: z.object({ description: z.string(), value: z.string() }),
  sources: z.array(memoryConsolidationParticipantSchema),
  survivor: memoryConsolidationParticipantSchema,
});

export const memoryConsolidationPlanSchema = z.object({
  expiresAt: z.string().nullable(),
  id: z.string(),
  operations: z.array(memoryConsolidationOperationSchema),
  plannedOperationCount: z.number().int().nonnegative(),
  plannedSourceCount: z.number().int().nonnegative(),
  target: z.literal("user_model"),
});

export const decideMemoryConsolidationPlanRequestSchema = z.object({
  decision: z.enum(["apply", "dismiss"]),
});

export const memoryConsolidationReceiptSchema = z.object({
  applied: z.number().int().nonnegative(),
  conflicted: z.number().int().nonnegative(),
  disposition: z.string(),
  failed: z.number().int().nonnegative(),
  id: z.string(),
  planned: z.number().int().nonnegative(),
  skipped: z.number().int().nonnegative(),
  target: z.literal("user_model"),
});

export const memoryEntrySchema = z.object({
  description: z.string(),
  key: z.string(),
});

export const userMemoryResponseSchema = z.object({
  items: z.array(memoryEntrySchema),
  reason: z.string(),
  sha256: z.string(),
  sizeBytes: z.string(),
  supported: z.boolean(),
});

export const memoryRevisionSchema = z.object({
  description: z.string(),
  key: z.string(),
  origin: z.string(),
  sourceSessionId: z.string(),
  status: z.string(),
  updatedAt: z.string().nullable(),
  value: z.string(),
  version: z.string(),
  writer: z.string(),
});

export const memoryDetailResponseSchema = z.object({
  current: memoryRevisionSchema,
  history: z.array(memoryRevisionSchema),
  historyAvailable: z.boolean(),
});

export type ConfiguredSkillsResponse = z.infer<typeof configuredSkillsResponseSchema>;
export type DecideLearningProposalRequest = z.infer<typeof decideLearningProposalRequestSchema>;
export type DecideMemoryConsolidationPlanRequest = z.infer<
  typeof decideMemoryConsolidationPlanRequestSchema
>;
export type LearnedSkillActionRequest = z.infer<typeof learnedSkillActionRequestSchema>;
export type LearnedSkillChangesResponse = z.infer<typeof learnedSkillChangesResponseSchema>;
export type LearnedSkillDiffResponse = z.infer<typeof learnedSkillDiffResponseSchema>;
export type LearnedSkillResponse = z.infer<typeof learnedSkillSchema>;
export type LearnedSkillsResponse = z.infer<typeof learnedSkillsResponseSchema>;
export type LearningProposalResponse = z.infer<typeof learningProposalSchema>;
export type LearningProposalsResponse = z.infer<typeof learningProposalsResponseSchema>;
export type MemoryConsolidationPlanResponse = z.infer<typeof memoryConsolidationPlanSchema>;
export type MemoryConsolidationReceiptResponse = z.infer<typeof memoryConsolidationReceiptSchema>;
export type MemoryDetailResponse = z.infer<typeof memoryDetailResponseSchema>;
export type ReflectionReceiptResponse = z.infer<typeof reflectionReceiptSchema>;
export type UndoLearningPromotionRequest = z.infer<typeof undoLearningPromotionRequestSchema>;
export type UserMemoryResponse = z.infer<typeof userMemoryResponseSchema>;
