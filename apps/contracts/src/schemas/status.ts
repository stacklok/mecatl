// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

/** The complete public transport and browser sign-in view. */
export const publicStatusResponseSchema = z.strictObject({
  connection: z.enum(["checking", "reachable", "unavailable"]),
  signInRequired: z.boolean(),
});

export type PublicStatusResponse = z.infer<typeof publicStatusResponseSchema>;
