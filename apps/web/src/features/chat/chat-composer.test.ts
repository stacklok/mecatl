// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { resolveComposerAction } from "./chat-composer";

describe("composer Enter behavior", () => {
  it("sends Enter and preserves Shift+Enter as a newline while idle", () => {
    expect(resolveComposerAction({ behavior: "queue", shift: false, working: false })).toBe("send");
    expect(resolveComposerAction({ behavior: "queue", shift: true, working: false })).toBe(
      "newline",
    );
  });

  it("uses the preference and reverses it with Shift during a run", () => {
    expect(resolveComposerAction({ behavior: "queue", shift: false, working: true })).toBe("queue");
    expect(resolveComposerAction({ behavior: "queue", shift: true, working: true })).toBe("steer");
    expect(resolveComposerAction({ behavior: "steer", shift: true, working: true })).toBe("queue");
  });
});
