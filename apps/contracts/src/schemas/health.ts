// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

export const healthResponseSchema = z.object({
  service: z.literal("mecatl-studio"),
  status: z.literal("ok"),
});

export type HealthResponse = z.infer<typeof healthResponseSchema>;
