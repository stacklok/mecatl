import { afterEach, describe, expect, it, vi } from "vitest";
import type { AgentMessage } from "@/features/agent";
import {
  copyFailureMessage,
  copyText,
  isSelectAllChord,
  selectElementContents,
  selectionInside,
  serializeTranscript,
} from "./transcript-text";

/**
 * The transcript select/copy helpers: the serialized conversation mirrors
 * what the per-message Copy button copies (content only, author headers,
 * attachments listed), the DOM select-all scopes to one element, and the
 * selection probe drives Esc's first layer.
 */

function msg(
  role: AgentMessage["role"],
  content: string,
  extra: Partial<AgentMessage> = {},
): AgentMessage {
  return {
    id: `${role}-${content.slice(0, 8)}-${Math.random()}`,
    role,
    content,
    timestamp: 1,
    ...extra,
  };
}

describe("serializeTranscript", () => {
  it("renders author headers from botName, agentName and You, blank-line separated", () => {
    const text = serializeTranscript(
      [
        msg("user", "Hello there"),
        msg("assistant", "Hi! How can I help?"),
        msg("assistant", "Reviewing now.", { agentName: "Code Reviewer" }),
      ],
      { botName: "Mecatl" },
    );
    expect(text).toBe(
      [
        "**You**",
        "",
        "Hello there",
        "",
        "**Mecatl**",
        "",
        "Hi! How can I help?",
        "",
        "**Code Reviewer**",
        "",
        "Reviewing now.",
      ].join("\n"),
    );
  });

  it("uses a custom user name when given", () => {
    expect(
      serializeTranscript([msg("user", "ping")], {
        botName: "Bot",
        userName: "James",
      }),
    ).toBe("**James**\n\nping");
  });

  it("skips tool-role messages and empty turns", () => {
    const text = serializeTranscript(
      [
        msg("tool", "raw tool output"),
        msg("assistant", "   ", { reasoning: "thinking only" }),
        msg("assistant", "", { failed: true, failureDetail: "boom" }),
        msg("user", "real question"),
      ],
      { botName: "Bot" },
    );
    expect(text).toBe("**You**\n\nreal question");
    expect(text).not.toContain("raw tool output");
    expect(text).not.toContain("thinking only");
    expect(text).not.toContain("boom");
  });

  it("lists attachments by name, and keeps an attachment-only message", () => {
    const text = serializeTranscript(
      [
        msg("user", "See the file", {
          attachments: [
            { name: "notes.md", type: "text/markdown", content: "secret" },
          ],
        }),
        msg("user", "", {
          attachments: [{ name: "photo.png", type: "image/png" }],
        }),
      ],
      { botName: "Bot" },
    );
    expect(text).toBe(
      [
        "**You**",
        "",
        "See the file",
        "[attachment: notes.md]",
        "",
        "**You**",
        "[attachment: photo.png]",
      ].join("\n"),
    );
    // Attachment CONTENT is never inlined — only the name.
    expect(text).not.toContain("secret");
  });

  it("returns an empty string for an empty or tool-only conversation", () => {
    expect(serializeTranscript([], { botName: "Bot" })).toBe("");
    expect(serializeTranscript([msg("tool", "x")], { botName: "Bot" })).toBe(
      "",
    );
  });
});

describe("selectElementContents / selectionInside", () => {
  afterEach(() => {
    window.getSelection()?.removeAllRanges();
    document.body.innerHTML = "";
  });

  it("selects exactly the element's contents and reports the selection inside it", () => {
    const container = document.createElement("div");
    const first = document.createElement("p");
    first.textContent = "first paragraph";
    const second = document.createElement("p");
    second.textContent = "second paragraph";
    container.append(first, second);
    const other = document.createElement("aside");
    other.textContent = "sidebar text";
    document.body.append(container, other);

    selectElementContents(container);

    const selected = window.getSelection()?.toString() ?? "";
    expect(selected).toContain("first paragraph");
    expect(selected).toContain("second paragraph");
    expect(selected).not.toContain("sidebar text");
    expect(selectionInside(container)).toBe(true);
    expect(selectionInside(other)).toBe(false);
    expect(selectionInside(null)).toBe(false);
  });

  it("reports no selection once the ranges are cleared, or when it is collapsed", () => {
    const container = document.createElement("div");
    container.textContent = "some text";
    document.body.append(container);

    selectElementContents(container);
    expect(selectionInside(container)).toBe(true);

    window.getSelection()?.removeAllRanges();
    expect(selectionInside(container)).toBe(false);

    // A bare caret inside the element is not a selection.
    const caret = document.createRange();
    caret.setStart(container.firstChild as Node, 2);
    caret.collapse(true);
    window.getSelection()?.addRange(caret);
    expect(selectionInside(container)).toBe(false);
  });
});

describe("isSelectAllChord", () => {
  const base = {
    key: "a",
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    target: null,
  };

  it("matches ⌘A and Ctrl+A on a non-editable target (case-blind)", () => {
    expect(isSelectAllChord({ ...base, metaKey: true })).toBe(true);
    expect(isSelectAllChord({ ...base, ctrlKey: true })).toBe(true);
    expect(isSelectAllChord({ ...base, key: "A", ctrlKey: true })).toBe(true);
    const section = document.createElement("section");
    expect(isSelectAllChord({ ...base, metaKey: true, target: section })).toBe(
      true,
    );
  });

  it("rejects a bare A, other keys, and shifted/alted chords", () => {
    expect(isSelectAllChord(base)).toBe(false);
    expect(isSelectAllChord({ ...base, metaKey: true, key: "c" })).toBe(false);
    expect(isSelectAllChord({ ...base, metaKey: true, shiftKey: true })).toBe(
      false,
    );
    expect(isSelectAllChord({ ...base, ctrlKey: true, altKey: true })).toBe(
      false,
    );
  });

  it("leaves the browser's own select-all to editable fields", () => {
    const input = document.createElement("input");
    const textarea = document.createElement("textarea");
    const editable = document.createElement("div");
    Object.defineProperty(editable, "isContentEditable", { value: true });
    expect(isSelectAllChord({ ...base, metaKey: true, target: input })).toBe(
      false,
    );
    expect(isSelectAllChord({ ...base, ctrlKey: true, target: textarea })).toBe(
      false,
    );
    expect(isSelectAllChord({ ...base, metaKey: true, target: editable })).toBe(
      false,
    );
  });
});

describe("copyText", () => {
  afterEach(() => {
    delete (navigator as { clipboard?: unknown }).clipboard;
  });

  it("writes through the Clipboard API when present", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    await expect(copyText("hello")).resolves.toEqual({ ok: true });
    expect(writeText).toHaveBeenCalledWith("hello");
  });

  it("names the insecure-page cause when the API is absent (plain http)", async () => {
    delete (navigator as { clipboard?: unknown }).clipboard;
    const outcome = await copyText("hello");
    expect(outcome).toEqual({ ok: false, reason: "insecure-context" });
    expect(copyFailureMessage("insecure-context")).toContain(
      "https or localhost",
    );
  });

  it("reports a refused write as denied, never throws", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: {
        writeText: vi.fn(async () => {
          throw new DOMException("blocked", "NotAllowedError");
        }),
      },
      configurable: true,
    });
    await expect(copyText("hello")).resolves.toEqual({
      ok: false,
      reason: "denied",
    });
    expect(copyFailureMessage("denied")).toBe(
      "The browser refused clipboard access.",
    );
  });
});
