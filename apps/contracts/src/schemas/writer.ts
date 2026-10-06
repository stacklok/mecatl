// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";
import { sessionModelSelectionSchema } from "./chat.ts";

const documentSchema = z.strictObject({
  content: z.string().max(100_000),
  revision: z.number().int().nonnegative().safe(),
});

const observationSchema = z.strictObject({
  revision: z.number().int().nonnegative().safe(),
  status: z.enum(["open", "addressed", "not-relevant"]),
  text: z.string().min(1).max(1_000),
  decision: z.string().trim().min(1).max(500).optional(),
  selected: z.boolean().optional(),
});

const decisionSchema = z.strictObject({
  text: z.string().min(1).max(1_000),
  decision: z.string().trim().min(1).max(500),
});

const discussionEntrySchema = z.strictObject({
  role: z.enum(["user", "assistant"]),
  text: z.string().min(1).max(2_000),
});

const writerContextSchema = z
  .strictObject({
    document: documentSchema,
    model: sessionModelSelectionSchema.optional(),
    checkpoint: documentSchema.optional(),
    brief: z.string().trim().max(2_000).optional(),
    observations: z.array(observationSchema).max(12),
    decisions: z.array(decisionSchema).max(100).optional(),
    discussion: z.array(discussionEntrySchema).max(12),
  })
  .refine(
    ({ document, checkpoint, observations }) =>
      (checkpoint === undefined ||
        (checkpoint.revision <= document.revision &&
          (checkpoint.revision !== document.revision ||
            checkpoint.content === document.content))) &&
      observations.every(({ revision }) => revision <= document.revision),
    {
      message:
        "Checkpoint and observations must not be newer than the document; equal revisions must match.",
    },
  );

export const observeWriterRequestSchema = writerContextSchema;
export const discussWriterRequestSchema = writerContextSchema.safeExtend({
  message: z.string().trim().min(1).max(2_000),
});

export const observeWriterResponseSchema = z.discriminatedUnion("status", [
  z.strictObject({ status: z.literal("silent") }),
  z.strictObject({
    status: z.literal("observe"),
    text: z.string().trim().min(1).max(1_000),
    quote: z.string().max(500).optional(),
    quotes: z.array(z.string().min(1).max(500)).min(1).max(3).optional(),
    reason: z.string().max(500).optional(),
  }),
]);
export const discussWriterResponseSchema = z.strictObject({
  text: z.string().trim().min(1).max(2_000),
});

export type ObserveWriterRequest = z.infer<typeof observeWriterRequestSchema>;
export type DiscussWriterRequest = z.infer<typeof discussWriterRequestSchema>;
export type ObserveWriterResponse = z.infer<typeof observeWriterResponseSchema>;
export type DiscussWriterResponse = z.infer<typeof discussWriterResponseSchema>;
