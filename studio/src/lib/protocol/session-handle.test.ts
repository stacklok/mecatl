import { describe, expect, it } from "vitest";
import { SESSION_HANDLE_WIDTH, shortSessionHandle } from "./session-handle";

/**
 * Pins the 12-column session handle against the Go encoder's grammar
 * (`cmd/mecatui/client/client.go` `SessionHandle`, its vectors in
 * `debug_test.go`): a Studio handle must equal the mecatui handle for the
 * same id, or a handle pasted between the two clients stops resolving.
 */
describe("shortSessionHandle", () => {
  it("copies a safe id literally and cuts it at twelve columns", () => {
    expect(shortSessionHandle("session-fixture-1")).toBe("session-fixt");
    expect(shortSessionHandle("abc")).toBe("abc");
    // The Go suite's width vector: the 13th byte and everything after it
    // is dropped, a trailing hyphen included.
    expect(shortSessionHandle("123456789012-rest")).toBe("123456789012");
    expect(shortSessionHandle("a.b_c-d")).toBe("a.b_c-d");
  });

  it("escapes a LEADING hyphen as %2D and keeps later hyphens literal", () => {
    expect(shortSessionHandle("-abc-def")).toBe("%2Dabc-def");
  });

  it("encodes every non-safe byte as an uppercase %HH atom", () => {
    expect(shortSessionHandle("a b")).toBe("a%20b");
    expect(shortSessionHandle("a/b")).toBe("a%2Fb");
    // Multi-byte UTF-8: one atom per BYTE (é = C3 A9).
    expect(shortSessionHandle("é")).toBe("%C3%A9");
  });

  it("keeps only complete atoms that fit — never a split %HH", () => {
    // 11 safe columns leave one column: a 3-column atom does not fit, and
    // the safe byte AFTER it is not reached either (the Go loop breaks).
    expect(shortSessionHandle("abcdefghijk/x")).toBe("abcdefghijk");
    // 10 safe columns + a 3-column atom would be 13: dropped.
    expect(shortSessionHandle("abcdefghij é")).toBe("abcdefghij");
    // 9 safe columns + one atom fits exactly.
    expect(shortSessionHandle("abcdefghi/xyz")).toBe("abcdefghi%2F");
  });

  it("has no handle for an empty id", () => {
    expect(shortSessionHandle("")).toBe("");
  });

  it("never exceeds the fixed width", () => {
    for (const id of ["x".repeat(40), "é".repeat(20), "-".repeat(20)]) {
      expect(shortSessionHandle(id).length).toBeLessThanOrEqual(
        SESSION_HANDLE_WIDTH,
      );
    }
  });
});
