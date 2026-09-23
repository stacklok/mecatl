// SPDX-License-Identifier: Apache-2.0

import type { GetAuthSessionResponse } from "@mecatl-studio/contracts/generated";

export type AuthSessionResponse = GetAuthSessionResponse;

/** What the auth gate renders, derived from the `/api/v1/auth/session` query. */
export type AuthGateState = "checking" | "error" | "sign-in" | "ready";

/**
 * Sign-in is shown only when the runtime requires interactive login (`oidc`)
 * and no browser session exists (`anonymous`). A static-token or no-auth
 * runtime reports `disabled` and goes straight to the workspace.
 */
export function authGateState(session: {
  isPending: boolean;
  isError: boolean;
  data?: AuthSessionResponse;
}): AuthGateState {
  if (session.isPending) return "checking";
  if (session.isError || session.data === undefined) return "error";
  if (session.data.mode === "oidc" && session.data.status === "anonymous") return "sign-in";
  return "ready";
}
