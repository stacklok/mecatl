// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const problemDetailsSchema = z.object({
  code: z.string(),
  detail: z.string(),
  instance: z.string(),
  status: z.number().int(),
  title: z.string(),
  type: z.string(),
});

export type ProblemDetails = z.infer<typeof problemDetailsSchema>;
