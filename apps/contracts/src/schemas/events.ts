// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

/**
 * The application-owned SSE event union every Studio stream speaks.
 *
 * Feature plans add their own members. An SDK event whose kind Studio does not
 * model is NEVER dropped: it is forwarded as `unknown` with its kind and raw
 * payload so the UI can surface protocol drift instead of hiding it.
 */
export const unknownStreamEventSchema = z.object({
  kind: z.literal("unknown"),
  payload: z.unknown(),
  sourceKind: z.string(),
});

export const streamEventSchema = z.discriminatedUnion("kind", [unknownStreamEventSchema]);

export type StreamEvent = z.infer<typeof streamEventSchema>;
export type UnknownStreamEvent = z.infer<typeof unknownStreamEventSchema>;
