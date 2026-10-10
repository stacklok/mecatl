// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import type { EnterSendBehavior } from "../../lib/profile-preferences";
import { ChatComposer, type ComposerEnterAction, resolveComposerAction } from "./chat-composer";

/**
 * Pins the composer behaviour the prototype alignment (#2207, the composer
 * step) must keep: Studio's Enter, queue, and steer matrix, its placeholders
 * and submit labels, the send outcome contract, the escape draft clear, and
 * the image attachment limits. These pass unchanged before and after the
 * composer is re-skinned.
 */

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.sessionStorage.clear();
});

function composerInput() {
  return screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message Mecatl" });
}

/** The hidden file input. Its label names what it accepts, so match both wordings. */
function fileInput() {
  return screen.getByLabelText(/^Choose (images|files) to attach$/);
}

describe("composer Enter, queue, and steer matrix", () => {
  const matrix: [boolean, EnterSendBehavior, boolean, ComposerEnterAction][] = [
    [false, "queue", false, "send"],
    [false, "queue", true, "newline"],
    [false, "steer", false, "send"],
    [false, "steer", true, "newline"],
    [true, "queue", false, "queue"],
    [true, "queue", true, "steer"],
    [true, "steer", false, "steer"],
    [true, "steer", true, "queue"],
  ];

  for (const [working, behavior, shift, action] of matrix) {
    it(`resolves ${working ? "a running" : "an idle"} ${behavior} Enter${shift ? " with Shift" : ""} to ${action}`, async () => {
      expect(resolveComposerAction({ behavior, shift, working })).toBe(action);
      const onSend = vi.fn().mockResolvedValue(true);
      render(<ChatComposer onSend={onSend} working={working} workingBehavior={behavior} />);
      const input = composerInput();
      fireEvent.change(input, { target: { value: "hello" } });
      const event = fireEvent.keyDown(input, { key: "Enter", shiftKey: shift });
      if (action === "newline") {
        // Shift+Enter is left to the textarea, so the browser inserts the line break.
        expect(event).toBe(true);
        await Promise.resolve();
        expect(onSend).not.toHaveBeenCalled();
        expect(input.value).toBe("hello");
      } else {
        expect(event).toBe(false);
        await waitFor(() => expect(onSend).toHaveBeenCalledWith("hello", action, []));
        await waitFor(() => expect(input.value).toBe(""));
      }
    });
  }

  it("names the submit button and placeholder after the Enter action", () => {
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(<ChatComposer onSend={onSend} />);
    expect(screen.getByRole("button", { name: "Send message" })).toBeTruthy();
    expect(composerInput().placeholder).toBe("Send a message…");

    view.rerender(
      <ChatComposer
        configuration={{ mode: "default", reasoningEffort: "default", toolAccess: "all" }}
        onSend={onSend}
      />,
    );
    expect(composerInput().placeholder).toBe("Start a new chat…");

    view.rerender(<ChatComposer onSend={onSend} working workingBehavior="queue" />);
    expect(screen.getByRole("button", { name: "Queue message" })).toBeTruthy();
    expect(composerInput().placeholder).toBe("Queue a message…");

    view.rerender(<ChatComposer onSend={onSend} working workingBehavior="steer" />);
    expect(screen.getByRole("button", { name: "Steer message" })).toBeTruthy();
    expect(composerInput().placeholder).toBe("Steer the agent…");

    view.rerender(<ChatComposer disabled onSend={onSend} />);
    expect(screen.getByRole("button", { name: "Mecatl is working" })).toBeTruthy();
    expect(composerInput().placeholder).toBe("Mecatl is working…");
    expect(composerInput().disabled).toBe(true);
  });

  it("submits the button's action with the trimmed prompt", async () => {
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} working workingBehavior="steer" />);
    fireEvent.change(composerInput(), { target: { value: "  change direction  " } });
    fireEvent.click(screen.getByRole("button", { name: "Steer message" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("change direction", "steer", []));
  });

  it("never sends a blank prompt", async () => {
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "   " } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await Promise.resolve();
    expect(onSend).not.toHaveBeenCalled();
    expect(
      (screen.getByRole("button", { name: "Send message" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });
});

describe("composer send outcome", () => {
  it("keeps the prompt and images when the send is refused, and clears both once accepted", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    render(<ChatComposer imageAttachmentsSupported onPreviewImage={vi.fn()} onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "with a picture" } });
    await user.upload(fileInput(), new File(["image data"], "picture.png", { type: "image/png" }));
    await waitFor(() => expect(screen.getByText("picture.png")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend.mock.calls[0]?.[0]).toBe("with a picture");
    expect(onSend.mock.calls[0]?.[1]).toBe("send");
    expect(onSend.mock.calls[0]?.[2]).toMatchObject([
      { mimeType: "image/png", name: "picture.png" },
    ]);
    await waitFor(() =>
      expect(
        (screen.getByRole("button", { name: "Send message" }) as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    expect(composerInput().value).toBe("with a picture");
    expect(screen.getByText("picture.png")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(composerInput().value).toBe(""));
    expect(screen.queryByText("picture.png")).toBeNull();
  });

  it("clears the text, images, and attachment error on the escape clear signal", async () => {
    const user = userEvent.setup();
    const onDraftChange = vi.fn();
    const view = render(
      <ChatComposer
        clearDraftSignal={0}
        imageAttachmentsSupported
        onDraftChange={onDraftChange}
        onPreviewImage={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    fireEvent.change(composerInput(), { target: { value: "unsent" } });
    await user.upload(fileInput(), [
      new File(["image data"], "picture.png", { type: "image/png" }),
      new File([], "empty.png", { type: "image/png" }),
    ]);
    await waitFor(() => expect(screen.getByText("picture.png")).toBeTruthy());
    expect(screen.getByRole("alert").textContent).toContain("empty.png is empty.");
    expect(onDraftChange).toHaveBeenLastCalledWith(true);

    view.rerender(
      <ChatComposer
        clearDraftSignal={1}
        imageAttachmentsSupported
        onDraftChange={onDraftChange}
        onPreviewImage={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await waitFor(() => expect(composerInput().value).toBe(""));
    expect(screen.queryByText("picture.png")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(onDraftChange).toHaveBeenLastCalledWith(false);
  });
});

describe("composer image attachment limits", () => {
  it("refuses an image over 10 MB and keeps the typed text", async () => {
    const user = userEvent.setup();
    render(
      <ChatComposer
        imageAttachmentsSupported
        onPreviewImage={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    fireEvent.change(composerInput(), { target: { value: "Typed text" } });
    await user.upload(
      fileInput(),
      new File([new Uint8Array(10 * 1024 * 1024 + 1)], "huge.png", { type: "image/png" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "huge.png is larger than the 10 MB image limit.",
      ),
    );
    expect(screen.queryByRole("button", { name: "Preview huge.png" })).toBeNull();
    expect(composerInput().value).toBe("Typed text");
  });

  it("refuses the image that would take a prompt over 20 MB of images", async () => {
    const user = userEvent.setup();
    render(
      <ChatComposer
        imageAttachmentsSupported
        onPreviewImage={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    const eightMiB = 8 * 1024 * 1024;
    await user.upload(fileInput(), [
      new File([new Uint8Array(eightMiB)], "one.png", { type: "image/png" }),
      new File([new Uint8Array(eightMiB)], "two.png", { type: "image/png" }),
      new File([new Uint8Array(eightMiB)], "three.png", { type: "image/png" }),
    ]);
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "three.png exceeds the 20 MB total image limit.",
      ),
    );
    expect(screen.getByRole("button", { name: "Preview one.png" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Preview two.png" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Preview three.png" })).toBeNull();
  });

  it("previews and removes an attached image by name", async () => {
    const user = userEvent.setup();
    const onPreviewImage = vi.fn();
    render(
      <ChatComposer
        imageAttachmentsSupported
        onPreviewImage={onPreviewImage}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.upload(fileInput(), new File(["image data"], "picture.png", { type: "image/png" }));
    await user.click(await screen.findByRole("button", { name: "Preview picture.png" }));
    expect(onPreviewImage).toHaveBeenCalledWith(
      expect.objectContaining({ mimeType: "image/png", name: "picture.png" }),
    );
    await user.click(screen.getByRole("button", { name: "Remove picture.png" }));
    expect(screen.queryByText("picture.png")).toBeNull();
    expect(
      (screen.getByRole("button", { name: "Send message" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });
});
