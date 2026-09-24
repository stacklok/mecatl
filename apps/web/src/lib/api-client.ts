// SPDX-License-Identifier: Apache-2.0

import { client } from "@mecatl-studio/contracts/client";
import {
  isRecoverableAuthError,
  type RecoveryState,
  shouldPauseProtectedRequest,
} from "../features/auth/auth-recovery-state";

export const csrfCookieName = "studio_csrf";
export const csrfHeaderName = "X-Studio-CSRF";

const mutatingMethods = new Set(["POST", "PUT", "PATCH", "DELETE"]);
let recoveryState: RecoveryState = {
  identityEpoch: 0,
  phase: "checking",
  workspaceMounted: false,
};
let recoveryInterceptorInstalled = false;
const authFailureListeners = new Set<() => void>();

export function setRequestRecoveryState(state: RecoveryState): void {
  recoveryState = state;
}

export function protectedRequestsPaused(): boolean {
  return recoveryState.phase !== "ready";
}

export function onAuthenticationRequired(listener: () => void): () => void {
  authFailureListeners.add(listener);
  return () => authFailureListeners.delete(listener);
}

/** The generated SSE client reports only the HTTP status, not a parsed problem body. */
export function reportSseAuthFailure(error: unknown): void {
  if (!(error instanceof Error) || !/^SSE failed: 401\b/.test(error.message)) return;
  for (const listener of authFailureListeners) listener();
}

/** Applies the pause to generated mutations as well as direct SDK client calls. */
export function installRecoveryInterceptor(): void {
  if (recoveryInterceptorInstalled) return;
  recoveryInterceptorInstalled = true;
  client.interceptors.request.use((request) => {
    const pathname = new URL(request.url).pathname;
    if (shouldPauseProtectedRequest(request.method, pathname, recoveryState)) {
      throw { code: "session_verification_required" };
    }
    return request;
  });
  client.interceptors.error.use((error, response, request) => {
    if (
      response?.status === 401 &&
      request !== undefined &&
      !new URL(request.url).pathname.startsWith("/api/v1/auth/") &&
      isRecoverableAuthError(error)
    ) {
      for (const listener of authFailureListeners) listener();
    }
    return error;
  });
}

/** Reads the double-submit token the BFF issued, from a `document.cookie` string. */
export function readCsrfToken(cookieString: string): string | undefined {
  for (const pair of cookieString.split(";")) {
    const [name, ...rest] = pair.trim().split("=");
    if (name === csrfCookieName) {
      const value = rest.join("=");
      return value === "" ? undefined : value;
    }
  }
  return undefined;
}

/**
 * Every state-changing call to the BFF must carry `X-Studio-CSRF` equal to
 * the `studio_csrf` cookie (the BFF's double-submit check). The generated
 * client is shared by every feature, so the header is attached once here.
 */
export function installCsrfInterceptor(readCookies: () => string = () => document.cookie): void {
  client.interceptors.request.use((request) => {
    if (!mutatingMethods.has(request.method.toUpperCase())) return request;
    const token = readCsrfToken(readCookies());
    if (token !== undefined) request.headers.set(csrfHeaderName, token);
    return request;
  });
}
