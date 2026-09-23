// SPDX-License-Identifier: Apache-2.0

import type { GetPublicStatusResponse } from "@mecatl-studio/contracts/generated";

export type PublicStatus = GetPublicStatusResponse;

export type StatusBannerInput = {
  publicStatus?: PublicStatus;
  publicStatusFailed: boolean;
  sessionCheckFailed: boolean;
  authenticated: boolean;
};

export type StatusBannerState =
  | "hidden"
  | "bff-unavailable"
  | "daemon-unavailable"
  | "session-check-failed"
  | "sign-in";

export const statusBannerMessages = {
  "bff-unavailable": "Studio is unavailable right now.",
  "daemon-unavailable": "The Mecatl instance is unavailable right now.",
  "session-check-failed": "We couldn't verify your sign-in. Try again.",
  "sign-in": "Sign in to use this Mecatl workspace.",
} as const;

export function statusBannerState(input: StatusBannerInput): StatusBannerState {
  if (input.publicStatusFailed) return "bff-unavailable";
  if (input.publicStatus?.connection === "unavailable") return "daemon-unavailable";
  if (input.sessionCheckFailed) return "session-check-failed";
  if (input.publicStatus?.connection === "checking") return "hidden";
  if (input.publicStatus?.signInRequired && !input.authenticated) return "sign-in";
  return "hidden";
}
