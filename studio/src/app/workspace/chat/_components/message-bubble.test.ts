import { describe, expect, it } from "vitest";
import { splitLeadingQuote } from "./message-bubble";

describe("splitLeadingQuote", () => {
  it("splits the leading quote block from the user's words", () => {
    expect(splitLeadingQuote("> a\n> b\n\nmy question")).toEqual({
      quote: "a\nb",
      rest: "my question",
    });
  });
  it("leaves unquoted messages untouched", () => {
    expect(splitLeadingQuote("plain > not a quote")).toEqual({
      quote: null,
      rest: "plain > not a quote",
    });
  });
  it("treats a mid-message quote as plain text", () => {
    expect(splitLeadingQuote("hello\n> quoted later")).toEqual({
      quote: null,
      rest: "hello\n> quoted later",
    });
  });
  it("collapses nested quote markers", () => {
    expect(splitLeadingQuote("> > deep\n\nwords")).toEqual({
      quote: "deep",
      rest: "words",
    });
  });
  it("handles a quote-only message", () => {
    expect(splitLeadingQuote("> just a quote")).toEqual({
      quote: "just a quote",
      rest: "",
    });
  });
});
