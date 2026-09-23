// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { readCsrfToken } from "./api-client";

describe("CSRF token cookie reader", () => {
  it("extracts studio_csrf from a cookie string and ignores other cookies", () => {
    expect(readCsrfToken("theme=dark; studio_csrf=abc=def; other=1")).toBe("abc=def");
    expect(readCsrfToken("studio_csrf=tok")).toBe("tok");
    expect(readCsrfToken("theme=dark")).toBeUndefined();
    expect(readCsrfToken("studio_csrf=")).toBeUndefined();
    expect(readCsrfToken("")).toBeUndefined();
  });
});
