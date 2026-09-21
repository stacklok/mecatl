// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const sessionModeSchema = z.enum(["default", "plan", "acceptEdits"]);

export const sessionToolAccessSchema = z.enum(["all", "noFilesystem"]);

export const reasoningEffortSchema = z.enum(["default", "low", "medium", "high", "xhigh", "max"]);

export const sessionModelSelectionSchema = z.object({
  id: z.string().trim().min(1).max(256),
  providerId: z.string().trim().min(1).max(128),
});

export const sessionActionCapabilitiesSchema = z.object({
  delete: z.boolean(),
  deleteReason: z.string(),
  rename: z.boolean(),
  renameReason: z.string(),
});

export const sessionSummarySchema = z.object({
  capabilities: sessionActionCapabilitiesSchema,
  createdAt: z.string(),
  /** Non-empty when this session is an AI-debug chat bound to that target. */
  debugTargetSessionId: z.string(),
  id: z.string(),
  modelId: z.string(),
  state: z.string(),
  title: z.string(),
  turns: z.number().int().nonnegative(),
  updatedAt: z.string(),
});

export const listSessionsResponseSchema = z.object({
  complete: z.boolean(),
  items: z.array(sessionSummarySchema),
});

export const createSessionRequestSchema = z.object({
  /**
   * Binds this session as an AI-debug chat over that target (ADR 0254): the
   * daemon requires the no-fs profile and authorizes the target itself.
   */
  debugTargetSessionId: z.string().trim().min(1).max(256).optional(),
  mode: sessionModeSchema.default("default"),
  model: sessionModelSelectionSchema.optional(),
  reasoningEffort: reasoningEffortSchema.default("default"),
  toolAccess: sessionToolAccessSchema.default("all"),
});

export const createSessionResponseSchema = z.object({
  id: z.string(),
});

export const sessionUsageSchema = z.object({
  cacheReadTokens: z.string(),
  cacheWriteTokens: z.string(),
  inputTokens: z.string(),
  outputTokens: z.string(),
  reasoningTokens: z.string(),
});

export const sessionDetailResponseSchema = z.object({
  capabilities: z.object({
    image: z.boolean(),
    manualCompaction: z.boolean(),
    modelSelection: z.boolean(),
  }),
  id: z.string(),
  mode: sessionModeSchema,
  model: sessionModelSelectionSchema
    .extend({
      contextWindow: z.string(),
      reasoningEffort: reasoningEffortSchema,
    })
    .optional(),
  state: z.string(),
  usage: sessionUsageSchema,
});

export const setSessionModeRequestSchema = z.object({
  mode: sessionModeSchema,
});

export const setSessionModeResponseSchema = z.object({
  mode: sessionModeSchema,
});

export const forkSessionRequestSchema = z.object({
  model: sessionModelSelectionSchema,
  reasoningEffort: reasoningEffortSchema,
});

export const forkSessionResponseSchema = z.object({
  id: z.string(),
});

export const clearSessionResponseSchema = z.object({
  id: z.string(),
});

export const compactSessionResponseSchema = z.object({
  compacted: z.boolean(),
});

export const renameSessionRequestSchema = z.object({
  title: z.string().trim().min(1).max(160),
});

export const renameSessionResponseSchema = z.object({
  title: z.string(),
});

export const transcriptToolCallSchema = z.object({
  args: z.string(),
  id: z.string(),
  name: z.string(),
});

export const transcriptToolResultSchema = z.object({
  callId: z.string(),
  content: z.string(),
  isError: z.boolean(),
});

const imageMimeTypeSchema = z
  .string()
  .trim()
  .min(7)
  .max(128)
  .regex(/^image\/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*$/);

/**
 * Padded standard base64, checked in one linear pass. A grouped, repeated
 * regular expression backtracks through the whole attachment and overflows
 * the regex stack long before the 10 MiB bound is reached.
 */
export function isPaddedBase64(value: string): boolean {
  if (value.length % 4 !== 0) return false;
  let padding = 0;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code === 0x3d) {
      padding += 1;
      continue;
    }
    if (padding > 0) return false;
    const alphabet =
      (code >= 0x41 && code <= 0x5a) ||
      (code >= 0x61 && code <= 0x7a) ||
      (code >= 0x30 && code <= 0x39) ||
      code === 0x2b ||
      code === 0x2f;
    if (!alphabet) return false;
  }
  return padding <= 2;
}

const base64Schema = z
  .string()
  .min(4)
  // 10 MiB, encoded as padded base64. The SDK remains authoritative for
  // decoded per-part and aggregate prompt limits.
  .max(13_981_016)
  .refine(isPaddedBase64, { message: "Must be padded standard base64" });

export const imageAttachmentSchema = z.object({
  data: base64Schema,
  mimeType: imageMimeTypeSchema,
  name: z.string().trim().min(1).max(256),
});

export const transcriptImageSchema = z.object({
  data: base64Schema.optional(),
  mimeType: imageMimeTypeSchema,
  name: z.string().trim().min(1).max(256),
  url: z.string().url().max(2_048).optional(),
});

export const transcriptMessageSchema = z.object({
  images: z.array(transcriptImageSchema),
  role: z.string(),
  text: z.string(),
  toolCalls: z.array(transcriptToolCallSchema),
  toolResult: transcriptToolResultSchema.optional(),
});

export const sessionTranscriptResponseSchema = z.object({
  complete: z.boolean(),
  messages: z.array(transcriptMessageSchema),
  sessionId: z.string(),
});

export const startRunRequestSchema = z
  .object({
    images: z.array(imageAttachmentSchema).max(16).default([]),
    prompt: z.string().trim().max(1_000_000),
  })
  .refine(
    // Includes one base64-padding quantum per allowed part. The SDK checks
    // the exact decoded 20 MiB aggregate limit after this JSON-size bound.
    (request) => request.images.reduce((size, image) => size + image.data.length, 0) <= 27_962_096,
    { message: "Prompt images exceed the 20 MiB aggregate limit." },
  )
  .refine((request) => request.prompt.length > 0 || request.images.length > 0, {
    message: "A prompt must contain text or at least one image.",
  });

export const permissionVerdictSchema = z.enum(["allow_once", "allow_always", "deny"]);

export const resolvePermissionRequestSchema = z.object({
  verdict: permissionVerdictSchema,
});

export const steerRunRequestSchema = z.object({
  text: z.string().trim().min(1).max(1_000_000),
});

export const eventUsageSchema = sessionUsageSchema;

export const serializedMecatlEventSchema = z.object({
  kind: z.string(),
  payload: z.unknown().optional(),
  raw: z.unknown().optional(),
  runId: z.string(),
  seq: z.string(),
  text: z.string(),
  turn: z.number().int(),
  unknown: z.boolean(),
  usage: eventUsageSchema.optional(),
});

/**
 * Ends a bounded replay. `bound` means the session's durable history is longer
 * than one request replays: read the authoritative transcript, then reattach
 * from `cursor`. `gap` means durable delivery has a hole, which carries no
 * cursor to resume from.
 */
export const runTruncatedSchema = z.object({
  cursor: z.string(),
  reason: z.enum(["bound", "gap"]),
  type: z.literal("run.truncated"),
});

export const runStreamEventSchema = z.discriminatedUnion("type", [
  z.object({
    runId: z.string(),
    sessionId: z.string(),
    type: z.literal("run.started"),
  }),
  z.object({
    event: serializedMecatlEventSchema,
    type: z.literal("run.event"),
  }),
  runTruncatedSchema,
  z.object({
    code: z.string(),
    message: z.string(),
    type: z.literal("run.error"),
  }),
]);

export type CreateSessionRequest = z.infer<typeof createSessionRequestSchema>;
export type ForkSessionRequest = z.infer<typeof forkSessionRequestSchema>;
export type ListSessionsResponse = z.infer<typeof listSessionsResponseSchema>;
export type RunStreamEvent = z.infer<typeof runStreamEventSchema>;
export type RunTruncated = z.infer<typeof runTruncatedSchema>;
export type SessionDetailResponse = z.infer<typeof sessionDetailResponseSchema>;
export type SessionSummaryResponse = z.infer<typeof sessionSummarySchema>;
export type SessionTranscriptResponse = z.infer<typeof sessionTranscriptResponseSchema>;
export type SessionUsageResponse = z.infer<typeof sessionUsageSchema>;
export type StartRunRequest = z.infer<typeof startRunRequestSchema>;
