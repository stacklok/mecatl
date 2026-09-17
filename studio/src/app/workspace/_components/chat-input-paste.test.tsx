import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatInput } from "./chat-input";
import { PASTE_CHAR_THRESHOLD, PASTE_LINE_THRESHOLD } from "./composer-paste";

/**
 * The composer's paste listener, driven through the rendered ChatInput: a
 * `paste` event on the ProseMirror DOM with a fake DataTransfer (jsdom has no
 * ClipboardEvent), then the visible outcome — an attachment pill, a
 * `[Pasted text #N]` chip that Enter expands into onSend, or the editor's own
 * literal insert for a small paste. The classification table itself is unit
 * tested in composer-paste.test.ts; this file proves the wiring.
 */

// jsdom lays nothing out: ProseMirror's scroll-into-view after a focus or a
// send asks the selection for its rects, so give it empty ones.
const zeroRect = () =>
  ({
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    width: 0,
    height: 0,
    toJSON: () => ({}),
  }) as DOMRect;
for (const proto of [Element.prototype, Range.prototype]) {
  if (!proto.getClientRects) {
    proto.getClientRects = () => [] as unknown as DOMRectList;
  }
  if (!proto.getBoundingClientRect) {
    proto.getBoundingClientRect = zeroRect;
  }
}

const png = (name = "shot.png") =>
  new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });

const lines = (n: number) =>
  Array.from({ length: n }, (_, i) => `line ${i + 1}`).join("\n");

/** A paste event carrying `files` and a `text/plain` flavour. */
function pasteEvent(files: File[], text: string): Event {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  const data = {
    files,
    items: [],
    types: text ? ["text/plain"] : [],
    getData: (format: string) => (format === "text/plain" ? text : ""),
  };
  Object.defineProperty(event, "clipboardData", { value: data });
  return event;
}

async function renderComposer(props: Parameters<typeof ChatInput>[0] = {}) {
  const utils = render(<ChatInput {...props} />);
  // useEditor with immediatelyRender:false mounts the ProseMirror view in an
  // effect, so the DOM arrives a tick after render.
  const dom = await waitFor(() => {
    const el = utils.container.querySelector<HTMLElement>(".ProseMirror");
    if (!el) throw new Error("editor not mounted yet");
    return el;
  });
  return { ...utils, dom };
}

const paste = (dom: HTMLElement, files: File[], text: string) => {
  let event: Event | undefined;
  act(() => {
    event = pasteEvent(files, text);
    dom.dispatchEvent(event);
  });
  if (!event) throw new Error("paste not dispatched");
  return event;
};

const pressEnter = (dom: HTMLElement) => {
  act(() => {
    dom.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
  });
};

describe("ChatInput paste", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("stages a pasted clipboard image as an attachment and sends it with the text", async () => {
    const onSend = vi.fn();
    const { dom } = await renderComposer({ onSend });
    const file = png();
    const event = paste(dom, [file], "");
    // Handled here: the browser's default (and ProseMirror's paste) must not run.
    expect(event.defaultPrevented).toBe(true);
    expect(await screen.findByText("shot.png")).toBeInTheDocument();
    // The staged file rides the send like a picked or dropped one.
    paste(dom, [], "see the screenshot");
    await waitFor(() =>
      expect(dom.textContent).toContain("see the screenshot"),
    );
    pressEnter(dom);
    expect(onSend).toHaveBeenCalledWith("see the screenshot", [file]);
  });

  it("refuses a pasted image through the same staging gate as the picker", async () => {
    const { dom } = await renderComposer({
      mediaCapabilities: { image: false, audio: false },
    });
    paste(dom, [png("nope.png")], "");
    expect(
      await screen.findByText(
        "nope.png: this model does not accept image input.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("nope.png")).toBeNull();
  });

  it("stages a large text paste as a [Pasted text #N] chip that expands on send", async () => {
    const onSend = vi.fn();
    const { dom } = await renderComposer({ onSend });
    const payload = lines(PASTE_LINE_THRESHOLD);
    const event = paste(dom, [], payload);
    expect(event.defaultPrevented).toBe(true);
    await waitFor(() => expect(dom.textContent).toBe("[Pasted text #1]"));
    // The payload is not in the DOM…
    expect(dom.textContent).not.toContain("line 2");
    // …but Enter sends the full text.
    pressEnter(dom);
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend.mock.calls[0][0]).toBe(payload);
    await waitFor(() => expect(dom.textContent).toBe(""));
  });

  it("numbers chips within a draft and restarts after a send", async () => {
    const onSend = vi.fn();
    const { dom } = await renderComposer({ onSend });
    paste(dom, [], "x".repeat(PASTE_CHAR_THRESHOLD));
    paste(dom, [], "y".repeat(PASTE_CHAR_THRESHOLD));
    await waitFor(() =>
      expect(dom.textContent).toBe("[Pasted text #1][Pasted text #2]"),
    );
    pressEnter(dom);
    expect(onSend.mock.calls[0][0]).toBe(
      "x".repeat(PASTE_CHAR_THRESHOLD) + "y".repeat(PASTE_CHAR_THRESHOLD),
    );
    await waitFor(() => expect(dom.textContent).toBe(""));
    paste(dom, [], "z".repeat(PASTE_CHAR_THRESHOLD));
    await waitFor(() => expect(dom.textContent).toBe("[Pasted text #1]"));
  });

  it("leaves a small text paste to the editor (literal insert)", async () => {
    const onSend = vi.fn();
    const { dom } = await renderComposer({ onSend });
    paste(dom, [], "hello there");
    // Not intercepted: ProseMirror's own handler inserted the text literally
    // (it is the one that prevented the browser default), and no chip exists.
    await waitFor(() => expect(dom.textContent).toBe("hello there"));
    expect(dom.querySelector("[data-paste-placeholder]")).toBeNull();
    pressEnter(dom);
    expect(onSend).toHaveBeenCalledWith("hello there", undefined);
  });

  it("is inert while the composer is disabled", async () => {
    const { dom } = await renderComposer({ disabled: true });
    const event = paste(dom, [png()], "");
    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByText("shot.png")).toBeNull();
  });
});
