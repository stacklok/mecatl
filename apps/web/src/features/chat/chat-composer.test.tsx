// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatComposer, type DraftChatConfiguration, resolveComposerAction } from "./chat-composer";

const configuration: DraftChatConfiguration = {
  mode: "default",
  reasoningEffort: "default",
  toolAccess: "all",
};

const models = [
  { id: "image", image: true, label: "Image model", providerId: "provider" },
  { id: "text", image: false, label: "Text model", providerId: "provider" },
];

afterEach(() => cleanup());

describe("composer Enter behavior", () => {
  it("sends Enter and preserves Shift+Enter as a newline while idle", () => {
    expect(resolveComposerAction({ behavior: "queue", shift: false, working: false })).toBe("send");
    expect(resolveComposerAction({ behavior: "queue", shift: true, working: false })).toBe(
      "newline",
    );
  });

  it("uses the preference and reverses it with Shift during a run", () => {
    expect(resolveComposerAction({ behavior: "queue", shift: false, working: true })).toBe("queue");
    expect(resolveComposerAction({ behavior: "queue", shift: true, working: true })).toBe("steer");
    expect(resolveComposerAction({ behavior: "steer", shift: true, working: true })).toBe("queue");
  });
});

describe("chat composer", () => {
  it("keeps composer controls reachable at narrow widths", async () => {
    const user = userEvent.setup();
    const onConfigurationChange = vi.fn();
    for (const width of [320, 500, 1280]) {
      Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
      const view = render(
        <ChatComposer
          configuration={configuration}
          imageAttachmentsSupported
          models={models}
          onConfigurationChange={onConfigurationChange}
          onPreviewImage={vi.fn()}
          onSend={vi.fn().mockResolvedValue(true)}
        />,
      );
      expect(screen.getByRole("textbox", { name: "Message Mecatl" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Send message" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Attach images" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Start dictation" })).toBeTruthy();
      const options = screen.getByRole("button", { name: "Chat options" });
      await user.click(options);
      expect(screen.getByRole("dialog", { name: "Chat options" })).toBeTruthy();
      expect(screen.getByLabelText("Model")).toBeTruthy();
      expect(screen.getByLabelText("Effort")).toBeTruthy();
      expect(screen.getByLabelText("Mode")).toBeTruthy();
      expect(screen.getByLabelText("Tools")).toBeTruthy();
      await user.click(screen.getByRole("button", { name: "Close chat options" }));
      expect(document.activeElement).toBe(options);
      view.unmount();
    }
  });

  it("never submits the IME commit key", async () => {
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} workingBehavior="queue" />);
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "日本語" } });
    fireEvent.compositionStart(input);
    fireEvent.keyDown(input, { key: "Enter", isComposing: true });
    fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
    fireEvent.compositionEnd(input);
    fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
    expect(onSend).not.toHaveBeenCalled();
    expect((input as HTMLTextAreaElement).value).toBe("日本語");
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend).toHaveBeenCalledWith("日本語", "send", []);
  });

  it("suppresses an Enter delivered just after compositionend", async () => {
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} />);
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "候補" } });
    fireEvent.compositionStart(input);
    fireEvent.compositionEnd(input);
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSend).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
  });

  it("allows a later Enter after an IME candidate commits without Enter", async () => {
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} />);
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "候補" } });
    fireEvent.compositionStart(input);
    fireEvent.compositionEnd(input);
    await Promise.resolve();
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
  });

  it("does not double submit while the first send is pending", async () => {
    let finish: ((value: boolean) => void) | undefined;
    const onSend = vi.fn().mockImplementation(
      () =>
        new Promise<boolean>((resolve) => {
          finish = resolve;
        }),
    );
    render(<ChatComposer onSend={onSend} />);
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "hello" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSend).toHaveBeenCalledTimes(1);
    finish?.(true);
  });

  it("keeps an arrival seed behind an explicit Send or Edit choice", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const onSeedConsumed = vi.fn();
    const view = render(
      <ChatComposer
        disabled
        onSeedConsumed={onSeedConsumed}
        onSend={onSend}
        seedRequiresConfirmation
        seedText="Review this change"
      />,
    );
    expect(onSeedConsumed).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("dialog", { name: "Send this prompt?" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send prompt" }).hasAttribute("disabled")).toBe(true);
    expect(onSend).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Edit prompt" }));
    expect(screen.queryByRole("dialog", { name: "Send this prompt?" })).toBeNull();
    expect(
      (screen.getByRole("textbox", { name: "Message Mecatl" }) as HTMLTextAreaElement).value,
    ).toBe("Review this change");
    view.rerender(
      <ChatComposer
        onSeedConsumed={onSeedConsumed}
        onSend={onSend}
        seedRequiresConfirmation
        seedText="Another prompt"
      />,
    );
    expect(onSend).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Send prompt" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("Another prompt", "send", []));
  });

  it("limits image sending to the selected model capability", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(
      <ChatComposer imageAttachmentsSupported={false} onPreviewImage={vi.fn()} onSend={onSend} />,
    );
    expect(screen.queryByRole("button", { name: "Attach images" })).toBeNull();
    view.rerender(
      <ChatComposer imageAttachmentsSupported onPreviewImage={vi.fn()} onSend={onSend} />,
    );
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "Keep this text" } });
    const image = new File(["image data"], "picture.png", { type: "image/png" });
    await user.upload(screen.getByLabelText("Choose images to attach"), image);
    await waitFor(() => expect(screen.getByText("picture.png")).toBeTruthy());
    view.rerender(
      <ChatComposer imageAttachmentsSupported={false} onPreviewImage={vi.fn()} onSend={onSend} />,
    );
    expect(screen.queryByText("picture.png")).toBeNull();
    expect(screen.getByRole("alert").textContent).toMatch(/model does not support/i);
    expect((input as HTMLTextAreaElement).value).toBe("Keep this text");
    expect(screen.queryByRole("button", { name: "Attach images" })).toBeNull();
  });

  it("holds images until the active run ends", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(
      <ChatComposer imageAttachmentsSupported onPreviewImage={vi.fn()} onSend={onSend} />,
    );
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "image prompt" } });
    await user.upload(
      screen.getByLabelText("Choose images to attach"),
      new File(["image data"], "picture.png", { type: "image/png" }),
    );
    await waitFor(() => expect(screen.getByText("picture.png")).toBeTruthy());
    view.rerender(
      <ChatComposer imageAttachmentsSupported onPreviewImage={vi.fn()} onSend={onSend} working />,
    );
    await user.click(screen.getByRole("button", { name: "Queue message" }));
    expect(onSend).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/wait for the active run/i);
    expect((input as HTMLTextAreaElement).value).toBe("image prompt");
  });

  it("limits the picker to sixteen images without clearing the text", async () => {
    const user = userEvent.setup();
    render(
      <ChatComposer
        imageAttachmentsSupported
        onPreviewImage={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "Typed text" } });
    const files = Array.from(
      { length: 17 },
      (_, index) => new File(["a"], `${index}.png`, { type: "image/png" }),
    );
    await user.upload(screen.getByLabelText("Choose images to attach"), files);
    await waitFor(() =>
      expect(screen.getAllByRole("button", { name: /^Preview / })).toHaveLength(16),
    );
    expect(screen.getByRole("alert").textContent).toMatch(/up to 16 images/i);
    expect((input as HTMLTextAreaElement).value).toBe("Typed text");
  });

  it("shows a typing fallback when the browser cannot dictate", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} />);
    const input = screen.getByRole("textbox", { name: "Message Mecatl" });
    fireEvent.change(input, { target: { value: "Typed draft" } });
    await user.click(screen.getByRole("button", { name: "Start dictation" }));
    expect(screen.getByRole("alert").textContent).toMatch(/type your message instead/i);
    expect((input as HTMLTextAreaElement).value).toBe("Typed draft");
    await user.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("Typed draft", "send", []));
  });
});
