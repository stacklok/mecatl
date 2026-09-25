// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const soulInspectionResponseSchema = z.object({
  present: z.boolean(),
  content: z.string(),
  sizeBytes: z.string().regex(/^(0|[1-9]\d*)$/),
  sha256: z.string(),
  provenance: z.enum(["unspecified", "user", "project", "driver"]),
  trusted: z.boolean(),
  drifted: z.boolean(),
});

export const sessionWorktreesResponseSchema = z.object({
  items: z.array(
    z.object({
      selector: z.string(),
      kind: z.string(),
      label: z.string(),
      branch: z.string(),
      revision: z.string(),
      bare: z.boolean(),
    }),
  ),
});

export type SoulInspectionResponse = z.infer<typeof soulInspectionResponseSchema>;
export type SessionWorktreesResponse = z.infer<typeof sessionWorktreesResponseSchema>;
