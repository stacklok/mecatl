// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { pickAutoOpenTarget } from "./use-latest-chat-auto-open";

const base = {
  hold: false,
  latestChatId: "latest-id",
  loading: false,
  sessionId: undefined,
  startOn: "latest" as const,
};

describe("pickAutoOpenTarget", () => {
  it("picks the eligible chat once loaded, on the latest preference, with no session selected", () => {
    expect(pickAutoOpenTarget(base)).toBe("latest-id");
  });

  it("never fires once a chat is already selected", () => {
    expect(pickAutoOpenTarget({ ...base, sessionId: "already-open" })).toBeUndefined();
  });

  it("never fires under the draft preference", () => {
    expect(pickAutoOpenTarget({ ...base, startOn: "draft" })).toBeUndefined();
  });

  it("waits while the session inventory is still loading", () => {
    expect(pickAutoOpenTarget({ ...base, loading: true })).toBeUndefined();
  });

  it("never yanks the draft away when it already holds work", () => {
    expect(pickAutoOpenTarget({ ...base, hold: true })).toBeUndefined();
  });

  it("starts fresh when nothing is eligible", () => {
    expect(pickAutoOpenTarget({ ...base, latestChatId: undefined })).toBeUndefined();
  });
});
