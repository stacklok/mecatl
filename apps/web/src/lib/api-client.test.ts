// SPDX-License-Identifier: Apache-2.0

import { client } from "@mecatl-studio/contracts/client";
import { createSession } from "@mecatl-studio/contracts/generated";
import { describe, expect, it, vi } from "vitest";
import {
  captureSseFailure,
  installRecoveryInterceptor,
  onAuthenticationRequired,
  readCsrfToken,
  setRequestRecoveryState,
} from "./api-client";

describe("CSRF token cookie reader", () => {
  it("extracts studio_csrf from a cookie string and ignores other cookies", () => {
    expect(readCsrfToken("theme=dark; studio_csrf=abc=def; other=1")).toBe("abc=def");
    expect(readCsrfToken("studio_csrf=tok")).toBe("tok");
    expect(readCsrfToken("theme=dark")).toBeUndefined();
    expect(readCsrfToken("studio_csrf=")).toBeUndefined();
    expect(readCsrfToken("")).toBeUndefined();
  });
});

describe("protected request boundary", () => {
  it("holds an actual generated write until the session is verified again", async () => {
    const fetch = vi.fn(async () => new Response("{}", { status: 200 }));
    client.setConfig({ baseUrl: "https://studio.example", fetch });
    installRecoveryInterceptor();
    setRequestRecoveryState({
      account: "opaque-a",
      identityEpoch: 0,
      phase: "verification-unavailable",
      workspaceMounted: true,
    });
    await expect(
      createSession({
        body: { mode: "default", reasoningEffort: "default", toolAccess: "all" },
        throwOnError: true,
      }),
    ).rejects.toMatchObject({ code: "session_verification_required" });
    expect(fetch).not.toHaveBeenCalled();
    setRequestRecoveryState({
      account: "opaque-a",
      identityEpoch: 0,
      phase: "ready",
      workspaceMounted: true,
    });
    await createSession({
      body: { mode: "default", reasoningEffort: "default", toolAccess: "all" },
      throwOnError: true,
    });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

describe("SSE authentication recovery", () => {
  it("records stream failures and reports only a 401 to the auth gate", () => {
    const authRequired = vi.fn();
    const unsubscribe = onAuthenticationRequired(authRequired);
    try {
      const failure: { error?: unknown } = {};
      const capture = captureSseFailure(failure);
      const unavailable = new Error("SSE failed: 503");
      capture(unavailable);
      expect(failure.error).toBe(unavailable);
      expect(authRequired).not.toHaveBeenCalled();

      const expired = new Error("SSE failed: 401");
      capture(expired);
      expect(failure.error).toBe(expired);
      expect(authRequired).toHaveBeenCalledOnce();
    } finally {
      unsubscribe();
    }
  });
});
