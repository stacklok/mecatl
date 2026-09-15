import { render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AttachmentPill, resolveComposerAction } from "./chat-input";

/**
 * The Enter matrix is tested through `resolveComposerAction`, the pure
 * decision function the composer's keydown handler and send button both call.
 * Driving the TipTap editor's ProseMirror view with synthetic keydowns in
 * jsdom is impractical (the editor mounts asynchronously and owns its own
 * capture-phase handlers), so the decision table is extracted and tested
 * exhaustively instead; "newline" means the key is NOT intercepted — the
 * editor's own hardBreak inserts the newline and onSend is never called.
 */
describe("resolveComposerAction", () => {
  const resolve = (
    shift: boolean,
    isStreaming: boolean,
    behavior: "queue" | "steer",
  ) => resolveComposerAction({ shift, isStreaming, behavior });

  it("sends on idle Enter, whatever the preference", () => {
    expect(resolve(false, false, "queue")).toBe("send");
    expect(resolve(false, false, "steer")).toBe("send");
  });

  it("keeps idle Shift+Enter as a newline (the key is not intercepted, so onSend is never called)", () => {
    expect(resolve(true, false, "queue")).toBe("newline");
    expect(resolve(true, false, "steer")).toBe("newline");
  });

  it("queues on streaming Enter with the default preference", () => {
    expect(resolve(false, true, "queue")).toBe("queue");
  });

  it("steers on streaming Shift+Enter with the default preference", () => {
    expect(resolve(true, true, "queue")).toBe("steer");
  });

  it("inverts both keys when the preference is steer", () => {
    expect(resolve(false, true, "steer")).toBe("steer");
    expect(resolve(true, true, "steer")).toBe("queue");
  });

  // Attachments no longer force the queue path: a steer carries staged image
  // parts (ADR 0251). Availability is the handler's business — performAction
  // degrades steer→queue when onSteer is absent, keeping the files attached.
});

describe("AttachmentPill", () => {
  // jsdom has no object-URL implementation; stub the pair the pill uses.
  const createObjectURL = vi.fn(() => "blob:thumb");
  const revokeObjectURL = vi.fn();

  beforeEach(() => {
    createObjectURL.mockClear();
    revokeObjectURL.mockClear();
    vi.stubGlobal("URL", {
      ...URL,
      createObjectURL,
      revokeObjectURL,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows a thumbnail for an image file and revokes its object URL on unmount", async () => {
    const file = new File(["x"], "photo.png", { type: "image/png" });
    const { container, unmount } = render(
      <AttachmentPill file={file} onRemove={() => {}} />,
    );
    await waitFor(() =>
      expect(container.querySelector("img")?.getAttribute("src")).toBe(
        "blob:thumb",
      ),
    );
    unmount();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:thumb");
  });

  it("shows a file-kind glyph, not a thumbnail, for a non-image file", () => {
    const file = new File(["x"], "report.pdf", { type: "application/pdf" });
    const { container } = render(
      <AttachmentPill file={file} onRemove={() => {}} />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(container.querySelector('[aria-label="PDF"]')).toBeTruthy();
    expect(container.textContent).toContain("report.pdf");
  });
});
