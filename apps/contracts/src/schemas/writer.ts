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

const referenceSchema = z.strictObject({
  name: z
    .string()
    .min(1)
    .max(120)
    .refine(
      (name) =>
        name.trim().length > 0 &&
        [...name].every((char) => {
          const code = char.codePointAt(0) ?? 0;
          return (
            code > 31 &&
            code !== 127 &&
            (code < 0xd800 || code > 0xdfff) &&
            char !== "/" &&
            char !== "\\"
          );
        }),
    ),
  content: z.string().min(1).max(8_000),
});
const referencesSchema = z
  .array(referenceSchema)
  .max(3)
  .refine(
    (files) =>
      files.reduce((sum, file) => sum + new TextEncoder().encode(file.content).length, 0) <=
        16_000 &&
      files.every(
        (file) =>
          new TextEncoder().encode(file.content).length <= 8_000 &&
          [...file.content].every((character) => {
            const code = character.codePointAt(0) ?? 0;
            return (
              (code >= 32 || code === 9 || code === 10 || code === 13) &&
              code !== 127 &&
              (code < 0xd800 || code > 0xdfff)
            );
          }),
      ),
    { message: "References must be bounded UTF-8 text." },
  );

const passageSchema = z.strictObject({
  from: z.number().int().nonnegative().safe(),
  to: z.number().int().positive().safe(),
  text: z.string().min(1).max(4_000),
});

const writerContextSchema = z
  .strictObject({
    document: documentSchema,
    references: referencesSchema.optional(),
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
export const discussWriterRequestSchema = writerContextSchema
  .safeExtend({
    message: z.string().trim().min(1).max(2_000),
    passage: passageSchema.optional(),
    previousCandidate: z.string().min(1).max(4_000).optional(),
  })
  .refine(
    ({ document, passage }) =>
      !passage ||
      (passage.from < passage.to &&
        document.content.slice(passage.from, passage.to) === passage.text),
    { message: "Passage must match the document range." },
  );

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
export const discussWriterResponseSchema = z.discriminatedUnion("mode", [
  z.strictObject({ mode: z.literal("reply"), text: z.string().trim().min(1).max(2_000) }),
  z.strictObject({
    mode: z.literal("proposal"),
    text: z.string().trim().min(1).max(2_000),
    candidate: z
      .string()
      .min(1)
      .max(4_000)
      .refine((text) => !!text.trim()),
  }),
]);

export type ObserveWriterRequest = z.infer<typeof observeWriterRequestSchema>;
export type DiscussWriterRequest = z.infer<typeof discussWriterRequestSchema>;
export type ObserveWriterResponse = z.infer<typeof observeWriterResponseSchema>;
export type DiscussWriterResponse = z.infer<typeof discussWriterResponseSchema>;
