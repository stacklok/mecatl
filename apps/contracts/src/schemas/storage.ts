// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const storageHealthResponseSchema = z.object({
  activeJob: z.boolean(),
  available: z.boolean(),
  childCount: z.string(),
  corruptCount: z.string(),
  currentBytes: z.string().nullable(),
  lastFailure: z.boolean(),
  mainCount: z.string(),
  reclaimableBytes: z.string().nullable(),
  scheduledCount: z.string(),
  sessionCount: z.string(),
  supported: z.boolean(),
  unknownCount: z.string(),
});

export type StorageHealthResponse = z.infer<typeof storageHealthResponseSchema>;
