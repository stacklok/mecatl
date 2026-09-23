// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

const dreamTargetCapabilitySchema = z.object({
  decide: z.boolean(),
  generate: z.boolean(),
  unavailableReason: z.string().optional(),
});

export const serverCapabilitiesSchema = z.object({
  agents: z.boolean(),
  audio: z.boolean(),
  bash: z.boolean(),
  debugMcp: z.boolean(),
  image: z.boolean(),
  learnedSkills: z.boolean(),
  learningProposals: z.boolean(),
  manualCompaction: z.boolean(),
  manualDream: z
    .object({
      projectMemory: dreamTargetCapabilitySchema.optional(),
      userModel: dreamTargetCapabilitySchema.optional(),
    })
    .optional(),
  mcp: z.boolean(),
  mcpConnectorStatus: z.boolean(),
  memory: z.boolean(),
  modelSelection: z.boolean(),
  posture: z.string(),
  reflection: z.boolean(),
  scheduling: z.boolean(),
  sessionDebug: z.boolean(),
  skills: z.boolean(),
  slashCommands: z.boolean(),
  soul: z.boolean(),
  steer: z.boolean(),
  storageCleanup: z.boolean(),
  storageHealth: z.boolean(),
  storageMigration: z.boolean(),
  teams: z.boolean(),
  userModel: z.boolean(),
  workspaceEnrollment: z.boolean(),
  worktrees: z.boolean(),
});

export const runtimeResponseSchema = z.object({
  apiMajor: z.number().int(),
  capabilities: serverCapabilitiesSchema,
  connection: z.enum([
    "connecting",
    "online",
    "reconnecting",
    "offline",
    "unauthorized",
    "incompatible",
  ]),
  deployment: z.string().optional(),
  features: z.array(z.string()),
  mock: z.boolean(),
  source: z.enum(["external", "local"]),
});

export type RuntimeResponse = z.infer<typeof runtimeResponseSchema>;
export type ServerCapabilitiesResponse = z.infer<typeof serverCapabilitiesSchema>;
