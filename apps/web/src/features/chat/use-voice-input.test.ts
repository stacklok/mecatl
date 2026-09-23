// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { joinTranscript } from "./use-voice-input";

describe("voice input", () => {
  it("appends a transcript to existing input", () => {
    expect(joinTranscript("Explain this", "in simple terms")).toBe("Explain this in simple terms");
  });

  it("does not add leading space to an empty prompt", () => {
    expect(joinTranscript("", " hello world")).toBe("hello world");
  });
});
