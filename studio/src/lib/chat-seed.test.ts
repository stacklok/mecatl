import { describe, expect, it } from "vitest";
import {
  isCommandSeed,
  MAX_CHAT_SEED_CHARS,
  resolveChatSeed,
} from "./chat-seed";

/**
 * The `?prompt=` deep link's resolver: first value of a repeated key, trimmed,
 * control characters dropped (newline and tab kept), nothing on
 * empty/whitespace, clamped to the cap (never splitting a surrogate
 * pair), `send=1` and only `send=1` asks for the one-click send, a leading
 * `/` is a command and never auto-sends, and a PWA share's title/url join
 * the text on their own lines without repeating what the text already says.
 */
describe("resolveChatSeed", () => {
  it("returns null when there is nothing to seed", () => {
    expect(resolveChatSeed(undefined)).toBeNull();
    expect(resolveChatSeed(null)).toBeNull();
    expect(resolveChatSeed({})).toBeNull();
    expect(resolveChatSeed({ prompt: "" })).toBeNull();
    expect(resolveChatSeed({ prompt: "   \n\t " })).toBeNull();
    expect(resolveChatSeed({ prompt: [] })).toBeNull();
    // `send` alone is not a prompt.
    expect(resolveChatSeed({ send: "1" })).toBeNull();
  });

  it("trims the prompt and defaults to prefill-only", () => {
    expect(resolveChatSeed({ prompt: "  Hello fixture \n" })).toEqual({
      prompt: "Hello fixture",
      autoSend: false,
    });
  });

  it("drops control characters but keeps newlines and tabs", () => {
    expect(
      resolveChatSeed({
        prompt: "line one\u0000\u001b[31m\r\nline\ttwo\u007f",
      }),
    ).toEqual({ prompt: "line one[31m\nline\ttwo", autoSend: false });
    // A prompt that is nothing but controls is nothing.
    expect(resolveChatSeed({ prompt: "\u0000\u0007\u001b" })).toBeNull();
    // A share's title and url are cleaned the same way.
    expect(
      resolveChatSeed({
        title: "Pa\u0000ge",
        url: "https://example.test/\u0001",
      })?.prompt,
    ).toBe("Page\nhttps://example.test/");
  });

  it("takes only the first value of a repeated key", () => {
    expect(resolveChatSeed({ prompt: ["first", "second"] })).toEqual({
      prompt: "first",
      autoSend: false,
    });
    expect(resolveChatSeed({ prompt: "x", send: ["1", "0"] })?.autoSend).toBe(
      true,
    );
    expect(resolveChatSeed({ prompt: "x", send: ["0", "1"] })?.autoSend).toBe(
      false,
    );
  });

  it("asks for the send on send=1 and on nothing else", () => {
    expect(resolveChatSeed({ prompt: "x", send: "1" })?.autoSend).toBe(true);
    for (const send of ["0", "true", "yes", "", " 1", "2", undefined]) {
      expect(resolveChatSeed({ prompt: "x", send })?.autoSend).toBe(false);
    }
  });

  it("never auto-sends a slash command, whatever send says", () => {
    expect(resolveChatSeed({ prompt: "/clear", send: "1" })).toEqual({
      prompt: "/clear",
      autoSend: false,
    });
    expect(resolveChatSeed({ prompt: "  /help", send: "1" })?.autoSend).toBe(
      false,
    );
    expect(isCommandSeed("/compact")).toBe(true);
    expect(isCommandSeed("not /a command")).toBe(false);
  });

  it("clamps an oversized prompt to the cap", () => {
    const huge = "a".repeat(MAX_CHAT_SEED_CHARS + 500);
    const seed = resolveChatSeed({ prompt: huge });
    expect(seed?.prompt).toHaveLength(MAX_CHAT_SEED_CHARS);
  });

  it("does not leave a dangling lead surrogate at the cut", () => {
    // An emoji straddling the cap: the cut lands between its two halves.
    const prefix = "a".repeat(MAX_CHAT_SEED_CHARS - 1);
    const seed = resolveChatSeed({ prompt: `${prefix}😀tail` });
    expect(seed?.prompt).toBe(prefix);
    expect(seed?.prompt).toHaveLength(MAX_CHAT_SEED_CHARS - 1);
  });

  it("joins a share's title, text and url on their own lines", () => {
    expect(
      resolveChatSeed({
        title: "Release notes",
        prompt: "Summarise this",
        url: "https://example.test/notes",
      }),
    ).toEqual({
      prompt: "Release notes\nSummarise this\nhttps://example.test/notes",
      autoSend: false,
    });
    // A share with no text still seeds (title + url).
    expect(
      resolveChatSeed({ title: "Page", url: "https://example.test/" }),
    ).toEqual({ prompt: "Page\nhttps://example.test/", autoSend: false });
  });

  it("does not repeat a title or url the text already contains", () => {
    expect(
      resolveChatSeed({
        title: "Page",
        prompt: "Page — https://example.test/a",
        url: "https://example.test/a",
      })?.prompt,
    ).toBe("Page — https://example.test/a");
    // A share whose title IS the url (some browsers) lists it once.
    expect(
      resolveChatSeed({
        title: "https://example.test/b",
        url: "https://example.test/b",
      })?.prompt,
    ).toBe("https://example.test/b");
  });
});
