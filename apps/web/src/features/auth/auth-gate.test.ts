// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { type AuthSessionResponse, authGateState } from "./auth-gate-state";

function loaded(data: AuthSessionResponse) {
  return { data, isError: false, isPending: false };
}

describe("authGateState", () => {
  it("the auth gate shows sign-in only when the runtime requires interactive login", () => {
    expect(authGateState(loaded({ mode: "oidc", status: "anonymous" }))).toBe("sign-in");
    expect(authGateState(loaded({ mode: "oidc", status: "authenticated" }))).toBe("ready");
    expect(authGateState(loaded({ mode: "static", status: "disabled" }))).toBe("ready");
    expect(authGateState(loaded({ mode: "none", status: "disabled" }))).toBe("ready");
    expect(authGateState({ isError: false, isPending: true })).toBe("checking");
    expect(authGateState({ isError: true, isPending: false })).toBe("error");
  });
});
