// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { appendCanvasQuote } from "./local-canvas";

describe("local canvas", () => {
  it("appends selected text as a markdown quote", () => {
    expect(appendCanvasQuote("# Notes\n", "one\ntwo")).toBe("# Notes\n\n> one\n> two\n");
  });
});
