// SPDX-License-Identifier: Apache-2.0

import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { statusBannerState } from "../../components/shell/connection-status-banner-state";
import { clearUserScopedStorage } from "../../lib/account-storage";
import {
  acceptsPopupResult,
  commitRecoveryCheck,
  isRecoverableAuthError,
  nextRecoveryState,
  publicStatusFetchFailed,
  type RecoveryState,
  shouldPauseProtectedRequest,
} from "./auth-recovery-state";

const ready: RecoveryState = {
  account: "opaque-a",
  identityEpoch: 0,
  phase: "ready",
  workspaceMounted: true,
};

describe("Studio auth recovery", () => {
  it("the auth gate offers sign-in only for anonymous OIDC sessions", () => {
    const checking: RecoveryState = {
      identityEpoch: 0,
      phase: "checking",
      workspaceMounted: false,
    };
    expect(nextRecoveryState(checking, { kind: "anonymous" }).state).toMatchObject({
      phase: "sign-in",
      workspaceMounted: false,
    });
    expect(nextRecoveryState(checking, { kind: "disabled" }).state).toMatchObject({
      phase: "ready",
      workspaceMounted: true,
    });
    expect(
      nextRecoveryState(checking, { kind: "authenticated", account: "opaque-a" }).state,
    ).toMatchObject({ phase: "ready", workspaceMounted: true });
    expect(nextRecoveryState(checking, { kind: "session-check-failed" }).state).toMatchObject({
      phase: "verification-unavailable",
      workspaceMounted: false,
    });
  });

  it("popup messages require origin source and active attempt", () => {
    const popup = {} as Window;
    const other = {} as Window;
    const expected = {
      activeAttempt: 2,
      attempt: 2,
      origin: "https://studio.example",
      popup,
    };
    const event = {
      data: { type: "studio.auth.result", result: "success" },
      origin: "https://studio.example",
      source: popup,
    };
    expect(acceptsPopupResult(event, expected)).toBe(true);
    expect(acceptsPopupResult({ ...event, origin: "https://other.example" }, expected)).toBe(false);
    expect(acceptsPopupResult({ ...event, source: other }, expected)).toBe(false);
    expect(acceptsPopupResult(event, { ...expected, activeAttempt: 3 })).toBe(false);
    expect(
      acceptsPopupResult(
        { ...event, data: { type: "studio.auth.result", result: "maybe" } },
        expected,
      ),
    ).toBe(false);
    expect(
      acceptsPopupResult({ ...event, data: { ...event.data, sub: "raw-sub" } }, expected),
    ).toBe(false);
  });

  it("a throttled status check stays neutral", () => {
    const limited = { code: "rate_limited", status: 429 };
    expect(publicStatusFetchFailed(true, limited)).toBe(false);
    expect(publicStatusFetchFailed(false, limited)).toBe(false);
    expect(publicStatusFetchFailed(true, { code: "internal_error", status: 503 })).toBe(true);
    expect(publicStatusFetchFailed(true, new Error("Network failure"))).toBe(true);
    expect(
      statusBannerState({
        authenticated: false,
        publicStatus: undefined,
        publicStatusFailed: publicStatusFetchFailed(true, limited),
        sessionCheckFailed: false,
      }),
    ).toBe("hidden");
  });

  it("expired session preserves a draft and never replays writes", () => {
    const draft = { text: "Draft with an attachment", attachments: ["image.png"] };
    const route = "/workspace/chat?sessionId=s1#composer";
    const expired = nextRecoveryState(ready, { kind: "anonymous" });
    expect(expired.state.workspaceMounted).toBe(true);
    expect(expired.state.phase).toBe("sign-in");
    expect(expired.state.account).toBe("opaque-a");
    expect(expired.refetchReads).toBe(false);
    expect(draft).toEqual({ text: "Draft with an attachment", attachments: ["image.png"] });
    expect(route).toBe("/workspace/chat?sessionId=s1#composer");
    expect(isRecoverableAuthError({ code: "session_expired", status: 401 })).toBe(true);
    expect(isRecoverableAuthError({ code: "unauthenticated", status: 401 })).toBe(true);
    expect(shouldPauseProtectedRequest("POST", "/api/v1/sessions/s1/runs", expired.state)).toBe(
      true,
    );
    expect(shouldPauseProtectedRequest("GET", "/api/v1/sessions/s1/activity", expired.state)).toBe(
      true,
    );
    expect(shouldPauseProtectedRequest("GET", "/api/v1/sessions", expired.state)).toBe(false);
    const restored = nextRecoveryState(expired.state, {
      kind: "authenticated",
      account: "opaque-a",
    });
    expect(restored.refetchReads).toBe(true);
    expect(restored.replayWrites).toBe(false);
    expect(restored.state.identityEpoch).toBe(0);
  });

  it("same account keeps draft and different account clears it", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(["private", "sessions"], { title: "Alice's chat" });
    const storage = new Map([
      ["studio.account", "opaque-a"],
      ["studio.chat.queue.same", "draft"],
    ]);
    const store = {
      getItem: (key: string) => storage.get(key) ?? null,
      key: (index: number) => [...storage.keys()][index] ?? null,
      get length() {
        return storage.size;
      },
      removeItem: (key: string) => void storage.delete(key),
      setItem: (key: string, value: string) => void storage.set(key, value),
    };
    const same = commitRecoveryCheck(
      ready,
      { kind: "authenticated", account: "opaque-a" },
      queryClient,
      store,
    );
    expect(same.clearAccount).toBe(false);
    expect(same.state.identityEpoch).toBe(0);
    expect(storage.get("studio.chat.queue.same")).toBe("draft");

    const different = commitRecoveryCheck(
      ready,
      { kind: "authenticated", account: "opaque-b" },
      queryClient,
      store,
    );
    expect(different.clearAccount).toBe(true);
    expect(different.state.identityEpoch).toBe(1);
    expect(storage.get("studio.chat.queue.same")).toBeUndefined();
    expect(storage.get("studio.account")).toBe("opaque-b");
    expect(queryClient.getQueryData(["private", "sessions"])).toBeUndefined();

    storage.set("studio.account", "opaque-a");
    storage.set("studio.chat.queue.same", "wrong-account draft");
    queryClient.setQueryData(["private", "sessions"], { title: "wrong account" });
    const missing = commitRecoveryCheck(ready, { kind: "authenticated" }, queryClient, store);
    expect(missing.clearAccount).toBe(true);
    expect(missing.state.workspaceMounted).toBe(false);
    expect(missing.state.account).toBeUndefined();
    expect(storage.get("studio.chat.queue.same")).toBeUndefined();
    expect(queryClient.getQueryData(["private", "sessions"])).toBeUndefined();

    storage.set("studio.account", "opaque-a");
    storage.set("studio.chat.queue.same", "signed-out draft");
    queryClient.setQueryData(["private", "sessions"], { title: "signed-out data" });
    const signedOut = commitRecoveryCheck(ready, { kind: "signed-out" }, queryClient, store);
    expect(signedOut.state.workspaceMounted).toBe(false);
    expect(signedOut.state.phase).toBe("sign-in");
    expect(storage.get("studio.chat.queue.same")).toBeUndefined();
    expect(queryClient.getQueryData(["private", "sessions"])).toBeUndefined();
  });

  it("preserves a peer's marked shared data when authenticated Bob arrives first", () => {
    const data = new Map<string, string>();
    const store = {
      getItem: (key: string) => data.get(key) ?? null,
      key: (index: number) => [...data.keys()][index] ?? null,
      get length() {
        return data.size;
      },
      removeItem: (key: string) => void data.delete(key),
      setItem: (key: string, value: string) => void data.set(key, value),
    };
    clearUserScopedStorage(store);
    data.set("studio.account", "opaque-a");
    const queries = new QueryClient();
    commitRecoveryCheck(ready, { kind: "authenticated", account: "opaque-a" }, queries, store);
    queries.setQueryData(["private", "sessions"], { title: "Alice's chat" });
    data.set("studio.account", "opaque-b");
    data.set("studio.chat.folders", "Bob's folder");
    data.set("studio.chat.queue.same", "Bob's queued prompt");

    const transition = commitRecoveryCheck(
      ready,
      { kind: "authenticated", account: "opaque-b" },
      queries,
      store,
    );
    expect(transition.state).toMatchObject({ account: "opaque-b", phase: "ready" });
    expect(data.get("studio.account")).toBe("opaque-b");
    expect(data.get("studio.chat.folders")).toBe("Bob's folder");
    expect(data.get("studio.chat.queue.same")).toBe("Bob's queued prompt");
    expect(queries.getQueryData(["private", "sessions"])).toBeUndefined();
    queries.clear();
  });

  it.each([
    ["missing identity", { kind: "authenticated" } as const],
    ["failed check", { kind: "session-check-failed" } as const],
  ])("quarantines a stale account after a %s without a storage event", (_case, check) => {
    const data = new Map([
      ["studio.account", "opaque-b"],
      ["studio.chat.folders", "Bob's folder"],
      ["studio.chat.queue.same", "Bob's queued prompt"],
    ]);
    const store = {
      getItem: (key: string) => data.get(key) ?? null,
      key: (index: number) => [...data.keys()][index] ?? null,
      get length() {
        return data.size;
      },
      removeItem: (key: string) => void data.delete(key),
      setItem: (key: string, value: string) => void data.set(key, value),
    };
    const queries = new QueryClient();
    queries.setQueryData(["private", "sessions"], { title: "Alice's chat" });
    const transition = commitRecoveryCheck(ready, check, queries, store);
    expect(transition.state.workspaceMounted).toBe(false);
    expect(data.get("studio.account")).toBe("opaque-b");
    expect(data.get("studio.chat.folders")).toBe("Bob's folder");
    expect(data.get("studio.chat.queue.same")).toBe("Bob's queued prompt");
    expect(queries.getQueryData(["private", "sessions"])).toBeUndefined();
    queries.clear();
  });

  it("session check failure keeps the mounted draft", () => {
    const route = "/workspace/chat?sessionId=s1#composer";
    const draft = "Please keep this unsent";
    const failed = nextRecoveryState(ready, { kind: "session-check-failed" });
    expect(failed.state).toMatchObject({
      account: "opaque-a",
      identityEpoch: 0,
      phase: "verification-unavailable",
      workspaceMounted: true,
    });
    expect(shouldPauseProtectedRequest("PATCH", "/api/v1/sessions/s1", failed.state)).toBe(true);
    expect(shouldPauseProtectedRequest("GET", "/api/v1/sessions/s1/activity", failed.state)).toBe(
      true,
    );
    expect(route).toBe("/workspace/chat?sessionId=s1#composer");
    expect(draft).toBe("Please keep this unsent");
    const initial = nextRecoveryState(
      { identityEpoch: 0, phase: "checking", workspaceMounted: false },
      { kind: "session-check-failed" },
    );
    expect(initial.state.workspaceMounted).toBe(false);
    expect(initial.state.phase).toBe("verification-unavailable");
  });
});
