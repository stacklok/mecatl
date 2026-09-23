// SPDX-License-Identifier: Apache-2.0

import { z } from "zod";

/**
 * `mode` names how the BFF identifies callers to mecatl: `oidc` (browser
 * login through the deployment's issuer), `static` (one operator-supplied
 * bearer for every browser), or `none` (the runtime advertises no
 * authentication). `status` is this browser's own state; it is `disabled`
 * whenever `mode` is not `oidc`. `account`, present only when authenticated,
 * is an opaque, stable key for the signed-in identity (a hash of the issuer
 * subject, never the subject itself); the browser compares it to drop data a
 * previous account left in this origin's storage.
 */
export const authSessionResponseSchema = z.object({
  account: z.string().optional(),
  mode: z.enum(["oidc", "static", "none"]),
  status: z.enum(["authenticated", "anonymous", "disabled"]),
});

export type AuthSessionResponse = z.infer<typeof authSessionResponseSchema>;
