// SPDX-License-Identifier: Apache-2.0

import { readUserScopedItem, writeUserScopedItem } from "../../lib/account-storage";
import type { RunFailure } from "./chat-state";

type Store = Pick<Storage, "getItem" | "key" | "length" | "removeItem" | "setItem">;

function browserSessionStore(): Store | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

function failedRunStorageKey(sessionId: string) {
  return `studio.chat.failedRun.${sessionId}`;
}

export function saveFailedRun(
  sessionId: string,
  failure: RunFailure,
  store: Store | null = browserSessionStore(),
): void {
  writeUserScopedItem(failedRunStorageKey(sessionId), JSON.stringify(failure), store);
}

export function clearFailedRun(
  sessionId: string,
  store: Store | null = browserSessionStore(),
): void {
  writeUserScopedItem(failedRunStorageKey(sessionId), null, store);
}

export function readFailedRun(
  sessionId: string,
  store: Store | null = browserSessionStore(),
): RunFailure | undefined {
  const raw = readUserScopedItem(failedRunStorageKey(sessionId), store);
  if (!raw) return undefined;
  try {
    const value: unknown = JSON.parse(raw);
    if (
      typeof value !== "object" ||
      value === null ||
      !("message" in value) ||
      !("prompt" in value) ||
      typeof value.message !== "string" ||
      typeof value.prompt !== "string"
    ) {
      return undefined;
    }
    return {
      message: value.message,
      permanent: "permanent" in value && value.permanent === true,
      prompt: value.prompt,
    };
  } catch {
    return undefined;
  }
}
