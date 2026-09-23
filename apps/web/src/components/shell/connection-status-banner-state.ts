// SPDX-License-Identifier: Apache-2.0

import type { GetRuntimeResponse } from "@mecatl-studio/contracts/generated";

export type RuntimeResponse = GetRuntimeResponse;

/** What the connection banner renders, derived from the `/api/v1/runtime` query. */
export type ConnectionBannerState = "hidden" | { kind: "reconnecting" } | { kind: "unavailable" };

export const connectionBannerMessages = {
  reconnecting: "Reconnecting to the Mecatl instance…",
  unavailable: "Mecatl is unavailable right now.",
} as const satisfies Record<Exclude<ConnectionBannerState, "hidden">["kind"], string>;

/**
 * Hidden while the query is still resolving or the connection is `online`;
 * `reconnecting` maps to its own message; an error (including a `503`) or
 * any other non-online connection reads as unavailable.
 */
export function connectionBannerState(runtime: {
  isPending: boolean;
  isError: boolean;
  data?: RuntimeResponse;
}): ConnectionBannerState {
  if (runtime.isPending) return "hidden";
  if (runtime.isError || runtime.data === undefined) return { kind: "unavailable" };
  switch (runtime.data.connection) {
    case "online":
      return "hidden";
    case "reconnecting":
      return { kind: "reconnecting" };
    default:
      return { kind: "unavailable" };
  }
}
