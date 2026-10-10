// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { errorMessage, REFUSAL_BANNER_CLASS, REFUSAL_BANNER_DIALOG_CLASS } from "./error-message";

describe("errorMessage", () => {
  it("reads an RFC 9457 problem+json body's detail", () => {
    expect(errorMessage({ detail: "The schedule store is unavailable." })).toBe(
      "The schedule store is unavailable.",
    );
  });

  it("stringifies a non-string detail field", () => {
    expect(errorMessage({ detail: 42 })).toBe("42");
  });

  it("reads an Error's message", () => {
    expect(errorMessage(new Error("boom"))).toBe("boom");
  });

  it("falls back to a generic message for anything else", () => {
    expect(errorMessage("a thrown string")).toBe("The request could not be completed.");
    expect(errorMessage(undefined)).toBe("The request could not be completed.");
  });
});

describe("refusal banner classes", () => {
  it("keeps the page and dialog variants visually distinct only in padding", () => {
    expect(REFUSAL_BANNER_CLASS).toContain("px-4 py-3");
    expect(REFUSAL_BANNER_DIALOG_CLASS).toContain("px-3 py-2");
    expect(REFUSAL_BANNER_CLASS.replace("px-4 py-3", "")).toBe(
      REFUSAL_BANNER_DIALOG_CLASS.replace("px-3 py-2", ""),
    );
  });
});
