// SPDX-License-Identifier: Apache-2.0

import type { QueryClient } from "@tanstack/react-query";
import { clearUserScopedStorage, reconcileAccount } from "../../lib/account-storage";

export type RecoveryPhase = "checking" | "ready" | "sign-in" | "verification-unavailable";

export interface RecoveryState {
  account?: string;
  identityEpoch: number;
  phase: RecoveryPhase;
  workspaceMounted: boolean;
}

export type SessionCheck =
  | { kind: "authenticated"; account?: string }
  | { kind: "disabled" }
  | { kind: "anonymous" }
  | { kind: "signed-out" }
  | { kind: "session-check-failed" };

export interface RecoveryTransition {
  clearAccount: boolean;
  refetchReads: boolean;
  replayWrites: false;
  state: RecoveryState;
}

/** A transient auth failure never throws the mounted workspace and its local state away. */
export function nextRecoveryState(
  previous: RecoveryState,
  check: SessionCheck,
): RecoveryTransition {
  const keep = (phase: RecoveryPhase): RecoveryTransition => ({
    clearAccount: false,
    refetchReads: false,
    replayWrites: false,
    state: { ...previous, phase },
  });
  if (check.kind === "session-check-failed") return keep("verification-unavailable");
  if (check.kind === "anonymous") return keep("sign-in");
  if (check.kind === "signed-out") {
    return {
      clearAccount: true,
      refetchReads: false,
      replayWrites: false,
      state: {
        identityEpoch: previous.identityEpoch + 1,
        phase: "sign-in",
        workspaceMounted: false,
      },
    };
  }

  if (check.kind === "authenticated" && !check.account) {
    return {
      clearAccount: true,
      refetchReads: false,
      replayWrites: false,
      state: {
        identityEpoch: previous.identityEpoch + 1,
        phase: "sign-in",
        workspaceMounted: false,
      },
    };
  }

  const account = check.kind === "authenticated" ? check.account : undefined;
  const changed = previous.workspaceMounted && previous.account !== account;
  return {
    clearAccount: changed,
    refetchReads: !changed && previous.workspaceMounted && previous.phase !== "ready",
    replayWrites: false,
    state: {
      account,
      identityEpoch: previous.identityEpoch + (changed ? 1 : 0),
      phase: "ready",
      workspaceMounted: true,
    },
  };
}

/** Clear the old principal's browser and query data before rendering the next one. */
export function commitRecoveryCheck(
  previous: RecoveryState,
  check: SessionCheck,
  queryClient: QueryClient,
  storage?: Parameters<typeof reconcileAccount>[1],
): RecoveryTransition {
  const transition = nextRecoveryState(previous, check);
  if (transition.clearAccount) clearUserScopedStorage(storage);
  const storageChanged =
    check.kind === "authenticated" && check.account
      ? reconcileAccount(check.account, storage)
      : false;
  if (transition.clearAccount || storageChanged) {
    queryClient.removeQueries({ predicate: (query) => !isPublicQuery(query.queryKey) });
  }
  return transition;
}

export function isPublicQuery(queryKey: readonly unknown[]): boolean {
  const key = queryKey[0];
  return (
    typeof key === "object" &&
    key !== null &&
    "_id" in key &&
    (key._id === "getAuthSession" || key._id === "getPublicStatus")
  );
}

/** The callback contains no identity; its only authority is the active same-origin popup. */
export function acceptsPopupResult(
  event: Pick<MessageEvent, "data" | "origin" | "source">,
  expected: { activeAttempt: number; attempt: number; origin: string; popup: Window | null },
): boolean {
  const data: unknown = event.data;
  return (
    expected.popup !== null &&
    expected.activeAttempt === expected.attempt &&
    event.origin === expected.origin &&
    event.source === expected.popup &&
    typeof data === "object" &&
    data !== null &&
    !Array.isArray(data) &&
    Object.keys(data).length === 2 &&
    "type" in data &&
    data.type === "studio.auth.result" &&
    "result" in data &&
    (data.result === "success" || data.result === "failure")
  );
}

export function isRecoverableAuthError(error: unknown): boolean {
  if (typeof error !== "object" || error === null || !("code" in error) || !("status" in error))
    return false;
  if (error.status !== 401) return false;
  return error.code === "session_expired" || error.code === "unauthenticated";
}

/** Only new writes and streams are held; reads can settle and refetch after verification. */
export function shouldPauseProtectedRequest(
  method: string,
  pathname: string,
  state: RecoveryState,
): boolean {
  if (state.phase === "ready" || !pathname.startsWith("/api/v1/")) return false;
  if (pathname.startsWith("/api/v1/auth/") || pathname === "/api/v1/status") return false;
  return method.toUpperCase() !== "GET" || pathname.endsWith("/activity");
}
