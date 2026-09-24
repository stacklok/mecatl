// SPDX-License-Identifier: Apache-2.0

import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  acceptsPopupResult,
  commitRecoveryCheck,
  isRecoverableAuthError,
  nextRecoveryState,
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
      ["studio.chat.queue", "draft"],
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
    expect(storage.get("studio.chat.queue")).toBe("draft");

    const different = commitRecoveryCheck(
      ready,
      { kind: "authenticated", account: "opaque-b" },
      queryClient,
      store,
    );
    expect(different.clearAccount).toBe(true);
    expect(different.state.identityEpoch).toBe(1);
    expect(storage.get("studio.chat.queue")).toBeUndefined();
    expect(storage.get("studio.account")).toBe("opaque-b");
    expect(queryClient.getQueryData(["private", "sessions"])).toBeUndefined();

    storage.set("studio.chat.queue", "wrong-account draft");
    queryClient.setQueryData(["private", "sessions"], { title: "wrong account" });
    const missing = commitRecoveryCheck(ready, { kind: "authenticated" }, queryClient, store);
    expect(missing.clearAccount).toBe(true);
    expect(missing.state.workspaceMounted).toBe(false);
    expect(missing.state.account).toBeUndefined();
    expect(storage.get("studio.chat.queue")).toBeUndefined();
    expect(queryClient.getQueryData(["private", "sessions"])).toBeUndefined();

    storage.set("studio.account", "opaque-a");
    storage.set("studio.chat.queue", "signed-out draft");
    queryClient.setQueryData(["private", "sessions"], { title: "signed-out data" });
    const signedOut = commitRecoveryCheck(ready, { kind: "signed-out" }, queryClient, store);
    expect(signedOut.state.workspaceMounted).toBe(false);
    expect(signedOut.state.phase).toBe("sign-in");
    expect(storage.get("studio.chat.queue")).toBeUndefined();
    expect(queryClient.getQueryData(["private", "sessions"])).toBeUndefined();
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
