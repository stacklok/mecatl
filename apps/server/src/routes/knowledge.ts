// SPDX-License-Identifier: Apache-2.0

import { createRoute, type OpenAPIHono, z } from "@hono/zod-openapi";
import {
  configuredSkillsResponseSchema,
  decideLearningProposalRequestSchema,
  decideMemoryConsolidationPlanRequestSchema,
  learnedSkillActionRequestSchema,
  learnedSkillChangesResponseSchema,
  learnedSkillDiffResponseSchema,
  learnedSkillSchema,
  learnedSkillsResponseSchema,
  learningProposalSchema,
  learningProposalsResponseSchema,
  memoryConsolidationPlanSchema,
  memoryConsolidationReceiptSchema,
  memoryDetailResponseSchema,
  problemDetailsSchema,
  reflectionReceiptSchema,
  undoLearningPromotionRequestSchema,
  userMemoryResponseSchema,
} from "@mecatl-studio/contracts";
import type { AppEnv } from "../http/env.js";
import { problem } from "../http/problem.js";
import { KnowledgeNotFoundError, type KnowledgeService } from "../mecatl/knowledge.js";

const learnedSkillParameters = z.object({
  skillId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "skillId" } }),
});
const memoryParameters = z.object({
  memoryKey: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "memoryKey" } }),
});
const learningProposalParameters = z.object({
  proposalId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "proposalId" } }),
});
const sessionParameters = z.object({
  sessionId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "sessionId" } }),
});
const memoryConsolidationPlanParameters = z.object({
  planId: z
    .string()
    .min(1)
    .openapi({ param: { in: "path", name: "planId" } }),
});
const learningProposalQuery = z.object({ status: z.string().optional() });
const learnedSkillLocatorQuery = z.object({
  ownerAgent: z.string().min(1),
  version: z.string().min(1),
});
const learnedSkillDiffQuery = z.object({
  fromVersion: z.string().min(1),
  ownerAgent: z.string().min(1),
  toVersion: z.string().min(1),
});
const errorResponse = {
  content: { "application/problem+json": { schema: problemDetailsSchema } },
  description: "The request could not be completed.",
} as const;

const listConfiguredSkillsRoute = createRoute({
  method: "get",
  operationId: "listConfiguredSkills",
  path: "/api/v1/skills",
  responses: {
    200: {
      content: { "application/json": { schema: configuredSkillsResponseSchema } },
      description: "The configured skill inventory.",
    },
    500: errorResponse,
    503: errorResponse,
  },
});

const listLearnedSkillsRoute = createRoute({
  method: "get",
  operationId: "listLearnedSkills",
  path: "/api/v1/learned-skills",
  responses: {
    200: {
      content: { "application/json": { schema: learnedSkillsResponseSchema } },
      description: "The learned skill lifecycle inventory.",
    },
    500: errorResponse,
    503: errorResponse,
  },
});

const learnedSkillActionRoute = createRoute({
  method: "post",
  operationId: "actOnLearnedSkill",
  path: "/api/v1/learned-skills/{skillId}/actions",
  request: {
    body: {
      content: { "application/json": { schema: learnedSkillActionRequestSchema } },
      required: true,
    },
    params: learnedSkillParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: learnedSkillSchema } },
      description: "The updated learned skill.",
    },
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const getLearnedSkillRoute = createRoute({
  method: "get",
  operationId: "getLearnedSkill",
  path: "/api/v1/learned-skills/{skillId}",
  request: { params: learnedSkillParameters, query: learnedSkillLocatorQuery },
  responses: {
    200: {
      content: { "application/json": { schema: learnedSkillSchema } },
      description: "One complete learned-skill version.",
    },
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const diffLearnedSkillVersionsRoute = createRoute({
  method: "get",
  operationId: "diffLearnedSkillVersions",
  path: "/api/v1/learned-skills/{skillId}/diff",
  request: { params: learnedSkillParameters, query: learnedSkillDiffQuery },
  responses: {
    200: {
      content: { "application/json": { schema: learnedSkillDiffResponseSchema } },
      description: "A unified diff between two learned-skill versions.",
    },
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const listLearnedSkillChangesRoute = createRoute({
  method: "get",
  operationId: "listLearnedSkillChanges",
  path: "/api/v1/learned-skills/changes",
  responses: {
    200: {
      content: { "application/json": { schema: learnedSkillChangesResponseSchema } },
      description: "Recent learned-skill lifecycle changes.",
    },
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const listMemoryRoute = createRoute({
  method: "get",
  operationId: "listUserMemory",
  path: "/api/v1/user-memory",
  responses: {
    200: {
      content: { "application/json": { schema: userMemoryResponseSchema } },
      description: "The user-model memory index.",
    },
    500: errorResponse,
    503: errorResponse,
  },
});

const listLearningProposalsRoute = createRoute({
  method: "get",
  operationId: "listLearningProposals",
  path: "/api/v1/learning-proposals",
  request: { query: learningProposalQuery },
  responses: {
    200: {
      content: { "application/json": { schema: learningProposalsResponseSchema } },
      description: "The daemon-curated learning proposal review queue.",
    },
    500: errorResponse,
    503: errorResponse,
  },
});

const decideLearningProposalRoute = createRoute({
  method: "post",
  operationId: "decideLearningProposal",
  path: "/api/v1/learning-proposals/{proposalId}/decisions",
  request: {
    body: {
      content: { "application/json": { schema: decideLearningProposalRequestSchema } },
      required: true,
    },
    params: learningProposalParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: learningProposalSchema } },
      description: "The proposal after the recorded review decision.",
    },
    409: errorResponse,
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const undoLearningPromotionRoute = createRoute({
  method: "post",
  operationId: "undoLearningPromotion",
  path: "/api/v1/learning-proposals/{proposalId}/undo",
  request: {
    body: {
      content: { "application/json": { schema: undoLearningPromotionRequestSchema } },
      required: true,
    },
    params: learningProposalParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: learningProposalSchema } },
      description: "The proposal after its promotion was reverted.",
    },
    409: errorResponse,
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const reflectSessionRoute = createRoute({
  method: "post",
  operationId: "reflectSession",
  path: "/api/v1/sessions/{sessionId}/reflection",
  request: { params: sessionParameters },
  responses: {
    200: {
      content: { "application/json": { schema: reflectionReceiptSchema } },
      description: "The completed session reflection receipt.",
    },
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const generateMemoryConsolidationPlanRoute = createRoute({
  method: "post",
  operationId: "generateMemoryConsolidationPlan",
  path: "/api/v1/user-memory/consolidation/plans",
  responses: {
    201: {
      content: { "application/json": { schema: memoryConsolidationPlanSchema } },
      description: "A bounded daemon-curated user-memory consolidation plan.",
    },
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const decideMemoryConsolidationPlanRoute = createRoute({
  method: "post",
  operationId: "decideMemoryConsolidationPlan",
  path: "/api/v1/user-memory/consolidation/plans/{planId}/decisions",
  request: {
    body: {
      content: { "application/json": { schema: decideMemoryConsolidationPlanRequestSchema } },
      required: true,
    },
    params: memoryConsolidationPlanParameters,
  },
  responses: {
    200: {
      content: { "application/json": { schema: memoryConsolidationReceiptSchema } },
      description: "The receipt for applying or dismissing the whole consolidation plan.",
    },
    404: errorResponse,
    409: errorResponse,
    410: errorResponse,
    500: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

const getMemoryRoute = createRoute({
  method: "get",
  operationId: "getUserMemory",
  path: "/api/v1/user-memory/{memoryKey}",
  request: { params: memoryParameters },
  responses: {
    200: {
      content: { "application/json": { schema: memoryDetailResponseSchema } },
      description: "One user-model memory entry and its revisions.",
    },
    500: errorResponse,
    404: errorResponse,
    501: errorResponse,
    503: errorResponse,
  },
});

export function registerKnowledgeRoutes(
  app: OpenAPIHono<AppEnv>,
  knowledge: KnowledgeService | undefined,
) {
  app.openapi(listConfiguredSkillsRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    return context.json(await knowledge.listConfiguredSkills(), 200);
  });
  app.openapi(listLearnedSkillsRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    return context.json(await knowledge.listLearnedSkills(), 200);
  });
  app.openapi(listLearnedSkillChangesRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.learnedSkills)
      return unsupported(
        context,
        "learned_skills_unsupported",
        "Learned skills are not enabled on this deployment.",
      );
    return context.json(await knowledge.listLearnedSkillChanges(), 200);
  });
  app.openapi(getLearnedSkillRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.learnedSkills)
      return unsupported(
        context,
        "learned_skills_unsupported",
        "Learned skills are not enabled on this deployment.",
      );
    const { skillId } = context.req.valid("param");
    const { ownerAgent, version } = context.req.valid("query");
    return context.json(await knowledge.getLearnedSkill(skillId, ownerAgent, version), 200);
  });
  app.openapi(diffLearnedSkillVersionsRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.learnedSkills)
      return unsupported(
        context,
        "learned_skills_unsupported",
        "Learned skills are not enabled on this deployment.",
      );
    const { skillId } = context.req.valid("param");
    const { fromVersion, ownerAgent, toVersion } = context.req.valid("query");
    return context.json(
      await knowledge.diffLearnedSkillVersions(skillId, ownerAgent, fromVersion, toVersion),
      200,
    );
  });
  app.openapi(learnedSkillActionRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.learnedSkills)
      return unsupported(
        context,
        "learned_skills_unsupported",
        "Learned skills are not enabled on this deployment.",
      );
    return context.json(
      await knowledge.actOnLearnedSkill(
        context.req.valid("param").skillId,
        context.req.valid("json"),
      ),
      200,
    );
  });
  app.openapi(listLearningProposalsRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    return context.json(
      await knowledge.listLearningProposals(context.req.valid("query").status ?? ""),
      200,
    );
  });
  app.openapi(decideLearningProposalRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.learningProposals)
      return unsupported(
        context,
        "learning_proposals_unsupported",
        "Learning proposals are not enabled on this deployment.",
      );
    return context.json(
      await knowledge.decideLearningProposal(
        context.req.valid("param").proposalId,
        context.req.valid("json"),
      ),
      200,
    );
  });
  app.openapi(undoLearningPromotionRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.learningProposals)
      return unsupported(
        context,
        "learning_proposals_unsupported",
        "Learning proposals are not enabled on this deployment.",
      );
    return context.json(
      await knowledge.undoLearningPromotion(
        context.req.valid("param").proposalId,
        context.req.valid("json"),
      ),
      200,
    );
  });
  app.openapi(reflectSessionRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.reflection)
      return unsupported(
        context,
        "reflection_unsupported",
        "Session reflection is not enabled on this deployment.",
      );
    return context.json(await knowledge.reflectSession(context.req.valid("param").sessionId), 200);
  });
  app.openapi(generateMemoryConsolidationPlanRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.memoryConsolidation.generate)
      return unsupported(
        context,
        "memory_consolidation_unsupported",
        knowledge.capabilities.memoryConsolidation.unavailableReason,
      );
    return context.json(await knowledge.generateMemoryConsolidationPlan(), 201);
  });
  app.openapi(decideMemoryConsolidationPlanRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.memoryConsolidation.decide)
      return unsupported(
        context,
        "memory_consolidation_decision_unsupported",
        knowledge.capabilities.memoryConsolidation.unavailableReason,
      );
    return context.json(
      await knowledge.decideMemoryConsolidationPlan(
        context.req.valid("param").planId,
        context.req.valid("json"),
      ),
      200,
    );
  });
  app.openapi(listMemoryRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    return context.json(await knowledge.listMemory(), 200);
  });
  app.openapi(getMemoryRoute, async (context) => {
    if (!knowledge) return unavailable(context);
    if (!knowledge.capabilities.userModel)
      return unsupported(
        context,
        "user_memory_unsupported",
        "User memory is not enabled on this deployment.",
      );
    try {
      return context.json(await knowledge.getMemory(context.req.valid("param").memoryKey), 200);
    } catch (error) {
      if (error instanceof KnowledgeNotFoundError) {
        return problem(context, 404, "not_found", "Not found", error.message);
      }
      throw error;
    }
  });
}

function unavailable(context: Parameters<typeof problem>[0]) {
  return problem(
    context,
    503,
    "runtime_unavailable",
    "Mecatl runtime unavailable",
    "The BFF has no Mecatl runtime connection.",
  );
}

function unsupported(context: Parameters<typeof problem>[0], code: string, detail: string) {
  return problem(context, 501, code, "Capability unavailable", detail);
}
