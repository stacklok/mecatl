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
 * previous account left in this origin's storage. `email` is present only for
 * an authenticated session when a verified ID token provided an accepted claim.
 */
export const authSessionResponseSchema = z.union([
  z.strictObject({
    account: z.string().min(1),
    email: z.string().optional(),
    mode: z.literal("oidc"),
    status: z.literal("authenticated"),
  }),
  z.strictObject({ mode: z.literal("oidc"), status: z.literal("anonymous") }),
  z.strictObject({ mode: z.enum(["static", "none"]), status: z.literal("disabled") }),
]);

export type AuthSessionResponse = z.infer<typeof authSessionResponseSchema>;
